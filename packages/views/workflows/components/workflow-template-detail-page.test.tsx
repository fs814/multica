// @vitest-environment jsdom

/**
 * Page-level tests for the graph editor.
 *
 * The reducer's own suite (editor/editor-state.test.ts) proves the state
 * transitions; these prove the *policy* that only exists at this level and that
 * no type can express:
 *
 *  - read-only is derived from three independent server facts, and the page says
 *    which one applies;
 *  - publishing while dirty is intercepted, because publish freezes an immutable
 *    version and would freeze the wrong graph;
 *  - a 422 from the save endpoint lands in the problems strip verbatim rather
 *    than in a toast that tells the author nothing actionable;
 *  - the i18n keys this page references actually exist in the EN bundle - tsc
 *    checks the selector path against the JSON's *type*, but a value the page
 *    interpolates into is only exercised by rendering it.
 *
 * xyflow is stubbed. It needs real layout (`getBoundingClientRect`, ResizeObserver
 * geometry) that jsdom does not provide, so a real canvas here would test the
 * library's jsdom compatibility rather than this page's wiring. The stub reports
 * the node count and keys, which is what the page is responsible for handing it.
 */

import { describe, expect, it, vi, beforeEach } from "vitest";
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import {
  WorkflowDefinitionSchema,
  type WorkflowDefinition,
  type WorkflowTemplateDetail,
} from "@multica/core/workflows";
import enCommon from "../../locales/en/common.json";
import enWorkflows from "../../locales/en/workflows.json";
import enUi from "../../locales/en/ui.json";
import { NavigationProvider, type NavigationAdapter } from "../../navigation";
import { bugFixDefinition } from "../graph/bug-fix.fixture";

const TEST_RESOURCES = {
  en: { common: enCommon, workflows: enWorkflows, ui: enUi },
};

const detailRef = vi.hoisted(() => ({
  current: null as WorkflowTemplateDetail | null,
}));
const publishedRef = vi.hoisted(() => ({ current: null as WorkflowTemplateDetail | null }));
const detailErrorRef = vi.hoisted(() => ({
  current: null as Error | null,
}));
const membersRef = vi.hoisted(() => ({ current: [] as unknown[] }));
const saveMock = vi.hoisted(() => vi.fn());
const runMock = vi.hoisted(() => vi.fn());
const publishMock = vi.hoisted(() => vi.fn());
const duplicateMock = vi.hoisted(() => vi.fn());
const validateMock = vi.hoisted(() => vi.fn());
const toastErrorMock = vi.hoisted(() => vi.fn());
const toastSuccessMock = vi.hoisted(() => vi.fn());
const navigationPushMock = vi.hoisted(() => vi.fn());
const clipboardWriteMock = vi.hoisted(() => vi.fn());

// The canvas is replaced wholesale (see the header). It still reports the graph
// it was handed, so a page that stopped feeding it nodes fails these tests.
vi.mock("../canvas/workflow-canvas", () => ({
  WorkflowCanvas: (props: {
    nodes: { id: string; data: { node: WorkflowDefinition["nodes"][number] } }[];
    onChangeNode(node: WorkflowDefinition["nodes"][number]): void;
    edges: { id: string }[];
    readOnly: boolean;
    selectedNodeId: string | null;
    onEdgesChange(edges: { id: string }[]): void;
    onSelectNode(nodeId: string | null): void;
  }) => (
    <div
      data-testid="canvas"
      data-read-only={String(props.readOnly)}
      data-selected={props.selectedNodeId ?? ""}
      data-edges={props.edges.map((edge) => edge.id).join(",")}
    >
      {props.nodes.map((node) => node.id).join(",")}
      {props.nodes.filter((node) => node.data.node.type === "input").map((node) => (
        <button key={node.id} data-testid="canvas-fill-input" onClick={() => props.onChangeNode({
          ...node.data.node, name: "Current machine", instruction: "List the current OS version",
        })} />
      ))}
      {!props.readOnly && props.edges.length > 0 ? (
        <button
          type="button"
          data-testid="canvas-delete-first-edge"
          onClick={() => props.onEdgesChange(props.edges.slice(1))}
        ></button>
      ) : null}
      {!props.readOnly &&
      props.nodes.some((node) => node.id === "acceptance") ? (
        <button
          type="button"
          data-testid="canvas-select-acceptance"
          onClick={() => props.onSelectNode("acceptance")}
          aria-label="Select acceptance"
        ></button>
      ) : null}
    </div>
  ),
}));

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({
    workflows: () => "/acme/workflows",
    workflowDetail: (id: string) => `/acme/workflows/${id}`,
    workflowRuns: () => "/acme/workflow-runs",
    workflowRunDetail: (id: string) => `/acme/workflow-runs/${id}`,
  }),
  // The header now mounts the Run dialog, which reads the workspace name for
  // its breadcrumb and the project list for its optional picker.
  useCurrentWorkspace: () => ({ id: "ws-1", name: "Acme", slug: "acme" }),
}));
vi.mock("@multica/core/projects/queries", () => ({
  projectListOptions: (wsId: string) => ({
    queryKey: ["projects", wsId],
    queryFn: () => Promise.resolve([]),
  }),
}));
vi.mock("@multica/core/workspace/queries", () => ({
  memberListOptions: (wsId: string) => ({
    queryKey: ["members", wsId],
    queryFn: () => Promise.resolve(membersRef.current),
  }),
  agentListOptions: (wsId: string) => ({
    queryKey: ["agents", wsId],
    queryFn: () => Promise.resolve([]),
  }),
}));
vi.mock("@multica/core/auth", () => {
  type AuthState = { user: { id: string } | null };
  const state = (): AuthState => ({ user: { id: "user-1" } });
  return {
    useAuthStore: Object.assign(
      (selector?: (s: AuthState) => unknown) =>
        selector ? selector(state()) : state(),
      { getState: state },
    ),
  };
});
vi.mock("@multica/core/api", () => ({
  api: { validateWorkflowDefinition: validateMock },
}));
vi.mock("@multica/core/workflows", async () => {
  const actual = await vi.importActual<
    typeof import("@multica/core/workflows")
  >("@multica/core/workflows");
  return {
    ...actual,
    workflowTemplateDetailOptions: (wsId: string, id: string) => ({
      queryKey: ["workflow-templates", wsId, "detail", id],
      queryFn: () =>
        detailErrorRef.current
          ? Promise.reject(detailErrorRef.current)
          : detailRef.current
          ? Promise.resolve(detailRef.current)
          : Promise.reject(new Error("not found")),
    }),
    workflowTemplateRunOptions: (wsId: string, id: string) => ({
      queryKey: ["workflow-templates", wsId, "published", id],
      queryFn: async () => publishedRef.current ?? detailRef.current,
    }),
    useUpdateWorkflowTemplate: () => ({
      mutateAsync: saveMock,
      isPending: false,
    }),
    usePublishWorkflowTemplate: () => ({
      mutateAsync: publishMock,
      isPending: false,
    }),
    useDuplicateWorkflowTemplate: () => ({
      mutateAsync: duplicateMock,
      isPending: false,
    }),
    workflowInputInstanceListOptions: () => ({ queryKey: ["test-input-instances"], queryFn: async () => [] }),
    useRunWorkflowTemplate: () => ({
      mutateAsync: runMock,
      isPending: false,
    }),
  };
});
vi.mock("sonner", () => ({
  toast: { success: toastSuccessMock, error: toastErrorMock },
}));

import { WorkflowTemplateDetailPage } from "./workflow-template-detail-page";

function detail(
  patch: Partial<WorkflowTemplateDetail> = {},
): WorkflowTemplateDetail {
  return {
    id: "wft-1",
    workspace_id: "ws-1",
    key: "bug_fix",
    name: "Bug Fix",
    description: "Reproduce, fix, validate, accept.",
    status: "draft",
    current_version: null,
    is_builtin: false,
    node_count: 5,
    revision: 7,
    created_at: "2026-05-28T00:00:00Z",
    updated_at: "2026-05-28T00:00:00Z",
    definition: bugFixDefinition(),
    // A draft template always HAS its draft version: the create endpoint makes
    // the two together (a template with no version has no graph). The empty list
    // was an impossible state, and the page now reads publishability off this
    // list rather than off `status`, because a published template's status never
    // returns to "draft" even though PATCH opens a new draft version on it.
    versions: [
      { id: "wftv-1", version: 1, status: "draft", published_at: null },
    ],
    ...patch,
  };
}

function allNodeTypesDefinition(): WorkflowDefinition {
  return WorkflowDefinitionSchema.parse({
    entry_node: "intake",
    nodes: [
      { key: "intake", type: "input", input_mode: "text", next: ["plan"] },
      {
        key: "plan",
        type: "agent",
        next: ["gate"],
        routing: { strategy: "capability", capability: "code_change" },
        submission_schema: "code_change",
      },
      {
        key: "gate",
        type: "condition",
        branches: [
          { when_verdict: "pass", target: "spread" },
          { when_verdict: "fail", target: "acceptance" },
        ],
      },
      {
        key: "spread",
        type: "fan_out",
        next: ["worker"],
        fan_out_max: 3,
      },
      {
        key: "worker",
        type: "agent",
        next: ["gather"],
        routing: { strategy: "capability", capability: "code_change" },
        submission_schema: "code_change",
      },
      {
        key: "gather",
        type: "join",
        next: ["acceptance"],
        join_policy: "fail_fast",
        join_sources: ["worker"],
      },
      {
        key: "acceptance",
        type: "acceptance",
        next: ["end"],
        rework_targets: ["plan"],
      },
      { key: "end", type: "end" },
    ],
  });
}

function renderPage() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const navigation: NavigationAdapter = {
    push: navigationPushMock,
    replace: vi.fn(),
    back: vi.fn(),
    pathname: "/acme/workflows/wft-1",
    searchParams: new URLSearchParams(),
    getShareableUrl: (path) => path,
  };
  return render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <NavigationProvider value={navigation}>
        <QueryClientProvider client={queryClient}>
          <WorkflowTemplateDetailPage templateId="wft-1" />
        </QueryClientProvider>
      </NavigationProvider>
    </I18nProvider>,
  );
}

/** The canvas stub, once the query has resolved and the reducer has hydrated. */
async function canvas(): Promise<HTMLElement> {
  return waitFor(async () => {
    const element = await screen.findByTestId("canvas");
    // Hydration is an effect, so the first paint has an empty graph. Waiting for
    // content is what distinguishes "hydrated" from "rendered".
    expect(element.textContent).not.toBe("");
    return element;
  });
}

beforeEach(() => {
  publishedRef.current = null;
  vi.clearAllMocks();
  Object.defineProperty(navigator, "clipboard", {
    configurable: true,
    value: { writeText: clipboardWriteMock },
  });
  clipboardWriteMock.mockResolvedValue(undefined);
  detailRef.current = detail();
  detailErrorRef.current = null;
  membersRef.current = [{ user_id: "user-1", role: "admin" }];
  saveMock.mockResolvedValue(detail());
  runMock.mockResolvedValue({ id: "run-1", status: "running" });
  publishMock.mockResolvedValue(detail({ status: "published" }));
  duplicateMock.mockResolvedValue(
    detail({
      id: "wft-copy",
      key: "bug_fix_copy",
      name: "Bug Fix Copy",
      status: "published",
      current_version: 1,
      is_builtin: false,
      versions: [
        {
          id: "wftv-copy-1",
          version: 1,
          status: "published",
          published_at: "2026-06-01T00:00:00Z",
        },
      ],
    }),
  );
  validateMock.mockResolvedValue({ valid: true, messages: [] });
});

describe("WorkflowTemplateDetailPage header", () => {
  it("shows the name, description and key line", async () => {
    renderPage();
    expect(await screen.findByText("Bug Fix")).toBeInTheDocument();
    expect(
      screen.getByText("Reproduce, fix, validate, accept."),
    ).toBeInTheDocument();
    expect(screen.getByText("Key: bug_fix")).toBeInTheDocument();
  });

  it("hands the whole graph to the canvas", async () => {
    renderPage();
    // All five Bug Fix nodes, in declaration order. A page that projected only
    // the nodes it renders controls for would silently shrink the graph.
    expect((await canvas()).textContent).toBe(
      "analyze,implement,validate,acceptance,end",
    );
  });
});

describe("read-only", () => {
  it("names the built-in refusal, which the server answers with 409", async () => {
    detailRef.current = detail({ is_builtin: true });
    renderPage();
    expect(
      await screen.findByText(
        "Built-in workflows cannot be edited. Duplicate it to change it.",
      ),
    ).toBeInTheDocument();
    expect((await canvas()).dataset.readOnly).toBe("true");
    // Publish is withheld too: the server refuses to publish a built-in.
    expect(
      screen.queryByRole("button", { name: "Publish" }),
    ).not.toBeInTheDocument();
  });

  it("duplicates a built-in and opens the editable runnable copy", async () => {
    detailRef.current = detail({ is_builtin: true });
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "Duplicate" }));

    await waitFor(() => expect(duplicateMock).toHaveBeenCalledWith("wft-1"));
    expect(navigationPushMock).toHaveBeenCalledWith("/acme/workflows/wft-copy");
    expect(toastSuccessMock).toHaveBeenCalledWith("Workflow duplicated");
  });

  it("does not let a plain member duplicate a built-in", async () => {
    detailRef.current = detail({ is_builtin: true });
    membersRef.current = [{ user_id: "user-1", role: "member" }];
    renderPage();

    expect(
      await screen.findByRole("button", { name: "Duplicate" }),
    ).toBeDisabled();
    expect(duplicateMock).not.toHaveBeenCalled();
  });

  it("names the archived refusal, since archival is one-way", async () => {
    detailRef.current = detail({ status: "archived" });
    renderPage();
    expect(
      await screen.findByText(
        "This workflow is archived, so it can no longer be edited.",
      ),
    ).toBeInTheDocument();
  });

  it("names the role refusal for a plain member", async () => {
    membersRef.current = [{ user_id: "user-1", role: "member" }];
    renderPage();
    expect(
      await screen.findByText(
        "Only workspace owners and admins can edit workflows.",
      ),
    ).toBeInTheDocument();
    expect((await canvas()).dataset.readOnly).toBe("true");
  });

  it("lets an admin edit", async () => {
    renderPage();
    expect((await canvas()).dataset.readOnly).toBe("false");
    expect(
      screen.getByRole("button", { name: "Add Issue step" }),
    ).toBeInTheDocument();
  });
});

describe("dirty tracking", () => {
  it("keeps Save disabled on a template nobody has edited", async () => {
    renderPage();
    await canvas();
    // The round-trip guarantee in action: opening a template is provably not an
    // edit, so offering a save here would produce an empty version diff.
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  });

  it("enables Save once a node is added", async () => {
    renderPage();
    await canvas();
    fireEvent.click(screen.getByRole("button", { name: "Add Issue step" }));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Save" })).toBeEnabled(),
    );
  });
});

describe("validate", () => {
  it("reports the client mirror's problems without a round trip", async () => {
    renderPage();
    await canvas();
    // A blank agent node is invalid several ways over (no routing strategy, no
    // outgoing edge, unreachable), so the mirror answers and the server is never
    // asked - the point being that the author gets an answer on the same frame.
    fireEvent.click(screen.getByRole("button", { name: "Add Issue step" }));
    fireEvent.click(screen.getByRole("button", { name: "Validate" }));

    expect(
      await screen.findByText(
        'Agent node "step_1" must have exactly one outgoing edge, got 0',
      ),
    ).toBeInTheDocument();
    expect(validateMock).not.toHaveBeenCalled();
  });

  it("asks the server when the mirror is clean, and says so", async () => {
    renderPage();
    await canvas();
    fireEvent.click(screen.getByRole("button", { name: "Validate" }));

    expect(await screen.findByText("No problems found.")).toBeInTheDocument();
    expect(validateMock).toHaveBeenCalledTimes(1);
    // The graph sent is the working definition, not the fetched one.
    expect(validateMock.mock.calls[0]?.[0]).toStrictEqual(bugFixDefinition());
  });

  it("does not present a transport failure as a broken graph", async () => {
    validateMock.mockRejectedValue(new Error("network down"));
    renderPage();
    await canvas();
    fireEvent.click(screen.getByRole("button", { name: "Validate" }));
    expect(await screen.findByText("network down")).toBeInTheDocument();
  });
});

describe("save", () => {
  it("saves one deleted edge and keeps it deleted after reopening", async () => {
    const before = bugFixDefinition();
    const view = renderPage();
    await canvas();

    fireEvent.click(screen.getByTestId("canvas-delete-first-edge"));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Save" })).toBeEnabled(),
    );
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(saveMock).toHaveBeenCalledTimes(1));
    const sent = saveMock.mock.calls[0]?.[0] as {
      definition: ReturnType<typeof bugFixDefinition>;
    };
    expect(sent.definition.nodes).toHaveLength(before.nodes.length);
    expect(sent.definition.nodes[0]?.next).toEqual([]);
    expect(sent.definition.nodes.slice(1)).toStrictEqual(before.nodes.slice(1));

    view.unmount();
    detailRef.current = detail({ definition: sent.definition });
    renderPage();
    const reopened = await canvas();
    expect(reopened.textContent).toContain(
      before.nodes.map((node) => node.key).join(","),
    );
    expect(reopened.dataset.edges?.split(",")).not.toContain(
      "next:analyze:0:implement",
    );
  });

  it("cleans polluted node types before validate, save, publish, and reload", async () => {
    const polluted = allNodeTypesDefinition();
    for (const node of polluted.nodes) {
      if (node.type !== "input") node.input_mode = "text";
    }
    detailRef.current = detail({
      definition: polluted,
      node_count: polluted.nodes.length,
    });

    const view = renderPage();
    await canvas();
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Save" })).toBeEnabled(),
    );

    fireEvent.click(screen.getByRole("button", { name: "Validate" }));
    await waitFor(() => expect(validateMock).toHaveBeenCalledTimes(1));
    const validated = validateMock.mock.calls[0]?.[0] as WorkflowDefinition;
    expect(
      validated.nodes.find((node) => node.type === "input")?.input_mode,
    ).toBe("text");
    for (const node of validated.nodes.filter(
      (node) => node.type !== "input",
    )) {
      expect(node).not.toHaveProperty("input_mode");
    }

    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(saveMock).toHaveBeenCalledTimes(1));
    const sent = saveMock.mock.calls[0]?.[0] as {
      id: string;
      definition: WorkflowDefinition;
    };
    expect(sent.id).toBe("wft-1");
    expect(new Set(sent.definition.nodes.map((node) => node.type))).toEqual(
      new Set([
        "input",
        "agent",
        "condition",
        "fan_out",
        "join",
        "acceptance",
        "end",
      ]),
    );
    for (const node of sent.definition.nodes.filter(
      (node) => node.type !== "input",
    )) {
      expect(node).not.toHaveProperty("input_mode");
    }

    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Save" })).toBeDisabled(),
    );
    fireEvent.click(screen.getByRole("button", { name: "Publish" }));
    await waitFor(() => expect(publishMock).toHaveBeenCalledWith("wft-1"));

    view.unmount();
    detailRef.current = detail({ definition: sent.definition });
    renderPage();
    await canvas();
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  });

  it("repairs an existing empty Acceptance target before save, publish, and reload", async () => {
    const incomplete = bugFixDefinition();
    incomplete.nodes.find((node) => node.key === "acceptance")!.rework_targets =
      [];
    detailRef.current = detail({ definition: incomplete });

    const view = renderPage();
    await canvas();
    fireEvent.click(screen.getByTestId("canvas-select-acceptance"));

    expect(
      await screen.findByText(
        "An acceptance step needs at least one target, so a rejection can route somewhere.",
      ),
    ).toBeInTheDocument();
    const group = screen.getByRole("group", { name: "Rework targets" });
    expect(within(group).getAllByRole("checkbox")).toHaveLength(3);
    fireEvent.click(within(group).getAllByRole("checkbox")[1]!);

    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Save" })).toBeEnabled(),
    );
    fireEvent.click(screen.getByRole("button", { name: "Validate" }));
    await waitFor(() => expect(validateMock).toHaveBeenCalledTimes(1));
    const validated = validateMock.mock.calls[0]?.[0] as WorkflowDefinition;
    expect(
      validated.nodes.find((node) => node.key === "acceptance")?.rework_targets,
    ).toEqual(["implement"]);

    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(saveMock).toHaveBeenCalledTimes(1));
    const sent = saveMock.mock.calls[0]?.[0] as {
      definition: WorkflowDefinition;
    };
    expect(
      sent.definition.nodes.find((node) => node.key === "acceptance")
        ?.rework_targets,
    ).toEqual(["implement"]);
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Save" })).toBeDisabled(),
    );
    fireEvent.click(screen.getByRole("button", { name: "Publish" }));
    await waitFor(() => expect(publishMock).toHaveBeenCalledWith("wft-1"));

    view.unmount();
    detailRef.current = detail({ definition: sent.definition });
    renderPage();
    await canvas();
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
    fireEvent.click(screen.getByTestId("canvas-select-acceptance"));
    const reopened = screen.getByRole("group", { name: "Rework targets" });
    expect(within(reopened).getAllByRole("checkbox")[1]).toBeChecked();
  });

  it("sends the working graph and reports success", async () => {
    renderPage();
    await canvas();
    fireEvent.click(screen.getByRole("button", { name: "Add Issue step" }));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Save" })).toBeEnabled(),
    );
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(saveMock).toHaveBeenCalledTimes(1));
    const sent = saveMock.mock.calls[0]?.[0] as {
      id: string;
      revision: number;
      definition: { nodes: { key: string }[] };
    };
    expect(sent.id).toBe("wft-1");
    expect(sent.revision).toBe(7);
    expect(sent.definition.nodes.map((node) => node.key)).toContain("step_1");
    await waitFor(() =>
      expect(toastSuccessMock).toHaveBeenCalledWith("Draft saved"),
    );
    // Dirty clears against the graph that was SENT, not against the response -
    // a save over a published template returns the published bytes.
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Save" })).toBeDisabled(),
    );
  });

  it("keeps conflicted working JSON copyable and retries it with the latest revision", async () => {
    saveMock
      .mockRejectedValueOnce(
        Object.assign(new Error("API error: 409"), {
          status: 409,
          body: { code: "workflow_template_revision_conflict" },
        }),
      )
      .mockResolvedValueOnce(detail({ revision: 9 }));
    renderPage();
    await canvas();
    fireEvent.click(screen.getByRole("button", { name: "Add Issue step" }));
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(
      await screen.findByRole("alertdialog", {
        name: "This draft changed elsewhere",
      }),
    ).toBeInTheDocument();
    expect((await canvas()).textContent).toContain("step_1");
    expect(
      screen.getByRole("button", { name: "Reload latest" }),
    ).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Copy working JSON" }));
    await waitFor(() => expect(clipboardWriteMock).toHaveBeenCalledTimes(1));
    const copied = JSON.parse(
      clipboardWriteMock.mock.calls[0]?.[0] as string,
    ) as {
      nodes: { key: string }[];
    };
    expect(copied.nodes.map((node) => node.key)).toContain("step_1");

    detailRef.current = detail({ revision: 8 });
    fireEvent.click(screen.getByRole("button", { name: "Reload and retry" }));
    await waitFor(() => expect(saveMock).toHaveBeenCalledTimes(2));
    const retried = saveMock.mock.calls[1]?.[0] as {
      revision: number;
      definition: { nodes: { key: string }[] };
    };
    expect(retried.revision).toBe(8);
    expect(retried.definition.nodes.map((node) => node.key)).toContain(
      "step_1",
    );
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Save" })).toBeDisabled(),
    );
  });

  it("reloads the winning draft of a published template only after explicit conflict recovery", async () => {
    saveMock.mockRejectedValueOnce(
      Object.assign(new Error("API error: 409"), {
        status: 409,
        body: { code: "workflow_template_revision_conflict" },
      }),
    );
    detailRef.current = detail({
      status: "published",
      current_version: 1,
      versions: [
        {
          id: "wftv-1",
          version: 1,
          status: "published",
          published_at: "2026-06-01T00:00:00Z",
        },
      ],
    });
    renderPage();
    await canvas();
    fireEvent.click(screen.getByRole("button", { name: "Add Issue step" }));
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await screen.findByText("This draft changed elsewhere");
    expect((await canvas()).textContent).toContain("step_1");

    const winnerDraft = WorkflowDefinitionSchema.parse({
      schema_version: 1,
      entry_node: "winner",
      nodes: [{ key: "winner", type: "end", name: "Winner draft" }],
    });
    detailRef.current = detail({
      status: "published",
      current_version: 1,
      revision: 8,
      definition: winnerDraft,
      versions: [
        {
          id: "wftv-2",
          version: 2,
          status: "draft",
          published_at: null,
        },
        {
          id: "wftv-1",
          version: 1,
          status: "published",
          published_at: "2026-06-01T00:00:00Z",
        },
      ],
    });
    fireEvent.click(screen.getByRole("button", { name: "Reload latest" }));
    await waitFor(() =>
      expect(
        screen.queryByText("This draft changed elsewhere"),
      ).not.toBeInTheDocument(),
    );
    expect((await canvas()).textContent).toBe("winner");
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  });

  it("keeps working JSON copyable when conflict refetch fails instead of using cached data", async () => {
    saveMock.mockRejectedValueOnce(
      Object.assign(new Error("API error: 409"), {
        status: 409,
        body: { code: "workflow_template_revision_conflict" },
      }),
    );
    renderPage();
    await canvas();
    fireEvent.click(screen.getByRole("button", { name: "Add Issue step" }));
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await screen.findByText("This draft changed elsewhere");

    detailErrorRef.current = new Error("refetch failed");
    fireEvent.click(screen.getByRole("button", { name: "Reload latest" }));
    await waitFor(() => expect(toastErrorMock).toHaveBeenCalled());

    expect(
      screen.getByRole("alertdialog", {
        name: "This draft changed elsewhere",
      }),
    ).toBeInTheDocument();
    expect((await canvas()).textContent).toContain("step_1");

    fireEvent.click(screen.getByRole("button", { name: "Copy working JSON" }));
    await waitFor(() => expect(clipboardWriteMock).toHaveBeenCalledTimes(1));
    const copied = JSON.parse(
      clipboardWriteMock.mock.calls[0]?.[0] as string,
    ) as { nodes: { key: string }[] };
    expect(copied.nodes.map((node) => node.key)).toContain("step_1");
  });

  it("surfaces a 422's messages inline rather than as a toast", async () => {
    // The whole reason the page reads `ApiError.body.messages`: "save failed" is
    // not something an author can act on, whereas the rule and the node are.
    saveMock.mockRejectedValue(
      Object.assign(new Error("API error: 422"), {
        status: 422,
        body: {
          error: "invalid workflow definition",
          messages: ['Agent node "step_1" declares no routing'],
        },
      }),
    );
    renderPage();
    await canvas();
    fireEvent.click(screen.getByRole("button", { name: "Add Issue step" }));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Save" })).toBeEnabled(),
    );
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(
      await screen.findByText('Agent node "step_1" declares no routing'),
    ).toBeInTheDocument();
    expect(
      screen.getByText("The server refused this graph."),
    ).toBeInTheDocument();
    expect(toastErrorMock).not.toHaveBeenCalled();
  });
});

describe("publish", () => {
  it("publishes straight away when there is nothing unsaved", async () => {
    renderPage();
    await canvas();
    fireEvent.click(screen.getByRole("button", { name: "Publish" }));
    await waitFor(() => expect(publishMock).toHaveBeenCalledWith("wft-1"));
  });

  // Regression: publishability used to be gated on `status === "draft"`, but
  // `workflow_template.status` goes draft -> published and never back, while
  // PATCH deliberately opens a NEW draft version over a published template. That
  // combination stranded the edit — it saved, and the button that could ship it
  // never reappeared. The version list is the honest signal.
  it("offers publish for a draft version of an already-published template", async () => {
    detailRef.current = detail({
      status: "published",
      current_version: 1,
      versions: [
        {
          id: "wftv-1",
          version: 1,
          status: "published",
          published_at: "2026-06-01T00:00:00Z",
        },
        { id: "wftv-2", version: 2, status: "draft", published_at: null },
      ],
    });
    renderPage();
    await canvas();
    fireEvent.click(screen.getByRole("button", { name: "Publish" }));
    await waitFor(() => expect(publishMock).toHaveBeenCalledWith("wft-1"));
  });

  it("hides publish when every version is already published", async () => {
    detailRef.current = detail({
      status: "published",
      current_version: 1,
      versions: [
        {
          id: "wftv-1",
          version: 1,
          status: "published",
          published_at: "2026-06-01T00:00:00Z",
        },
      ],
    });
    renderPage();
    await canvas();
    expect(
      screen.queryByRole("button", { name: "Publish" }),
    ).not.toBeInTheDocument();
  });

  it("intercepts a dirty publish, because a version is frozen forever", async () => {
    renderPage();
    await canvas();
    fireEvent.click(screen.getByRole("button", { name: "Add Issue step" }));
    fireEvent.click(screen.getByRole("button", { name: "Publish" }));

    expect(
      await screen.findByText("Publish without saving?"),
    ).toBeInTheDocument();
    // Nothing has been frozen yet - the dialog is a gate, not a notification.
    expect(publishMock).not.toHaveBeenCalled();
  });

  it("saves before publishing when asked to", async () => {
    renderPage();
    await canvas();
    fireEvent.click(screen.getByRole("button", { name: "Add Issue step" }));
    fireEvent.click(screen.getByRole("button", { name: "Publish" }));
    fireEvent.click(
      await screen.findByRole("button", { name: "Save, then publish" }),
    );

    await waitFor(() => expect(saveMock).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(publishMock).toHaveBeenCalledTimes(1));
  });

  it("does not publish when the pre-publish save is rejected", async () => {
    // Publishing after a failed save would freeze the OLDER graph - precisely the
    // outcome the dialog exists to prevent.
    saveMock.mockRejectedValue(
      Object.assign(new Error("API error: 422"), {
        status: 422,
        body: { messages: ['Agent node "step_1" declares no routing'] },
      }),
    );
    renderPage();
    await canvas();
    fireEvent.click(screen.getByRole("button", { name: "Add Issue step" }));
    fireEvent.click(screen.getByRole("button", { name: "Publish" }));
    fireEvent.click(
      await screen.findByRole("button", { name: "Save, then publish" }),
    );

    expect(
      await screen.findByText('Agent node "step_1" declares no routing'),
    ).toBeInTheDocument();
    expect(publishMock).not.toHaveBeenCalled();
  });
});

describe("unreadable payload", () => {
  it("refuses to render an editor for a template it could not parse", async () => {
    // parseWithFallback spreads the requested id onto the fallback, so `data` and
    // `data.id` are always truthy; `key` is the field that still separates a real
    // row from a parse miss. Without this gate the page would offer a Publish
    // button for a graph the user was never shown.
    detailRef.current = detail({ key: "" });
    renderPage();
    expect(await screen.findByText("Workflow not found")).toBeInTheDocument();
    expect(screen.queryByTestId("canvas")).not.toBeInTheDocument();
  });
});

describe("node deletion", () => {
  it("removes an added node from the panel and supports undo and redo", async () => {
    renderPage();
    await canvas();
    fireEvent.click(screen.getByRole("button", { name: "Add Issue step" }));
    expect(screen.getByTestId("canvas")).toHaveTextContent("step_1");
    fireEvent.click(screen.getByRole("button", { name: "Delete node" }));
    expect(screen.getByTestId("canvas")).not.toHaveTextContent("step_1");
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Undo" }));
    expect(screen.getByTestId("canvas")).toHaveTextContent("step_1");
    fireEvent.click(screen.getByRole("button", { name: "Redo" }));
    expect(screen.getByTestId("canvas")).not.toHaveTextContent("step_1");
  });

  it("saves a deleted node without incident edges and keeps it deleted after reopening", async () => {
    const view = renderPage();
    await canvas();
    fireEvent.click(screen.getByTestId("canvas-select-acceptance"));
    fireEvent.click(screen.getByRole("button", { name: "Delete node" }));
    expect(screen.getByTestId("canvas")).not.toHaveTextContent("acceptance");
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(saveMock).toHaveBeenCalledTimes(1));
    const sent = saveMock.mock.calls[0]?.[0] as { definition: WorkflowDefinition };
    expect(sent.definition.nodes.map((node) => node.key)).not.toContain("acceptance");
    expect(sent.definition.nodes.find((node) => node.key === "validate")?.next).toEqual([]);
    view.unmount();
    detailRef.current = detail({ definition: sent.definition });
    renderPage();
    const reopened = await canvas();
    expect(reopened).not.toHaveTextContent("acceptance");
    expect(reopened.dataset.edges).not.toContain("acceptance");
  });
});
describe("running with inline Input content", () => {
  it("passes freshly typed content to the Run request without saving or retyping", async () => {
    const definition = allNodeTypesDefinition();
    definition.nodes[0]!.instruction = "Previously published content";
        detailRef.current = detail({
      definition, status: "published", current_version: 1,
      versions: [{ id: "published-1", version: 1, status: "published", published_at: "2026-09-09T00:00:00Z" }],
    });
    renderPage();
    await canvas();
    fireEvent.click(screen.getByTestId("canvas-fill-input"));
    fireEvent.click(screen.getByRole("button", { name: "Run" }));
    expect(await screen.findByRole("textbox", { name: "Title" })).toHaveValue("Current machine");
    expect(screen.getByRole("textbox", { name: "Description" })).toHaveValue("List the current OS version");
    fireEvent.click(screen.getByRole("button", { name: "Run" }));
    await waitFor(() => expect(runMock).toHaveBeenCalledWith(expect.objectContaining({
      templateId: "wft-1", title: "Current machine", description: "List the current OS version",
    })));
    expect(saveMock).not.toHaveBeenCalled();
    expect(publishMock).not.toHaveBeenCalled();
    expect(definition.nodes[0]!.instruction).toBe("Previously published content");
  });
});
it("opens saved input controls with unsaved canvas values without starting a run", async () => {
  detailRef.current = detail({ definition: allNodeTypesDefinition(), status: "draft", current_version: null });
  renderPage();
  await canvas();
  fireEvent.click(screen.getByTestId("canvas-fill-input"));
  fireEvent.click(screen.getByRole("button", { name: "Save as instance" }));
  expect(await screen.findByRole("textbox", { name: "Instance name" })).toBeVisible();
  expect(screen.getByRole("textbox", { name: "Title" })).toHaveValue("Current machine");
  expect(screen.getByRole("textbox", { name: "Description" })).toHaveValue("List the current OS version");
  expect(runMock).not.toHaveBeenCalled();
  expect(saveMock).not.toHaveBeenCalled();
  expect(publishMock).not.toHaveBeenCalled();
});

it("uses the published input declaration while retaining unsaved draft text", async () => {
  const definition = allNodeTypesDefinition();
  const publication = detail({
    definition, status: "published", current_version: 1,
    versions: [{ id: "published-1", version: 1, status: "published", published_at: null }],
  });
  publishedRef.current = publication;
  const draft = structuredClone(definition);
  draft.nodes[0]!.input_fields = [{ key: "draft_only", label: "Draft-only field", type: "text", required: true, options: [], placeholder: "" }];
  detailRef.current = { ...publication, definition: draft };
  renderPage();
  await canvas();
  fireEvent.click(screen.getByTestId("canvas-fill-input"));
  await waitFor(() => expect(screen.getByRole("button", { name: "Run" })).toBeEnabled());
  fireEvent.click(screen.getByRole("button", { name: "Run" }));
  expect(await screen.findByRole("textbox", { name: "Description" })).toHaveValue("List the current OS version");
  expect(screen.queryByRole("textbox", { name: "Draft-only field" })).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Run" }));
  await waitFor(() => expect(runMock).toHaveBeenCalledWith(expect.objectContaining({
    templateVersionId: "published-1", description: "List the current OS version",
  })));
});
