import { create } from "zustand";
import { defaultStorage } from "../platform/storage";
import { getCurrentSlug, registerForWorkspaceRehydration } from "../platform/workspace-storage";
import { registerDraftCleanup } from "../drafts/cleanup-registry";
import type { MemoryEdit } from "./memory";

type Draft = MemoryEdit & { editId?: string };
type Entries = Record<string, Draft>;
const storageKey = "multica_memory_edits";
let epoch = 0;
let activeSlug = getCurrentSlug();
export const memoryDraftKey = (wsId: string, projectId: string, path: string) => JSON.stringify([wsId, projectId, path]);

function read(slug: string | null): Entries {
  if (!slug) return {};
  try {
    const raw = defaultStorage.getItem(`${storageKey}:${slug}`);
    return raw ? JSON.parse(raw).state?.draft?.entries ?? {} : {};
  } catch { return {}; }
}
function write(slug: string | null, entries: Entries) {
  if (slug) defaultStorage.setItem(`${storageKey}:${slug}`, JSON.stringify({ state: { draft: { entries } }, version: 0 }));
  if (slug === getCurrentSlug()) {
    activeSlug = slug;
    useMemoryDraftStore.setState({ draft: { entries } });
  }
}
function currentEntries() {
  return activeSlug === getCurrentSlug() ? useMemoryDraftStore.getState().draft.entries : read(getCurrentSlug());
}
export const useMemoryDraftStore = create<{
  draft: { entries: Entries };
  setDraft: (patch: { entries: Entries }) => void;
  clearDraft: () => void;
  hasDraft: () => boolean;
}>()((_, get) => ({
  draft: { entries: read(activeSlug) },
  setDraft: ({ entries }) => write(getCurrentSlug(), entries),
  clearDraft: () => { epoch++; write(getCurrentSlug(), {}); },
  hasDraft: () => Object.keys(get().draft.entries).length > 0,
}));
registerForWorkspaceRehydration(() => {
  activeSlug = getCurrentSlug();
  useMemoryDraftStore.setState({ draft: { entries: read(activeSlug) } });
});
registerDraftCleanup({ storageKey, workspaceScoped: true, resetInMemory: () => useMemoryDraftStore.getState().clearDraft() });

export function setMemoryDraft(key: string, value?: MemoryEdit) {
  const entries = { ...currentEntries() };
  if (value) entries[key] = { ...value, editId: crypto.randomUUID() };
  else delete entries[key];
  write(getCurrentSlug(), entries);
}

/** Freeze persistence destination and edit identity before starting asynchronous work. */
export function captureMemoryDraft(key: string) {
  let edit = currentEntries()[key];
  if (!edit) throw new Error("Memory draft is missing");
  // Adopt drafts persisted by the initial editor, without losing their receipt.
  if (!edit.editId) { setMemoryDraft(key, edit); edit = currentEntries()[key]!; }
  return { key, slug: getCurrentSlug(), epoch, edit };
}
export type MemoryDraftCapture = ReturnType<typeof captureMemoryDraft>;
export const isMemoryDraftSessionCurrent = (capture: MemoryDraftCapture) => capture.epoch === epoch;
/** A receipt belongs to one edit, never to its replacement or a later login. */
export function updateCapturedMemoryDraft(capture: MemoryDraftCapture, action: "submitted" | "saved" | "failed", requestId?: string) {
  if (capture.epoch !== epoch) return false;
  const entries = { ...(capture.slug === getCurrentSlug() ? currentEntries() : read(capture.slug)) };
  const current = entries[capture.key];
  if (!current || current.editId !== capture.edit.editId) return false;
  if (action === "saved") delete entries[capture.key];
  else entries[capture.key] = { ...current, requestId: action === "submitted" ? requestId : undefined };
  write(capture.slug, entries);
  return true;
}
