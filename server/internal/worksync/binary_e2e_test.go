package worksync_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/daemon"
	ws "github.com/multica-ai/multica/server/internal/worksync"
)

// Explicit opt-in uses full cmd/server and cmd/multica binaries built from the
// same checkout. No real agent executable or real profile is discovered. The
// ordinary daemon stays in allow-offline mode with no task claims; its actual
// startup hook owns the sync loop throughout, rather than a test-created client.
func TestWorkSyncFullBinariesRecovery(t *testing.T) {
	centerBin, cliBin := os.Getenv("WORK_SYNC_CENTER_BINARY"), os.Getenv("WORK_SYNC_DAEMON_BINARY")
	if centerBin == "" || cliBin == "" {
		t.Skip("requires explicitly built Center and daemon binaries")
	}
	r := setup(t)
	ctx := context.Background()
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	logs := os.Getenv("WORK_SYNC_BINARY_LOG_DIR")
	if logs == "" {
		logs = root
	}
	if err := os.MkdirAll(logs, 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	env := []string{}
	for _, kv := range os.Environ() {
		key := strings.SplitN(kv, "=", 2)[0]
		if strings.HasPrefix(key, "MULTICA_") || strings.HasPrefix(key, "REDIS_") || strings.HasPrefix(key, "WORK_SYNC_") || key == "PORT" || key == "MAINTENANCE_PORT" || key == "DATABASE_READ_URL" {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "MULTICA_WORK_SYNC_ENABLED=1", "DO_NOT_TRACK=1", "APP_ENV=test", "MAINTENANCE_PORT=0")
	type child struct {
		cmd     *exec.Cmd
		stopped bool
	}
	sequence := 0
	start := func(name, bin string, extra []string, args ...string) *child {
		t.Helper()
		sequence++
		logfile := filepath.Join(logs, fmt.Sprintf("%02d-%s.log", sequence, name))
		out, e := os.Create(logfile)
		if e != nil {
			t.Fatal(e)
		}
		c := exec.Command(bin, args...)
		c.Env = append(append([]string{}, env...), extra...)
		c.Dir = root
		c.Stdout = out
		c.Stderr = out
		if e = c.Start(); e != nil {
			_ = out.Close()
			t.Fatal(e)
		}
		_ = out.Close()
		t.Logf("started %s pid=%d", name, c.Process.Pid)
		return &child{cmd: c}
	}
	stop := func(c *child) {
		t.Helper()
		if c == nil || c.stopped {
			return
		}
		// The invoking harness supplies the current workspace-daemon PID obtained
		// with `multica daemon status --output json`; never terminate that process.
		protected, e := strconv.Atoi(os.Getenv("WORK_SYNC_PROTECTED_DAEMON_PID"))
		if e != nil || protected == c.cmd.Process.Pid {
			t.Fatal("missing or unsafe daemon PID guard")
		}
		if e = c.cmd.Process.Signal(syscall.SIGTERM); e != nil && e != os.ErrProcessDone {
			t.Fatal(e)
		}
		done := make(chan error, 1)
		go func() { done <- c.cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			_ = c.cmd.Process.Kill()
			<-done
			t.Error("fixture required forced termination")
		}
		c.stopped = true
	}
	center := start("center-old", centerBin, []string{fmt.Sprintf("PORT=%d", port)})
	t.Cleanup(func() { stop(center) })
	wait := func(label string, check func() bool) {
		t.Helper()
		deadline := time.Now().Add(80 * time.Second)
		for time.Now().Before(deadline) {
			if check() {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("timed out: %s; inspect binary logs", label)
	}
	wait("Center socket", func() bool {
		conn, e := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if e != nil {
			return false
		}
		_ = conn.Close()
		return true
	})
	issue := r.fx.Issue(t, "binary initial")
	survivor := r.fx.Issue(t, "binary survivor")
	project := r.fx.Project(t, "binary project")
	agent := r.fx.Agent(t, "binary agent", "")
	enroll(t, r)
	type node struct {
		id, profile, root, config, token string
		cfg                              ws.ReplicaConfig
		proc                             *child
	}
	nodes := []*node{}
	origin := sha256.Sum256([]byte(base))
	for i := 0; i < 2; i++ {
		n := &node{id: uuid.NewString(), profile: fmt.Sprintf("sync-%s-%d", uuid.NewString(), i), root: filepath.Join(root, fmt.Sprint(i))}
		if err = os.MkdirAll(n.root, 0700); err != nil {
			t.Fatal(err)
		}
		n.token = filepath.Join(n.root, "token")
		if err = os.WriteFile(n.token, []byte(httpGrant(t, r, n.id)), 0600); err != nil {
			t.Fatal(err)
		}
		n.config = filepath.Join(n.root, "sync.json")
		n.cfg = ws.ReplicaConfig{Enabled: true, Root: filepath.Join(n.root, "replicas", hex.EncodeToString(origin[:])), Scope: r.scope, Principal: ws.Principal{Account: r.fx.UserID, Actor: r.fx.UserID, Node: n.id}}
		// Named, uniquely owned fixture profiles avoid changing HOME or reading
		// the user's default profile. TASK_CONFIG_ROOT intentionally forbids daemon
		// lifecycle commands, so it cannot be used to launch a full binary.
		home, e := os.UserHomeDir()
		if e != nil {
			t.Fatal(e)
		}
		profileDir := filepath.Join(home, ".multica", "profiles", n.profile)
		if e = os.Mkdir(profileDir, 0700); e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(profileDir, "config.json"), []byte(`{}`), 0600); e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() {
			// Registered before process cleanup: LIFO stops the child first.
			_ = filepath.WalkDir(profileDir, func(path string, d os.DirEntry, err error) error {
				if err == nil && !d.IsDir() && strings.HasSuffix(path, ".log") {
					data, _ := os.ReadFile(path)
					_ = os.WriteFile(filepath.Join(logs, n.id+"-"+filepath.Base(path)), data, 0600)
				}
				return nil
			})
			_ = os.RemoveAll(profileDir)
		})
		nodes = append(nodes, n)
	}
	startNode := func(n *node) {
		t.Helper()
		settings := daemon.WorkSyncSettings{Root: filepath.Join(n.root, "replicas"), Targets: []daemon.WorkSyncTarget{{Scope: n.cfg.Scope, Actor: r.fx.UserID, TokenFile: n.token}}}
		data, _ := json.Marshal(settings)
		if err = os.WriteFile(n.config, data, 0600); err != nil {
			t.Fatal(err)
		}
		extra := []string{"MULTICA_WORK_SYNC_CONFIG=" + n.config}
		// Every built-in CLI has an explicit missing path, including aliases whose
		// environment prefix differs from the executable name. No PATH fallback.
		for _, name := range []string{"CLAUDE", "CODEX", "OPENCODE", "CODEARTS", "DEVECO", "OPENCLAW", "HERMES", "PI", "OMP", "CURSOR", "COPILOT", "KIMI", "REASONIX", "DSH", "KIRO", "CODEBUDDY", "ANTIGRAVITY", "QODER", "QODERCLICN", "TRAECLI", "GROK", "QWEN", "QWENPAW", "DIM", "MCODE", "ZEROCLAW", "KNOT", "KNOT_HTTP"} {
			extra = append(extra, "MULTICA_"+name+"_PATH="+filepath.Join(root, "absent-"+name))
		}
		n.proc = start("daemon-"+n.id, cliBin, extra, "daemon", "start", "--foreground", "--allow-offline", "--no-task-claims", "--no-auto-update", "--no-auto-reload", "--profile", n.profile, "--daemon-id", n.id, "--server-url", base, "--workspaces-root", filepath.Join(n.root, "workspaces"))
	}
	for _, n := range nodes {
		t.Cleanup(func() { stop(n.proc) })
		startNode(n)
	}
	read := func(n *node) (ws.ReplicaState, bool) {
		var state ws.ReplicaState
		files, _ := filepath.Glob(filepath.Join(n.cfg.Root, "work-replicas", "v1", "*", "checkpoint.json"))
		for _, file := range files {
			data, e := os.ReadFile(file)
			if e != nil {
				continue
			}
			var envelope struct {
				State ws.ReplicaState `json:"state"`
			}
			if json.Unmarshal(data, &envelope) == nil && envelope.State.Scope == n.cfg.Scope {
				state = envelope.State
				return state, state.Initialized
			}
		}
		return state, false
	}
	wait("two binary daemon snapshots", func() bool {
		for _, n := range nodes {
			s, ok := read(n)
			if !ok || len(s.Records) != 4 {
				return false
			}
		}
		return true
	})
	// Edit through the existing internal replica API while each process is
	// stopped; no offline UI/API is claimed by this binary integration test.
	for _, n := range nodes {
		stop(n.proc)
		rep, e := ws.OpenReplica(n.cfg)
		if e != nil {
			t.Fatal(e)
		}
		queue(t, rep, "issue", issue, "title", "local "+n.id)
		queue(t, rep, "project", project, "description", "project "+n.id)
		queue(t, rep, "agent", agent, "description", "agent "+n.id)
		_ = rep.Close()
	}
	stop(center)
	startNode(nodes[0])
	// With the Center process stopped, even a retrying full daemon must retain
	// all three edits durably. Then restart the same origin and let Run reconnect.
	time.Sleep(750 * time.Millisecond)
	offlineState, ok := read(nodes[0])
	if !ok || len(offlineState.Outbox) != 3 {
		t.Fatal("offline queue lost")
	}
	center = start("center-reconnected", centerBin, []string{fmt.Sprintf("PORT=%d", port)})
	wait("first binary pushes", func() bool { s, ok := read(nodes[0]); return ok && len(s.Outbox) == 0 })
	startNode(nodes[1])
	wait("second binary retains conflicts", func() bool { s, ok := read(nodes[1]); return ok && len(s.Outbox) == 0 && len(s.Review) == 3 })
	stop(nodes[1].proc)
	rep, e := ws.OpenReplica(nodes[1].cfg)
	if e != nil {
		t.Fatal(e)
	}
	queue(t, rep, "issue", issue, "description", "offline deletion edit")
	_ = rep.Close()
	r.fx.Exec(t, `DELETE FROM issue WHERE id=$1`, issue)
	startNode(nodes[1])
	wait("deletion conflict", func() bool {
		s, ok := read(nodes[1])
		return ok && s.Records["issue/"+issue].Deleted && len(s.Review) == 4
	})
	stop(nodes[0].proc)
	startNode(nodes[0])
	wait("delete propagation after restart", func() bool { s, ok := read(nodes[0]); return ok && s.Records["issue/"+issue].Deleted })
	// Fence the old complete Center process and retain both on-disk replicas.
	stop(center)
	for _, n := range nodes {
		stop(n.proc)
	}
	replicas := []*ws.Replica{}
	for _, n := range nodes {
		rep, e := ws.OpenReplica(n.cfg)
		if e != nil {
			t.Fatal(e)
		}
		replicas = append(replicas, rep)
	}
	bundles, plan := exportCopies(t, r, replicas)
	for _, rep := range replicas {
		_ = rep.Close()
	}
	recovery, _, fence := recoveryService(t, r, plan)
	// Isolation fixture verifies that the exact old process has exited. No
	// production fencing policy is inferred from this local process handle.
	recovery.VerifyFence = func(context.Context, ws.RecoveryPlan) error {
		if !center.stopped || center.cmd.ProcessState == nil || !center.cmd.ProcessState.Exited() {
			return ws.ErrDenied
		}
		if !fence.Load() {
			return ws.ErrDenied
		}
		return nil
	}
	report, e := recovery.Stage(ctx, r.principal, plan, bundles)
	if e != nil {
		t.Fatal(e)
	}
	if e = recovery.Activate(ctx, r.principal, plan, report.Digest); e == nil {
		t.Fatal("activation without fixture approval")
	}
	fence.Store(true)
	eraseSource(t, r)
	// A replacement Center gets independently provisioned runtime identities,
	// never the source Center's daemon tokens or machine registrations.
	r.fx.Exec(t, `DELETE FROM daemon_token WHERE workspace_id=$1`, r.scope.Workspace)
	r.fx.Exec(t, `DELETE FROM agent_runtime WHERE workspace_id=$1`, r.scope.Workspace)
	for i := 0; i < 2; i++ {
		if e = recovery.Activate(ctx, r.principal, plan, report.Digest); e != nil {
			t.Fatal(e)
		}
	}
	oldCenter := center
	center = start("center-recovered", centerBin, []string{fmt.Sprintf("PORT=%d", port)})
	wait("recovered Center socket", func() bool {
		conn, e := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if e != nil {
			return false
		}
		_ = conn.Close()
		return true
	})
	if !oldCenter.stopped {
		t.Fatal("old Center still active")
	}
	// Explicit fresh credentials and namespaces; never retarget old pending work.
	r.scope = plan.Target
	for _, n := range nodes {
		n.cfg.Scope = plan.Target
		if e = os.WriteFile(n.token, []byte(httpGrant(t, r, n.id)), 0600); e != nil {
			t.Fatal(e)
		}
		startNode(n)
	}
	wait("recovered epoch binary convergence", func() bool {
		a, ok := read(nodes[0])
		b, ok2 := read(nodes[1])
		return ok && ok2 && len(a.Records) == 4 && reflect.DeepEqual(a.Records, b.Records)
	})
	for _, n := range nodes {
		s, _ := read(n)
		if len(s.Outbox) != 0 || len(s.Review) != 0 || !s.Records["issue/"+issue].Deleted || string(s.Records["issue/"+survivor].Fields["title"]) != `"binary survivor"` {
			t.Fatal("new epoch replayed intent or resurrected deletion")
		}
	}
	if count := r.fx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1`, agent); count != 0 {
		t.Fatal("duplicate task execution")
	}
	if report.Conflicts != 4 {
		t.Fatalf("lost old conflict candidates: %d", report.Conflicts)
	}
	t.Logf("full binaries: 2 epochs, 2 durable daemon profiles, %d retained conflicts, %d records, zero execution tasks", report.Conflicts, len(report.Records))
}
