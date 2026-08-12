import { describe, expect, it } from "vitest";
import { buildIssueSurfaceQueryPlan } from "./query-plan";

describe("buildIssueSurfaceQueryPlan workflow scope", () => {
  it("carries the template filter through every server-backed issue mode", () => {
    expect(
      buildIssueSurfaceQueryPlan({ type: "workflow", templateId: "wf-1" }),
    ).toEqual({
      kind: "scoped",
      scopeKey: "workflow:wf-1",
      queryScope: "workflow:wf-1",
      queryFilter: { workflow_template_id: "wf-1" },
      groupedScopeFilter: { workflow_template_id: "wf-1" },
      loadMoreScope: "workflow:wf-1",
      loadMoreFilter: { workflow_template_id: "wf-1" },
      userId: undefined,
      createDefaults: {},
    });
  });
});

describe("buildIssueSurfaceQueryPlan excluded workspace scope", () => {
  it("carries exclusion through every server-backed mode with distinct scope keys", () => {
    const allPlan = buildIssueSurfaceQueryPlan({
      type: "workspace",
      excludeWorkflowIssues: true,
    });
    const agentsPlan = buildIssueSurfaceQueryPlan({
      type: "workspace",
      actorKind: "agents",
      excludeWorkflowIssues: true,
    });

    expect(allPlan).toEqual({
      kind: "scoped",
      scopeKey: "workspace:all:exclude-workflow-issues",
      queryScope: "workspace:all:exclude-workflow-issues",
      queryFilter: { exclude_workflow_issues: true },
      groupedScopeFilter: { exclude_workflow_issues: true },
      loadMoreScope: "workspace:all:exclude-workflow-issues",
      loadMoreFilter: { exclude_workflow_issues: true },
      userId: undefined,
      createDefaults: {},
    });
    expect(agentsPlan).toEqual({
      kind: "scoped",
      scopeKey: "workspace:agents:exclude-workflow-issues",
      queryScope: "workspace:agents:exclude-workflow-issues",
      queryFilter: {
        assignee_types: ["agent", "squad"],
        exclude_workflow_issues: true,
      },
      groupedScopeFilter: {
        assignee_types: ["agent", "squad"],
        exclude_workflow_issues: true,
      },
      loadMoreScope: "workspace:agents:exclude-workflow-issues",
      loadMoreFilter: {
        assignee_types: ["agent", "squad"],
        exclude_workflow_issues: true,
      },
      userId: undefined,
      createDefaults: {},
    });
    expect(allPlan.scopeKey).not.toBe(agentsPlan.scopeKey);
  });
});
