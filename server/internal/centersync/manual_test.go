package centersync

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	ws "github.com/multica-ai/multica/server/internal/worksync"
)

func fixture(t *testing.T) (*Handler, request) {
	t.Helper()
	h, err := New(nil, Config{Owner: uuid.NewString(), Root: filepath.Join(t.TempDir(), "private"), Origin: "https://destination.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	actor := uuid.NewString()
	return h, request{Source: "https://source.example.test", Scope: ws.Scope{Workspace: uuid.NewString(), Group: uuid.NewString(), Epoch: uuid.NewString()}, Principal: ws.Principal{Account: actor, Actor: actor, Node: h.node}}
}

func TestManualConfigurationFailsClosed(t *testing.T) {
	t.Setenv("MULTICA_CENTER_SYNC_ENABLED", "")
	if h, err := NewFromEnvironment(nil); h != nil || err != nil {
		t.Fatalf("disabled: %v %v", h, err)
	}
	valid := Config{Owner: uuid.NewString(), Root: t.TempDir(), Origin: "https://center.example.test"}
	for _, change := range []func(*Config){
		func(c *Config) { c.Owner = "invalid" },
		func(c *Config) { c.Root = "relative" },
		func(c *Config) { c.Origin = "http://center.example.test" },
		func(c *Config) { c.Origin = "https://user:password@center.example.test" },
		func(c *Config) { c.Origin = "https://center.example.test/path" },
	} {
		c := valid
		change(&c)
		if _, err := New(nil, c); err == nil {
			t.Fatalf("accepted invalid config: %+v", c)
		}
	}
}

func TestManualRejectsWrongActorBeforeDatabaseAccess(t *testing.T) {
	h, _ := fixture(t)
	for _, tc := range []struct {
		name, user, actor, method string
		status                    int
	}{
		{"missing", "", "", "POST", 403},
		{"other user", uuid.NewString(), "", "POST", 403},
		{"machine", h.config.Owner, "task_token", "POST", 403},
		{"cloud PAT", h.config.Owner, "cloud_pat", "POST", 403},
		{"read method", h.config.Owner, "", "GET", 405},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, Prefix+"/info", strings.NewReader("{}"))
			r.Header.Set("X-User-ID", tc.user)
			r.Header.Set("X-Actor-Source", tc.actor)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("missing no-store")
			}
		})
	}
}

func TestManualReplicaPersistsAndRejectsUnsafeChanges(t *testing.T) {
	h, input := fixture(t)
	record := ws.Record{Kind: "issue", ID: uuid.NewString(), Version: 1, Fields: ws.Fields{"title": json.RawMessage(`"original"`)}}
	batch := ws.Batch{Schema: ws.Schema, Scope: input.Scope, Snapshot: true, Cursor: 1, Records: []ws.Record{record}}
	batch.Seal()
	input.Batch = &batch
	if _, err := h.replica(input, "apply"); err != nil {
		t.Fatal(err)
	}
	input.Kind, input.Entity = "issue", record.ID
	input.Patch = ws.Fields{"title": json.RawMessage(`"edited"`)}
	if _, err := h.replica(input, "edit"); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(nil, h.config)
	if err != nil {
		t.Fatal(err)
	}
	result, err := restarted.replica(input, "replica")
	if err != nil {
		t.Fatal(err)
	}
	state := result.(ws.ReplicaState)
	if state.Cursor != 1 || len(state.Records) != 1 || len(state.Outbox) != 1 {
		t.Fatalf("lost durable state: %+v", state)
	}
	input.Patch = ws.Fields{"status": json.RawMessage(`"done"`)}
	if _, err := h.replica(input, "edit"); !errors.Is(err, ws.ErrOperation) {
		t.Fatalf("execution field accepted: %v", err)
	}
	input.Principal.Node = "different-node"
	if _, err := h.replica(input, "replica"); !errors.Is(err, ws.ErrScope) {
		t.Fatalf("wrong node accepted: %v", err)
	}
}

func TestManualReplicaRejectsSameSourceAndPublicDirectory(t *testing.T) {
	h, input := fixture(t)
	input.Source = h.config.Origin
	if _, err := h.replica(input, "replica"); !errors.Is(err, ws.ErrScope) {
		t.Fatalf("self-sync accepted: %v", err)
	}
	input.Source = "https://source.example.test"
	if err := os.Mkdir(h.config.Root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(h.config.Root, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := h.replica(input, "replica"); err == nil {
		t.Fatal("public state directory accepted")
	}
}

func TestManualErrorsDoNotExposeInternalDetails(t *testing.T) {
	w := httptest.NewRecorder()
	respondError(w, errors.New("private database details"))
	if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "private database") {
		t.Fatalf("unsafe response: %d %s", w.Code, w.Body.String())
	}
}
