package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// End-to-end daemon path.
//
// handleCLIRun is the whole daemon half of the feature: read the machine-local
// registry, validate, execute a real process, and report the real result over
// real HTTP. These tests drive that path with no mocks below the HTTP client —
// the process that runs is a genuine binary on this machine, and the output
// asserted on is what it actually wrote to stdout.

// cliTestHome points os.UserHomeDir() at a temp directory and returns the
// registry path the daemon will read, so a test controls the registry without
// touching the real ~/.multica.
func cliTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dir := filepath.Join(home, ".multica")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create .multica: %v", err)
	}
	return filepath.Join(dir, "clis.json")
}

func writeCLIRegistry(t *testing.T, path string, reg *cliRegistry) {
	t.Helper()
	raw, err := json.Marshal(reg)
	if err != nil {
		t.Fatalf("marshal registry: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write registry: %v", err)
	}
}

// cliReportDaemon wires a Daemon around an httptest server and decodes the
// report body, so assertions are on what the server would actually receive
// rather than on an in-process call.
func cliReportDaemon(t *testing.T) (*Daemon, *cliReportCapture) {
	t.Helper()
	capture := &cliReportCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var decoded map[string]any
		_ = json.Unmarshal(body, &decoded)
		capture.record(r.URL.Path, decoded)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	t.Cleanup(srv.Close)
	return &Daemon{client: NewClient(srv.URL), logger: slog.Default()}, capture
}

type cliReportCapture struct {
	mu      sync.Mutex
	paths   []string
	payload map[string]any
}

func (c *cliReportCapture) record(path string, payload map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.paths = append(c.paths, path)
	c.payload = payload
}

func (c *cliReportCapture) snapshot() ([]string, map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.paths...), c.payload
}

// The headline flow: a registered entry executes on this machine and its real
// stdout reaches the server through the real report endpoint.
func TestHandleCLIRunExecutesAndReportsRealOutput(t *testing.T) {
	registryPath := cliTestHome(t)
	entry := helperEntry(t, "args", cliParam{Name: "query", Type: "string", MaxLen: 200})
	entry.ArgTemplate = append(cliHelperArgs("args"), "{query}")
	writeCLIRegistry(t, registryPath, &cliRegistry{Version: 1, CLIs: []cliEntry{*entry}})

	d, capture := cliReportDaemon(t)

	d.handleCLIRun(context.Background(), Runtime{ID: "rt-1"}, PendingCLIRun{
		ID:     "run-1",
		CLIKey: "helper",
		Params: map[string]string{"query": "zhihu status"},
	})

	paths, payload := capture.snapshot()
	if len(paths) != 1 {
		t.Fatalf("expected exactly one report, got %v", paths)
	}
	if !strings.HasSuffix(paths[0], "/api/daemon/runtimes/rt-1/clis/runs/run-1/result") {
		t.Fatalf("unexpected report path %q", paths[0])
	}
	if payload["status"] != "completed" {
		t.Fatalf("expected completed, got %v (%v)", payload["status"], payload["error"])
	}
	if got := strings.TrimRight(payload["output"].(string), "\r\n"); got != "zhihu status" {
		t.Fatalf("expected the process's real stdout, got %q", got)
	}
	if code, ok := payload["exit_code"].(float64); !ok || code != 0 {
		t.Fatalf("expected exit_code 0, got %v", payload["exit_code"])
	}
	argv, ok := payload["resolved_argv"].([]any)
	if !ok || len(argv) == 0 {
		t.Fatalf("expected the report to carry resolved_argv, got %v", payload["resolved_argv"])
	}
	// The server never knew the command line; the report is the only place
	// the audit trail can learn it.
	if !strings.Contains(argv[len(argv)-1].(string), "zhihu status") {
		t.Fatalf("resolved_argv should end with the parameter value: %v", argv)
	}
}

// A key that is not in the machine-local registry never reaches execution —
// the whitelist refuses at the daemon, not merely at the server.
func TestHandleCLIRunRefusesUnregisteredKeyAtTheDaemon(t *testing.T) {
	registryPath := cliTestHome(t)
	writeCLIRegistry(t, registryPath, &cliRegistry{Version: 1})

	d, capture := cliReportDaemon(t)
	d.handleCLIRun(context.Background(), Runtime{ID: "rt-1"}, PendingCLIRun{
		ID:     "run-2",
		CLIKey: "not-registered",
	})

	_, payload := capture.snapshot()
	if payload["status"] != "failed" {
		t.Fatalf("expected failed, got %v", payload["status"])
	}
	if !strings.Contains(payload["error"].(string), "not registered") {
		t.Fatalf("unexpected error %v", payload["error"])
	}
}

// A stale hash pin stops the run. That is the property that makes the pin
// worth having: replacing the binary at the pinned path does not inherit the
// grant.
func TestHandleCLIRunRefusesWhenThePinnedBinaryChanged(t *testing.T) {
	registryPath := cliTestHome(t)
	entry := helperEntry(t, "args")
	entry.Executable.SHA256 = strings.Repeat("a", 64)
	writeCLIRegistry(t, registryPath, &cliRegistry{Version: 1, CLIs: []cliEntry{*entry}})

	d, capture := cliReportDaemon(t)
	d.handleCLIRun(context.Background(), Runtime{ID: "rt-1"}, PendingCLIRun{
		ID:     "run-3",
		CLIKey: "helper",
	})

	_, payload := capture.snapshot()
	if payload["status"] != "failed" {
		t.Fatalf("expected failed, got %v", payload["status"])
	}
	if !strings.Contains(payload["error"].(string), "hash mismatch") {
		t.Fatalf("unexpected error %v", payload["error"])
	}
}

// The registry listing reaches the server redacted — no path, no hash, no env.
func TestHandleCLIRegistryListReportsRedactedEntries(t *testing.T) {
	registryPath := cliTestHome(t)
	entry := helperEntry(t, "args", cliParam{
		Name: "command", Type: "enum", Required: true, Values: []string{"status"},
	})
	entry.Env = map[string]string{"ZHIHU_ACCESS_SECRET": "must-not-leave-the-machine"}
	writeCLIRegistry(t, registryPath, &cliRegistry{Version: 1, CLIs: []cliEntry{*entry}})

	d, capture := cliReportDaemon(t)
	d.handleCLIRegistryList(context.Background(), Runtime{ID: "rt-1"}, "req-1")

	paths, payload := capture.snapshot()
	if len(paths) != 1 || !strings.HasSuffix(paths[0], "/api/daemon/runtimes/rt-1/clis/req-1/result") {
		t.Fatalf("unexpected report path %v", paths)
	}
	if payload["status"] != "completed" {
		t.Fatalf("expected completed, got %v", payload["status"])
	}
	// The panel needs the registry path to answer "where do I add entries",
	// and it is machine configuration rather than a secret.
	if payload["registry_path"] != registryPath {
		t.Fatalf("expected registry_path %q, got %v", registryPath, payload["registry_path"])
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	blob := string(raw)
	for _, forbidden := range []string{
		"must-not-leave-the-machine",
		"ZHIHU_ACCESS_SECRET",
		entry.Executable.SHA256,
		entry.Executable.Path,
	} {
		if strings.Contains(blob, forbidden) {
			t.Fatalf("registry report leaked %q: %s", forbidden, blob)
		}
	}
	entries, ok := payload["clis"].([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("expected one entry, got %v", payload["clis"])
	}
	first := entries[0].(map[string]any)
	if first["key"] != "helper" || first["available"] != true {
		t.Fatalf("unexpected entry summary %v", first)
	}
}

// A missing registry file is an empty registry, not a failure — a machine with
// nothing registered must still render the panel (with its "add an entry"
// hint) rather than an error.
func TestHandleCLIRegistryListOnAMachineWithNoRegistry(t *testing.T) {
	cliTestHome(t) // points HOME at a temp dir with no clis.json

	d, capture := cliReportDaemon(t)
	d.handleCLIRegistryList(context.Background(), Runtime{ID: "rt-1"}, "req-1")

	_, payload := capture.snapshot()
	if payload["status"] != "completed" {
		t.Fatalf("expected completed, got %v (%v)", payload["status"], payload["error"])
	}
	entries, ok := payload["clis"].([]any)
	if !ok || len(entries) != 0 {
		t.Fatalf("expected an empty entry list, got %v", payload["clis"])
	}
}
