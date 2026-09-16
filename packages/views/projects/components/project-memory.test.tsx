import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ProjectMemoryEditor } from "./project-memory";
import { useMemoryDraftStore } from "@multica/core/projects";
import en from "../../locales/en/projects.json";
const mocks = vi.hoisted(() => ({ resolveProjectMemory: vi.fn(), submitProjectMemory: vi.fn(), getProjectMemoryReceipt: vi.fn() }));
vi.mock("@multica/core/api", async (original) => ({ ...await original<object>(), api: mocks }));
// The pure response and permission matrix is owned by core/projects/memory.test.ts.
vi.mock("../../i18n", () => ({ useT: () => ({ t: (selector: (v: typeof en) => string) => selector(en) }) }));
beforeEach(() => {
  useMemoryDraftStore.getState().clearDraft(); vi.resetAllMocks();
  let value = "published"; let revision = 1; const actions = new Map<string, string>();
  mocks.resolveProjectMemory.mockImplementation(async (_ws, project) => ({ workspace_id: "ws", project_id: project, binding_revision: 1, content_revision: revision, state: "ready", generation: "g" }));
  mocks.submitProjectMemory.mockImplementation(async (_ws, _project, op) => {
    const id = String(actions.size); actions.set(id, op.action);
    if (op.action === "write") { value = op.content; revision++; }
    return { id, status: "pending" };
  });
  mocks.getProjectMemoryReceipt.mockImplementation(async (_ws, project, id) => ({ status: "done", result: actions.get(id) === "write" ? { candidate: { content_revision: revision } } : { snapshot: { schema_version: 1, workspace_id: "ws", project_id: project, binding_revision: 1, content_revision: revision, files: { "README.md": project === "b" ? "project B" : value } } } }));
});
afterEach(cleanup);
function mount(project = "a") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return { client, ...render(<QueryClientProvider client={client}><ProjectMemoryEditor key={project} wsId="ws" projectId={project} /></QueryClientProvider>) };
}
it("opens content, edits and confirms only after persisted readback", async () => {
  mount();
  const editor = await screen.findByLabelText("Memory content");
  expect(editor).toHaveValue("published");
  fireEvent.change(editor, { target: { value: "edited" } });
  fireEvent.click(screen.getByRole("button", { name: "Save" }));
  expect(await screen.findByText("Saved and read back from project storage.")).toBeInTheDocument();
  expect(editor).toHaveValue("edited");
  expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
});
it("keeps an unsaved draft across close/remount without showing it in another project", async () => {
  let view = mount("a");
  fireEvent.change(await screen.findByLabelText("Memory content"), { target: { value: "only A draft" } });
  view.unmount(); view = mount("b");
  expect(await screen.findByLabelText("Memory content")).toHaveValue("project B");
  view.unmount(); mount("a");
  expect(await screen.findByLabelText("Memory content")).toHaveValue("only A draft");
  expect(mocks.submitProjectMemory.mock.calls.every((call) => call[2].action === "read")).toBe(true);
});
it("does not display save success for an accepted request without a receipt", async () => {
  mount();
  fireEvent.change(await screen.findByLabelText("Memory content"), { target: { value: "not confirmed" } });
  mocks.getProjectMemoryReceipt.mockRejectedValue(new Error("network"));
  fireEvent.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() => expect(screen.getByRole("alert")).toBeInTheDocument());
  expect(screen.queryByText("Saved and read back from project storage.")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Check save result" })).toBeEnabled();
  expect(screen.getByLabelText("Memory content")).toHaveValue("not confirmed");
});
it("closes discard confirmation and returns to published content", async () => {
  mount();
  fireEvent.change(await screen.findByLabelText("Memory content"), { target: { value: "discard me" } });
  fireEvent.click(screen.getByRole("button", { name: "Discard draft" }));
  const buttons = screen.getAllByRole("button", { name: "Discard draft" });
  fireEvent.click(buttons[buttons.length - 1]!);
  await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument());
  expect(screen.getByLabelText("Memory content")).toHaveValue("published");
});