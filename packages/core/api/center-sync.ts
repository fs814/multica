import { z } from "zod";
import { parseWithFallback } from "./schema";

export const centerSyncActions = ["info", "prepare", "pull", "push", "replica", "apply", "acknowledge", "edit"] as const;
export type CenterSyncAction = typeof centerSyncActions[number];

export async function readCenterSyncResponse(response: Response): Promise<unknown> {
  const reader = response.body?.getReader();
  if (!reader) throw new Error("Empty sync response");
  const chunks: Uint8Array[] = [];
  let bytes = 0;
  try {
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      bytes += value.byteLength;
      if (bytes > 32 * 1024 * 1024) throw new Error("Sync response exceeds capacity");
      chunks.push(value);
    }
  } finally { await reader.cancel(); reader.releaseLock(); }
  const data = new Uint8Array(bytes);
  let offset = 0;
  for (const chunk of chunks) { data.set(chunk, offset); offset += chunk.byteLength; }
  return JSON.parse(new TextDecoder().decode(data));
}
export interface CenterSyncEndpoint {
  origin: string;
  request(action: CenterSyncAction, body: unknown, signal: AbortSignal): Promise<unknown>;
}

const counter = z.number().int().nonnegative().max(Number.MAX_SAFE_INTEGER);
const scopeSchema = z.object({ workspace: z.string().uuid(), group: z.string().uuid(), epoch: z.string().uuid() });
const nodeSchema = z.string().regex(/^manual-center-[0-9a-f]{64}$/);
const principalSchema = z.object({ Account: z.string().uuid(), Actor: z.string().uuid(), Node: nodeSchema });
const infoSchema = z.object({ schema: z.literal(1), mode: z.literal("manual"), owner: z.string().uuid(), origin: z.string(), node: nodeSchema });
const preparedSchema = z.object({ scope: scopeSchema, principal: principalSchema });
const fieldValueSchema = z.union([z.string().max(1 << 20), z.number().finite(), z.null()]);
const exportedFields = {
  issue: ["title", "description", "priority", "status", "number", "project_id", "parent_issue_id", "assignee_type", "assignee_id"],
  project: ["title", "description", "priority", "status", "icon"],
  agent: ["name", "description", "avatar_url", "archived_at"],
};
const writableFields = { issue: ["title", "description", "priority"], project: ["title", "description", "priority", "icon"], agent: ["description", "avatar_url"] };
// This versioned replication protocol is intentionally fail-closed. Unknown
// content must not be relayed to a different origin before server validation.
const recordSchema = z.object({
  kind: z.enum(["issue", "project", "agent"]), id: z.string().uuid(), version: counter.positive(),
  deleted: z.boolean(), fields: z.record(z.string(), fieldValueSchema),
}).strict().refine(record => Object.keys(record.fields).every(key => exportedFields[record.kind].includes(key)) && (!record.deleted || Object.keys(record.fields).length === 0), "Unselected record fields");
const operationSchema = z.object({
  id: z.string().uuid(), node: nodeSchema, incarnation: z.string().uuid(), sequence: counter,
  actor: z.string().uuid(), scope: scopeSchema, kind: z.enum(["issue", "project", "agent"]),
  entity: z.string().uuid(), base: counter, depends_on: z.string().uuid().optional(),
  patch: z.record(z.string(), z.string().max(1 << 20).nullable()),
}).strict();
const batchSchema = z.object({
  schema: z.literal(1), scope: scopeSchema, from: counter, cursor: counter, snapshot: z.boolean(),
  records: z.array(recordSchema).max(10000).nullable(), digest: z.string().regex(/^[0-9a-f]{64}$/),
}).strict();
const emptyRecordSchema = z.object({ kind: z.literal(""), id: z.literal(""), version: z.literal(0), deleted: z.literal(false), fields: z.null() }).strict();
const receiptSchema = z.object({
  operation: z.string().uuid(), status: z.enum(["applied", "conflict", "rejected"]),
  record: z.union([recordSchema, emptyRecordSchema]), reason: z.string().max(1024).optional(),
  conflicts: z.array(z.object({ field: z.string(), base: fieldValueSchema, local: fieldValueSchema, center: fieldValueSchema }).strict()).max(10).optional(),
  local_fields: z.record(z.string(), z.object({ version: counter.positive(), value: fieldValueSchema }).strict()).optional(),
}).strict();
const replicaSchema = z.object({
  schema: z.literal(1), scope: scopeSchema, principal: principalSchema,
  initialized: z.boolean(), cursor: counter, revoked: z.boolean().optional(),
  records: z.record(z.string(), recordSchema),
  outbox: z.array(operationSchema).nullable(), review: z.array(z.unknown()).nullable(),
});
type Replica = z.infer<typeof replicaSchema>;
type Prepared = z.infer<typeof preparedSchema>;

const sourceRequestSchema = z.discriminatedUnion("action", [
  z.object({ action: z.literal("info"), body: z.object({}).strict() }).strict(),
  z.object({ action: z.literal("prepare"), body: z.object({ workspace: z.string().uuid(), principal: z.object({ Node: nodeSchema }).strict() }).strict() }).strict(),
  z.object({ action: z.literal("pull"), body: preparedSchema.extend({ cursor: counter, snapshot: z.boolean() }).strict() }).strict(),
  z.object({ action: z.literal("push"), body: preparedSchema.extend({ operation: operationSchema.strict() }).strict() }).strict(),
]).superRefine((input, ctx) => {
  if (input.action !== "pull" && input.action !== "push") return;
  const { principal, scope } = input.body;
  if (principal.Account !== principal.Actor) ctx.addIssue({ code: "custom", message: "Account mismatch" });
  if (input.action !== "push") return;
  const op = input.body.operation;
  if (!sameScope(op.scope, scope) || op.actor !== principal.Actor || op.node !== principal.Node) ctx.addIssue({ code: "custom", message: "Operation identity mismatch" });
  const allowed = writableFields[op.kind];
  if (!Object.keys(op.patch).length || Object.entries(op.patch).some(([key, value]) => !allowed.includes(key) || (value !== null && typeof value !== "string"))) ctx.addIssue({ code: "custom", message: "Unsafe edit fields" });
});
export type CenterSyncSourceRequest = z.infer<typeof sourceRequestSchema>;
export function validateCenterSyncSourceRequest(value: unknown): CenterSyncSourceRequest {
  return sourceRequestSchema.parse(value);
}

function parse<S extends z.ZodType>(value: unknown, schema: S, endpoint: string): z.infer<S> {
  // Do not include replicated user content in schema-failure telemetry.
  const checked = schema.safeParse(value);
  const parsed = parseWithFallback<z.infer<S> | null>(checked.success ? checked.data : null, schema, null, { endpoint });
  if (parsed === null) throw new Error(`Invalid sync response: ${endpoint}`);
  return parsed;
}

function checkedOrigin(value: string): string {
  const url = new URL(value);
  if (url.protocol !== "https:" || url.origin !== value || url.username || url.password || url.port === "0") {
    throw new Error("Sync requires two different HTTPS server origins");
  }
  return url.origin;
}

function sameScope(a: Prepared["scope"], b: Prepared["scope"]): boolean {
  return a.workspace === b.workspace && a.group === b.group && a.epoch === b.epoch;
}

export interface CenterSyncResult {
  cursor: number;
  records: z.infer<typeof recordSchema>[];
  pending: number;
  conflicts: number;
}

export interface CenterSyncProgress {
  phase: "checking" | "preparing" | "pulling" | "pushing" | "verifying" | "complete";
  batches: number;
  records: number;
  edits: number;
}

/** One bounded, explicit run. No timers schedule new runs and no credentials
 * enter the transferred payload. Durable cursors/outbox belong to the servers.
 */
export async function syncCenters(source: CenterSyncEndpoint, destination: CenterSyncEndpoint, workspace: string, signal: AbortSignal, onProgress?: (progress: CenterSyncProgress) => void): Promise<CenterSyncResult> {
  const sourceOrigin = checkedOrigin(source.origin);
  const destinationOrigin = checkedOrigin(destination.origin);
  if (sourceOrigin === destinationOrigin || !z.string().uuid().safeParse(workspace).success) throw new Error("Select a source workspace and a different sync server");
  const bounded = AbortSignal.any([signal, AbortSignal.timeout(120_000)]);
  const progress: CenterSyncProgress = { phase: "checking", batches: 0, records: 0, edits: 0 };
  const report = (phase: CenterSyncProgress["phase"]) => {
    bounded.throwIfAborted();
    progress.phase = phase;
    onProgress?.({ ...progress });
  };
  report("checking");
  async function call<S extends z.ZodType>(server: CenterSyncEndpoint, action: CenterSyncAction, body: unknown, schema: S) {
    bounded.throwIfAborted();
    const payload = server === source ? validateCenterSyncSourceRequest({ action, body }).body : body;
    const raw = await server.request(action, payload, bounded);
    bounded.throwIfAborted();
    return parse(raw, schema, action);
  }
  const sourceInfo = await call(source, "info", {}, infoSchema);
  const destinationInfo = await call(destination, "info", {}, infoSchema);
  if (sourceInfo.origin !== sourceOrigin || destinationInfo.origin !== destinationOrigin || sourceInfo.node === destinationInfo.node) throw new Error("Sync server identity mismatch");
  report("preparing");
  const prepared = await call(source, "prepare", { workspace, principal: { Node: destinationInfo.node } }, preparedSchema);
  if (prepared.scope.workspace !== workspace || prepared.principal.Account !== sourceInfo.owner || prepared.principal.Actor !== sourceInfo.owner || prepared.principal.Node !== destinationInfo.node) throw new Error("Sync workspace identity mismatch");
  const binding = { source: sourceOrigin, ...prepared };
  function checkReplica(state: Replica): Replica {
    if (state.revoked || !sameScope(state.scope, prepared.scope) || state.principal.Account !== prepared.principal.Account || state.principal.Actor !== prepared.principal.Actor || state.principal.Node !== prepared.principal.Node) throw new Error("Sync replica identity mismatch");
    return state;
  }
  let state = checkReplica(await call(destination, "replica", binding, replicaSchema));
  let batches = 0;
  async function pull() {
    for (;;) {
      if (++batches > 40) throw new Error("Sync run limit reached; saved progress will resume on the next click");
      const batch = await call(source, "pull", { ...prepared, cursor: state.cursor, snapshot: !state.initialized }, batchSchema);
      if (!sameScope(batch.scope, prepared.scope) || batch.snapshot !== !state.initialized || batch.from !== (batch.snapshot ? 0 : state.cursor) || batch.cursor < state.cursor) throw new Error("Sync history mismatch");
      const count = batch.records?.length ?? 0;
      if (!batch.snapshot && (count > 256 || batch.cursor - batch.from !== count)) throw new Error("Sync history gap");
      const next = checkReplica(await call(destination, "apply", { ...binding, batch }, replicaSchema));
      if (!next.initialized || next.cursor !== batch.cursor) throw new Error("Sync checkpoint not confirmed");
      state = next;
      // Count only batches whose checkpoint the destination confirmed. A lost
      // response or failed apply must not look like a completed transfer.
      progress.batches++;
      progress.records += count;
      report(progress.phase);
      if (!batch.snapshot && count < 256) return;
    }
  }
  report("pulling");
  await pull();
  report("pushing");
  let operations = 0;
  while (state.outbox?.length) {
    if (++operations > 256) throw new Error("Sync run limit reached; pending edits will resume on the next click");
    const operation = state.outbox[0]!;
    if (!sameScope(operation.scope, prepared.scope) || operation.node !== prepared.principal.Node || operation.actor !== prepared.principal.Actor) throw new Error("Sync operation identity mismatch");
    const receipt = await call(source, "push", { ...prepared, operation }, receiptSchema);
    if (receipt.operation !== operation.id) throw new Error("Sync receipt identity mismatch");
    if (Object.keys(receipt.local_fields ?? {}).some(key => !writableFields[operation.kind].includes(key)) || receipt.conflicts?.some(conflict => !writableFields[operation.kind].includes(conflict.field))) throw new Error("Unselected receipt fields");
    state = checkReplica(await call(destination, "acknowledge", { ...binding, receipt }, replicaSchema));
    if (state.outbox?.some(item => item.id === operation.id)) throw new Error("Sync receipt not saved");
    progress.edits++;
    report("pushing");
  }
  report("verifying");
  if (operations) await pull();
  report("complete");
  return { cursor: state.cursor, records: Object.values(state.records).filter(record => !record.deleted), pending: state.outbox?.length ?? 0, conflicts: state.review?.length ?? 0 };
}
