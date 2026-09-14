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
    deleteWorkflowInputInstance: vi.fn(),
    runWorkflowInstance: vi.fn(),
    runWorkflowTemplate: vi.fn(),
    getBaseUrl: () => "",
  },
}));
const push = vi.hoisted(() => vi.fn());
const replace = vi.hoisted(() => vi.fn());
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
          replace,
          back: vi.fn(),
          pathname: "/acme/workflow-instances/instance",
          searchParams: new URLSearchParams(),
          hash: "",
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
  replace.mockReset();
  vi.mocked(api.deleteWorkflowInputInstance).mockReset();
  vi.mocked(api.deleteWorkflowInputInstance).mockResolvedValue(undefined);
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
  expect(within(groupA).getByRole("link", { name: "Input A1" })).toHaveAttribute("href", "/acme/workflow-instances/a1?return_to=%2Facme%2Fworkflow-instances%2Finstance");
  expect(within(groupA).getByRole("link", { name: "Input A2" })).toBeInTheDocument();
  expect(within(groupA).queryByText("Input B1")).not.toBeInTheDocument();
  expect(within(groupB).getByRole("link", { name: "Input B1" })).toBeInTheDocument();
  expect(within(groupA).getByRole("link", { name: "Workflow A" })).toHaveAttribute("href", "/acme/workflows/workflow-a");
  fireEvent.click(within(groupA).getByRole("link", { name: "Input A1" }));
  expect(push).toHaveBeenCalledWith("/acme/workflow-instances/a1?return_to=%2Facme%2Fworkflow-instances%2Finstance");
});

it("requires an explicit keep/remove decision before applying an upgrade", async () => {
  const instance = { ...row(), input: { ...row().input, legacy: "historical value", discard: "obsolete" } };
  vi.mocked(api.getWorkflowInstance).mockResolvedValue(instance);
  mount(<WorkflowInstanceDetailPage instanceId="instance" />);
  fireEvent.click(await screen.findByRole("button", { name: "Review latest published version" }));
  const apply = await screen.findByRole("button", { name: "Apply version to this edit" });
  expect(apply).toBeDisabled();
  const keep = await screen.findAllByRole("radio", { name: "Keep value" });
  fireEvent.click(keep[0]!);
  expect(apply).toBeDisabled();
  fireEvent.click(screen.getAllByRole("radio", { name: "Remove" })[1]!);
  fireEvent.click(apply);
  expect(api.saveWorkflowInputInstance).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
  await waitFor(() => expect(api.saveWorkflowInputInstance).toHaveBeenCalled());
  expect(vi.mocked(api.saveWorkflowInputInstance).mock.calls[0]?.[1]).toMatchObject({ templateVersionId: "v2", input: { ...row().input, legacy: "historical value" } });
  expect(vi.mocked(api.saveWorkflowInputInstance).mock.calls[0]?.[1].input).not.toHaveProperty("discard");
  expect(instance.templateVersionId).toBe("v1");
});

it("Review: constructor field requires an explicit upgrade choice", async () => {
  const instance = row();
  instance.input = { ...instance.input, constructor: "keep this user value" };
  vi.mocked(api.getWorkflowInstance).mockResolvedValue(instance);
  mount(<WorkflowInstanceDetailPage instanceId="instance" />);
  fireEvent.click(await screen.findByRole("button", { name: "Review latest published version" }));
  await screen.findByRole("group", { name: /Field constructor is not in this version/ });
  expect(screen.getByRole("radio", { name: "Keep value" })).not.toBeChecked();
  expect(screen.getByRole("radio", { name: "Remove" })).not.toBeChecked();
  expect(screen.getByRole("button", { name: "Apply version to this edit" })).toBeDisabled();
});

it.each(["constructor", "toString", "__proto__", "legacy"].flatMap(key => ["keep", "remove"].map(choice => ({key, choice}))))("requires an explicit $choice decision for $key and saves only after confirmation", async ({key, choice}) => {
  const instance = { ...row(), input: { ...row().input, [key]: "historical value" } };
  vi.mocked(api.getWorkflowInstance).mockResolvedValue(instance);
  mount(<WorkflowInstanceDetailPage instanceId="instance" />);
  fireEvent.click(await screen.findByRole("button", { name: "Review latest published version" }));
  const apply = await screen.findByRole("button", { name: "Apply version to this edit" });
  const group = screen.getByRole("group", { name: new RegExp(`Field ${key} is not in this version`) });
  expect(apply).toBeDisabled();
  expect(within(group).getByRole("radio", { name: "Keep value" })).not.toBeChecked();
  expect(within(group).getByRole("radio", { name: "Remove" })).not.toBeChecked();
  fireEvent.click(apply);
  expect(api.saveWorkflowInputInstance).not.toHaveBeenCalled();
  fireEvent.click(within(group).getByRole("radio", { name: "Keep value" }));
  expect(apply).toBeEnabled();
  if (choice === "remove") fireEvent.click(within(group).getByRole("radio", { name: "Remove" }));
  fireEvent.click(apply);
  expect(api.saveWorkflowInputInstance).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
  await waitFor(() => expect(api.saveWorkflowInputInstance).toHaveBeenCalled());
  const saved = vi.mocked(api.saveWorkflowInputInstance).mock.calls[0]![1];
  expect(Object.hasOwn(saved.input, key)).toBe(choice === "keep");
  if (choice === "keep") expect(saved.input[key]).toBe("historical value");
  expect(saved.templateVersionId).toBe("v2");
  expect(instance.input[key]).toBe("historical value");
  expect(instance.templateVersionId).toBe("v1");
});

describe("directory script instance actions", () => {
 function scriptsRow(bound: boolean) {
  const result = row();
  result.templateVersionId = bound ? "v1" : null;
  result.inputNode = WorkflowDefinitionSchema.parse({entry_node:"input",nodes:[{key:"input",type:"input",input_mode:"scripts",script_pipeline:{directory:"C:/scripts",platform:"windows",steps:["run"],scripts:{},timeout_seconds:60}}]}).nodes[0]!;
  result.input = {title:"ACP UI",description:"Launch",script_directory:"C:/scripts",script_platform:"windows",script_steps:'["run"]',script_timeout_seconds:"60"};
  return result;
 }
 it.each(["clone", "build", "run"] as const)("runs only %s without saving edited inputs", async (step) => {
  vi.mocked(api.getWorkflowInstance).mockResolvedValue(scriptsRow(true));
  mount(<WorkflowInstanceDetailPage instanceId="instance" />);
  const directory = await screen.findByLabelText(enWorkflows.scripts.directory);
  const button = screen.getByRole("button", {name: enWorkflows.scripts.run_step[step]});
  fireEvent.click(button);
  await waitFor(() => expect(api.runWorkflowInstance).toHaveBeenCalledWith("instance", expect.objectContaining({mode:"saved",script_step:step,revision:1})));
  expect(vi.mocked(api.runWorkflowInstance).mock.lastCall![1]).not.toHaveProperty("input");
  await waitFor(() => expect(button).toBeEnabled());
  fireEvent.change(directory,{target:{value:"C:/edited"}});
  fireEvent.click(button);
  await waitFor(() => expect(api.runWorkflowInstance).toHaveBeenCalledWith("instance", expect.objectContaining({mode:"temporary",script_step:step,input:expect.objectContaining({script_directory:"C:/edited"})})));
  expect(api.saveWorkflowInputInstance).not.toHaveBeenCalled();
 });
 it("allows editing and saving an unbound instance and shows how to enable running", async () => {
  vi.mocked(api.getWorkflowInstance).mockResolvedValue(scriptsRow(false));
  vi.mocked(api.validateWorkflowInstance).mockResolvedValue({ready:false,problems:["instance needs a published version binding"],revision:1});
  mount(<WorkflowInstanceDetailPage instanceId="instance" />);
  const directory = await screen.findByLabelText(enWorkflows.scripts.directory);
  expect(screen.getByRole("button",{name:enWorkflows.input_instances.update})).toBeDisabled();
  expect(screen.getByRole("button",{name:enWorkflows.instances.run_saved})).toBeDisabled();
  expect(screen.getByRole("link",{name:enWorkflows.instances.configure_workflow})).toHaveAttribute("href","/acme/workflows/template?section=canvas");
  fireEvent.change(directory,{target:{value:"C:/scripts/acpui"}});
  expect(screen.getByRole("button",{name:enWorkflows.input_instances.update})).toBeEnabled();
  fireEvent.click(screen.getByRole("button",{name:enWorkflows.input_instances.update}));
  await waitFor(()=>expect(api.saveWorkflowInputInstance).toHaveBeenCalledWith("template",expect.objectContaining({input:expect.objectContaining({script_directory:"C:/scripts/acpui"})}),"instance"));
 });
 it("runs a bound saved instance and submits changed directories only for a temporary run", async () => {
  vi.mocked(api.getWorkflowInstance).mockResolvedValue(scriptsRow(true));
  mount(<WorkflowInstanceDetailPage instanceId="instance" />);
  const directory = await screen.findByLabelText(enWorkflows.scripts.directory);
  await waitFor(()=>expect(screen.getByRole("button",{name:enWorkflows.instances.run_saved})).toBeEnabled());
  fireEvent.click(screen.getByRole("button",{name:enWorkflows.instances.run_saved}));
  await waitFor(()=>expect(api.runWorkflowInstance).toHaveBeenCalledWith("instance",expect.objectContaining({mode:"saved",revision:1})));
  expect(vi.mocked(api.runWorkflowInstance).mock.lastCall![1]).not.toHaveProperty("input");
  fireEvent.change(directory,{target:{value:"C:/scripts/acpui"}});
  await waitFor(()=>expect(screen.getByRole("button",{name:enWorkflows.instances.run_temporary})).toBeEnabled());
  fireEvent.click(screen.getByRole("button",{name:enWorkflows.instances.run_temporary}));
  await waitFor(()=>expect(api.runWorkflowInstance).toHaveBeenCalledWith("instance",expect.objectContaining({mode:"temporary",input:expect.objectContaining({script_directory:"C:/scripts/acpui"})})));
  expect(api.saveWorkflowInputInstance).not.toHaveBeenCalled();
 });
});


describe("instance deletion", () => {
  it("names the instance and allows cancelling without sending a request", async () => {
    mount(<WorkflowInstanceDetailPage instanceId="instance" />);
    fireEvent.click(await screen.findByRole("button", {name:"Delete instance"}));
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent("Scenario A");
    expect(dialog).toHaveTextContent("Include archived");
    fireEvent.click(within(dialog).getByRole("button", {name:"Cancel"}));
    expect(api.deleteWorkflowInputInstance).not.toHaveBeenCalled();
    expect(replace).not.toHaveBeenCalled();
  });
  it("refreshes the active list after the server confirms deletion", async () => {
    vi.mocked(api.listWorkflowTemplates).mockResolvedValue({templates:[],total:0});
    vi.mocked(api.browseWorkflowInstances).mockResolvedValue({instances:[row()],total:1});
    vi.mocked(api.deleteWorkflowInputInstance).mockImplementation(async () => {
      vi.mocked(api.browseWorkflowInstances).mockResolvedValue({instances:[],total:0});
    });
    mount(<WorkflowInstancesPage />);
    fireEvent.click(await screen.findByRole("button", {name:"Delete instance"}));
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", {name:"Delete instance"}));
    await waitFor(() => expect(api.deleteWorkflowInputInstance).toHaveBeenCalledWith("template", "instance"));
    await waitFor(() => expect(screen.queryByRole("link", {name:"Scenario A"})).not.toBeInTheDocument());
    expect(api.browseWorkflowInstances).toHaveBeenCalledTimes(2);
  });
  it("keeps a failed deletion open and navigates only after successful retry", async () => {
    vi.mocked(api.deleteWorkflowInputInstance).mockRejectedValueOnce(new Error("Deletion failed"));
    mount(<WorkflowInstanceDetailPage instanceId="instance" />);
    fireEvent.click(await screen.findByRole("button", {name:"Delete instance"}));
    const dialog = await screen.findByRole("alertdialog");
    fireEvent.click(within(dialog).getByRole("button", {name:"Delete instance"}));
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("Deletion failed");
    expect(replace).not.toHaveBeenCalled();
    fireEvent.click(within(dialog).getByRole("button", {name:"Delete instance"}));
    await waitFor(() => expect(replace).toHaveBeenCalledWith("/acme/workflow-instances"));
    expect(api.deleteWorkflowInputInstance).toHaveBeenCalledTimes(2);
  });
  it("prevents duplicate deletion requests while waiting for the server", async () => {
    vi.mocked(api.deleteWorkflowInputInstance).mockImplementation(() => new Promise(() => {}));
    mount(<WorkflowInstanceDetailPage instanceId="instance" />);
    fireEvent.click(await screen.findByRole("button", {name:"Delete instance"}));
    const dialog = await screen.findByRole("alertdialog");
    const confirm = within(dialog).getByRole("button", {name:"Delete instance"});
    fireEvent.click(confirm);
    fireEvent.click(confirm);
    await waitFor(() => expect(api.deleteWorkflowInputInstance).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(confirm).toBeDisabled());
    expect(replace).not.toHaveBeenCalled();
  });
});
