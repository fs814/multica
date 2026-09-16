import { z } from "zod";
import { parseWithFallback } from "../api/schema";

export const memoryBindingSchema = z.object({
  workspace_id: z.string(), project_id: z.string(), binding_revision: z.number().int().positive(),
  content_revision: z.number().int().nonnegative(), generation: z.string().optional(), state: z.string(),
});
export const memorySnapshotSchema = z.object({
  schema_version: z.literal(1), workspace_id: z.string(), project_id: z.string(),
  binding_revision: z.number().int().positive(), content_revision: z.number().int().nonnegative(),
  files: z.record(z.string(), z.string()),
});
export const memoryWorkSchema = z.object({ id: z.string().min(1), status: z.string() });
export const memoryReceiptSchema = z.object({ status: z.string(), result: z.object({
  error: z.string().optional(), snapshot: memorySnapshotSchema.optional(),
  candidate: z.object({ content_revision: z.number().int().positive(), generation: z.string(), digest: z.string() }).optional(),
}).optional() });
export function parseMemory<T>(raw: unknown, schema: z.ZodType<T>, endpoint: string): T {
  const value = parseWithFallback<T | null>(raw, schema, null, { endpoint });
  if (value === null) throw new Error("Invalid memory response");
  return value;
}
export type MemorySnapshot = z.infer<typeof memorySnapshotSchema>;
export type MemoryOperation = { action: "read" | "write"; expected_revision: number; expected_binding_revision: number; path?: string; content?: string; source?: string };
