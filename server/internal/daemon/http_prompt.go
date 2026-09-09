package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Project only the assigned task text, never the entire issue API response.
type httpIssueContext struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

// httpIssueContext reads from the assigning server with the task credential.
// It never changes the shared client's identity or forwards credentials to Knot.
func (c *Client) httpIssueContext(ctx context.Context, task Task) (httpIssueContext, error) {
	var issue httpIssueContext
	token, err := taskScopedAuthToken(task)
	if err != nil {
		return issue, err
	}
	if task.WorkspaceID == "" {
		return issue, fmt.Errorf("HTTP issue task has no workspace ID")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	path := "/api/issues/" + url.PathEscape(task.IssueID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return issue, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Workspace-ID", task.WorkspaceID)
	c.setIdentityHeaders(req)
	// Never follow redirects with task credentials.
	client := *c.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return issue, fmt.Errorf("read HTTP issue from assigning server: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return issue, fmt.Errorf("read HTTP issue from assigning server: HTTP %d", resp.StatusCode)
	}
	const limit = 1 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return issue, err
	}
	if len(data) > limit {
		return issue, fmt.Errorf("HTTP issue context exceeds %d bytes", limit)
	}
	if err := json.Unmarshal(data, &issue); err != nil {
		return issue, fmt.Errorf("decode HTTP issue context: %w", err)
	}
	if issue.ID != task.IssueID || strings.TrimSpace(issue.Title) == "" {
		return issue, fmt.Errorf("assigning server returned missing or mismatched HTTP issue context")
	}
	return issue, nil
}

// buildExecutionPrompt makes ordinary HTTP assignments independent of the
// dispatcher's CLI configuration and workflow files. Other turn contracts,
// including comment replies and explicitly scoped handoffs, remain unchanged.
func (d *Daemon) buildExecutionPrompt(ctx context.Context, task Task, provider string) (string, error) {
	if provider != "knot-http" || task.IssueID == "" || task.TriggerCommentID != "" || task.HandoffNote != "" || task.WorkflowPrompt != "" || task.ChatSessionID != "" || task.AutopilotRunID != "" || task.QuickCreatePrompt != "" {
		return BuildPrompt(task, provider), nil
	}
	issue, err := d.client.httpIssueContext(ctx, task)
	if err != nil {
		return "", err
	}
	// The ID is used locally to validate the response, not sent to the tool host.
	taskText := struct {
		Title       string `json:"title"`
		Description string `json:"description"`
	}{Title: issue.Title, Description: issue.Description}
	data, err := json.MarshalIndent(taskText, "", "  ")
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("You are executing a Multica assignment through Knot HTTP on the selected tool host. Complete the supplied task using tools on that host. For system or OS information, query that host and include the actual command output.\n\n")
	b.WriteString("The assigning server already loaded the task below. Its Multica CLI configuration, credentials and runtime workflow files are not available on the tool host. Do not run Multica CLI commands to load or deliver this task, search local profiles or disks for an issue, reconfigure Multica, or start a local Multica server. Earlier instructions requiring that local workflow do not apply to this HTTP delivery path.\n\n")
	b.WriteString("Return the substantive result directly in your final reply. The assigning server records it on the original issue when the run completes. Do not claim you posted comments or changed issue status yourself. If a required resource is inaccessible, report the specific limitation without exploring unrelated services or inventing results.\n\n")
	b.WriteString("Task text from the assigning server:\n")
	b.Write(data)
	return b.String(), nil
}
