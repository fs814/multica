package workflow

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"sync"
	"testing"
)

func TestInstanceImagesAndConcurrentRuns(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()
	t.Cleanup(func() {
		env.pool.Exec(ctx, "DELETE FROM workflow_input_instance WHERE workspace_id=$1", env.workspaceID)
	})
	image := func() string {
		t.Helper()
		var id string
		err := env.pool.QueryRow(ctx, `INSERT INTO attachment(workspace_id,uploader_type,uploader_id,filename,url,content_type,size_bytes) VALUES($1,'member',$2,'input.png','https://storage.example.test/temporary','image/png',1) RETURNING id::text`, env.workspaceID, env.userID).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	a, b := image(), image()
	def, err := ParseDefinition([]byte(`{"schema_version":1,"entry_node":"input","nodes":[{"key":"input","type":"input","input_mode":"image","next":["end"]},{"key":"end","type":"end"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	def.Nodes[0].ImageAttachmentID = a
	env.publishTemplate(t, def)
	input := []byte(`{"title":"Task","description":"Image scenario"}`)
	create := func(name, id string) db.WorkflowInputInstance {
		t.Helper()
		row, err := env.q.CreateWorkflowInputInstance(ctx, db.CreateWorkflowInputInstanceParams{
			WorkspaceID: env.workspaceID, TemplateID: env.templateID, TemplateVersionID: env.versionID,
			Name: name, Input: input, CreatedByID: env.userID, ImageAttachmentID: pgtype.Text{String: id, Valid: true},
		})
		if err != nil {
			t.Fatal(err)
		}
		return row
	}
	first, second := create("A", a), create("B", b)
	start := func(row db.WorkflowInputInstance, key string) (*StartRunResult, error) {
		return env.engine.StartRun(ctx, StartRunInput{WorkspaceID: env.workspaceID, TemplateID: env.templateID, Source: "manual", IdempotencyKey: key,
			AccountableUserID: env.userID, ActorType: "member", ActorID: env.userID,
			Instance: &InstanceStart{ID: row.ID, Revision: row.Revision, Mode: "saved"},
			Issue:    &RunIssueInput{CreatorType: "member", CreatorID: env.userID},
		})
	}
	one, err := start(first, "image-a")
	if err != nil {
		t.Fatal(err)
	}
	two, err := start(second, "image-b")
	if err != nil {
		t.Fatal(err)
	}
	if imageAttachmentFromRunContext(one.Run.Context).ID != a || imageAttachmentFromRunContext(two.Run.Context).ID != b {
		t.Fatal("instances did not use independent images")
	}
	var contextValues map[string]any
	json.Unmarshal(two.Run.Context, &contextValues)
	if _, exists := contextValues["url"]; exists {
		t.Fatal("temporary storage URL leaked into snapshot")
	}
	var wg sync.WaitGroup
	ids := make(chan pgtype.UUID, 6)
	errs := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := start(first, "concurrent")
			if err != nil {
				errs <- err
			} else {
				ids <- result.Run.ID
			}
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	var original pgtype.UUID
	for id := range ids {
		if original.Valid && original != id {
			t.Fatal("concurrent retry duplicated a run")
		}
		original = id
	}
	// Removing a reference after saving must refuse a new run before any issue/run is committed.
	if _, err := env.pool.Exec(ctx, "DELETE FROM attachment WHERE id=$1 AND workspace_id=$2", b, env.workspaceID); err != nil {
		t.Fatal(err)
	}
	var before, after int
	env.pool.QueryRow(ctx, "SELECT count(*) FROM workflow_run WHERE workspace_id=$1", env.workspaceID).Scan(&before)
	if _, err := start(second, "deleted-image"); err == nil {
		t.Fatal("deleted image was accepted")
	}
	env.pool.QueryRow(ctx, "SELECT count(*) FROM workflow_run WHERE workspace_id=$1", env.workspaceID).Scan(&after)
	if before != after {
		t.Fatal("rejected image start persisted a run")
	}
	old, err := env.q.GetWorkflowRun(ctx, db.GetWorkflowRunParams{ID: two.Run.ID, WorkspaceID: env.workspaceID})
	if err != nil || imageAttachmentFromRunContext(old.Context).ID != b {
		t.Fatal("resource deletion changed historical snapshot")
	}
}
func TestInstanceInputSchemaCompatibility(t *testing.T) {
	a := &Node{Key: "input", Type: NodeTypeInput}
	b := &Node{Key: "input", Type: NodeTypeInput, InputFields: []InputField{}}
	if !SameInputSchema(a, b) {
		t.Fatal("empty declarations should normalize")
	}
	b.InputMode = InputModeImage
	if SameInputSchema(a, b) {
		t.Fatal("mode change must require review")
	}
}
