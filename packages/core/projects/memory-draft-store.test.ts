// @vitest-environment jsdom
import { beforeEach, expect, it } from "vitest";
import { setCurrentWorkspace } from "../platform/workspace-storage";
import { resetAllRegisteredDrafts } from "../drafts/cleanup-registry";
import { captureMemoryDraft, memoryDraftKey, setMemoryDraft, updateCapturedMemoryDraft, useMemoryDraftStore } from "./memory-draft-store";
const key = memoryDraftKey("ws-a", "project-a", "README.md");
const edit = { content: "old", bindingRevision: 1, contentRevision: 2 };
const switchTo = async (slug: string) => { setCurrentWorkspace(slug, `ws-${slug}`); await Promise.resolve(); };
const stored = (slug: string) => JSON.parse(localStorage.getItem(`multica_memory_edits:${slug}`) ?? "null");
beforeEach(async () => { localStorage.clear(); await switchTo("a"); useMemoryDraftStore.getState().clearDraft(); });
it.each(["submitted", "saved", "failed"] as const)("late %s preserves a replacement even with identical text", (action) => {
  setMemoryDraft(key, edit); const capture = captureMemoryDraft(key);
  setMemoryDraft(key, edit);
  expect(updateCapturedMemoryDraft(capture, action, "old-receipt")).toBe(false);
  expect(useMemoryDraftStore.getState().draft.entries[key]?.content).toBe("old");
  expect(useMemoryDraftStore.getState().draft.entries[key]?.requestId).toBeUndefined();
});
it("A to B to A keeps the origin receipt across rehydration and late success", async () => {
  setMemoryDraft(key, edit); const capture = captureMemoryDraft(key);
  await switchTo("b"); const bKey = memoryDraftKey("ws-b", "p", "README.md");
  setMemoryDraft(bKey, { ...edit, content: "B" });
  expect(updateCapturedMemoryDraft(capture, "submitted", "receipt-a")).toBe(true);
  expect(stored("a").state.draft.entries[key].requestId).toBe("receipt-a");
  expect(stored("b").state.draft.entries[key]).toBeUndefined();
  expect(useMemoryDraftStore.getState().draft.entries[bKey]?.content).toBe("B");
  await switchTo("a"); expect(useMemoryDraftStore.getState().draft.entries[key]?.requestId).toBe("receipt-a");
  expect(updateCapturedMemoryDraft(capture, "saved")).toBe(true);
  await switchTo("b"); expect(useMemoryDraftStore.getState().draft.entries[bKey]?.content).toBe("B");
});
it("late terminal error clears only the originating receipt", async () => {
  setMemoryDraft(key, edit); const capture = captureMemoryDraft(key);
  updateCapturedMemoryDraft(capture, "submitted", "receipt-a");
  await switchTo("b"); updateCapturedMemoryDraft(capture, "failed");
  expect(stored("a").state.draft.entries[key].requestId).toBeUndefined();
  expect(stored("b")).toBeNull();
  await switchTo("a"); expect(useMemoryDraftStore.getState().draft.entries[key]?.content).toBe("old");
});
it.each(["submitted", "saved", "failed"] as const)("logout invalidates late %s even after another login", async (action) => {
  setMemoryDraft(key, edit); const capture = captureMemoryDraft(key);
  resetAllRegisteredDrafts(); localStorage.clear(); await switchTo("b"); await switchTo("a");
  setMemoryDraft(key, { ...edit, content: "new session" });
  expect(updateCapturedMemoryDraft(capture, action, "old-receipt")).toBe(false);
  expect(stored("a").state.draft.entries[key].content).toBe("new session");
});
it("adopts the existing persisted format including pending requestId", async () => {
  localStorage.setItem("multica_memory_edits:a", JSON.stringify({state:{draft:{entries:{[key]:{...edit,requestId:"existing"}}}},version:0}));
  await switchTo("b"); await switchTo("a");
  const capture = captureMemoryDraft(key);
  expect(capture.edit.requestId).toBe("existing");
  expect(updateCapturedMemoryDraft(capture, "saved")).toBe(true);
});
