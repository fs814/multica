package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// Runtime CLI directory (TES-140) — HTTP contract.
//
// These tests drive the real handlers through the real routes: the request
// shapes, the owner-only gate, and the poll-to-terminal lifecycle. The
// execution itself lives on the daemon and is covered in
// server/internal/daemon/runtime_cli_e2e_test.go, which runs real processes.

func createRuntimeCLITestRuntime(t *testing.T, ownerID string) (runtimeID, daemonID string) {
	t.Helper()
	return createRuntimeCLITestRuntimeWithVisibility(t, ownerID, "private")
}

func createRuntimeCLITestRuntimeWithVisibility(t *testing.T, ownerID, visibility string) (runtimeID, daemonID string) {
	t.Helper()

	runtimeName := fmt.Sprintf("runtime-cli-%d", time.Now().UnixNano())
	daemonID = fmt.Sprintf("runtime-cli-daemon-%d", time.Now().UnixNano())

	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, last_seen_at, visibility
		)
		VALUES ($1, $2, $3, 'local', 'claude', 'online', 'Runtime CLI Test', '{}'::jsonb, $4, now(), $5)
		RETURNING id
	`, testWorkspaceID, daemonID, runtimeName, ownerID, visibility).Scan(&runtimeID); err != nil {
		t.Fatalf("create local runtime: %v", err)
	}

	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id = $1`, runtimeID)
	})
	return runtimeID, daemonID
}

func skipWithoutDB(t *testing.T) {
	t.Helper()
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
}

// realLocalOutput runs a genuine process on this machine and returns its real
// stdout, so nothing asserted below is fabricated by the test.
func realLocalOutput(t *testing.T) string {
	t.Helper()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd.exe", "/c", "echo multica-cli-chain-ok")
	} else {
		cmd = exec.Command("/bin/sh", "-c", "echo multica-cli-chain-ok")
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("produce real local output: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// Two gates stack here, and the status codes differ on purpose.
//
// A PRIVATE machine is already hidden from non-owners by
// requireRuntimeReadAccess, which answers 404 rather than 403 so a known
// runtime ID cannot be used as an oracle for what exists.
//
// A machine its owner has made PUBLIC is readable by the workspace, so the
// request reaches this feature's own gate — requireRuntimeLocalSkillAccess —
// which refuses with 403. That is the layer this change adds, and the one that
// must hold for a workspace admin too: reading files off someone's machine
// already requires ownership, and executing commands there is strictly more
// dangerous.
func TestInitiateCLIRunIsOwnerOnly(t *testing.T) {
	skipWithoutDB(t)

	ownerID := dbfx.User(t, "CLI owner", "cli-owner-"+fmt.Sprint(time.Now().UnixNano())+"@example.com")
	dbfx.Member(t, testWorkspaceID, ownerID, "member")

	adminID := dbfx.User(t, "CLI admin", "cli-admin-"+fmt.Sprint(time.Now().UnixNano())+"@example.com")
	dbfx.Member(t, testWorkspaceID, adminID, "admin")

	body := map[string]any{"params": map[string]string{"command": "status"}}

	// Layer 1: a private machine stays invisible to a non-owner admin.
	privateRuntimeID, _ := createRuntimeCLITestRuntimeWithVisibility(t, ownerID, "private")
	req := newRequestAsUser(adminID, http.MethodPost, "/api/runtimes/"+privateRuntimeID+"/clis/zhihu/runs", body)
	req = withURLParams(req, "runtimeId", privateRuntimeID, "cliKey", "zhihu")
	testutil.Call(t, testHandler.InitiateCLIRun, req).Want(http.StatusNotFound)

	listReq := newRequestAsUser(adminID, http.MethodPost, "/api/runtimes/"+privateRuntimeID+"/clis", nil)
	listReq = withURLParam(listReq, "runtimeId", privateRuntimeID)
	testutil.Call(t, testHandler.InitiateListCLIs, listReq).Want(http.StatusNotFound)

	// Layer 2: on a machine shared with the workspace, the admin still cannot
	// run a command on it. Admins are NOT exempt from this gate.
	publicRuntimeID, _ := createRuntimeCLITestRuntimeWithVisibility(t, ownerID, "public")
	sharedReq := newRequestAsUser(adminID, http.MethodPost, "/api/runtimes/"+publicRuntimeID+"/clis/zhihu/runs", body)
	sharedReq = withURLParams(sharedReq, "runtimeId", publicRuntimeID, "cliKey", "zhihu")
	testutil.Call(t, testHandler.InitiateCLIRun, sharedReq).Want(http.StatusForbidden)

	sharedListReq := newRequestAsUser(adminID, http.MethodPost, "/api/runtimes/"+publicRuntimeID+"/clis", nil)
	sharedListReq = withURLParam(sharedListReq, "runtimeId", publicRuntimeID)
	testutil.Call(t, testHandler.InitiateListCLIs, sharedListReq).Want(http.StatusForbidden)

	// The owner is allowed on their own machine.
	ownReq := newRequestAsUser(ownerID, http.MethodPost, "/api/runtimes/"+publicRuntimeID+"/clis/zhihu/runs", body)
	ownReq = withURLParams(ownReq, "runtimeId", publicRuntimeID, "cliKey", "zhihu")
	testutil.Call(t, testHandler.InitiateCLIRun, ownReq).Want(http.StatusOK)
}

// A malformed key never reaches the queue: the key is the whitelist's index,
// so its shape is checked before anything is enqueued or audited.
func TestInitiateCLIRunRejectsAMalformedKey(t *testing.T) {
	skipWithoutDB(t)

	ownerID := dbfx.User(t, "CLI key owner", "cli-key-owner-"+fmt.Sprint(time.Now().UnixNano())+"@example.com")
	dbfx.Member(t, testWorkspaceID, ownerID, "member")
	runtimeID, _ := createRuntimeCLITestRuntime(t, ownerID)

	for _, bad := range []string{"Zhihu", "zhihu; rm -rf /", "zhihu/../etc", "", "-leading"} {
		req := newRequestAsUser(ownerID, http.MethodPost, "/api/runtimes/"+runtimeID+"/clis/x/runs", map[string]any{})
		req = withURLParams(req, "runtimeId", runtimeID, "cliKey", bad)
		testutil.Call(t, testHandler.InitiateCLIRun, req).Want(http.StatusBadRequest)
	}
}

// Unexpected parameter names and oversized values are rejected as shape, before
// the request is audited or dispatched.
func TestInitiateCLIRunRejectsMalformedParams(t *testing.T) {
	skipWithoutDB(t)

	ownerID := dbfx.User(t, "CLI param owner", "cli-param-owner-"+fmt.Sprint(time.Now().UnixNano())+"@example.com")
	dbfx.Member(t, testWorkspaceID, ownerID, "member")
	runtimeID, _ := createRuntimeCLITestRuntime(t, ownerID)

	cases := []map[string]string{
		{"UPPER": "x"},
		{"with-dash": "x"},
		{"": "x"},
		{"query": strings.Repeat("a", cliMaxParamValueLen+1)},
	}
	for _, params := range cases {
		req := newRequestAsUser(ownerID, http.MethodPost, "/api/runtimes/"+runtimeID+"/clis/zhihu/runs",
			map[string]any{"params": params})
		req = withURLParams(req, "runtimeId", runtimeID, "cliKey", "zhihu")
		testutil.Call(t, testHandler.InitiateCLIRun, req).Want(http.StatusBadRequest)
	}
}

// The acceptance flow for the channel layer: POST a run, let the daemon claim
// and report it, then poll to a terminal state and read the output. No UI is
// involved, and every request goes through the real handler and the real store.
func TestCLIRunLifecycleReachesCompletedWithRealOutput(t *testing.T) {
	skipWithoutDB(t)

	ownerID := dbfx.User(t, "CLI chain owner", "cli-chain-owner-"+fmt.Sprint(time.Now().UnixNano())+"@example.com")
	dbfx.Member(t, testWorkspaceID, ownerID, "member")
	runtimeID, daemonID := createRuntimeCLITestRuntime(t, ownerID)

	expected := realLocalOutput(t)

	// 1. The panel (or a script) triggers the run.
	initReq := newRequestAsUser(ownerID, http.MethodPost, "/api/runtimes/"+runtimeID+"/clis/zhihu/runs",
		map[string]any{"params": map[string]string{"command": "status"}, "timeout_seconds": 30})
	initReq = withURLParams(initReq, "runtimeId", runtimeID, "cliKey", "zhihu")
	initW := testutil.Call(t, testHandler.InitiateCLIRun, initReq).Want(http.StatusOK)

	var started RuntimeCLIRunRequest
	if err := json.Unmarshal(initW.Body.Bytes(), &started); err != nil {
		t.Fatalf("decode initiate response: %v", err)
	}
	if started.Status != RuntimeCLIPending {
		t.Fatalf("a fresh run must start pending, got %q", started.Status)
	}
	if started.ID == "" {
		t.Fatal("expected a run id")
	}

	// 2. The daemon's next heartbeat claims it. This is the same call the
	//    heartbeat handler makes, against the same store.
	pending, err := testHandler.CLIRunStore.PopPending(context.Background(), runtimeID)
	if err != nil {
		t.Fatalf("pop pending: %v", err)
	}
	if pending == nil || pending.ID != started.ID {
		t.Fatalf("expected the queued run to be claimable, got %+v", pending)
	}
	if pending.CLIKey != "zhihu" {
		t.Fatalf("expected the claim to carry the registry key, got %q", pending.CLIKey)
	}

	// 3. Poll while it is running — the panel's loop.
	pollReq := newRequestAsUser(ownerID, http.MethodGet, "/api/runtimes/"+runtimeID+"/clis/runs/"+started.ID, nil)
	pollReq = withURLParams(pollReq, "runtimeId", runtimeID, "runId", started.ID)
	pollW := testutil.Call(t, testHandler.GetCLIRun, pollReq).Want(http.StatusOK)
	var running RuntimeCLIRunRequest
	if err := json.Unmarshal(pollW.Body.Bytes(), &running); err != nil {
		t.Fatalf("decode poll response: %v", err)
	}
	if running.Status != RuntimeCLIRunning {
		t.Fatalf("expected running after the claim, got %q", running.Status)
	}

	// 4. The daemon reports the result from the machine.
	exitCode := 0
	reportReq := newDaemonTokenRequest(http.MethodPost,
		"/api/daemon/runtimes/"+runtimeID+"/clis/runs/"+started.ID+"/result",
		map[string]any{
			"status":        "completed",
			"output":        expected,
			"truncated":     false,
			"exit_code":     exitCode,
			"duration_ms":   42,
			"output_bytes":  len(expected),
			"resolved_argv": []string{"/usr/local/bin/zhihu", "status"},
		}, testWorkspaceID, daemonID)
	reportReq = withURLParams(reportReq, "runtimeId", runtimeID, "runId", started.ID)
	testutil.Call(t, testHandler.ReportCLIRunResult, reportReq).Want(http.StatusOK)

	// 5. Poll again: terminal, with the machine's real output.
	finalReq := newRequestAsUser(ownerID, http.MethodGet, "/api/runtimes/"+runtimeID+"/clis/runs/"+started.ID, nil)
	finalReq = withURLParams(finalReq, "runtimeId", runtimeID, "runId", started.ID)
	finalW := testutil.Call(t, testHandler.GetCLIRun, finalReq).Want(http.StatusOK)

	var final RuntimeCLIRunRequest
	if err := json.Unmarshal(finalW.Body.Bytes(), &final); err != nil {
		t.Fatalf("decode final response: %v", err)
	}
	if final.Status != RuntimeCLICompleted {
		t.Fatalf("expected completed, got %q (%s)", final.Status, final.Error)
	}
	if final.Output != expected {
		t.Fatalf("expected the machine's real output %q, got %q", expected, final.Output)
	}
	if final.ExitCode == nil || *final.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %v", final.ExitCode)
	}
	// The server never resolved the command line; the reported argv is the
	// only record of what actually ran.
	if len(final.ResolvedArgv) == 0 || final.ResolvedArgv[1] != "status" {
		t.Fatalf("expected resolved_argv from the report, got %v", final.ResolvedArgv)
	}
}

// The audit trail must name both principals: whoever clicked, and the machine
// identity that executed. Conflating them makes an incident unreconstructable.
func TestCLIRunAuditRecordsInitiatorAndExecutor(t *testing.T) {
	skipWithoutDB(t)

	ownerID := dbfx.User(t, "CLI audit owner", "cli-audit-owner-"+fmt.Sprint(time.Now().UnixNano())+"@example.com")
	dbfx.Member(t, testWorkspaceID, ownerID, "member")
	runtimeID, _ := createRuntimeCLITestRuntime(t, ownerID)

	req := newRequestAsUser(ownerID, http.MethodPost, "/api/runtimes/"+runtimeID+"/clis/zhihu/runs",
		map[string]any{"params": map[string]string{"command": "status"}})
	req = withURLParams(req, "runtimeId", runtimeID, "cliKey", "zhihu")
	initW := testutil.Call(t, testHandler.InitiateCLIRun, req).Want(http.StatusOK)

	var started RuntimeCLIRunRequest
	if err := json.Unmarshal(initW.Body.Bytes(), &started); err != nil {
		t.Fatalf("decode initiate response: %v", err)
	}

	var raw []byte
	if err := testPool.QueryRow(context.Background(), `
		SELECT details FROM activity_log
		WHERE action = $1 AND workspace_id = $2
		ORDER BY created_at DESC
		LIMIT 1
	`, cliActivityInitiated, testWorkspaceID).Scan(&raw); err != nil {
		t.Fatalf("load audit row: %v", err)
	}

	var details map[string]any
	if err := json.Unmarshal(raw, &details); err != nil {
		t.Fatalf("decode audit details: %v", err)
	}

	initiator, ok := details["initiator"].(map[string]any)
	if !ok {
		t.Fatalf("audit details must carry an initiator, got %v", details)
	}
	if initiator["id"] != ownerID {
		t.Fatalf("initiator id = %v, want %v", initiator["id"], ownerID)
	}

	executor, ok := details["executor"].(map[string]any)
	if !ok {
		t.Fatalf("audit details must carry an executor, got %v", details)
	}
	// The executor is the runtime — the machine identity — not a member.
	if executor["type"] != "runtime" {
		t.Fatalf("executor type = %v, want runtime", executor["type"])
	}
	if executor["id"] != runtimeID {
		t.Fatalf("executor id = %v, want %v", executor["id"], runtimeID)
	}

	if details["cli_key"] != "zhihu" {
		t.Fatalf("audit details must name the registry key, got %v", details["cli_key"])
	}
}

// A run is one at a time per machine: concurrent CLIs compete for the same
// resources and make the panel's output attribution ambiguous.
func TestInitiateCLIRunRejectsAnOverlappingRun(t *testing.T) {
	skipWithoutDB(t)

	ownerID := dbfx.User(t, "CLI serial owner", "cli-serial-owner-"+fmt.Sprint(time.Now().UnixNano())+"@example.com")
	dbfx.Member(t, testWorkspaceID, ownerID, "member")
	runtimeID, _ := createRuntimeCLITestRuntime(t, ownerID)

	for i, want := range []int{http.StatusOK, http.StatusConflict} {
		req := newRequestAsUser(ownerID, http.MethodPost, "/api/runtimes/"+runtimeID+"/clis/zhihu/runs",
			map[string]any{"params": map[string]string{"command": "status"}})
		req = withURLParams(req, "runtimeId", runtimeID, "cliKey", "zhihu")
		w := testutil.Call(t, testHandler.InitiateCLIRun, req)
		if w.Code != want {
			t.Fatalf("attempt %d: expected %d, got %d: %s", i+1, want, w.Code, w.Body.String())
		}
	}
}

// The daemon's registry listing is persisted redacted and reaches the poller.
func TestCLIRegistryListLifecycle(t *testing.T) {
	skipWithoutDB(t)

	ownerID := dbfx.User(t, "CLI list owner", "cli-list-owner-"+fmt.Sprint(time.Now().UnixNano())+"@example.com")
	dbfx.Member(t, testWorkspaceID, ownerID, "member")
	runtimeID, daemonID := createRuntimeCLITestRuntime(t, ownerID)

	initReq := newRequestAsUser(ownerID, http.MethodPost, "/api/runtimes/"+runtimeID+"/clis", nil)
	initReq = withURLParam(initReq, "runtimeId", runtimeID)
	initW := testutil.Call(t, testHandler.InitiateListCLIs, initReq).Want(http.StatusOK)

	var started RuntimeCLIListRequest
	if err := json.Unmarshal(initW.Body.Bytes(), &started); err != nil {
		t.Fatalf("decode initiate response: %v", err)
	}

	reportReq := newDaemonTokenRequest(http.MethodPost,
		"/api/daemon/runtimes/"+runtimeID+"/clis/"+started.ID+"/result",
		map[string]any{
			"status":        "completed",
			"registry_path": "/home/someone/.multica/clis.json",
			"clis": []map[string]any{{
				"key":              "zhihu",
				"label":            "Zhihu CLI",
				"timeout_seconds":  60,
				"max_output_bytes": 65536,
				"available":        true,
			}},
		}, testWorkspaceID, daemonID)
	reportReq = withURLParams(reportReq, "runtimeId", runtimeID, "requestId", started.ID)
	testutil.Call(t, testHandler.ReportCLIListResult, reportReq).Want(http.StatusOK)

	pollReq := newRequestAsUser(ownerID, http.MethodGet, "/api/runtimes/"+runtimeID+"/clis/"+started.ID, nil)
	pollReq = withURLParams(pollReq, "runtimeId", runtimeID, "requestId", started.ID)
	pollW := testutil.Call(t, testHandler.GetCLIListRequest, pollReq).Want(http.StatusOK)

	var final RuntimeCLIListRequest
	if err := json.Unmarshal(pollW.Body.Bytes(), &final); err != nil {
		t.Fatalf("decode poll response: %v", err)
	}
	if final.Status != RuntimeCLICompleted {
		t.Fatalf("expected completed, got %q", final.Status)
	}
	if len(final.CLIs) != 1 || final.CLIs[0].Key != "zhihu" {
		t.Fatalf("unexpected entries %+v", final.CLIs)
	}
	if final.RegistryPath == "" {
		t.Fatal("expected the registry path so the panel can say where entries live")
	}
}
