package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Workflow Issues is a full IssueSurface, so the template membership predicate
// must be identical across the status list, assignee-grouped board and Table.
// A client-only filter would silently lose matches beyond the current page.
func TestWorkflowTemplateIssueFilterAcrossIssueSurfaces(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	suffix := time.Now().UnixNano()

	var projectID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title) VALUES ($1, $2) RETURNING id
	`, testWorkspaceID, fmt.Sprintf("Workflow Issues %d", suffix)).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}

	insertIssue := func(title string, parentIssueID *string) string {
		var number int
		if err := testPool.QueryRow(ctx, `
			UPDATE workspace
			SET issue_counter = GREATEST(issue_counter, (SELECT COALESCE(MAX(number), 0) FROM issue WHERE workspace_id = $1)) + 1
			WHERE id = $1 RETURNING issue_counter
		`, testWorkspaceID).Scan(&number); err != nil {
			t.Fatalf("next issue number: %v", err)
		}
		var id string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO issue (
				workspace_id, title, status, priority, creator_type, creator_id,
				position, number, project_id, parent_issue_id
			) VALUES ($1, $2, 'todo', 'none', 'member', $3, 0, $4, $5, $6)
			RETURNING id
		`, testWorkspaceID, title, testUserID, number, projectID, parentIssueID).Scan(&id); err != nil {
			t.Fatalf("create issue: %v", err)
		}
		return id
	}

	matchingRootIssueID := insertIssue(fmt.Sprintf("workflow-root-%d", suffix), nil)
	matchingIssueID := insertIssue(fmt.Sprintf("workflow-match-%d", suffix), &matchingRootIssueID)
	secondMatchingIssueID := insertIssue(fmt.Sprintf("workflow-match-grandchild-%d", suffix), &matchingIssueID)
	otherRootIssueID := insertIssue(fmt.Sprintf("workflow-other-root-%d", suffix), nil)
	otherIssueID := insertIssue(fmt.Sprintf("workflow-other-child-%d", suffix), &otherRootIssueID)

	insertTemplate := func(key string) (string, string) {
		var templateID string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO workflow_template (
				workspace_id, key, name, created_by_type, created_by_id
			) VALUES ($1, $2, $3, 'member', $4) RETURNING id
		`, testWorkspaceID, key, key, testUserID).Scan(&templateID); err != nil {
			t.Fatalf("create workflow template: %v", err)
		}
		var versionID string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO workflow_template_version (
				workspace_id, template_id, version, definition
			) VALUES ($1, $2, 1, '{}'::jsonb) RETURNING id
		`, testWorkspaceID, templateID).Scan(&versionID); err != nil {
			t.Fatalf("create workflow version: %v", err)
		}
		return templateID, versionID
	}

	matchingTemplateID, matchingVersionID := insertTemplate(fmt.Sprintf("workflow_match_%d", suffix))
	otherTemplateID, otherVersionID := insertTemplate(fmt.Sprintf("workflow_other_%d", suffix))

	insertRun := func(issueID, templateID, versionID, key string) {
		if _, err := testPool.Exec(ctx, `
			INSERT INTO workflow_run (
				workspace_id, issue_id, template_id, template_version_id,
				status, source, idempotency_key
			) VALUES ($1, $2, $3, $4, 'pending', 'manual', $5)
		`, testWorkspaceID, issueID, templateID, versionID, key); err != nil {
			t.Fatalf("create workflow run: %v", err)
		}
	}
	insertRun(matchingRootIssueID, matchingTemplateID, matchingVersionID, fmt.Sprintf("match-1-%d", suffix))
	// A second run of the same template must not duplicate its descendants.
	insertRun(matchingRootIssueID, matchingTemplateID, matchingVersionID, fmt.Sprintf("match-2-%d", suffix))
	insertRun(otherRootIssueID, otherTemplateID, otherVersionID, fmt.Sprintf("other-%d", suffix))

	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workflow_run WHERE template_id IN ($1, $2)`, matchingTemplateID, otherTemplateID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workflow_template_version WHERE template_id IN ($1, $2)`, matchingTemplateID, otherTemplateID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workflow_template WHERE id IN ($1, $2)`, matchingTemplateID, otherTemplateID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id IN ($1, $2, $3, $4, $5)`, matchingRootIssueID, matchingIssueID, secondMatchingIssueID, otherRootIssueID, otherIssueID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM project WHERE id = $1`, projectID)
	})

	query := fmt.Sprintf("?workspace_id=%s&project_id=%s&workflow_template_id=%s&open_only=true", testWorkspaceID, projectID, matchingTemplateID)

	listRecorder := httptest.NewRecorder()
	testHandler.ListIssues(listRecorder, newRequest(http.MethodGet, "/api/issues"+query, nil))
	if listRecorder.Code != http.StatusOK {
		t.Fatalf("list issues status = %d: %s", listRecorder.Code, listRecorder.Body.String())
	}
	var listResponse struct {
		Issues []IssueResponse `json:"issues"`
		Total  int64           `json:"total"`
	}
	if err := json.NewDecoder(listRecorder.Body).Decode(&listResponse); err != nil {
		t.Fatalf("decode list issues: %v", err)
	}
	listIssueIDs := make(map[string]bool, len(listResponse.Issues))
	for _, issue := range listResponse.Issues {
		listIssueIDs[issue.ID] = true
	}
	if listResponse.Total != 2 || len(listResponse.Issues) != 2 || !listIssueIDs[matchingIssueID] || !listIssueIDs[secondMatchingIssueID] || listIssueIDs[matchingRootIssueID] || listIssueIDs[otherRootIssueID] || listIssueIDs[otherIssueID] {
		t.Fatalf("list filter = total %d issues %+v, want only %s and %s", listResponse.Total, listResponse.Issues, matchingIssueID, secondMatchingIssueID)
	}

	groupedRecorder := httptest.NewRecorder()
	testHandler.ListGroupedIssues(groupedRecorder, newRequest(http.MethodGet, "/api/issues/grouped"+query+"&group_by=assignee", nil))
	if groupedRecorder.Code != http.StatusOK {
		t.Fatalf("grouped issues status = %d: %s", groupedRecorder.Code, groupedRecorder.Body.String())
	}
	var groupedResponse struct {
		Groups []IssueAssigneeGroupResponse `json:"groups"`
	}
	if err := json.NewDecoder(groupedRecorder.Body).Decode(&groupedResponse); err != nil {
		t.Fatalf("decode grouped issues: %v", err)
	}
	var groupedIDs []string
	for _, group := range groupedResponse.Groups {
		for _, issue := range group.Issues {
			groupedIDs = append(groupedIDs, issue.ID)
		}
	}
	groupedIssueIDs := make(map[string]bool, len(groupedIDs))
	for _, issueID := range groupedIDs {
		groupedIssueIDs[issueID] = true
	}
	if len(groupedIDs) != 2 || !groupedIssueIDs[matchingIssueID] || !groupedIssueIDs[secondMatchingIssueID] || groupedIssueIDs[matchingRootIssueID] || groupedIssueIDs[otherRootIssueID] || groupedIssueIDs[otherIssueID] {
		t.Fatalf("grouped filter = %v, want only %s and %s", groupedIDs, matchingIssueID, secondMatchingIssueID)
	}

	tableRequest := issueTableRowsRequest{
		Query: issueTableQuerySpec{
			Scope: issueTableScope{Kind: "workflow", WorkflowTemplateID: matchingTemplateID},
			Sort:  issueTableSortRequest{Field: "position", Direction: "asc"},
		},
		Group:     issueTableGroupSpec{Kind: "none"},
		Hierarchy: issueTableHierarchyRequest{Enabled: false},
		Page:      issueTablePageRequest{Limit: 1},
	}
	tableRecorder := httptest.NewRecorder()
	testHandler.ListIssueTableRows(tableRecorder, newRequest(http.MethodPost, "/api/issues/table/rows", tableRequest))
	if tableRecorder.Code != http.StatusOK {
		t.Fatalf("table rows status = %d: %s", tableRecorder.Code, tableRecorder.Body.String())
	}
	var tableResponse issueTableRowsResponse
	if err := json.NewDecoder(tableRecorder.Body).Decode(&tableResponse); err != nil {
		t.Fatalf("decode table rows: %v", err)
	}
	if tableResponse.Total != 2 || len(tableResponse.Rows) != 1 || tableResponse.NextCursor == nil {
		t.Fatalf("table first page = total %d rows %+v next cursor %v, want total 2, one row and a cursor", tableResponse.Total, tableResponse.Rows, tableResponse.NextCursor)
	}
	firstTableIssueID := tableResponse.Rows[0].Issue.ID
	if firstTableIssueID != matchingIssueID && firstTableIssueID != secondMatchingIssueID {
		t.Fatalf("table first page issue = %s, want %s or %s", firstTableIssueID, matchingIssueID, secondMatchingIssueID)
	}

	tableRequest.Page.Cursor = tableResponse.NextCursor
	secondTableRecorder := httptest.NewRecorder()
	testHandler.ListIssueTableRows(secondTableRecorder, newRequest(http.MethodPost, "/api/issues/table/rows", tableRequest))
	if secondTableRecorder.Code != http.StatusOK {
		t.Fatalf("table second page status = %d: %s", secondTableRecorder.Code, secondTableRecorder.Body.String())
	}
	var secondTableResponse issueTableRowsResponse
	if err := json.NewDecoder(secondTableRecorder.Body).Decode(&secondTableResponse); err != nil {
		t.Fatalf("decode table second page: %v", err)
	}
	if secondTableResponse.Total != 0 || len(secondTableResponse.Rows) != 1 {
		t.Fatalf("table second page = total %d rows %+v, want continuation total 0 and one row", secondTableResponse.Total, secondTableResponse.Rows)
	}
	secondTableIssueID := secondTableResponse.Rows[0].Issue.ID
	if secondTableIssueID == firstTableIssueID || (secondTableIssueID != matchingIssueID && secondTableIssueID != secondMatchingIssueID) || secondTableIssueID == matchingRootIssueID || secondTableIssueID == otherRootIssueID || secondTableIssueID == otherIssueID {
		t.Fatalf("table second page issue = %s after %s, want the other matching descendant", secondTableIssueID, firstTableIssueID)
	}
	tableIssueIDs := map[string]bool{firstTableIssueID: true, secondTableIssueID: true}
	if len(tableIssueIDs) != 2 || !tableIssueIDs[matchingIssueID] || !tableIssueIDs[secondMatchingIssueID] || tableIssueIDs[matchingRootIssueID] || tableIssueIDs[otherRootIssueID] || tableIssueIDs[otherIssueID] {
		t.Fatalf("table filter = %v, want only %s and %s", tableIssueIDs, matchingIssueID, secondMatchingIssueID)
	}

	excludedQuery := fmt.Sprintf("?workspace_id=%s&project_id=%s&open_only=true&exclude_workflow_issues=true", testWorkspaceID, projectID)
	excludedListRecorder := httptest.NewRecorder()
	testHandler.ListIssues(excludedListRecorder, newRequest(http.MethodGet, "/api/issues"+excludedQuery, nil))
	if excludedListRecorder.Code != http.StatusOK {
		t.Fatalf("excluded list issues status = %d: %s", excludedListRecorder.Code, excludedListRecorder.Body.String())
	}
	var excludedListResponse struct {
		Issues []IssueResponse `json:"issues"`
		Total  int64           `json:"total"`
	}
	if err := json.NewDecoder(excludedListRecorder.Body).Decode(&excludedListResponse); err != nil {
		t.Fatalf("decode excluded list issues: %v", err)
	}
	excludedListIDs := make(map[string]bool, len(excludedListResponse.Issues))
	for _, issue := range excludedListResponse.Issues {
		excludedListIDs[issue.ID] = true
	}
	if excludedListResponse.Total != 2 || len(excludedListIDs) != 2 || !excludedListIDs[matchingRootIssueID] || !excludedListIDs[otherRootIssueID] || excludedListIDs[matchingIssueID] || excludedListIDs[secondMatchingIssueID] || excludedListIDs[otherIssueID] {
		t.Fatalf("excluded list filter = %v, want only run roots", excludedListIDs)
	}

	excludedGroupedRecorder := httptest.NewRecorder()
	testHandler.ListGroupedIssues(excludedGroupedRecorder, newRequest(http.MethodGet, "/api/issues/grouped"+excludedQuery+"&group_by=assignee", nil))
	if excludedGroupedRecorder.Code != http.StatusOK {
		t.Fatalf("excluded grouped issues status = %d: %s", excludedGroupedRecorder.Code, excludedGroupedRecorder.Body.String())
	}
	var excludedGroupedResponse struct {
		Groups []IssueAssigneeGroupResponse `json:"groups"`
	}
	if err := json.NewDecoder(excludedGroupedRecorder.Body).Decode(&excludedGroupedResponse); err != nil {
		t.Fatalf("decode excluded grouped issues: %v", err)
	}
	excludedGroupedIDs := make(map[string]bool)
	for _, group := range excludedGroupedResponse.Groups {
		for _, issue := range group.Issues {
			excludedGroupedIDs[issue.ID] = true
		}
	}
	if len(excludedGroupedIDs) != 2 || !excludedGroupedIDs[matchingRootIssueID] || !excludedGroupedIDs[otherRootIssueID] || excludedGroupedIDs[matchingIssueID] || excludedGroupedIDs[secondMatchingIssueID] || excludedGroupedIDs[otherIssueID] {
		t.Fatalf("excluded grouped filter = %v, want only run roots", excludedGroupedIDs)
	}

	excludedTableRequest := issueTableRowsRequest{
		Query: issueTableQuerySpec{
			Scope:   issueTableScope{Kind: "workspace", ExcludeWorkflowIssues: true},
			Filters: issueTableFiltersRequest{ProjectIDs: []string{projectID}},
			Sort:    issueTableSortRequest{Field: "position", Direction: "asc"},
		},
		Group:     issueTableGroupSpec{Kind: "none"},
		Hierarchy: issueTableHierarchyRequest{Enabled: false},
		Page:      issueTablePageRequest{Limit: 10},
	}
	excludedTableRecorder := httptest.NewRecorder()
	testHandler.ListIssueTableRows(excludedTableRecorder, newRequest(http.MethodPost, "/api/issues/table/rows", excludedTableRequest))
	if excludedTableRecorder.Code != http.StatusOK {
		t.Fatalf("excluded table rows status = %d: %s", excludedTableRecorder.Code, excludedTableRecorder.Body.String())
	}
	var excludedTableResponse issueTableRowsResponse
	if err := json.NewDecoder(excludedTableRecorder.Body).Decode(&excludedTableResponse); err != nil {
		t.Fatalf("decode excluded table rows: %v", err)
	}
	excludedTableIDs := make(map[string]bool, len(excludedTableResponse.Rows))
	for _, row := range excludedTableResponse.Rows {
		excludedTableIDs[row.Issue.ID] = true
	}
	if excludedTableResponse.Total != 2 || len(excludedTableIDs) != 2 || !excludedTableIDs[matchingRootIssueID] || !excludedTableIDs[otherRootIssueID] || excludedTableIDs[matchingIssueID] || excludedTableIDs[secondMatchingIssueID] || excludedTableIDs[otherIssueID] {
		t.Fatalf("excluded table filter = %v, want only run roots", excludedTableIDs)
	}
}
