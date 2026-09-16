package projectmemory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Transactions interface {
	Begin(context.Context) (pgx.Tx, error)
}
type Coordinator struct{ DB Transactions }

func decodeBinding(raw []byte) (Binding, error) {
	var b Binding
	err := json.Unmarshal(raw, &b)
	return b, err
}
func binding(ctx context.Context, tx pgx.Tx, ws, project string) (Binding, error) {
	var raw []byte
	err := tx.QueryRow(ctx, "SELECT binding FROM project_memory_binding WHERE workspace_id=$1 AND project_id=$2 FOR UPDATE", ws, project).Scan(&raw)
	if err != nil {
		return Binding{}, err
	}
	return decodeBinding(raw)
}

// Scope locks the source row as well as its epoch, serializing publication with project changes.
func scope(ctx context.Context, tx pgx.Tx, c Context) (Context, error) {
	var project *string
	switch c.ScopeKind {
	case "issue", "chat_session":
		err := tx.QueryRow(ctx, "SELECT project_id::text FROM "+c.ScopeKind+" WHERE id=$1 AND workspace_id=$2 FOR SHARE", c.ScopeID, c.WorkspaceID).Scan(&project)
		if err != nil {
			return Context{}, err
		}
		if project == nil {
			c.ProjectID = ""
		} else {
			c.ProjectID = *project
		}
	case "task":
		var status string
		if err := tx.QueryRow(ctx, "SELECT status FROM agent_task_queue WHERE id=$1 FOR SHARE", c.ScopeID).Scan(&status); err != nil {
			return Context{}, err
		}
		c.Epoch = 1
		return c, nil
	default:
		return Context{}, errors.New("unknown memory task scope")
	}
	_, err := tx.Exec(ctx, "INSERT INTO project_memory_scope(workspace_id,scope_kind,scope_id,project_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING", c.WorkspaceID, c.ScopeKind, c.ScopeID, project)
	if err != nil {
		return Context{}, err
	}
	err = tx.QueryRow(ctx, "SELECT epoch FROM project_memory_scope WHERE workspace_id=$1 AND scope_kind=$2 AND scope_id=$3 FOR SHARE", c.WorkspaceID, c.ScopeKind, c.ScopeID).Scan(&c.Epoch)
	return c, err
}

func (s Coordinator) Claim(ctx context.Context, task string, c Context, initial Binding) (Context, Binding, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return c, initial, err
	}
	defer tx.Rollback(ctx)
	current, err := scope(ctx, tx, c)
	if err != nil {
		return c, initial, err
	}
	if current.ProjectID != c.ProjectID {
		return c, initial, errors.New("project changed while claiming task")
	}
	raw, _ := json.Marshal(initial)
	_, err = tx.Exec(ctx, "INSERT INTO project_memory_binding(workspace_id,project_id,binding) VALUES($1,$2,$3) ON CONFLICT DO NOTHING", c.WorkspaceID, c.ProjectID, raw)
	if err != nil {
		return c, initial, err
	}
	b, err := binding(ctx, tx, c.WorkspaceID, c.ProjectID)
	if err != nil {
		return c, b, err
	}
	current.BindingRevision = b.Revision
	raw, _ = json.Marshal(current)
	_, err = tx.Exec(ctx, "INSERT INTO project_memory_task(task_id,workspace_id,context) VALUES($1,$2,$3) ON CONFLICT DO NOTHING", task, c.WorkspaceID, raw)
	if err != nil {
		return c, b, err
	}
	var frozenRaw []byte
	err = tx.QueryRow(ctx, "SELECT context FROM project_memory_task WHERE task_id=$1", task).Scan(&frozenRaw)
	if err != nil {
		return c, b, err
	}
	var frozen Context
	if err = json.Unmarshal(frozenRaw, &frozen); err != nil {
		return c, b, err
	}
	if err = frozen.Authorize(current, b); err != nil {
		return c, b, err
	}
	return frozen, b, tx.Commit(ctx)
}

func authorize(ctx context.Context, tx pgx.Tx, task, ws, project string, b Binding) (*Context, error) {
	if task == "" {
		return nil, nil
	}
	var raw []byte
	err := tx.QueryRow(ctx, "SELECT m.context FROM project_memory_task m JOIN agent_task_queue t ON t.id=m.task_id WHERE m.task_id=$1 AND m.workspace_id=$2 AND t.status IN ('dispatched','running','waiting_local_directory') FOR SHARE OF t", task, ws).Scan(&raw)
	if err != nil {
		return nil, errors.New("task has no active project memory context")
	}
	var frozen Context
	if err = json.Unmarshal(raw, &frozen); err != nil {
		return nil, err
	}
	if frozen.ProjectID != project {
		return nil, errors.New("task cannot access another project's memory")
	}
	current, err := scope(ctx, tx, frozen)
	if err != nil {
		return nil, err
	}
	if err = frozen.Authorize(current, b); err != nil {
		return nil, err
	}
	return &frozen, nil
}

func (s Coordinator) Resolve(ctx context.Context, ws, project, task string) (Binding, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return Binding{}, err
	}
	defer tx.Rollback(ctx)
	// Lock scope before binding, matching Claim and the project update trigger.
	if task != "" {
		var raw []byte
		if err = tx.QueryRow(ctx, "SELECT context FROM project_memory_task WHERE task_id=$1 AND workspace_id=$2", task, ws).Scan(&raw); err != nil {
			return Binding{}, err
		}
		var c Context
		if err = json.Unmarshal(raw, &c); err != nil {
			return Binding{}, err
		}
		if _, err = scope(ctx, tx, c); err != nil {
			return Binding{}, err
		}
	}
	b, err := binding(ctx, tx, ws, project)
	if err != nil {
		return b, err
	}
	if _, err = authorize(ctx, tx, task, ws, project, b); err != nil {
		return b, err
	}
	return b, tx.Commit(ctx)
}

func (s Coordinator) Submit(ctx context.Context, ws, project, task string, op Operation, initial *Binding) (Work, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return Work{}, err
	}
	defer tx.Rollback(ctx)
	// Serialize scope before binding for the same lock order used at publication.
	if task != "" {
		var raw []byte
		if err = tx.QueryRow(ctx, "SELECT context FROM project_memory_task WHERE task_id=$1 AND workspace_id=$2", task, ws).Scan(&raw); err != nil {
			return Work{}, err
		}
		var c Context
		if err = json.Unmarshal(raw, &c); err != nil {
			return Work{}, err
		}
		if _, err = scope(ctx, tx, c); err != nil {
			return Work{}, err
		}
	}
	if initial != nil {
		if task != "" {
			return Work{}, errors.New("binding creation requires human authorization")
		}
		if err = ValidateBinding(*initial); err != nil {
			return Work{}, err
		}
		raw, _ := json.Marshal(initial)
		_, err = tx.Exec(ctx, "INSERT INTO project_memory_binding(workspace_id,project_id,binding) VALUES($1,$2,$3) ON CONFLICT DO NOTHING", ws, project, raw)
		if err != nil {
			return Work{}, err
		}
	}
	b, err := binding(ctx, tx, ws, project)
	if err != nil {
		return Work{}, err
	}
	frozen, err := authorize(ctx, tx, task, ws, project, b)
	if err != nil {
		return Work{}, err
	}
	if op.ExpectedBindingRevision != b.Revision || op.ExpectedRevision != b.ContentRevision {
		return Work{}, ErrConflict
	}
	switch op.Action {
	case "read", "init", "write", "delete", "migrate", "import":
	default:
		return Work{}, errors.New("invalid operation")
	}
	if op.Action == "write" || op.Action == "delete" {
		if err = ValidateName(op.Path); err != nil {
			return Work{}, err
		}
	}
	if op.Action == "import" {
		if err = validateFiles(op.Files); err != nil {
			return Work{}, err
		}
	}
	if len(op.Content) > MaxBytes {
		return Work{}, errors.New("memory content exceeds limit")
	}
	target := b
	if op.Action == "migrate" {
		if task != "" {
			return Work{}, errors.New("binding migration requires human authorization")
		}
		target.Revision++
		target.Backend = "managed"
		target.SourceRoot = ""
		if op.Destination != "" {
			target.Backend = "source"
			target.SourceRoot = op.Destination
		}
		if err = ValidateBinding(target); err != nil {
			return Work{}, err
		}
	}
	w := Work{ID: uuid.NewString(), Binding: b, Target: target, Context: frozen, Operation: op, Status: "pending"}
	raw, _ := json.Marshal(w)
	var taskArg any
	if task != "" {
		taskArg = task
	}
	_, err = tx.Exec(ctx, "INSERT INTO project_memory_request(id,workspace_id,project_id,owner_daemon_id,task_id,request) VALUES($1,$2,$3,$4,$5,$6)", w.ID, ws, project, b.OwnerDaemonID, taskArg, raw)
	if err != nil {
		return w, err
	}
	return w, tx.Commit(ctx)
}

func (s Coordinator) Next(ctx context.Context, ws, owner string) (*Work, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var raw []byte
	err = tx.QueryRow(ctx, `UPDATE project_memory_request SET status='processing' WHERE id=(SELECT id FROM project_memory_request WHERE workspace_id=$1 AND owner_daemon_id=$2 AND status='pending' AND expires_at>now() ORDER BY expires_at LIMIT 1 FOR UPDATE SKIP LOCKED) RETURNING request`, ws, owner).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var w Work
	if err = json.Unmarshal(raw, &w); err != nil {
		return nil, err
	}
	return &w, tx.Commit(ctx)
}

type Receipt struct {
	Status string  `json:"status"`
	Result *Result `json:"result,omitempty"`
}

func (s Coordinator) Receipt(ctx context.Context, ws, project, task, id string) (Receipt, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return Receipt{}, err
	}
	defer tx.Rollback(ctx)
	if task != "" {
		var frozenRaw []byte
		if err = tx.QueryRow(ctx, "SELECT context FROM project_memory_task WHERE task_id=$1 AND workspace_id=$2", task, ws).Scan(&frozenRaw); err != nil {
			return Receipt{}, err
		}
		var frozen Context
		if err = json.Unmarshal(frozenRaw, &frozen); err != nil {
			return Receipt{}, err
		}
		if _, err = scope(ctx, tx, frozen); err != nil {
			return Receipt{}, err
		}
	}
	b, err := binding(ctx, tx, ws, project)
	if err != nil {
		return Receipt{}, err
	}
	if _, err = authorize(ctx, tx, task, ws, project, b); err != nil {
		return Receipt{}, err
	}
	var status string
	var raw []byte
	var expires time.Time
	err = tx.QueryRow(ctx, "SELECT status,result,expires_at FROM project_memory_request WHERE id=$1 AND workspace_id=$2 AND project_id=$3 AND ($4::uuid IS NULL OR task_id=$4)", id, ws, project, nullable(task)).Scan(&status, &raw, &expires)
	if err != nil {
		return Receipt{}, err
	}
	result := Receipt{Status: status}
	if raw != nil {
		var r Result
		if err = json.Unmarshal(raw, &r); err != nil {
			return result, err
		}
		result.Result = &r
	}
	if (status == "pending" || status == "processing") && time.Now().After(expires) {
		result.Status = "unavailable"
		result.Result = &Result{Error: "owner unavailable or request expired; no replica was selected"}
	}
	return result, nil
}
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Complete chooses the candidate in one database transaction; stale or expired work never publishes.
func (s Coordinator) Complete(ctx context.Context, ws, owner, id string, result Result) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var raw []byte
	var task *string
	var expires time.Time
	var status string
	err = tx.QueryRow(ctx, "SELECT request,task_id::text,expires_at,status FROM project_memory_request WHERE id=$1 AND workspace_id=$2 AND owner_daemon_id=$3 FOR UPDATE", id, ws, owner).Scan(&raw, &task, &expires, &status)
	if err != nil {
		return err
	}
	if status == "done" || status == "failed" {
		return nil
	}
	var w Work
	if err = json.Unmarshal(raw, &w); err != nil {
		return err
	}
	if w.Context != nil {
		if _, err = scope(ctx, tx, *w.Context); err != nil {
			result = Result{Error: "project scope no longer exists"}
		}
	}
	b, err := binding(ctx, tx, ws, w.Binding.ProjectID)
	if err != nil {
		return err
	}
	if task != nil {
		if _, e := authorize(ctx, tx, *task, ws, b.ProjectID, b); e != nil {
			result = Result{Error: e.Error()}
		}
	}
	if time.Now().After(expires) {
		result = Result{Error: "request expired"}
	}
	if b.Revision != w.Binding.Revision || b.ContentRevision != w.Binding.ContentRevision || b.Generation != w.Binding.Generation {
		result = Result{Error: ErrConflict.Error()}
	}
	status = "done"
	if result.Error == "" && w.Operation.Action == "read" {
		snap := result.Snapshot
		if snap == nil || snap.SchemaVersion != 1 || snap.WorkspaceID != ws || snap.ProjectID != b.ProjectID || snap.BindingRevision != b.Revision || snap.ContentRevision != b.ContentRevision {
			result = Result{Error: "owner returned a mismatched snapshot"}
		} else if err := validateFiles(snap.Files); err != nil {
			result = Result{Error: err.Error()}
		}
	}
	if result.Error == "" && w.Operation.Action != "read" {
		if result.Candidate == nil || result.Candidate.ContentRevision != b.ContentRevision+1 || !validID(result.Candidate.Generation) || len(result.Candidate.Digest) != 64 {
			return fmt.Errorf("invalid candidate")
		}
		target := w.Target
		target.Generation = result.Candidate.Generation
		target.Digest = result.Candidate.Digest
		target.ContentRevision = result.Candidate.ContentRevision
		target.State = "ready"
		raw, _ = json.Marshal(target)
		if _, err = tx.Exec(ctx, "UPDATE project_memory_binding SET binding=$3 WHERE workspace_id=$1 AND project_id=$2", ws, b.ProjectID, raw); err != nil {
			return err
		}
	}
	if result.Error != "" {
		status = "failed"
		result.Candidate = nil
		result.Snapshot = nil
	}
	raw, _ = json.Marshal(result)
	if _, err = tx.Exec(ctx, "UPDATE project_memory_request SET status=$2,result=$3 WHERE id=$1", id, status, raw); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
