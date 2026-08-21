import { describe, expect, it } from "vitest";
import { buildIssueSurfaceQueryPlan } from "./query-plan";

describe("buildIssueSurfaceQueryPlan workflow scope", () => {
  it("carries the template filter into the Gantt residue", () => {
    expect(
      buildIssueSurfaceQueryPlan({ type: "workflow", templateId: "wf-1" }),
    ).toEqual({
      scopeKey: "workflow:wf-1",
      queryFilter: { workflow_template_id: "wf-1" },
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
      scopeKey: "workspace:all:exclude-workflow-issues",
      queryFilter: { exclude_workflow_issues: true },
      createDefaults: {},
    });
    expect(agentsPlan).toEqual({
      scopeKey: "workspace:agents:exclude-workflow-issues",
      queryFilter: {
        assignee_types: ["agent", "squad"],
        exclude_workflow_issues: true,
      },
      createDefaults: {},
    });
    expect(allPlan.scopeKey).not.toBe(agentsPlan.scopeKey);
  });
});
