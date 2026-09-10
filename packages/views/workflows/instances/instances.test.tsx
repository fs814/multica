import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  fireEvent,
  render,
  screen,
  waitFor,
  cleanup,
  within,
} from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import {
  WorkflowDefinitionSchema,
  WorkflowInputInstanceSchema,
} from "@multica/core/workflows";
import { api } from "@multica/core/api";
import { NavigationProvider } from "../../navigation";
import enWorkflows from "../../locales/en/workflows.json";
import enCommon from "../../locales/en/common.json";
import enUi from "../../locales/en/ui.json";
import { WorkflowInstancesPage } from "./workflow-instances-page";
import { CreateInstanceDialog } from "./create-instance-dialog";
import { WorkflowInstanceDetailPage } from "./workflow-instance-detail-page";
vi.mock("@multica/core/api", () => ({
  api: {
    browseWorkflowInstances: vi.fn(),
    listWorkflowTemplates: vi.fn(),
    getWorkflowInstance: vi.fn(),
    getWorkflowInstanceVersion: vi.fn(),
    getWorkflowTemplate: vi.fn(),
    validateWorkflowInstance: vi.fn(),
    listWorkflowInstanceRuns: vi.fn(),
    saveWorkflowInputInstance: vi.fn(),
    runWorkflowInstance: vi.fn(),
    runWorkflowTemplate: vi.fn(),
    getBaseUrl: () => "",
  },
}));
const push = vi.hoisted(() => vi.fn());
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws" }));
vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => ({ id: "ws", name: "Acme" }),
  useWorkspacePaths: () => ({
    workflows: () => "/acme/workflows",
    workflowInstances: () => "/acme/workflow-instances",
    workflowInstanceDetail: (id: string) => "/acme/workflow-instances/" + id,
    workflowRunDetail: (id: string) => "/acme/workflow-runs/" + id,
    workflowDetail: (id: string) => "/acme/workflows/" + id,
  }),
}));
vi.mock("@multica/core/projects/queries", () => ({
  projectListOptions: () => ({
    queryKey: ["projects"],
    queryFn: async () => [],
  }),
}));
const graph = WorkflowDefinitionSchema.parse({
  entry_node: "input",
  nodes: [
    {
      key: "input",
      type: "input",
      name: "Current title",
      instruction: "Unsubmitted input",
      input_fields: [],
    },
    { key: "end", type: "end" },
  ],
});
const row = () =>
  WorkflowInputInstanceSchema.parse({
    id: "instance",
    template_id: "template",
    template_version_id: "v1",
    name: "Scenario A",
    input: { title: "A", description: "Input A" },
    revision: 1,
    input_node: graph.nodes[0],
    image_attachment_id: "",
    updated_at: "2026-09-09T00:00:00Z",
  });
function mount(ui: React.ReactNode) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <I18nProvider
      locale="en"
      resources={{ en: { workflows: enWorkflows, common: enCommon, ui: enUi } }}
    >
      <NavigationProvider
        value={{
          push,
          replace: vi.fn(),
          back: vi.fn(),
          pathname: "/acme/workflow-instances/instance",
          searchParams: new URLSearchParams(),
          getShareableUrl: (p) => p,
        }}
      >
        <QueryClientProvider client={qc}>{ui}</QueryClientProvider>
      </NavigationProvider>
    </I18nProvider>,
  );
}
beforeEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.clearAllMocks();
  push.mockReset();
  vi.spyOn(api, "getWorkflowInstance").mockResolvedValue(row());
  vi.spyOn(api, "getWorkflowInstanceVersion").mockResolvedValue({
    id: "v1",
    version: 1,
    definition: graph,
  });
  vi.spyOn(api, "getWorkflowTemplate").mockResolvedValue({
    id: "template",
    name: "Template",
    definition: graph,
    current_version: 2,
    status: "published",
    versions: [
      { id: "v1", version: 1, status: "published", published_at: null },
      { id: "v2", version: 2, status: "published", published_at: null },
    ],
  } as Awaited<ReturnType<typeof api.getWorkflowTemplate>>);
  vi.spyOn(api, "validateWorkflowInstance").mockResolvedValue({
    ready: true,
    problems: [],
    revision: 1,
  });
  vi.spyOn(api, "listWorkflowInstanceRuns").mockResolvedValue({
    runs: [],
    total: 0,
  });
  vi.spyOn(api, "saveWorkflowInputInstance").mockResolvedValue(row());
  vi.spyOn(api, "runWorkflowInstance").mockResolvedValue({
    id: "run-a",
    status: "completed",
  } as Awaited<ReturnType<typeof api.runWorkflowInstance>>);
  vi.spyOn(api, "runWorkflowTemplate");
});
describe("independent instance creation", () => {
  it("captures unsubmitted node values and creates without starting a run", async () => {
    mount(
      <CreateInstanceDialog
        open
        onOpenChange={vi.fn()}
        templateId="template"
        templateName="Example"
        definition={graph}
        publishedDefinition={graph}
        versionId="v1"
      />,
    );
    expect(screen.getByRole("dialog")).toHaveTextContent(
      "New workflow instance",
    );
    expect(
      screen.queryByRole("button", { name: "Run" }),
    ).not.toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "Description" })).toHaveValue(
      "Unsubmitted input",
    );
    fireEvent.change(screen.getByRole("textbox", { name: "Description" }), {
      target: { value: "" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create instance" }));
    await waitFor(() =>
      expect(api.saveWorkflowInputInstance).toHaveBeenCalledWith(
        "template",
        expect.objectContaining({
          input: { title: "Current title", description: "" },
          templateVersionId: "v1",
          idempotencyKey: expect.any(String),
        }),
        undefined,
      ),
    );
    expect(api.runWorkflowTemplate).not.toHaveBeenCalled();
    expect(api.runWorkflowInstance).not.toHaveBeenCalled();
    expect(push).toHaveBeenCalledWith("/acme/workflow-instances/instance");
  });
  it("saves changed draft declarations unbound, without falsely pinning the current publication", async () => {
    const draft = structuredClone(graph);
    draft.nodes[0]!.input_fields = [
      {
        key: "new",
        type: "text",
        label: "New",
        required: true,
        options: [],
        placeholder: "",
      },
    ];
    mount(
      <CreateInstanceDialog
        open
        onOpenChange={vi.fn()}
        templateId="template"
        templateName="Example"
        definition={draft}
        publishedDefinition={graph}
        versionId="v1"
      />,
    );
    expect(screen.getByText(/Pending version binding/)).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "Create instance" }));
    await waitFor(() =>
      expect(api.saveWorkflowInputInstance).toHaveBeenCalledWith(
        "template",
        expect.objectContaining({
          templateVersionId: null,
          inputNode: expect.objectContaining({
            input_fields: expect.arrayContaining([
              expect.objectContaining({ key: "new" }),
            ]),
          }),
        }),
        undefined,
      ),
    );
  });
  it("retains values and reuses the idempotency key after a failed response", async () => {
    vi.mocked(api.saveWorkflowInputInstance).mockRejectedValue(
      new Error("network failure"),
    );
    mount(
      <CreateInstanceDialog
        open
        onOpenChange={vi.fn()}
        templateId="template"
        templateName="Example"
        definition={graph}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Create instance" }));
    await screen.findByText("network failure");
    const first = vi.mocked(api.saveWorkflowInputInstance).mock.calls[0]![1]
      .idempotencyKey;
    fireEvent.click(screen.getByRole("button", { name: "Create instance" }));
    await waitFor(() =>
      expect(api.saveWorkflowInputInstance).toHaveBeenCalledTimes(2),
    );
    expect(
      vi.mocked(api.saveWorkflowInputInstance).mock.calls[1]![1].idempotencyKey,
    ).toBe(first);
    expect(screen.getByRole("textbox", { name: "Description" })).toHaveValue(
      "Unsubmitted input",
    );
  });
});
describe("instance detail execution", () => {
  it("runs the saved server snapshot, even when the editor has temporary changes", async () => {
    mount(<WorkflowInstanceDetailPage instanceId="instance" />);
    await screen.findByRole("textbox", { name: "Description" });
    fireEvent.change(screen.getByRole("textbox", { name: "Description" }), {
      target: { value: "Input B" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Run saved inputs" }));
    await waitFor(() =>
      expect(api.runWorkflowInstance).toHaveBeenCalledWith("instance", {
        mode: "saved",
        revision: 1,
        idempotency_key: expect.any(String),
      }),
    );
    expect(api.saveWorkflowInputInstance).not.toHaveBeenCalled();
  });
  it("runs temporary values without saving and explicitly saves only on Save changes", async () => {
    mount(<WorkflowInstanceDetailPage instanceId="instance" />);
    await screen.findByRole("textbox", { name: "Description" });
    fireEvent.change(screen.getByRole("textbox", { name: "Description" }), {
      target: { value: "Input B" },
    });
    fireEvent.click(
      screen.getByRole("button", { name: "Run these edits only" }),
    );
    await waitFor(() =>
      expect(api.runWorkflowInstance).toHaveBeenCalledWith(
        "instance",
        expect.objectContaining({
          mode: "temporary",
          revision: 1,
          input: { title: "A", description: "Input B" },
          image_attachment_id: "",
        }),
      ),
    );
    expect(api.saveWorkflowInputInstance).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() =>
      expect(api.saveWorkflowInputInstance).toHaveBeenCalledWith(
        "template",
        expect.objectContaining({
          revision: 1,
          input: { title: "A", description: "Input B" },
          templateVersionId: "v1",
        }),
        "instance",
      ),
    );
  });
  it("preserves edits on a revision conflict and keeps the original bound version", async () => {
    vi.mocked(api.saveWorkflowInputInstance).mockRejectedValue(
      new Error("revision conflict"),
    );
    mount(<WorkflowInstanceDetailPage instanceId="instance" />);
    await screen.findByRole("textbox", { name: "Description" });
    fireEvent.change(screen.getByRole("textbox", { name: "Description" }), {
      target: { value: "My edits" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    await screen.findByText("revision conflict");
    expect(screen.getByRole("textbox", { name: "Description" })).toHaveValue(
      "My edits",
    );
    expect(screen.getByText("Pinned workflow version: 1")).toBeVisible();
    expect(
      screen.getByRole("button", { name: "Reload latest instance" }),
    ).toBeVisible();
  });
  it("warns before leaving a dirty instance", async () => {
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    mount(<WorkflowInstanceDetailPage instanceId="instance" />);
    await screen.findByRole("textbox", { name: "Description" });
    fireEvent.change(screen.getByRole("textbox", { name: "Description" }), {
      target: { value: "Unsaved" },
    });
    fireEvent.click(screen.getByRole("link", { name: "Workflow instances" }));
    expect(confirm).toHaveBeenCalled();
    expect(push).not.toHaveBeenCalled();
  });
});

it("organizes workspace instances under their parent workflow with working detail links", async () => {
  vi.mocked(api.listWorkflowTemplates).mockResolvedValue({ templates: [], total: 0 });
  vi.mocked(api.browseWorkflowInstances).mockResolvedValue({
    instances: [
      { ...row(), id: "a1", name: "Input A1", templateId: "workflow-a", templateName: "Workflow A" },
      { ...row(), id: "b1", name: "Input B1", templateId: "workflow-b", templateName: "Workflow B" },
      { ...row(), id: "a2", name: "Input A2", templateId: "workflow-a", templateName: "Workflow A" },
    ],
    total: 3,
  });
  mount(<WorkflowInstancesPage />);
  const groupA = await screen.findByRole("region", { name: "Workflow A" });
  const groupB = screen.getByRole("region", { name: "Workflow B" });
  expect(within(groupA).getByRole("link", { name: "Input A1" })).toHaveAttribute("href", "/acme/workflow-instances/a1");
  expect(within(groupA).getByRole("link", { name: "Input A2" })).toBeInTheDocument();
  expect(within(groupA).queryByText("Input B1")).not.toBeInTheDocument();
  expect(within(groupB).getByRole("link", { name: "Input B1" })).toBeInTheDocument();
  expect(within(groupA).getByRole("link", { name: "Workflow A" })).toHaveAttribute("href", "/acme/workflows/workflow-a");
  fireEvent.click(within(groupA).getByRole("link", { name: "Input A1" }));
  expect(push).toHaveBeenCalledWith("/acme/workflow-instances/a1");
});
