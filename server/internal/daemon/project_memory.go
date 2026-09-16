package daemon

import (
	"context"
	"fmt"
	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/projectmemory"
	"path/filepath"
	"time"
)

// processProjectMemoryWork runs on the owner independently of disposable task homes.
func (d *Daemon) processProjectMemoryWork(ctx context.Context, runtimeID string) {
	var response struct {
		Work *projectmemory.Work `json:"work"`
	}
	if err := d.client.getJSON(ctx, "/api/daemon/runtimes/"+runtimeID+"/memory/next", &response); err != nil {
		return
	}
	if response.Work == nil {
		return
	}
	root, err := cli.ProfileDir(d.cfg.Profile)
	result := projectmemory.Result{}
	if err != nil {
		result.Error = err.Error()
	} else {
		result = projectmemory.Execute(projectmemory.Store{ManagedRoot: filepath.Join(root, "project-memory"), DaemonID: d.cfg.DaemonID}, *response.Work)
	}
	var receipt map[string]any
	if err := d.client.postJSON(ctx, "/api/daemon/runtimes/"+runtimeID+"/memory/"+response.Work.ID+"/result", result, &receipt); err != nil {
		d.logger.Warn("project memory result not published", "request_id", response.Work.ID, "error", err)
	}
}

func (d *Daemon) loadProjectMemory(ctx context.Context, task Task) (*projectmemory.Snapshot, error) {
	if task.ProjectMemory == nil {
		if task.ProjectID != "" {
			return nil, fmt.Errorf("server did not provide a project memory context; upgrade the server before running project tasks")
		}
		return nil, nil
	}
	token, err := taskScopedAuthToken(task)
	if err != nil {
		return nil, err
	}
	client := cli.NewAPIClient(d.client.baseURL, task.WorkspaceID, token)
	ctx, cancel := context.WithTimeout(ctx, 55*time.Second)
	defer cancel()
	base := "/api/projects/" + task.ProjectID + "/memory"
	var b projectmemory.Binding
	if err = client.GetJSON(ctx, base, &b); err != nil {
		return nil, err
	}
	for _, action := range []string{"init", "read"} {
		if action == "init" && b.Generation != "" {
			continue
		}
		var work projectmemory.Work
		op := projectmemory.Operation{Action: action, ExpectedRevision: b.ContentRevision, ExpectedBindingRevision: b.Revision, Source: "runtime task " + task.ID}
		if err = client.PostJSON(ctx, base+"/operations", op, &work); err != nil {
			return nil, err
		}
		for {
			if b.OwnerDaemonID == d.cfg.DaemonID {
				d.processProjectMemoryWork(ctx, task.RuntimeID)
			}
			var receipt projectmemory.Receipt
			if err = client.GetJSON(ctx, base+"/operations/"+work.ID, &receipt); err != nil {
				return nil, err
			}
			if receipt.Status == "done" {
				if action == "read" {
					if receipt.Result == nil || receipt.Result.Snapshot == nil {
						return nil, fmt.Errorf("empty memory snapshot")
					}
					return receipt.Result.Snapshot, nil
				}
				if err = client.GetJSON(ctx, base, &b); err != nil {
					return nil, err
				}
				break
			}
			if receipt.Status == "failed" || receipt.Status == "unavailable" {
				if action == "init" && client.GetJSON(ctx, base, &b) == nil && b.Generation != "" {
					break
				}
				return nil, fmt.Errorf("project memory %s: %v", receipt.Status, receipt.Result)
			}
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("project memory owner unavailable: %w", ctx.Err())
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
	return nil, nil
}
