import { queryOptions } from "@tanstack/react-query";
import { api, ApiError } from "../api";
import type { MemorySnapshot } from "./memory-schemas";
export type MemoryProblem = "permission" | "uninitialized" | "conflict" | "unavailable" | "unsupported" | "invalid" | "error";
export class MemoryError extends Error {
  constructor(public kind: MemoryProblem, public terminal = false) { super(kind); }
}
export function memoryProblem(error: unknown): MemoryProblem {
  if (error instanceof MemoryError) return error.kind;
  if (error instanceof ApiError && (error.status === 401 || error.status === 403)) return "permission";
  if (error instanceof ApiError && error.status === 404) return "unsupported";
  const message = error instanceof Error ? error.message : "";
  if (/not initialized|no rows in result set/.test(message)) return "uninitialized";
  if (/changed; read and merge|conflict/i.test(message)) return "conflict";
  if (/unavailable|expired/.test(message)) return "unavailable";
  if (/Invalid memory response/.test(message)) return "invalid";
  return "error";
}
export const memoryKey = (wsId: string, projectId: string) => ["project-memory", wsId, projectId] as const;
export async function awaitMemory(wsId: string, projectId: string, id: string, signal?: AbortSignal) {
  const deadline = Date.now() + 30_000;
  do {
    signal?.throwIfAborted();
    const receipt = await api.getProjectMemoryReceipt(wsId, projectId, id, signal);
    if (receipt.result?.error) throw new MemoryError(memoryProblem(new Error(receipt.result.error)), true);
    if (receipt.status === "done" && receipt.result) return receipt.result;
    if (receipt.status === "unavailable") throw new MemoryError("unavailable", true);
    if (receipt.status === "failed") throw new MemoryError("error", true);
    if (receipt.status !== "pending" && receipt.status !== "processing") throw new MemoryError("invalid");
    await new Promise((resolve) => setTimeout(resolve, 500));
  } while (Date.now() < deadline);
  // A timeout is not proof the owner did not commit. Keep the request for verification.
  throw new MemoryError("unavailable");
}
export async function readMemory(wsId: string, projectId: string, signal?: AbortSignal): Promise<MemorySnapshot> {
  const binding = await api.resolveProjectMemory(wsId, projectId, signal);
  if (binding.workspace_id !== wsId || binding.project_id !== projectId) throw new MemoryError("invalid");
  if (!binding.generation || binding.state === "pending") throw new MemoryError("uninitialized");
  if (binding.state !== "ready") throw new MemoryError("invalid");
  const work = await api.submitProjectMemory(wsId, projectId, { action: "read", expected_revision: binding.content_revision, expected_binding_revision: binding.binding_revision }, signal);
  const result = await awaitMemory(wsId, projectId, work.id, signal);
  const snapshot = result.snapshot;
  if (!snapshot || snapshot.workspace_id !== wsId || snapshot.project_id !== projectId || snapshot.binding_revision !== binding.binding_revision || snapshot.content_revision !== binding.content_revision) throw new MemoryError("invalid");
  return snapshot;
}
export const memoryOptions = (wsId: string, projectId: string) => queryOptions({
  queryKey: memoryKey(wsId, projectId), queryFn: ({ signal }) => readMemory(wsId, projectId, signal),
  retry: false, refetchOnWindowFocus: false, staleTime: 0,
});
export interface MemoryEdit { content: string; bindingRevision: number; contentRevision: number; requestId?: string }
export async function saveMemory(wsId: string, projectId: string, path: string, edit: MemoryEdit, submitted: (id: string) => void) {
  let id = edit.requestId;
  if (!id) {
    const work = await api.submitProjectMemory(wsId, projectId, { action: "write", source: "Project memory editor", path, content: edit.content, expected_revision: edit.contentRevision, expected_binding_revision: edit.bindingRevision });
    id = work.id; submitted(id);
  }
  const result = await awaitMemory(wsId, projectId, id);
  if (!result.candidate || result.candidate.content_revision !== edit.contentRevision + 1) throw new MemoryError("invalid");
  const snapshot = await readMemory(wsId, projectId);
  if (snapshot.binding_revision !== edit.bindingRevision || snapshot.content_revision !== result.candidate.content_revision || snapshot.files[path] !== edit.content) throw new MemoryError("conflict", true);
  return snapshot;
}
