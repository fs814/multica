import { describe, it, expect } from "vitest";
import {
  WorkflowDebugRunSchema,
  WorkflowDebugDefinitionSchema,
} from "./debug-schemas";
import { EMPTY_WORKFLOW_RUN_DETAIL } from "./schemas";

const id = "00000000-0000-4000-8000-000000000001";
const response = () => ({
  schema_version: "1",
  created: true,
  execution_ref: {
    kind: "draft_test",
    snapshot_id: id,
    definition_hash: "abc",
    graph_schema_version: 2,
    base_revision: 1,
    base_draft_version_id: null,
  },
  run: {
    ...EMPTY_WORKFLOW_RUN_DETAIL,
    id,
    status: "running",
    template_version_id: "",
  },
  debug: {
    effective_limits: {
      max_attempts_per_node: 3,
      max_rework_rounds: 3,
      max_fan_out: 10,
      max_duration_seconds: 1800,
      max_total_steps: 100,
      max_cost_cents: 10000,
    },
    deadline_at: "2026-09-14T12:00:00Z",
    policy_revision: 1,
    retention_seconds: 86400,
    purge_after: null,
    payload_bytes: 123,
    cleanup_state: "retained",
    details_purged_at: null,
    purge_completed_at: null,
    stop_requested_at: null,
  },
});
describe("draft trial identity", () => {
  it("requires a valid execution handle and snapshot source", () => {
    expect(WorkflowDebugRunSchema.parse(response()).executionRef.kind).toBe(
      "draft_test",
    );
    for (const bad of ["", "not-an-id"]) {
      const value = response();
      value.run.id = bad;
      expect(WorkflowDebugRunSchema.safeParse(value).success).toBe(false);
    }
  });
  it("does not degrade an unknown source to a publication", () => {
    const value = response();
    value.execution_ref.kind = "future_mode";
    expect(WorkflowDebugRunSchema.safeParse(value).success).toBe(false);
    expect(
      WorkflowDebugDefinitionSchema.safeParse({
        schema_version: "1",
        execution_ref: value.execution_ref,
        definition: {},
      }).success,
    ).toBe(false);
  });
  it("rejects a publication or business issue attached to the trial identity", () => {
    const value = response();
    value.run.template_version_id = id;
    expect(WorkflowDebugRunSchema.safeParse(value).success).toBe(false);
    value.run.template_version_id = "";
    value.run.issue_id = id;
    expect(WorkflowDebugRunSchema.safeParse(value).success).toBe(false);
  });
  it("keeps tombstone metadata readable without pretending it has a graph", () => {
    const value = response();
    value.run.status = "completed";
    const tombstone = {
      ...value,
      debug: {
        ...value.debug,
        details_purged_at: "2026-09-15T12:00:00Z",
        cleanup_state: "purging",
      },
    };
    expect(
      WorkflowDebugRunSchema.parse(tombstone).debug.detailsPurgedAt,
    ).toBeTruthy();
  });
});
