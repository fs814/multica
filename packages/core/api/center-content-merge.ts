import { z } from "zod";
import { parseWithFallback } from "./schema";
import type { CenterSyncEndpoint, CenterSyncProgress, CenterSyncResult } from "./center-sync";

// Keep this wire allowlist aligned with centersync/contentTables. Unknown
// fields (including credential/config fields) must never cross either origin.
const columns: Record<string, string> = {
  workspace: "id,name,slug,description,context,issue_prefix,avatar_url",
  agent: "id,name,description,instructions,avatar_url,model,thinking_level,service_tier,conversation_starters,archived_at",
  project: "id,title,description,icon,status,priority,start_date,due_date",
  issue_status: "id,key,name,description,category,color,is_system,position,archived_at,icon",
  issue_label: "id,name,color,resource_type,description",
  issue_property: "id,name,type,description,icon,config,position,archived_at",
  skill: "id,name,description,content",
  skill_file: "id,skill_id,path,content",
  agent_skill: "agent_id,skill_id,enabled",
  agent_to_label: "agent_id,label_id",
  skill_to_label: "skill_id,label_id",
  squad: "id,name,description,leader_id,avatar_url,instructions,archived_at",
  squad_member: "id,squad_id,member_type,member_id,role",
  issue: "id,title,description,status,priority,assignee_type,assignee_id,creator_type,creator_id,parent_issue_id,acceptance_criteria,position,due_date,number,project_id,start_date,properties",
  comment: "id,issue_id,author_type,author_id,content,type,parent_id,created_at,resolved_at,resolved_by_type,resolved_by_id,deleted_at",
  issue_to_label: "issue_id,label_id",
  issue_dependency: "id,issue_id,depends_on_issue_id,type",
  chat_session: "id,agent_id,creator_id,title,status,created_at,project_id,model",
  chat_message: "id,chat_session_id,role,content,created_at,message_kind",
  attachment: "id,issue_id,comment_id,chat_session_id,chat_message_id,uploader_type,uploader_id,filename,content_type,size_bytes,created_at,content_sha256",
};
const recordSchema = z.object({ table: z.string(), fields: z.record(z.string(), z.json()) }).strict().refine(record => {
  const allowed = Object.hasOwn(columns, record.table) ? columns[record.table]!.split(",") : undefined;
  return !!allowed && Object.keys(record.fields).length === allowed.length && Object.keys(record.fields).every(key => allowed.includes(key));
}, "Unselected workspace content fields");
export const contentBundleSchema = z.object({
  version: z.literal(1), workspace: z.string().uuid(), owner: z.string().uuid(),
  records: z.array(recordSchema).max(10000),
  users: z.array(z.object({ id: z.string().uuid(), email: z.string().email().max(320), name: z.string().max(1024), role: z.enum(["", "member", "admin", "owner"]) }).strict()).max(10000),
  files: z.array(z.object({ attachment: z.string().uuid(), data: z.string().max(12 * 1024 * 1024).regex(/^[A-Za-z0-9+/]*={0,2}$/) }).strict()).max(10000),
  unavailable_attachments: z.array(z.string().uuid()).max(10000).optional(),
  attachment_mode: z.literal("chunked").optional(),
}).strict().refine(bundle => {
  const missing = bundle.unavailable_attachments ?? [];
  const ids = new Set(missing);
  return (bundle.attachment_mode !== "chunked" || bundle.files.length === 0) && ids.size === missing.length && missing.length + bundle.records.length <= 10000 &&
    !bundle.records.some(row => row.table === "attachment" && typeof row.fields.id === "string" && ids.has(row.fields.id)) &&
    !bundle.files.some(file => ids.has(file.attachment));
}, "Invalid unavailable attachments");
const httpsOrigin = z.string().refine(value => { try { const u = new URL(value); return u.protocol === "https:" && u.origin === value && !u.username && !u.password && u.port !== "0"; } catch { return false; } });
const exportBody = z.object({ workspace: z.string().uuid(), peer: httpsOrigin }).strict();
const fileBody = exportBody.extend({ attachment: z.string().uuid(), hash: z.string().regex(/^[a-f0-9]{64}$/), size: z.number().int().min(0).max(8 * 1024 * 1024), offset: z.number().int().min(0).max(8 * 1024 * 1024) }).strict();
const chunkData = z.string().max(1398104).regex(/^[A-Za-z0-9+/]*={0,2}$/).refine(value => value.length % 4 === 0);
export const contentMergeRequestSchema = z.discriminatedUnion("action", [
  z.object({ action: z.literal("merge-list"), body: z.object({ workspace: z.literal(""), peer: httpsOrigin }).strict() }).strict(),
  z.object({ action: z.literal("merge-export"), body: exportBody.extend({ attachment_mode: z.literal("chunked").optional() }).strict() }).strict(),
  z.object({ action: z.literal("merge-apply"), body: exportBody.extend({ bundle: contentBundleSchema }).strict().refine(body => body.bundle.workspace === body.workspace && body.bundle.records.some(row => row.table === "workspace" && row.fields.id === body.workspace)) }).strict(),
  z.object({ action: z.literal("merge-file-status"), body: fileBody.extend({ offset: z.literal(0) }).strict() }).strict(),
  z.object({ action: z.literal("merge-file-read"), body: fileBody.refine(v => v.offset <= v.size) }).strict(),
  z.object({ action: z.literal("merge-file-write"), body: fileBody.extend({ data: chunkData }).strict().refine(v => v.offset <= v.size) }).strict(),
]);
const fileResultSchema = z.object({ attachment: z.string().uuid(), hash: z.string().regex(/^[a-f0-9]{64}$/), size: z.number().int().min(0).max(8 * 1024 * 1024), offset: z.number().int().min(0).max(8 * 1024 * 1024), ready: z.boolean(), unavailable: z.boolean().optional(), data: chunkData.optional() }).strict();
const conflictSchema = z.object({ table: z.string(), key: z.string(), field: z.string(), local: z.json().nullable(), incoming: z.json().nullable() }).strict();
export type ContentMergeConflict = z.infer<typeof conflictSchema> & { origin: string };
export interface ContentMergeWarning {
  code: "attachment_unavailable";
  origin: string;
  workspace: string;
  attachment: string;
}
const resultSchema = z.object({ workspace: z.string().uuid(), updated: z.number().int().nonnegative(), conflicts: z.array(conflictSchema).max(1000) }).strict();
const infoSchema = z.object({ mode: z.literal("manual"), origin: httpsOrigin, owner: z.string().uuid(), node: z.string(), content_merge: z.literal(1), attachment_warnings: z.number().int().optional(), attachment_chunks: z.literal(1) });

function checked<S extends z.ZodType>(schema: S, raw: unknown): z.infer<S> {
  const valid = schema.safeParse(raw);
  const value = parseWithFallback<z.infer<S> | null>(valid.success ? valid.data : null, schema, null, { endpoint: "center-content-merge" });
  if (value === null) throw new Error("Invalid workspace merge response; update both centers and Desktop");
  return value;
}

/** Both snapshots precede either write. A second exchange acknowledges common
 * values; divergent edits stay on their own center and are returned for review.
 * There is no timer, automatic retry, deletion, or recovery/database replacement.
 */
export async function mergeCenters(source: CenterSyncEndpoint, peer: CenterSyncEndpoint, workspace: string, signal: AbortSignal, onProgress?: (progress: CenterSyncProgress) => void): Promise<CenterSyncResult> {
  httpsOrigin.parse(source.origin); httpsOrigin.parse(peer.origin);
  if (source.origin === peer.origin) throw new Error("Choose two different HTTPS centers");
  if (workspace !== "all") z.string().uuid().parse(workspace);
  const active = AbortSignal.any([signal, AbortSignal.timeout(15 * 60 * 1000)]);
  const progress: CenterSyncProgress = { phase: "checking", batches: 0, records: 0, edits: 0, percent: 0 };
  const warnings = new Map<string, ContentMergeWarning>();
  const report = (phase: CenterSyncProgress["phase"]) => { active.throwIfAborted(); progress.phase = phase; onProgress?.({ ...progress }); };
  const reportWarnings = (server: CenterSyncEndpoint, bundle: z.infer<typeof contentBundleSchema>) => {
    for (const attachment of bundle.unavailable_attachments ?? []) {
      warnings.set(`${server.origin}:${bundle.workspace}:${attachment}`, { code: "attachment_unavailable", origin: server.origin, workspace: bundle.workspace, attachment });
      if (warnings.size > 10000) throw new Error("Attachment warning limit reached; sync one workspace at a time");
    }
    progress.warnings = [...warnings.values()];
    report(progress.phase);
  };
  let requests = 0;
  async function request<S extends z.ZodType>(server: CenterSyncEndpoint, action: z.infer<typeof contentMergeRequestSchema>["action"], body: unknown, schema: S): Promise<z.infer<S>> {
    active.throwIfAborted();
    if (++requests > 20000) throw new Error("Sync request limit reached; click Sync again to resume staged attachments");
    const input = contentMergeRequestSchema.parse({ action, body });
    const raw = await server.request(input.action, input.body, active);
    active.throwIfAborted(); return checked(schema, raw);
  }
  let sentBytes = 0;
  async function transferFiles(from: CenterSyncEndpoint, to: CenterSyncEndpoint, bundle: z.infer<typeof contentBundleSchema>) {
    if (bundle.attachment_mode !== "chunked") throw new Error("Server did not return a chunked attachment manifest; update both centers");
    const unavailable = new Set(bundle.unavailable_attachments ?? []);
    const attachments = bundle.records.filter(row => row.table === "attachment");
    for (let index = 0; index < attachments.length; index++) {
      const row = attachments[index]!;
      const file = fileBody.parse({ workspace: bundle.workspace, peer: from.origin, attachment: row.fields.id, hash: row.fields.content_sha256, size: row.fields.size_bytes, offset: 0 });
      const checkFile = (value: z.infer<typeof fileResultSchema>) => {
        if (value.attachment !== file.attachment || value.hash !== file.hash || value.size !== file.size || value.offset > file.size || (value.ready && value.offset !== file.size)) throw new Error("Attachment chunk receipt mismatch");
        return value;
      };
      let state = checkFile(await request(to, "merge-file-status", file, fileResultSchema));
      if (state.unavailable || state.data !== undefined) throw new Error("Invalid attachment status");
      while (!state.ready) {
        const chunk = checkFile(await request(from, "merge-file-read", { ...file, peer: to.origin, offset: state.offset }, fileResultSchema));
        if (chunk.unavailable) {
          unavailable.add(file.attachment);
          break;
        }
        const data = chunk.data ?? "";
        const length = data.length / 4 * 3 - (data.endsWith("==") ? 2 : data.endsWith("=") ? 1 : 0);
        if (length > 1024 * 1024 || chunk.offset !== state.offset + length || (length === 0 && file.size !== 0)) throw new Error("Invalid attachment chunk length");
        if (sentBytes + length > 1024 * 1024 * 1024) throw new Error("Sync byte limit reached; click Sync again to resume staged attachments");
        const receipt = checkFile(await request(to, "merge-file-write", { ...file, offset: state.offset, data }, fileResultSchema));
        if (receipt.unavailable || receipt.data !== undefined || receipt.offset !== chunk.offset || receipt.ready !== (receipt.offset === file.size)) throw new Error("Attachment write was not confirmed");
        sentBytes += length;
        progress.fileBytes = (progress.fileBytes ?? 0) + length;
        progress.fileChunks = (progress.fileChunks ?? 0) + 1;
        state = receipt;
        progress.percent = Math.max(progress.percent ?? 0, Math.min(99, 10 + 90 * (steps + (index + state.offset / Math.max(1, file.size)) / Math.max(1, attachments.length)) / (ids.length * 8)));
        report(progress.phase);
      }
    }
    const result = { ...bundle, records: bundle.records.filter(row => row.table !== "attachment" || typeof row.fields.id !== "string" || !unavailable.has(row.fields.id)), ...(unavailable.size ? { unavailable_attachments: [...unavailable] } : {}) };
    reportWarnings(from, result);
    return result;
  }
  report("checking");
  const a = checked(infoSchema, await source.request("info", {}, active));
  active.throwIfAborted();
  const b = checked(infoSchema, await peer.request("info", {}, active));
  if (a.origin !== source.origin || b.origin !== peer.origin || a.node === b.node) throw new Error("Sync server identity mismatch");
  if (a.attachment_warnings !== 1 || b.attachment_warnings !== 1) throw new Error("Update both centers to support attachment unavailable warnings before syncing");
  progress.percent = 5; report("preparing");
  let ids = [workspace];
  if (workspace === "all") {
    const listSchema = z.object({ workspaces: z.array(z.string().uuid()).max(100) }).strict();
    const left = await request(source, "merge-list", { workspace: "", peer: peer.origin }, listSchema);
    const right = await request(peer, "merge-list", { workspace: "", peer: source.origin }, listSchema);
    ids = [...new Set([...left.workspaces, ...right.workspaces])].sort();
  }
  const preview: unknown[] = []; const review: ContentMergeConflict[] = [];
  let steps = 0, reviewBytes = 0;
  progress.percent = 10;
  const advance = () => { progress.percent = Math.min(99, 10 + 90 * ++steps / (ids.length * 8)); report(progress.phase); };
  for (const id of ids) {
    for (let round = 0; round < 2; round++) {
      report(round ? "verifying" : "pulling");
      const left = await request(source, "merge-export", { workspace: id, peer: peer.origin, attachment_mode: "chunked" }, contentBundleSchema);
      if (left.workspace !== id) throw new Error("Workspace mapping mismatch");
      reportWarnings(source, left);
      advance();
      const right = await request(peer, "merge-export", { workspace: id, peer: source.origin, attachment_mode: "chunked" }, contentBundleSchema);
      if (right.workspace !== id) throw new Error("Workspace mapping mismatch");
      reportWarnings(peer, right);
      advance();
      if (left.workspace !== id || right.workspace !== id || left.owner !== right.owner) throw new Error("Workspace or account mapping mismatch");
      report(round ? "verifying" : "pushing");
      for (const [server, other, bundle] of [[peer, source, left], [source, peer, right]] as const) {
        if (!bundle.records.length) { advance(); continue; }
        const prepared = await transferFiles(other, server, bundle);
        const result = await request(server, "merge-apply", { workspace: id, peer: other.origin, bundle: prepared }, resultSchema);
        if (result.workspace !== id) throw new Error("Merge receipt workspace mismatch");
        progress.batches++; progress.records += result.updated; progress.edits += result.updated;
        advance();
        if (round) {
          reviewBytes += JSON.stringify(result.conflicts).length;
          if (review.length + result.conflicts.length > 1000 || reviewBytes > 8 * 1024 * 1024) throw new Error("Conflict review limit reached; select one workspace and resolve its conflicts before syncing all workspaces");
          review.push(...result.conflicts.map(conflict => ({ ...conflict, origin: server.origin })));
        }
      }
      if (round && preview.length < 50) preview.push(...left.records.slice(0, 50 - preview.length));
    }
  }
  progress.percent = 100; report("complete");
  return { cursor: 0, records: preview, pending: 0, conflicts: review.length, review, workspaces: ids, warnings: [...warnings.values()] };
}
