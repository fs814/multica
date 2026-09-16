package daemon

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/projectmemory"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProjectMemoryRuntimeHTTPAndUnavailable(t *testing.T) {
	ws, project := uuid.NewString(), uuid.NewString()
	task := Task{ID: uuid.NewString(), WorkspaceID: ws, ProjectID: project, AuthToken: "mat_fixture", ProjectMemory: &projectmemory.Context{WorkspaceID: ws, ProjectID: project, BindingRevision: 1}}
	b := projectmemory.Binding{WorkspaceID: ws, ProjectID: project, OwnerDaemonID: uuid.NewString(), Revision: 1, ContentRevision: 3, Generation: uuid.NewString(), State: "ready"}
	base := "/api/projects/" + project + "/memory"
	unavailable := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mat_fixture" || r.Header.Get("X-Workspace-ID") != ws {
			t.Error("runtime lost task/workspace authentication")
			http.Error(w, "forbidden", 403)
			return
		}
		switch r.URL.Path {
		case base:
			json.NewEncoder(w).Encode(b)
		case base + "/operations":
			var op projectmemory.Operation
			if err := json.NewDecoder(r.Body).Decode(&op); err != nil {
				t.Error(err)
			}
			if op.Action != "read" || op.ExpectedRevision != 3 || op.ExpectedBindingRevision != 1 {
				t.Errorf("wrong operation %+v", op)
			}
			json.NewEncoder(w).Encode(projectmemory.Work{ID: "fixture-request", Status: "pending"})
		case base + "/operations/fixture-request":
			receipt := projectmemory.Receipt{Status: "done", Result: &projectmemory.Result{Snapshot: &projectmemory.Snapshot{WorkspaceID: ws, ProjectID: project, BindingRevision: 1, ContentRevision: 3, Files: map[string]string{"README.md": "current project"}}}}
			if unavailable {
				receipt = projectmemory.Receipt{Status: "unavailable", Result: &projectmemory.Result{Error: "owner offline"}}
			}
			json.NewEncoder(w).Encode(receipt)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	d := &Daemon{client: NewClient(server.URL), cfg: Config{DaemonID: uuid.NewString()}}
	snapshot, err := d.loadProjectMemory(context.Background(), task)
	if err != nil || snapshot.Files["README.md"] != "current project" {
		t.Fatalf("load: %+v %v", snapshot, err)
	}
	unavailable = true
	if _, err = d.loadProjectMemory(context.Background(), task); err == nil {
		t.Fatal("offline owner silently replaced")
	}
	task.ProjectMemory = nil
	if _, err = d.loadProjectMemory(context.Background(), task); err == nil {
		t.Fatal("missing server capability accepted")
	}
}
