package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/worksync"
)

func TestWorkSyncTransportNeverRedirectsOrLeaksCredentials(t *testing.T) {
	var hits atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer receiver.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, receiver.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	transport, err := NewHTTPWorkSyncTransport(redirect.URL, func() (string, error) { return "mdt_test", nil })
	if err != nil {
		t.Fatal(err)
	}
	err = transport.Handshake(context.Background(), worksync.Principal{}, worksync.Scope{})
	if !errors.Is(err, worksync.ErrScope) || hits.Load() != 0 {
		t.Fatalf("redirect followed: %v %d", err, hits.Load())
	}
	for _, base := range []string{"http://example.test", "https://user:password@example.test", "https://example.test/?token=secret", "https://example.test/other"} {
		if _, err := NewHTTPWorkSyncTransport(base, func() (string, error) { return "mdt_test", nil }); err == nil {
			t.Fatal("unsafe origin accepted")
		}
	}
}

func TestWorkSyncLifecyclePersistsAndJoinsWithoutTaskExecution(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows sync activation requires a future credential ACL implementation")
	}
	scope := worksync.Scope{Workspace: uuid.NewString(), Group: uuid.NewString(), Epoch: uuid.NewString()}
	actor, node, entity := uuid.NewString(), uuid.NewString(), uuid.NewString()
	principal := worksync.Principal{Account: actor, Actor: actor, Node: node}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer mdt_fixture" {
			t.Error("wrong credential")
		}
		switch r.URL.Path {
		case worksync.HTTPPrefix + "handshake":
			_ = json.NewEncoder(w).Encode(worksync.Handshake{Schema: worksync.Schema, Scope: scope, Principal: principal, MaxBatch: worksync.MaxBatch})
		case worksync.HTTPPrefix + "pull":
			var req worksync.Request
			_ = json.NewDecoder(r.Body).Decode(&req)
			b := worksync.Batch{Schema: worksync.Schema, Scope: scope, From: req.Cursor, Cursor: 1, Snapshot: req.Snapshot, Records: []worksync.Record{}}
			if req.Snapshot {
				b.Records = []worksync.Record{{Kind: "issue", ID: entity, Version: 1, Fields: worksync.Fields{"title": json.RawMessage(`"persisted"`)}}}
			}
			b.Seal()
			_ = json.NewEncoder(w).Encode(b)
		default:
			t.Errorf("unexpected execution/API path %s", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	root := t.TempDir()
	tokenFile := filepath.Join(root, "credential")
	if err := os.WriteFile(tokenFile, []byte("mdt_fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{ServerBaseURL: server.URL, DaemonID: node, WorkSync: &WorkSyncSettings{Root: root, Targets: []WorkSyncTarget{{Scope: scope, Actor: actor, TokenFile: tokenFile}}}}
	d := &Daemon{cfg: cfg, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), reconcile: newReconcileBroadcaster()}
	stop, err := d.startWorkSync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Wait for a durable checkpoint rather than interpreting an HTTP request as
	// successful storage; stopping must join before the restart can take its lock.
	deadline := time.Now().Add(3 * time.Second)
	found := false
	for time.Now().Before(deadline) {
		files, _ := filepath.Glob(filepath.Join(root, "*", "work-replicas", "v1", "*", "checkpoint.json"))
		if len(files) == 1 {
			data, _ := os.ReadFile(files[0])
			var envelope struct {
				State worksync.ReplicaState `json:"state"`
			}
			if json.Unmarshal(data, &envelope) == nil && envelope.State.Initialized {
				found = true
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	stop()
	if !found {
		t.Fatal("lifecycle did not persist projection")
	}
	first := requests.Load()
	stop, err = d.startWorkSync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for requests.Load() == first && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	stop()
	if requests.Load() == first {
		t.Fatal("restart did not resume")
	}
	// Disabled configuration neither opens checkpoints nor sends requests.
	before := requests.Load()
	d.cfg.WorkSync = nil
	stop, err = d.startWorkSync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stop()
	if requests.Load() != before {
		t.Fatal("disabled lifecycle sent a request")
	}
}

func TestWorkSyncConfigurationFailClosed(t *testing.T) {
	t.Setenv("MULTICA_WORK_SYNC_ENABLED", "")
	t.Setenv("MULTICA_WORK_SYNC_CONFIG", "missing")
	if cfg, err := loadWorkSyncSettings(); err != nil || cfg != nil {
		t.Fatal("default configuration enabled")
	}
	t.Setenv("MULTICA_WORK_SYNC_ENABLED", "1")
	if _, err := loadWorkSyncSettings(); err == nil {
		t.Fatal("enabled without explicit targets")
	}
	root := t.TempDir()
	file := filepath.Join(root, "token")
	if err := os.WriteFile(file, []byte("mdt_fixture"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readWorkSyncToken(file); err == nil {
		t.Fatal("world-readable credential accepted")
	}
	if err := os.Chmod(file, 0600); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		if _, err := readWorkSyncToken(file); !errors.Is(err, worksync.ErrDisabled) {
			t.Fatal("Windows credential ACL gate did not fail closed")
		}
		return
	}
	if value, err := readWorkSyncToken(file); err != nil || value != "mdt_fixture" {
		t.Fatal("private credential rejected")
	}
}

func TestWorkSyncTransportStatusClassification(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   error
	}{
		{401, worksync.ErrUnauthenticated}, {403, worksync.ErrDenied}, {404, worksync.ErrDisabled},
		{409, worksync.ErrScope}, {429, ErrSyncUnavailable}, {503, ErrSyncUnavailable}, {507, worksync.ErrLimit},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status) }))
			defer server.Close()
			transport, err := NewHTTPWorkSyncTransport(server.URL, func() (string, error) { return "mdt_fixture", nil })
			if err != nil {
				t.Fatal(err)
			}
			if err = transport.Handshake(context.Background(), worksync.Principal{}, worksync.Scope{}); !errors.Is(err, tc.want) {
				t.Fatalf("status %d: %v", tc.status, err)
			}
		})
	}
}
