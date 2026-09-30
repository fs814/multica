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
}).strict();
const httpsOrigin = z.string().refine(value => { try { const u = new URL(value); return u.protocol === "https:" && u.origin === value && !u.username && !u.password && u.port !== "0"; } catch { return false; } });
const exportBody = z.object({ workspace: z.string().uuid(), peer: httpsOrigin }).strict();
export const contentMergeRequestSchema = z.discriminatedUnion("action", [
  z.object({ action: z.literal("merge-list"), body: z.object({ workspace: z.literal(""), peer: httpsOrigin }).strict() }).strict(),
  z.object({ action: z.literal("merge-export"), body: exportBody }).strict(),
  z.object({ action: z.literal("merge-apply"), body: exportBody.extend({ bundle: contentBundleSchema }).strict().refine(body => body.bundle.workspace === body.workspace && body.bundle.records.some(row => row.table === "workspace" && row.fields.id === body.workspace)) }).strict(),
]);
const conflictSchema = z.object({ table: z.string(), key: z.string(), field: z.string(), local: z.json().nullable(), incoming: z.json().nullable() }).strict();
export type ContentMergeConflict = z.infer<typeof conflictSchema> & { origin: string };
const resultSchema = z.object({ workspace: z.string().uuid(), updated: z.number().int().nonnegative(), conflicts: z.array(conflictSchema).max(1000) }).strict();
const infoSchema = z.object({ mode: z.literal("manual"), origin: httpsOrigin, owner: z.string().uuid(), node: z.string(), content_merge: z.literal(1) });

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
  const active = AbortSignal.any([signal, AbortSignal.timeout(120000)]);
  const progress: CenterSyncProgress = { phase: "checking", batches: 0, records: 0, edits: 0, percent: 0 };
  const report = (phase: CenterSyncProgress["phase"]) => { active.throwIfAborted(); progress.phase = phase; onProgress?.({ ...progress }); };
  async function request<S extends z.ZodType>(server: CenterSyncEndpoint, action: "merge-export" | "merge-apply" | "merge-list", body: unknown, schema: S): Promise<z.infer<S>> {
    active.throwIfAborted();
    const input = contentMergeRequestSchema.parse({ action, body });
    const raw = await server.request(input.action, input.body, active);
    active.throwIfAborted(); return checked(schema, raw);
  }
  report("checking");
  const a = checked(infoSchema, await source.request("info", {}, active));
  active.throwIfAborted();
  const b = checked(infoSchema, await peer.request("info", {}, active));
  if (a.origin !== source.origin || b.origin !== peer.origin || a.node === b.node) throw new Error("Sync server identity mismatch");
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
      const left = await request(source, "merge-export", { workspace: id, peer: peer.origin }, contentBundleSchema);
      advance();
      const right = await request(peer, "merge-export", { workspace: id, peer: source.origin }, contentBundleSchema);
      advance();
      if (left.workspace !== id || right.workspace !== id || left.owner !== right.owner) throw new Error("Workspace or account mapping mismatch");
      report(round ? "verifying" : "pushing");
      for (const [server, other, bundle] of [[peer, source, left], [source, peer, right]] as const) {
        if (!bundle.records.length) { advance(); continue; }
        const result = await request(server, "merge-apply", { workspace: id, peer: other.origin, bundle }, resultSchema);
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
  return { cursor: 0, records: preview, pending: 0, conflicts: review.length, review, workspaces: ids };
}
