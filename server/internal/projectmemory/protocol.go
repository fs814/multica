package projectmemory

import (
	"errors"
	"fmt"
)

type Context struct {
	WorkspaceID     string `json:"workspace_id"`
	ProjectID       string `json:"project_id"`
	ScopeKind       string `json:"scope_kind"`
	ScopeID         string `json:"scope_id"`
	Epoch           int64  `json:"context_epoch"`
	BindingRevision int64  `json:"binding_revision"`
}

func (c Context) Namespace() string {
	return fmt.Sprintf("%s/%s/%s/%d/%d", c.WorkspaceID, c.ProjectID, c.ScopeID, c.Epoch, c.BindingRevision)
}

func (c Context) Authorize(current Context, b Binding) error {
	if c.ProjectID == "" || c.WorkspaceID != current.WorkspaceID || c.ProjectID != current.ProjectID ||
		c.ScopeKind != current.ScopeKind || c.ScopeID != current.ScopeID || c.Epoch != current.Epoch ||
		c.WorkspaceID != b.WorkspaceID || c.ProjectID != b.ProjectID || c.BindingRevision != b.Revision {
		return errors.New("project memory context expired or belongs to another project")
	}
	return nil
}

type Operation struct {
	Files                   map[string]string `json:"files,omitempty"`
	Action                  string            `json:"action"`
	Path                    string            `json:"path,omitempty"`
	Content                 string            `json:"content,omitempty"`
	Source                  string            `json:"source,omitempty"`
	ExpectedRevision        int64             `json:"expected_revision"`
	ExpectedBindingRevision int64             `json:"expected_binding_revision"`
	Destination             string            `json:"destination,omitempty"`
	OwnerRuntimeID          string            `json:"owner_runtime_id,omitempty"`
}

type Work struct {
	ID        string    `json:"id"`
	Binding   Binding   `json:"binding"`
	Target    Binding   `json:"target"`
	Context   *Context  `json:"context,omitempty"`
	Operation Operation `json:"operation"`
	Status    string    `json:"status"`
}

type Result struct {
	Candidate *Candidate `json:"candidate,omitempty"`
	Snapshot  *Snapshot  `json:"snapshot,omitempty"`
	Error     string     `json:"error,omitempty"`
}

func Execute(s Store, w Work) Result {
	files := map[string]string{"README.md": "# Project memory\n\nNo knowledge has been published yet.\n"}
	if w.Binding.Generation != "" {
		snap, err := s.Read(w.Binding)
		if err != nil {
			return Result{Error: err.Error()}
		}
		files = snap.Files
		if w.Operation.Action == "read" {
			return Result{Snapshot: &snap}
		}
	} else if w.Operation.Action != "init" {
		return Result{Error: "project memory is not initialized"}
	}
	switch w.Operation.Action {
	case "init":
		if w.Binding.Generation != "" {
			return Result{Error: "project memory is already initialized"}
		}
	case "write":
		if err := ValidateName(w.Operation.Path); err != nil {
			return Result{Error: err.Error()}
		}
		files[w.Operation.Path] = w.Operation.Content
	case "delete":
		if err := ValidateName(w.Operation.Path); err != nil {
			return Result{Error: err.Error()}
		}
		delete(files, w.Operation.Path)
	case "import":
		files = w.Operation.Files
	case "migrate":
	default:
		return Result{Error: "unknown memory operation"}
	}
	candidate, err := s.Stage(w.Target, files, w.Operation.Source)
	if err != nil {
		return Result{Error: err.Error()}
	}
	return Result{Candidate: &candidate}
}
