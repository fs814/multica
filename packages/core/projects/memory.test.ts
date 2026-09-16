// @vitest-environment node
import { beforeEach, describe, expect, it, vi } from "vitest";
import { setCurrentWorkspace } from "../platform/workspace-storage";
import { ApiClient, ApiError } from "../api/client";
import { memoryBindingSchema, memoryReceiptSchema, parseMemory } from "./memory-schemas";
import { memoryKey, memoryProblem, readMemory, saveMemory } from "./memory";
const mocks = vi.hoisted(() => ({ resolveProjectMemory: vi.fn(), submitProjectMemory: vi.fn(), getProjectMemoryReceipt: vi.fn() }));
vi.mock("../api", async (original) => ({ ...await original<object>(), api: mocks }));
const binding = { workspace_id: "ws", project_id: "p", binding_revision: 1, content_revision: 3, state: "ready", generation: "g" };
const snapshot = { schema_version: 1, workspace_id: "ws", project_id: "p", binding_revision: 1, content_revision: 3, files: { "README.md": "published" } };
beforeEach(() => {
  vi.resetAllMocks();
  mocks.resolveProjectMemory.mockResolvedValue(binding);
  mocks.submitProjectMemory.mockResolvedValue({ id: "request", status: "pending" });
  mocks.getProjectMemoryReceipt.mockResolvedValue({ status: "done", result: { snapshot } });
});
describe("project memory boundary", () => {
  it("rejects malformed payloads instead of inventing successful empty memory", () => {
    expect(() => parseMemory({}, memoryBindingSchema, "binding")).toThrow();
    expect(() => parseMemory({ status: "done", result: { snapshot: { files: [] } } }, memoryReceiptSchema, "receipt")).toThrow();
  });
  it("freezes workspace headers on each request", async () => {
    const client = new ApiClient("https://fixture.invalid");
    const fetch = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify(binding)));
    setCurrentWorkspace("another-workspace", "ws-b");
    await client.resolveProjectMemory("ws-a", "project-a");
    expect(fetch.mock.calls[0]?.[1]?.headers).toMatchObject({ "X-Workspace-ID": "ws-a", "X-Workspace-Slug": "" });
    fetch.mockRestore(); setCurrentWorkspace(null, null);
    expect(memoryKey("a", "p")).not.toEqual(memoryKey("b", "p"));
    expect(memoryKey("a", "p")).not.toEqual(memoryKey("a", "q"));
  });
  it("rejects a cross-project binding before enqueueing a read", async () => {
    mocks.resolveProjectMemory.mockResolvedValue({ ...binding, project_id: "other" });
    await expect(readMemory("ws", "p")).rejects.toMatchObject({ kind: "invalid" });
    expect(mocks.submitProjectMemory).not.toHaveBeenCalled();
  });
  it("rejects cross-workspace snapshots", async () => {
    mocks.getProjectMemoryReceipt.mockResolvedValue({ status: "done", result: { snapshot: { ...snapshot, workspace_id: "other" } } });
    await expect(readMemory("ws", "p")).rejects.toMatchObject({ kind: "invalid" });
  });
  it("describes uninitialized memory without submitting reads", async () => {
    mocks.resolveProjectMemory.mockResolvedValue({ ...binding, generation: undefined });
    await expect(readMemory("ws", "p")).rejects.toMatchObject({ kind: "uninitialized" });
  });
  it("does not claim success when owner reports unavailable", async () => {
    mocks.getProjectMemoryReceipt.mockResolvedValue({ status: "unavailable" });
    await expect(readMemory("ws", "p")).rejects.toMatchObject({ kind: "unavailable" });
  });
  it("unknown receipt states fail closed", async () => {
    mocks.getProjectMemoryReceipt.mockResolvedValue({ status: "accepted" });
    await expect(readMemory("ws", "p")).rejects.toMatchObject({ kind: "invalid" });
  });
  it("save waits for commit and an exact readback", async () => {
    mocks.getProjectMemoryReceipt.mockResolvedValueOnce({ status: "done", result: { candidate: { content_revision: 3 } } });
    const submitted = vi.fn();
    await expect(saveMemory("ws", "p", "README.md", { content: "published", bindingRevision: 1, contentRevision: 2 }, submitted)).resolves.toEqual(snapshot);
    expect(submitted).toHaveBeenCalledWith("request");
    expect(mocks.submitProjectMemory.mock.calls[0]?.[2]).toMatchObject({ action: "write", source: "Project memory editor", expected_revision: 2, expected_binding_revision: 1 });
    expect(mocks.submitProjectMemory.mock.calls[1]?.[2]).toMatchObject({ action: "read" });
  });
  it("resumes an ambiguous save without submitting a second write", async () => {
    mocks.getProjectMemoryReceipt.mockResolvedValueOnce({ status: "done", result: { candidate: { content_revision: 3 } } });
    await saveMemory("ws", "p", "README.md", { content: "published", bindingRevision: 1, contentRevision: 2, requestId: "existing" }, vi.fn());
    expect(mocks.submitProjectMemory).toHaveBeenCalledTimes(1);
    expect(mocks.submitProjectMemory.mock.calls[0]?.[2].action).toBe("read");
  });
  it("does not call a committed-but-different value saved", async () => {
    mocks.getProjectMemoryReceipt.mockResolvedValueOnce({ status: "done", result: { candidate: { content_revision: 3 } } });
    await expect(saveMemory("ws", "p", "README.md", { content: "my draft", bindingRevision: 1, contentRevision: 2 }, vi.fn())).rejects.toMatchObject({ kind: "conflict" });
  });
  it("retains uncertainty for a disconnected receipt", async () => {
    mocks.getProjectMemoryReceipt.mockRejectedValue(new Error("network"));
    const submitted = vi.fn();
    await expect(saveMemory("ws", "p", "README.md", { content: "x", bindingRevision: 1, contentRevision: 2 }, submitted)).rejects.toThrow("network");
    expect(submitted).toHaveBeenCalledWith("request");
  });
  it("maps denied access and CAS errors separately", () => {
    expect(memoryProblem(new ApiError("forbidden", 403, "Forbidden"))).toBe("permission");
    expect(memoryProblem(new Error("project memory changed; read and merge before retrying"))).toBe("conflict");
  });
});
it("rejects a done receipt that did not advance the written revision", async () => {
  mocks.getProjectMemoryReceipt.mockResolvedValue({ status: "done", result: { candidate: { content_revision: 2 } } });
  await expect(saveMemory("ws", "p", "README.md", { content: "published", bindingRevision: 1, contentRevision: 2 }, vi.fn())).rejects.toMatchObject({ kind: "invalid" });
  expect(mocks.resolveProjectMemory).not.toHaveBeenCalled();
});