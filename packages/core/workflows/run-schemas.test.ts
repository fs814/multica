import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "../api/client";
import {
  WorkflowRunSchema,
  WorkflowRunDetailSchema,
  WorkflowStepSchema,
  WorkflowSubmissionSchema,
  WorkflowAcceptanceSchema,
  WorkflowRunListResponseSchema,
  EMPTY_WORKFLOW_RUN_DETAIL,
  EMPTY_WORKFLOW_RUN_LIST_RESPONSE,
} from "./schemas";

/**
 * Boundary-defense tests for the workflow *run* endpoints, mirroring
 * `packages/core/api/schema.test.ts` and this directory's `schemas.test.ts`.
 *
 * Runs raise the stakes over templates in two ways, and both are what these
 * tests are actually about:
 *
 *  1. The engine owns the state machine, so `status` / step `status` /
 *     `verdict` / `node_type` drift on the *server's* release schedule while an
 *     installed desktop build keeps running. An unfamiliar value must render as
 *     an unfamiliar badge, never as a blank Runs page.
 *
 *  2. A run detail's failure mode is silent in a way a template's is not. The
 *     by-id methods spread the requested id onto the fallback, so a parse miss
 *     produces an object with a real id, no steps, and no acceptance - which
 *     reads exactly like a freshly created run that hasn't done anything yet.
 *     `status: ""` is the field that separates the two, and the last group below
 *     pins that behaviour because getting it wrong has already shipped twice.
 */

function stubFetchJson(body: unknown, status = 200) {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(
      new Response(typeof body === "string" ? body : JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/json" },
      }),
    ),
  );
}

const client = () => new ApiClient("https://api.example.test");

/** A running Bug Fix run: analyze passed, implement is queued on an agent. */
const runningRun = {
  id: "wfr-1",
  workspace_id: "ws-1",
  issue_id: "iss-1",
  template_id: "wft-1",
  template_version_id: "wftv-1",
  status: "running",
  source: "manual",
  accountable_user_id: "u-1",
  blocked_reason: null,
  failure_reason: null,
  started_at: "2026-07-01T00:00:01Z",
  completed_at: null,
  created_at: "2026-07-01T00:00:00Z",
  updated_at: "2026-07-01T00:00:05Z",
  template_name: "Bug Fix",
  template_key: "bug_fix",
  step_count: 2,
  current_node_key: "implement",
};

const passedAnalyzeStep = {
  id: "wfs-1",
  node_key: "analyze",
  node_type: "agent",
  attempt: 1,
  status: "passed",
  agent_id: "ag-1",
  agent_name: "Root Cause Rita",
  task_id: "atq-1",
  routing_reason: 'matched capability "bug_analysis"',
  failure_reason: null,
  started_at: "2026-07-01T00:00:01Z",
  completed_at: "2026-07-01T00:00:04Z",
  submission: {
    verdict: "pass",
    artifact: {
      type: "analysis",
      summary: "Null deref in the claim handler.",
      references: ["server/internal/handler/daemon.go:2322"],
    },
    rationale: "Reproduced against the scratch DB.",
    confidence: 0.9,
    root_cause: null,
    validation_errors: null,
    submitted_at: "2026-07-01T00:00:04Z",
  },
};

afterEach(() => {
  vi.unstubAllGlobals();
});

// ---------------------------------------------------------------------------
// listWorkflowRuns
// ---------------------------------------------------------------------------

describe("listWorkflowRuns", () => {
  it("falls back to an empty list when the body is null", async () => {
    stubFetchJson(null);
    expect(await client().listWorkflowRuns()).toEqual(
      EMPTY_WORKFLOW_RUN_LIST_RESPONSE,
    );
  });

  it("falls back to an empty list when `runs` is not an array", async () => {
    // A wrong *type* is the drift that fails the parse. An object with only
    // unexpected keys would succeed (every declared field but `id`/`status` has
    // a default) and pass the extras through via `.loose()`.
    stubFetchJson({ runs: "not-an-array", total: 3 });
    expect(await client().listWorkflowRuns()).toEqual(
      EMPTY_WORKFLOW_RUN_LIST_RESPONSE,
    );
  });

  it("parses a well-formed list", async () => {
    stubFetchJson({ runs: [runningRun], total: 1 });
    const res = await client().listWorkflowRuns();
    expect(res.total).toBe(1);
    expect(res.runs[0]?.template_key).toBe("bug_fix");
    expect(res.runs[0]?.current_node_key).toBe("implement");
  });

  it("keeps a run whose status this client has never heard of", async () => {
    // The engine can add a state in any server release. Dropping the row would
    // make the Runs page silently omit in-flight work.
    stubFetchJson({
      runs: [{ ...runningRun, status: "awaiting_human_input" }],
      total: 1,
    });
    const res = await client().listWorkflowRuns();
    expect(res.runs).toHaveLength(1);
    expect(res.runs[0]?.status).toBe("awaiting_human_input");
  });

  it("keeps a run with an unknown source", async () => {
    stubFetchJson({ runs: [{ ...runningRun, source: "scheduler" }], total: 1 });
    const res = await client().listWorkflowRuns();
    expect(res.runs[0]?.source).toBe("scheduler");
  });

  it("defaults an older server's missing enrichment fields", async () => {
    // `template_name` / `template_key` / `step_count` / `current_node_key` are
    // joins, not columns, so a server that predates one of them omits it.
    stubFetchJson({
      runs: [
        {
          id: "wfr-2",
          status: "pending",
          template_id: "wft-1",
        },
      ],
      total: 1,
    });
    const res = await client().listWorkflowRuns();
    expect(res.runs[0]).toMatchObject({
      id: "wfr-2",
      status: "pending",
      template_name: "",
      template_key: "",
      step_count: 0,
      current_node_key: null,
      issue_id: null,
    });
  });

  it("drops a run row that has no status rather than inventing one", () => {
    // `status` has no `.default()` on purpose - it is the field callers read to
    // detect an unreadable payload, so fabricating it would defeat that. A row
    // missing it fails the array element and therefore the envelope.
    const { status: _omit, ...noStatus } = runningRun;
    expect(
      WorkflowRunListResponseSchema.safeParse({ runs: [noStatus], total: 1 })
        .success,
    ).toBe(false);
  });

  it("passes filters through as query params", async () => {
    stubFetchJson({ runs: [], total: 0 });
    await client().listWorkflowRuns({
      status: "blocked",
      template_id: "wft-1",
      limit: 25,
      offset: 50,
    });
    const url = String(
      (globalThis.fetch as unknown as { mock: { calls: unknown[][] } }).mock
        .calls[0]?.[0],
    );
    expect(url).toContain("status=blocked");
    expect(url).toContain("template_id=wft-1");
    expect(url).toContain("limit=25");
    expect(url).toContain("offset=50");
  });

  it("sends offset=0 rather than omitting it", async () => {
    // `if (params.offset)` would drop a zero and silently re-request page 1
    // while the caller believed it asked for it explicitly.
    stubFetchJson({ runs: [], total: 0 });
    await client().listWorkflowRuns({ offset: 0, limit: 0 });
    const url = String(
      (globalThis.fetch as unknown as { mock: { calls: unknown[][] } }).mock
        .calls[0]?.[0],
    );
    expect(url).toContain("offset=0");
    expect(url).toContain("limit=0");
  });

  it("preserves unknown fields the schema didn't list", async () => {
    stubFetchJson({
      runs: [{ ...runningRun, cost_cents: 42 }],
      total: 1,
      page_token: "abc",
    });
    const res = await client().listWorkflowRuns();
    const row = res.runs[0] as unknown as Record<string, unknown>;
    expect(row.cost_cents).toBe(42);
    expect((res as unknown as Record<string, unknown>).page_token).toBe("abc");
  });
});

// ---------------------------------------------------------------------------
// getWorkflowRun
// ---------------------------------------------------------------------------

describe("getWorkflowRun", () => {
  it("parses a run with its full trace", async () => {
    stubFetchJson({
      ...runningRun,
      input: { title: "Crash on claim", description: "500 on POST /claim" },
      steps: [passedAnalyzeStep],
      acceptance: null,
    });
    const run = await client().getWorkflowRun("wfr-1");
    expect(run.status).toBe("running");
    expect(run.input.title).toBe("Crash on claim");
    expect(run.steps[0]?.submission?.verdict).toBe("pass");
    expect(run.steps[0]?.routing_reason).toBe('matched capability "bug_analysis"');
  });

  it("defaults input / steps / acceptance when the server omits them", async () => {
    stubFetchJson(runningRun);
    const run = await client().getWorkflowRun("wfr-1");
    expect(run.input).toEqual({});
    expect(run.steps).toEqual([]);
    expect(run.acceptance).toBeNull();
  });

  it("keeps the run header when one step is malformed", async () => {
    // `.catch` on `steps` degrades the trace *locally*: the header (status,
    // template, timing) does not depend on it, so a reshaped step must not cost
    // the whole page. The non-zero `step_count` is the tell that this fired.
    stubFetchJson({
      ...runningRun,
      steps: [passedAnalyzeStep, { node_key: "implement" /* no id */ }],
    });
    const run = await client().getWorkflowRun("wfr-1");
    expect(run.status).toBe("running");
    expect(run.step_count).toBe(2);
    expect(run.steps).toEqual([]);
  });

  it("degrades a non-object submission to null instead of dropping the step", async () => {
    // A server that regressed to prose (`submission: "pass"`) is the exact case
    // the submission contract exists to defeat, and it must not survive as a
    // verdict. Losing the whole step would hide that the node ran at all, so
    // null - "we have no verdict" - is the honest degradation.
    stubFetchJson({
      ...runningRun,
      steps: [{ ...passedAnalyzeStep, submission: "pass" }],
    });
    const run = await client().getWorkflowRun("wfr-1");
    expect(run.steps).toHaveLength(1);
    expect(run.steps[0]?.node_key).toBe("analyze");
    expect(run.steps[0]?.submission).toBeNull();
  });

  it("keeps the verdict when only the artifact is unreadable", async () => {
    // Precedence inside a submission: the artifact has its own `.catch`, so a
    // reshaped artifact degrades to `{}` and the verdict - the field the
    // engine's own branching depends on - survives. The reverse (losing a
    // legitimate "fail" because the artifact grew a field) would be worse.
    stubFetchJson({
      ...runningRun,
      steps: [
        {
          ...passedAnalyzeStep,
          submission: {
            ...passedAnalyzeStep.submission,
            verdict: "fail",
            artifact: "not-an-object",
          },
        },
      ],
    });
    const run = await client().getWorkflowRun("wfr-1");
    expect(run.steps[0]?.submission?.verdict).toBe("fail");
    expect(run.steps[0]?.submission?.artifact).toEqual({});
  });

  it("keeps an unknown step status and node_type", async () => {
    stubFetchJson({
      ...runningRun,
      steps: [
        { ...passedAnalyzeStep, status: "awaiting_review", node_type: "fan_out" },
      ],
    });
    const run = await client().getWorkflowRun("wfr-1");
    expect(run.steps[0]?.status).toBe("awaiting_review");
    expect(run.steps[0]?.node_type).toBe("fan_out");
  });

  it("keeps an unknown submission verdict verbatim", async () => {
    stubFetchJson({
      ...runningRun,
      steps: [
        {
          ...passedAnalyzeStep,
          submission: { ...passedAnalyzeStep.submission, verdict: "needs_info" },
        },
      ],
    });
    const run = await client().getWorkflowRun("wfr-1");
    expect(run.steps[0]?.submission?.verdict).toBe("needs_info");
  });

  it("parses an open acceptance with its criteria and rework targets", async () => {
    stubFetchJson({
      ...runningRun,
      status: "blocked",
      acceptance: {
        id: "wfa-1",
        step_id: "wfs-3",
        status: "pending",
        reason: null,
        rework_target_node_key: null,
        criteria: ["Tests pass", "No new lint errors"],
        rework_targets: ["analyze", "implement", "validate"],
        created_at: "2026-07-01T00:01:00Z",
      },
    });
    const run = await client().getWorkflowRun("wfr-1");
    expect(run.acceptance?.rework_targets).toEqual([
      "analyze",
      "implement",
      "validate",
    ]);
    expect(run.acceptance?.criteria).toHaveLength(2);
  });

  it("degrades a malformed acceptance to null (accept-only, never permissive)", async () => {
    stubFetchJson({ ...runningRun, acceptance: { status: "pending" /* no id */ } });
    const run = await client().getWorkflowRun("wfr-1");
    expect(run.acceptance).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// The trap: an unreadable detail must be DETECTABLE
// ---------------------------------------------------------------------------

describe("an unreadable run detail is distinguishable from a real run", () => {
  it("returns status:\"\" (not just the spread id) when the body is null", async () => {
    stubFetchJson(null);
    const run = await client().getWorkflowRun("wfr-1");
    // The id survives so the page keeps its breadcrumb and cancel target...
    expect(run.id).toBe("wfr-1");
    // ...which is exactly why `id` cannot be the signal. `status` is.
    expect(run.status).toBe("");
  });

  it("returns status:\"\" when the body is the wrong shape", async () => {
    stubFetchJson({ unexpected: "payload" });
    const run = await client().getWorkflowRun("wfr-1");
    expect(run.id).toBe("wfr-1");
    expect(run.status).toBe("");
  });

  it("is not confusable with a real run that genuinely has no steps", async () => {
    // This is the pair that matters. Both objects have a real id, an empty
    // `steps`, and a null `acceptance`; only `status` tells them apart. A
    // caller gating its trace on `steps.length` would render "nothing happened
    // yet" for a run that is actually mid-flight.
    stubFetchJson({ ...runningRun, status: "pending", steps: [] });
    const fresh = await client().getWorkflowRun("wfr-1");
    stubFetchJson(null);
    const unreadable = await client().getWorkflowRun("wfr-1");

    expect(fresh.steps).toEqual(unreadable.steps);
    expect(fresh.acceptance).toEqual(unreadable.acceptance);
    expect(fresh.id).toEqual(unreadable.id);
    // The one field that differs, and therefore the only correct guard:
    expect(fresh.status).toBe("pending");
    expect(unreadable.status).toBe("");
  });

  it("never leaks a non-empty status onto the fallback constant", () => {
    // Guards against a future edit spreading a real run over the fallback,
    // which would re-break detection everywhere at once.
    expect(EMPTY_WORKFLOW_RUN_DETAIL.status).toBe("");
    expect(EMPTY_WORKFLOW_RUN_DETAIL.id).toBe("");
  });
});

// ---------------------------------------------------------------------------
// Writes
// ---------------------------------------------------------------------------

describe("runWorkflowTemplate", () => {
  it("posts the freeform run input to the template's run route", async () => {
    stubFetchJson({ ...runningRun, input: {}, steps: [], acceptance: null }, 201);
    await client().runWorkflowTemplate("wft-1", {
      title: "Crash on claim",
      description: "500 on POST /claim",
      project_id: null,
    });
    const call = (
      globalThis.fetch as unknown as { mock: { calls: unknown[][] } }
    ).mock.calls[0];
    expect(String(call?.[0])).toContain("/api/workflow-templates/wft-1/run");
    const init = call?.[1] as RequestInit;
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toEqual({
      title: "Crash on claim",
      description: "500 on POST /claim",
      project_id: null,
    });
  });

  it("does not send a client-side idempotency key", async () => {
    // The server derives it, precisely so a double-clicked button cannot start
    // two runs. A client key would let the second click through.
    stubFetchJson({ ...runningRun }, 201);
    await client().runWorkflowTemplate("wft-1", {
      title: "t",
      description: "d",
    });
    const init = (
      globalThis.fetch as unknown as { mock: { calls: unknown[][] } }
    ).mock.calls[0]?.[1] as RequestInit;
    const body = JSON.parse(String(init.body)) as Record<string, unknown>;
    expect(body).not.toHaveProperty("idempotency_key");
  });

  it("returns an empty id AND empty status when the response is unreadable", async () => {
    // No id to spread here - the run's id is what this call returns - so a
    // caller must not navigate on it.
    stubFetchJson({ nope: true }, 201);
    const run = await client().runWorkflowTemplate("wft-1", {
      title: "t",
      description: "d",
    });
    expect(run.id).toBe("");
    expect(run.status).toBe("");
  });

  it("surfaces a 409 (no published version) as a rejection, not a fallback", async () => {
    // "Your run did not start" is not a state a fallback can represent.
    stubFetchJson({ error: "template has no published version" }, 409);
    await expect(
      client().runWorkflowTemplate("wft-1", { title: "t", description: "d" }),
    ).rejects.toThrow();
  });
});

describe("cancelWorkflowRun", () => {
  it("returns the summary row", async () => {
    stubFetchJson({ ...runningRun, status: "cancelled", completed_at: "2026-07-01T00:02:00Z" });
    const run = await client().cancelWorkflowRun("wfr-1");
    expect(run.status).toBe("cancelled");
    expect(run.completed_at).toBe("2026-07-01T00:02:00Z");
  });

  it("keeps the requested id but an empty status when unreadable", async () => {
    stubFetchJson(null);
    const run = await client().cancelWorkflowRun("wfr-1");
    expect(run.id).toBe("wfr-1");
    expect(run.status).toBe("");
  });
});

describe("decideWorkflowAcceptance", () => {
  it("posts the rejection with its reason and rework target", async () => {
    stubFetchJson({ ...runningRun, status: "running" });
    await client().decideWorkflowAcceptance("wfr-1", {
      accept: false,
      reason: "Validation never ran the new test.",
      rework_target: "validate",
    });
    const call = (
      globalThis.fetch as unknown as { mock: { calls: unknown[][] } }
    ).mock.calls[0];
    expect(String(call?.[0])).toContain("/api/workflow-runs/wfr-1/acceptance");
    const init = call?.[1] as RequestInit;
    expect(JSON.parse(String(init.body))).toEqual({
      accept: false,
      reason: "Validation never ran the new test.",
      rework_target: "validate",
    });
  });

  it("returns the post-decision detail so the reviewer sees the consequence", async () => {
    stubFetchJson({
      ...runningRun,
      status: "completed",
      completed_at: "2026-07-01T00:03:00Z",
      acceptance: {
        id: "wfa-1",
        step_id: "wfs-3",
        status: "accepted",
        reason: null,
        rework_target_node_key: null,
        criteria: [],
        rework_targets: [],
        created_at: "2026-07-01T00:01:00Z",
      },
    });
    const run = await client().decideWorkflowAcceptance("wfr-1", { accept: true });
    expect(run.status).toBe("completed");
    expect(run.acceptance?.status).toBe("accepted");
  });

  it("keeps the requested id but an empty status when unreadable", async () => {
    stubFetchJson({ wrong: "shape" });
    const run = await client().decideWorkflowAcceptance("wfr-1", { accept: true });
    expect(run.id).toBe("wfr-1");
    expect(run.status).toBe("");
  });
});

// ---------------------------------------------------------------------------
// Schema-level invariants worth pinning directly
// ---------------------------------------------------------------------------

describe("WorkflowSubmissionSchema", () => {
  it("never defaults a missing verdict to a passing one", () => {
    // The server's rule is that prose never implies pass. A lenient default of
    // "pass" here would be a client-side way around that rule.
    const parsed = WorkflowSubmissionSchema.parse({
      artifact: {},
      rationale: "looks fine to me",
    });
    expect(parsed.verdict).toBe("");
  });

  it("degrades unexpected validation_errors elements to null, keeping the verdict", () => {
    const parsed = WorkflowSubmissionSchema.parse({
      verdict: "blocked",
      validation_errors: [{ field: "artifact", message: "required" }],
    });
    expect(parsed.verdict).toBe("blocked");
    expect(parsed.validation_errors).toBeNull();
  });

  it("distinguishes 'nothing to report' (null) from 'checked, clean' ([])", () => {
    expect(WorkflowSubmissionSchema.parse({ verdict: "pass" }).validation_errors)
      .toBeNull();
    expect(
      WorkflowSubmissionSchema.parse({ verdict: "fail", validation_errors: [] })
        .validation_errors,
    ).toEqual([]);
  });
});

describe("WorkflowStepSchema / WorkflowAcceptanceSchema", () => {
  it("requires an id on a step - a step with no identity cannot be keyed", () => {
    expect(WorkflowStepSchema.safeParse({ node_key: "analyze" }).success).toBe(
      false,
    );
  });

  it("defaults rework_targets to [] so an unreadable list means accept-only", () => {
    const parsed = WorkflowAcceptanceSchema.parse({
      id: "wfa-1",
      rework_targets: "analyze,implement",
    });
    expect(parsed.rework_targets).toEqual([]);
  });
});

describe("WorkflowRunSchema", () => {
  it("requires id and status and nothing else", () => {
    const parsed = WorkflowRunSchema.parse({ id: "wfr-1", status: "pending" });
    expect(parsed).toMatchObject({
      id: "wfr-1",
      status: "pending",
      source: "",
      issue_id: null,
      accountable_user_id: null,
      blocked_reason: null,
      step_count: 0,
    });
  });
});

describe("WorkflowRunDetailSchema", () => {
  it("keeps a run input this client has no schema for", () => {
    // Typed input fields are a deferred, additive change. A client that pinned
    // a shape here would reject the first run started with one.
    const parsed = WorkflowRunDetailSchema.parse({
      id: "wfr-1",
      status: "running",
      input: { title: "t", severity: "p0", labels: ["regression"] },
    });
    expect(parsed.input).toEqual({
      title: "t",
      severity: "p0",
      labels: ["regression"],
    });
  });
});
