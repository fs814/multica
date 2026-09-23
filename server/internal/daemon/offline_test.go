package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
)

func offlineTestDaemon(t *testing.T) *Daemon {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("MULTICA_TASK_CONFIG_ROOT", "")
	t.Setenv("MULTICA_LAUNCHED_BY", "")
	return New(Config{AllowOffline: true, Profile: "desktop-offline-test", DaemonID: "offline-test",
		WorkspacesRoot: t.TempDir(), Agents: map[string]AgentEntry{}, NoTaskClaims: true}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func awaitOfflineHealth(t *testing.T, url string, check func(HealthResponse) bool) HealthResponse {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url + "/health")
		if err == nil {
			var health HealthResponse
			err = json.NewDecoder(resp.Body).Decode(&health)
			resp.Body.Close()
			if err == nil && check(health) {
				return health
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("daemon did not reach expected local health state")
	return HealthResponse{}
}

func TestOfflineDaemonRunsWithoutCenterAndShutsDown(t *testing.T) {
	d := offlineTestDaemon(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	d.cfg.HealthPort = ln.Addr().(*net.TCPAddr).Port
	url := "http://" + ln.Addr().String()
	ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("daemon failed to stop")
		}
	})
	health := awaitOfflineHealth(t, url, func(h HealthResponse) bool { return h.OfflineReason == "unconfigured" })
	if health.Status != "running" || health.PID != os.Getpid() || health.ServerURL != "" || health.TaskReady || health.CenterConnected || health.ActiveTaskCount != 0 {
		t.Fatalf("incorrect offline health: %+v", health)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".multica", "center.json")); !os.IsNotExist(err) {
		t.Fatal("daemon invented a center")
	}
	resp, err := http.Post(url+"/shutdown", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("shutdown: %v", err)
		}
		done <- err
	case <-time.After(3 * time.Second):
		t.Fatal("local shutdown did not stop daemon")
	}
}

func TestOfflineDaemonConnectsAfterConfigurationAndServerRecovery(t *testing.T) {
	d := offlineTestDaemon(t)
	var requests atomic.Int32
	var available atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer offline-fixture" {
			t.Error("wrong credential")
		}
		w.Header().Set("Content-Type", "application/json")
		if !available.Load() {
			w.WriteHeader(503)
			io.WriteString(w, `{"error":"offline"}`)
			return
		}
		if r.URL.Path == "/api/daemon/workspaces" {
			io.WriteString(w, `[]`)
		} else {
			io.WriteString(w, `{"renewed":false}`)
		}
	}))
	defer server.Close()
	d.offline.Store(&offlineState{Reason: "unconfigured"})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	url := "http://" + ln.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.serveHealth(ctx, ln, time.Now())
	done := make(chan error, 1)
	go func() { done <- d.connectWhenConfigured(ctx, 10*time.Millisecond) }()
	awaitOfflineHealth(t, url, func(h HealthResponse) bool { return h.OfflineReason == "unconfigured" })
	if requests.Load() != 0 {
		t.Fatal("network access before center configured")
	}
	centerPath := filepath.Join(os.Getenv("HOME"), ".multica", "center.json")
	if err := os.MkdirAll(filepath.Dir(centerPath), 0o700); err != nil {
		t.Fatal(err)
	}
	saved, _ := json.Marshal(map[string]any{"version": 1, "url": server.URL, "profile": d.cfg.Profile})
	if err := os.WriteFile(centerPath, saved, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cli.SaveCLIConfigForProfile(cli.CLIConfig{ServerURL: server.URL}, d.cfg.Profile); err != nil {
		t.Fatal(err)
	}
	awaitOfflineHealth(t, url, func(h HealthResponse) bool { return h.OfflineReason == "unauthenticated" })
	if requests.Load() != 0 {
		t.Fatal("network access before login")
	}
	if err := cli.SaveCLIConfigForProfile(cli.CLIConfig{ServerURL: "http://old.invalid", Token: "wrong-center"}, d.cfg.Profile); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if requests.Load() != 0 {
		t.Fatal("sent old Center credentials to the new Center")
	}
	if err := cli.SaveCLIConfigForProfile(cli.CLIConfig{ServerURL: server.URL, Token: "offline-fixture"}, d.cfg.Profile); err != nil {
		t.Fatal(err)
	}
	awaitOfflineHealth(t, url, func(h HealthResponse) bool { return h.OfflineReason == "unreachable" })
	available.Store(true)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("did not recover")
	}
	health := awaitOfflineHealth(t, url, func(h HealthResponse) bool { return h.OfflineReason == "" })
	if health.PID != os.Getpid() || health.ServerURL != server.URL || health.CenterConnected {
		t.Fatalf("incorrect transition: %+v", health)
	}
}
