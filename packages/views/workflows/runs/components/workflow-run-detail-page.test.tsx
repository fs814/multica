// @vitest-environment jsdom

/**
 * Tests for the run trace, the acceptance gate and the Run dialog.
 *
 * These pin the decisions no type can express, and every one of them is a
 * failure mode that has a cheaper-looking wrong answer:
 *
 *  - an UNREADABLE run detail must not render as a live run that has done
 *    nothing. The client spreads the requested id onto its parse-miss fallback,
 *    so `id` and `steps.length` are both ambiguous; `status === ""` is the only
 *    signal, and the "confusable pair" test asserts exactly that by feeding two
 *    runs that differ in nothing else.
 *  - a terminal reason must be EXPLAINED, not printed as an identifier: the
 *    engine's classification is the whole value of stopping.
 *  - rejecting with no reason must be impossible in the UI, because the reason
 *    IS the rework brief and the server's 409 arrives too late to help.
 *  - an acceptance whose `rework_targets` is empty must be accept-only. The
 *    schema defaults that list to `[]` on a parse failure, so a permissive
 *    reading would turn contract drift into 422s.
 *  - the trace is a HISTORY: two attempts at one node must both render.
 */

import { describe, expect, it, vi, beforeEach } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import type {
  WorkflowRunDetail,
  WorkflowStep,
} from "@multica/core/workflows";
import enCommon from "../../../locales/en/common.json";
import enWorkflows from "../../../locales/en/workflows.json";
import enUi from "../../../locales/en/ui.json";
import enProjects from "../../../locales/en/projects.json";
import { NavigationProvider, type NavigationAdapter } from "../../../navigation";

const TEST_RESOURCES = {
  en: {
    common: enCommon,
    workflows: enWorkflows,
    ui: enUi,
    projects: enProjects,
  },
};

const runRef = vi.hoisted(() => ({ current: null as WorkflowRunDetail | null }));
const cancelMock = vi.hoisted(() => vi.fn());
const decideMock = vi.hoisted(() => vi.fn());
const runTemplateMock = vi.hoisted(() => vi.fn());
const toastSuccessMock = vi.hoisted(() => vi.fn());
const toastWarningMock = vi.hoisted(() => vi.fn());
const toastErrorMock = vi.hoisted(() => vi.fn());
const pushMock = vi.hoisted(() => vi.fn());

// The transcript dialog owns a WS subscription and a message fetch; this suite
// is about whether the trace OFFERS the log for a step that has a task, not
// about the dialog's own behaviour (which has its own tests).
vi.mock("../../../common/task-transcript", () => ({
  TranscriptButton: (props: { title: string; task: { id: string } }) => (
    <button type="button" data-testid={`transcript-${props.task.id}`}>
      {props.title}
    </button>
  ),
}));

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => ({ id: "ws-1", name: "Acme", slug: "acme" }),
  useWorkspacePaths: () => ({
    workflowRuns: () => "/acme/workflow-runs",
    workflowRunDetail: (id: string) => `/acme/workflow-runs/${id}`,
    issueDetail: (id: string) => `/acme/issues/${id}`,
  }),
}));
vi.mock("@multica/core/projects/queries", () => ({
  projectListOptions: (wsId: string) => ({
    queryKey: ["projects", wsId],
    queryFn: () => Promise.resolve([]),
  }),
}));
vi.mock("@multica/core/workflows", async () => {
  const actual =
    await vi.importActual<typeof import("@multica/core/workflows")>(
      "@multica/core/workflows",
    );
  return {
    ...actual,
    workflowRunDetailOptions: (wsId: string, id: string) => ({
      queryKey: ["workflow-runs", wsId, "detail", id],
      queryFn: () =>
        runRef.current
          ? Promise.resolve(runRef.current)
          : Promise.reject(new Error("not found")),
    }),
    useCancelWorkflowRun: () => ({ mutateAsync: cancelMock, isPending: false }),
    useDecideWorkflowAcceptance: () => ({
      mutateAsync: decideMock,
      isPending: false,
    }),
    useRunWorkflowTemplate: () => ({
      mutateAsync: runTemplateMock,
      isPending: false,
    }),
  };
});
vi.mock("sonner", () => ({
  toast: {
    success: toastSuccessMock,
    warning: toastWarningMock,
    error: toastErrorMock,
  },
}));

import { WorkflowRunDetailPage } from "./workflow-run-detail-page";
import { WorkflowRunDialog } from "./workflow-run-dialog";

function step(patch: Partial<WorkflowStep> = {}): WorkflowStep {
  return {
    id: "wfs-1",
    node_key: "analyze",
    node_type: "agent",
    attempt: 1,
    status: "passed",
    agent_id: "ag-1",
    agent_name: "Ada",
    task_id: "task-1",
    routing_reason: "matched capability bug_analysis",
    failure_reason: null,
    failure_detail: null,
    started_at: "2026-06-01T10:00:00Z",
    completed_at: "2026-06-01T10:05:00Z",
    submission: {
      verdict: "pass",
      artifact: {
        type: "analysis",
        summary: "Null deref in the claim handler.",
        references: ["server/internal/handler/daemon.go:2322"],
      },
      rationale: "Reproduced on the failing input.",
      confidence: 0.8,
      root_cause: "Missing nil check",
      validation_errors: null,
      submitted_at: "2026-06-01T10:05:00Z",
    },
    ...patch,
  };
}

function run(patch: Partial<WorkflowRunDetail> = {}): WorkflowRunDetail {
  return {
    id: "wfr-1",
    workspace_id: "ws-1",
    issue_id: "iss-1",
    template_id: "wft-1",
    template_version_id: "wftv-1",
    status: "running",
    source: "manual",
    source_event_id: null,
    accountable_user_id: "user-1",
    blocked_reason: null,
    failure_reason: null,
    failure_detail: null,
    started_at: "2026-06-01T10:00:00Z",
    completed_at: null,
    created_at: "2026-06-01T09:59:00Z",
    updated_at: "2026-06-01T10:05:00Z",
    template_name: "Bug Fix",
    template_key: "bug_fix",
    step_count: 1,
    current_node_key: "implement",
    input: {
      title: "Claim endpoint 500s",
      description: "POST /api/daemon/claim returns 500 for an empty queue.",
    },
    steps: [step()],
    acceptance: null,
    ...patch,
  };
}

function renderRun() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const navigation: NavigationAdapter = {
    push: pushMock,
    replace: vi.fn(),
    back: vi.fn(),
    pathname: "/acme/workflow-runs/wfr-1",
    searchParams: new URLSearchParams(),
    getShareableUrl: (path) => path,
  };
  render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <NavigationProvider value={navigation}>
        <QueryClientProvider client={queryClient}>
          <WorkflowRunDetailPage runId="wfr-1" />
        </QueryClientProvider>
      </NavigationProvider>
    </I18nProvider>,
  );
}

function renderDialog(
  props: Partial<React.ComponentProps<typeof WorkflowRunDialog>> = {},
) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const navigation: NavigationAdapter = {
    push: pushMock,
    replace: vi.fn(),
    back: vi.fn(),
    pathname: "/acme/workflows/wft-1",
    searchParams: new URLSearchParams(),
    getShareableUrl: (path) => path,
  };
  render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <NavigationProvider value={navigation}>
        <QueryClientProvider client={queryClient}>
          <WorkflowRunDialog
            templateId="wft-1"
            templateName="Bug Fix"
            runnable
            open
            onOpenChange={vi.fn()}
            {...props}
          />
        </QueryClientProvider>
      </NavigationProvider>
    </I18nProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  runRef.current = run();
  cancelMock.mockResolvedValue(run({ status: "cancelled" }));
  decideMock.mockResolvedValue(run({ status: "completed" }));
  runTemplateMock.mockResolvedValue(run({ id: "wfr-2" }));
});

describe("run detail header", () => {
  it("titles the run by its input, not by the template", async () => {
    // Every Bug Fix run would otherwise be called "Bug Fix" — the input title is
    // the only thing that distinguishes two runs of one template. Scoped to the
    // heading because the input section legitimately shows the same text.
    renderRun();
    expect(
      await screen.findByRole("heading", { name: "Claim endpoint 500s" }),
    ).toBeInTheDocument();
  });

  it("falls back to the template name when the input carries no title", async () => {
    runRef.current = run({ input: { description: "no title here" } });
    renderRun();
    expect(
      await screen.findByRole("heading", { name: "Bug Fix" }),
    ).toBeInTheDocument();
  });

  it("offers Cancel while the run can still be stopped", async () => {
    renderRun();
    fireEvent.click(await screen.findByRole("button", { name: "Cancel run" }));
    await waitFor(() => expect(cancelMock).toHaveBeenCalledWith("wfr-1"));
  });

  it("withholds Cancel once the run is terminal", async () => {
    runRef.current = run({
      status: "completed",
      completed_at: "2026-06-01T10:10:00Z",
    });
    renderRun();
    await screen.findByRole("heading", { name: "Claim endpoint 500s" });
    expect(
      screen.queryByRole("button", { name: "Cancel run" }),
    ).not.toBeInTheDocument();
  });
});

describe("unreadable run detail", () => {
  it("refuses to render a trace for a run it could not parse", async () => {
    // THE trap. `getWorkflowRun` spreads the requested id onto
    // EMPTY_WORKFLOW_RUN_DETAIL, so `id` is non-empty on a parse miss too;
    // `steps: []` is equally ambiguous because a fresh run has none either.
    // Only `status: ""` separates the two.
    runRef.current = run({ status: "", steps: [], step_count: 0 });
    renderRun();
    expect(
      await screen.findByText(
        "This run's details could not be read, so its trace is not shown. Nothing has been changed.",
      ),
    ).toBeInTheDocument();
    // And crucially no Cancel: cancelling a run we cannot describe is an action
    // taken on an unknown target.
    expect(
      screen.queryByRole("button", { name: "Cancel run" }),
    ).not.toBeInTheDocument();
  });

  it("renders the confusable pair differently", async () => {
    // A real pending run and an unreadable one are identical in id, steps and
    // acceptance. If this pair ever renders the same, the gate has regressed to
    // reading `id` or `steps.length`.
    runRef.current = run({
      status: "pending",
      steps: [],
      step_count: 0,
      started_at: null,
      input: {},
    });
    renderRun();
    expect(
      await screen.findByText(
        "No steps yet. The first step activates as soon as the run is routed.",
      ),
    ).toBeInTheDocument();
  });

  it("says the trace was LOST, not empty, when steps could not be read", async () => {
    // The schema degrades a malformed step list to `[]` locally so the header
    // survives. A non-zero step_count with no steps is the tell, and calling
    // that "nothing has happened yet" would hide a mid-flight run.
    runRef.current = run({ steps: [], step_count: 4 });
    renderRun();
    expect(
      await screen.findByText(
        "This run has 4 steps, but none of them could be read.",
      ),
    ).toBeInTheDocument();
  });
});

describe("terminal reasons", () => {
  it("explains a blocked reason and keeps the raw identifier", async () => {
    runRef.current = run({
      status: "blocked",
      blocked_reason: "routing_no_candidate",
    });
    renderRun();
    expect(
      await screen.findByText(
        "No eligible agent could run this step. The run stopped rather than handing the work to the wrong specialist.",
      ),
    ).toBeInTheDocument();
    // The token stays: it is what an operator greps a server log for.
    expect(screen.getByText("routing_no_candidate")).toBeInTheDocument();
  });

  it("shows an unrecognized reason verbatim rather than swallowing it", async () => {
    // A newer server's classification is still searchable; a generic "something
    // went wrong" would leave an operator with a stopped run and nothing to go on.
    runRef.current = run({
      status: "failed",
      failure_reason: "quota_exhausted_by_provider",
      failure_detail: null,
    });
    renderRun();
    expect(
      await screen.findAllByText(/quota_exhausted_by_provider/),
    ).not.toHaveLength(0);
  });

  it("names a cancellation as a cancellation, not a failure", async () => {
    runRef.current = run({ status: "cancelled", failure_reason: "cancelled" });
    renderRun();
    expect(
      await screen.findByText("This run was cancelled"),
    ).toBeInTheDocument();
    expect(screen.queryByText("This run failed")).not.toBeInTheDocument();
  });
});

describe("trace", () => {
  it("shows the routing reason so a surprising agent is diagnosable", async () => {
    renderRun();
    expect(
      await screen.findByText("Routed because: matched capability bug_analysis"),
    ).toBeInTheDocument();
  });

  it("renders every attempt at a node, not just the latest", async () => {
    // A rework round is a NEW step with the same node_key. Collapsing to the
    // latest attempt would erase why `implement` ran three times.
    runRef.current = run({
      step_count: 2,
      steps: [
        step({ id: "s1", node_key: "implement", attempt: 1, status: "failed" }),
        step({ id: "s2", node_key: "implement", attempt: 2, status: "passed" }),
      ],
    });
    renderRun();
    await screen.findByRole("heading", { name: "Claim endpoint 500s" });
    expect(screen.getAllByText("implement")).toHaveLength(2);
    expect(screen.getByText("attempt 2")).toBeInTheDocument();
  });

  it("offers the agent log only for a step that has a task", async () => {
    runRef.current = run({
      step_count: 2,
      steps: [
        step({ id: "s1", task_id: "task-1" }),
        step({
          id: "s2",
          node_key: "acceptance",
          node_type: "acceptance",
          task_id: null,
          agent_id: null,
          agent_name: null,
          submission: null,
        }),
      ],
    });
    renderRun();
    expect(await screen.findByTestId("transcript-task-1")).toBeInTheDocument();
    expect(screen.queryByTestId("transcript-null")).not.toBeInTheDocument();
  });

  it("shows the artifact summary and rationale", async () => {
    renderRun();
    expect(
      await screen.findByText("Null deref in the claim handler."),
    ).toBeInTheDocument();
    expect(
      screen.getByText("Reproduced on the failing input."),
    ).toBeInTheDocument();
  });

  it("never reads a missing verdict as a pass", async () => {
    // Mirrors the server rule from the other side: prose never implies pass.
    runRef.current = run({
      steps: [
        step({
          submission: {
            verdict: "",
            artifact: {},
            rationale: "I think it is fine",
            confidence: null,
            root_cause: null,
            validation_errors: null,
            submitted_at: "2026-06-01T10:05:00Z",
          },
        }),
      ],
    });
    renderRun();
    expect(await screen.findByText("no verdict")).toBeInTheDocument();
    expect(screen.queryByText("pass")).not.toBeInTheDocument();
  });

  it("surfaces the server's submission validation errors verbatim", async () => {
    runRef.current = run({
      steps: [
        step({
          status: "blocked",
          failure_reason: "submission_contract_invalid",
          failure_detail: null,
          submission: {
            verdict: "",
            artifact: {},
            rationale: "",
            confidence: null,
            root_cause: null,
            validation_errors: ['artifact.summary: expected string, got number'],
            submitted_at: "2026-06-01T10:05:00Z",
          },
        }),
      ],
    });
    renderRun();
    expect(
      await screen.findByText(
        "artifact.summary: expected string, got number",
      ),
    ).toBeInTheDocument();
  });

  it("does not stringify a structured artifact summary as prose", async () => {
    // `String({})` is "[object Object]", indistinguishable from a real summary.
    // A node contract that makes `summary` structured must degrade to no summary.
    runRef.current = run({
      steps: [
        step({
          submission: {
            verdict: "pass",
            artifact: { summary: { text: "nested" } },
            rationale: "still readable",
            confidence: null,
            root_cause: null,
            validation_errors: null,
            submitted_at: "2026-06-01T10:05:00Z",
          },
        }),
      ],
    });
    renderRun();
    await screen.findByText("still readable");
    expect(screen.queryByText(/\[object Object\]/)).not.toBeInTheDocument();
  });
});

describe("acceptance gate", () => {
  // Reject renders twice: the button that OPENS the form, and the confirm inside
  // it. The confirm is the later one in DOM order, and it is the one whose
  // disabled state is the guard under test.
  const rejectConfirm = () => {
    const buttons = screen.getAllByRole("button", { name: "Request rework" });
    return buttons[buttons.length - 1]!;
  };

  const gate = (patch: Record<string, unknown> = {}) =>
    run({
      status: "waiting_acceptance",
      step_count: 2,
      steps: [
        step({ id: "s1", node_key: "validate" }),
        step({
          id: "s2",
          node_key: "acceptance",
          node_type: "acceptance",
          status: "waiting_acceptance",
          task_id: null,
          agent_id: null,
          agent_name: null,
          submission: null,
        }),
      ],
      acceptance: {
        id: "wfa-1",
        step_id: "s2",
        status: "pending",
        reason: null,
        rework_target_node_key: null,
        criteria: ["The failing input now returns 200."],
        rework_targets: ["analyze", "implement", "validate"],
        created_at: "2026-06-01T10:06:00Z",
        ...patch,
      },
    });

  it("renders the criteria and the upstream evidence", async () => {
    runRef.current = gate();
    renderRun();
    expect(
      await screen.findByText("The failing input now returns 200."),
    ).toBeInTheDocument();
    // The gate's own step has no submission (an acceptance node produces a
    // decision, not a verdict), so the evidence is the step before it.
    expect(screen.getByText("Evidence from validate")).toBeInTheDocument();
  });

  it("accepts in one click, because an accept carries no payload", async () => {
    runRef.current = gate();
    renderRun();
    fireEvent.click(await screen.findByRole("button", { name: "Accept" }));
    await waitFor(() =>
      expect(decideMock).toHaveBeenCalledWith({ runId: "wfr-1", accept: true }),
    );
  });

  it("makes a reason-less rejection impossible, not merely refused", async () => {
    runRef.current = gate();
    renderRun();
    fireEvent.click(
      await screen.findByRole("button", { name: "Request rework" }),
    );
    // The confirm inside the form is disabled until BOTH a reason and a target
    // exist. Discovering the server's 409 instead would arrive at the one moment
    // the reviewer's attention is on the work, not on the API.
    await waitFor(() => expect(rejectConfirm()).toBeDisabled());
    expect(decideMock).not.toHaveBeenCalled();
  });

  it("sends the reason and the chosen target once both are present", async () => {
    runRef.current = gate();
    renderRun();
    fireEvent.click(
      await screen.findByRole("button", { name: "Request rework" }),
    );
    const textarea = await screen.findByRole("textbox");
    fireEvent.change(textarea, {
      target: { value: "The regression test still fails." },
    });

    // A reason alone is not enough — the target is the other half of the brief,
    // and the engine validates it against the PINNED node, so a missing one is
    // a 422 rather than a defaulted choice.
    expect(rejectConfirm()).toBeDisabled();

    // Drive the real Base UI select. `userEvent` (not fireEvent) because the
    // Select trigger opens on a full pointer gesture, and that is the behaviour
    // being relied on: a reviewer really does have to pick a target.
    const user = userEvent.setup();
    await user.click(
      screen.getByRole("combobox", { name: "Send the work back to" }),
    );
    await user.click(await screen.findByRole("option", { name: "implement" }));

    await waitFor(() => expect(rejectConfirm()).toBeEnabled());
    await user.click(rejectConfirm());

    await waitFor(() =>
      expect(decideMock).toHaveBeenCalledWith({
        runId: "wfr-1",
        accept: false,
        reason: "The regression test still fails.",
        rework_target: "implement",
      }),
    );
  });

  it("is accept-only when the gate permits no rework", async () => {
    // `rework_targets` defaults to [] on a parse failure, so a permissive
    // reading would turn contract drift into a stream of 422s. Empty means
    // accept-only, and the panel SAYS so rather than silently omitting Reject.
    runRef.current = gate({ rework_targets: [] });
    renderRun();
    expect(
      await screen.findByText(
        "This gate permits no rework, so the only decision available is Accept.",
      ),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Request rework" }),
    ).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Accept" })).toBeEnabled();
  });

  it("keeps a decided gate's reason visible after the fact", async () => {
    // The reviewer's reason is the only record of WHY a rework round exists.
    runRef.current = gate({
      status: "rejected",
      reason: "Fix does not cover the empty-queue case.",
      rework_target_node_key: "implement",
    });
    renderRun();
    expect(
      await screen.findByText(
        "Reason: Fix does not cover the empty-queue case.",
      ),
    ).toBeInTheDocument();
    expect(screen.getByText("Sent back to implement")).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Accept" }),
    ).not.toBeInTheDocument();
  });
});

describe("run dialog", () => {
  it("keeps Run disabled until both title and description are present", async () => {
    renderDialog();
    const runButton = screen.getByRole("button", { name: "Run" });
    expect(runButton).toBeDisabled();

    fireEvent.change(screen.getByPlaceholderText("One line naming the work"), {
      target: { value: "Claim endpoint 500s" },
    });
    // Title alone is not enough: the description is what every agent step's
    // prompt is built from, and without it the first agent guesses.
    expect(screen.getByRole("button", { name: "Run" })).toBeDisabled();

    fireEvent.change(
      screen.getByPlaceholderText(
        "What happened, how to reproduce it, and what done looks like",
      ),
      { target: { value: "Returns 500 for an empty queue." } },
    );
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Run" })).toBeEnabled(),
    );
  });

  it("says WHY an unpublished template cannot run, and refuses the click", () => {
    // A run pins a published version, so the server answers 409. Letting the
    // click through would teach the reader nothing about publishing being the fix.
    renderDialog({ runnable: false, refusal: "unpublished" });
    expect(
      screen.getByText(
        "Publish this workflow first — a run pins a published version, and this one has none.",
      ),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Run" })).toBeDisabled();
  });

  it("distinguishes the archived refusal from the unpublished one", () => {
    // An archived template may well HAVE a published version; it is refused
    // because archival is one-way. The fixes differ, so the wording must.
    renderDialog({ runnable: false, refusal: "archived" });
    expect(
      screen.getByText(
        "This workflow is archived, so it can no longer be run.",
      ),
    ).toBeInTheDocument();
  });

  it("navigates to the new run on success", async () => {
    renderDialog();
    fireEvent.change(screen.getByPlaceholderText("One line naming the work"), {
      target: { value: "Claim endpoint 500s" },
    });
    fireEvent.change(
      screen.getByPlaceholderText(
        "What happened, how to reproduce it, and what done looks like",
      ),
      { target: { value: "Returns 500 for an empty queue." } },
    );
    fireEvent.click(screen.getByRole("button", { name: "Run" }));

    await waitFor(() =>
      expect(runTemplateMock).toHaveBeenCalledWith({
        templateId: "wft-1",
        title: "Claim endpoint 500s",
        description: "Returns 500 for an empty queue.",
        project_id: null,
      }),
    );
    await waitFor(() =>
      expect(pushMock).toHaveBeenCalledWith("/acme/workflow-runs/wfr-2"),
    );
    expect(toastSuccessMock).toHaveBeenCalledWith("Run started");
  });

  it("does not navigate to an unreadable run, and does not call it an error", async () => {
    // The run DID start; what was lost is the handle. Navigating would land on a
    // detail page for a run we cannot name, and an error toast would suggest
    // retrying — which the server's idempotency key would collapse anyway.
    runTemplateMock.mockResolvedValue(run({ id: "", status: "" }));
    renderDialog();
    fireEvent.change(screen.getByPlaceholderText("One line naming the work"), {
      target: { value: "Claim endpoint 500s" },
    });
    fireEvent.change(
      screen.getByPlaceholderText(
        "What happened, how to reproduce it, and what done looks like",
      ),
      { target: { value: "Returns 500 for an empty queue." } },
    );
    fireEvent.click(screen.getByRole("button", { name: "Run" }));

    await waitFor(() => expect(toastWarningMock).toHaveBeenCalled());
    expect(toastErrorMock).not.toHaveBeenCalled();
    expect(pushMock).toHaveBeenCalledWith("/acme/workflow-runs");
    expect(pushMock).not.toHaveBeenCalledWith("/acme/workflow-runs/");
  });

  it("surfaces the server's own message when the run is refused", async () => {
    // A 409, a 422 and a 503 imply three different next actions; a generic
    // failure string collapses them into one.
    runTemplateMock.mockRejectedValue(
      new Error("template has no published version"),
    );
    renderDialog();
    fireEvent.change(screen.getByPlaceholderText("One line naming the work"), {
      target: { value: "x" },
    });
    fireEvent.change(
      screen.getByPlaceholderText(
        "What happened, how to reproduce it, and what done looks like",
      ),
      { target: { value: "y" } },
    );
    fireEvent.click(screen.getByRole("button", { name: "Run" }));

    await waitFor(() =>
      expect(toastErrorMock).toHaveBeenCalledWith(
        "template has no published version",
      ),
    );
    expect(pushMock).not.toHaveBeenCalled();
  });
});
