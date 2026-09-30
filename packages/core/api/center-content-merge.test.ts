// @vitest-environment node
import { describe, expect, it, vi } from "vitest";
import { contentBundleSchema, contentMergeRequestSchema, mergeCenters } from "./center-content-merge";
import type { CenterSyncEndpoint, CenterSyncProgress } from "./center-sync";

const workspace = "12345678-1234-4234-8234-123456789012";
const owner = "22345678-1234-4234-8234-123456789012";
const row = { table: "workspace", fields: { id: workspace, name: "Workspace", slug: "workspace", description: null, context: null, issue_prefix: "MRG", avatar_url: null } };
const bundle = { version: 1 as const, attachment_mode: "chunked" as const, workspace, owner, records: [row], users: [{ id: owner, email: "owner@example.test", name: "Owner", role: "owner" as const }], files: [] };
function chunkFixture(options: { resume?: number; unavailable?: boolean; badReceipt?: boolean } = {}) {
  const f = fixture();
  const ids = ["32345678-1234-4234-8234-123456789012", "42345678-1234-4234-8234-123456789012", "52345678-1234-4234-8234-123456789012"];
  const size = 6 * 1048576, hash = "a".repeat(64);
  const attachmentRows = ids.map(id => ({ table: "attachment", fields: { id, issue_id: null, comment_id: null, chat_session_id: workspace, chat_message_id: null, uploader_type: "member", uploader_id: owner, filename: "fixture.bin", content_type: "application/octet-stream", size_bytes: size, created_at: "2026-01-01T00:00:00Z", content_sha256: hash } }));
  const offsets = new Map(ids.map(id => [id, options.resume ?? 0]));
  const reads: number[] = [], writes: number[] = [];
  const sourceRequest = f.source.request, peerRequest = f.peer.request;
  f.source.request = vi.fn(async (action, body, signal) => {
    if (action === "merge-file-read") {
      const input = contentMergeRequestSchema.parse({ action, body });
      if (input.action !== "merge-file-read") throw new Error("invalid test request");
      const { attachment, offset } = input.body;
      reads.push(offset);
      if (options.unavailable) return { attachment, hash, size, offset: 0, ready: false, unavailable: true };
      const next = Math.min(size, offset + 1048576);
      return { attachment, hash, size, offset: next, ready: next === size, data: Buffer.alloc(next - offset, 1).toString("base64") };
    }
    const result = await sourceRequest(action, body, signal);
    return action === "merge-export" ? { ...contentBundleSchema.parse(result), records: [row, ...attachmentRows] } : result;
  });
  f.peer.request = vi.fn(async (action, body, signal) => {
    if (action === "merge-file-status" || action === "merge-file-write") {
      const input = contentMergeRequestSchema.parse({ action, body });
      if (input.action !== "merge-file-status" && input.action !== "merge-file-write") throw new Error("invalid test request");
      const { attachment } = input.body;
      if (input.action === "merge-file-write") {
        const bytes = Buffer.from(input.body.data, "base64").length;
        writes.push(bytes);
        offsets.set(attachment, input.body.offset + bytes);
      }
      const offset = offsets.get(attachment)!;
      return { attachment, hash: options.badReceipt ? "b".repeat(64) : hash, size, offset, ready: offset === size };
    }
    const result = await peerRequest(action, body, signal);
    // Keep reverse snapshots empty in this relay test; real two-way application
    // and matching-local-file reuse are covered by the isolated backend test.
    return action === "merge-export" ? { ...contentBundleSchema.parse(result), records: [], unavailable_attachments: undefined } : result;
  });
  return { ...f, reads, writes };
}
function fixture() {
  const order: string[] = [];
  let left = structuredClone(bundle), right = { ...structuredClone(bundle), records: [] as typeof bundle.records };
  const make = (name: "a" | "b"): CenterSyncEndpoint => ({ origin: `https://${name}.example.test`, request: vi.fn(async (action, body) => {
    order.push(`${name}:${action}`);
    if (action === "info") return { mode: "manual", origin: `https://${name}.example.test`, owner, node: name, content_merge: 1, attachment_warnings: 1, attachment_chunks: 1 };
    const input = contentMergeRequestSchema.parse({ action, body });
    if (input.action === "merge-list") return { workspaces: name === "a" ? [workspace] : [] };
    if (input.action === "merge-export") return name === "a" ? left : right;
    if (input.action === "merge-apply") {
      const parsed = contentBundleSchema.parse(input.body.bundle);
      const copied = structuredClone(parsed) as typeof bundle;
      if (name === "a") left = copied; else right = copied;
      return { workspace, updated: 1, conflicts: [] };
    }
    throw new Error("unexpected action");
  }) });
  return { source: make("a"), peer: make("b"), order };
}

describe("normal workspace merge", () => {
  it("relays more than 16 MiB in bounded chunks with metadata-only apply and confirmed progress", async () => {
    const f = chunkFixture(), progress: CenterSyncProgress[] = [];
    await mergeCenters(f.source, f.peer, workspace, new AbortController().signal, p => progress.push(p));
    expect(f.writes).toHaveLength(18);
    expect(f.writes.every(bytes => bytes === 1048576)).toBe(true);
    expect(progress.at(-1)).toMatchObject({ fileBytes: 18 * 1048576, fileChunks: 18, percent: 100 });
    expect(progress.map(p => p.percent)).toEqual(progress.map(p => p.percent).sort((a, b) => a! - b!));
    for (const [action, body] of vi.mocked(f.peer.request).mock.calls) {
      if (action === "merge-apply") expect((body as { bundle: { files: unknown[] } }).bundle.files).toEqual([]);
    }
  });
  it("resumes retained chunks and does not retransmit completed files", async () => {
    const f = chunkFixture({ resume: 5 * 1048576 });
    await mergeCenters(f.source, f.peer, workspace, new AbortController().signal);
    expect(f.reads).toEqual([5 * 1048576, 5 * 1048576, 5 * 1048576]);
    expect(f.writes).toHaveLength(3);
  });
  it("rejects mismatched chunk receipts before applying workspace content", async () => {
    const f = chunkFixture({ badReceipt: true });
    await expect(mergeCenters(f.source, f.peer, workspace, new AbortController().signal)).rejects.toThrow("receipt mismatch");
    expect(f.order.some(v => v.endsWith("merge-apply"))).toBe(false);
  });
  it("warns and skips attachment rows if files disappear during chunk transfer", async () => {
    const f = chunkFixture({ unavailable: true });
    const result = await mergeCenters(f.source, f.peer, workspace, new AbortController().signal);
    expect(result.warnings).toHaveLength(3);
    expect(f.writes).toEqual([]);
    for (const [action, body] of vi.mocked(f.peer.request).mock.calls) {
      if (action !== "merge-apply") continue;
      const input = contentMergeRequestSchema.parse({ action, body });
      if (input.action !== "merge-apply") throw new Error("invalid test request");
      expect(input.body.bundle.records.some(row => row.table === "attachment")).toBe(false);
      expect(input.body.bundle.unavailable_attachments).toHaveLength(3);
    }
  });
  it("stops after the confirmed chunk when cancelled without publishing the workspace", async () => {
    const f = chunkFixture(), active = new AbortController();
    await expect(mergeCenters(f.source, f.peer, workspace, active.signal, p => { if (p.fileChunks === 1) active.abort(); })).rejects.toThrow();
    expect(f.writes).toHaveLength(1);
    expect(f.order.some(v => v.endsWith("merge-apply"))).toBe(false);
  });
  it("only starts when called, snapshots both sides before writing and acknowledges in a second round", async () => {
    const f = fixture(); expect(f.order).toEqual([]);
    const progress: CenterSyncProgress[] = [];
    const result = await mergeCenters(f.source, f.peer, workspace, new AbortController().signal, value => progress.push(value));
    expect(f.order.slice(0, 5)).toEqual(["a:info", "b:info", "a:merge-export", "b:merge-export", "b:merge-apply"]);
    expect(f.order.filter(v => v.endsWith("merge-apply"))).toHaveLength(3);
    expect(result.workspaces).toEqual([workspace]); expect(result.conflicts).toBe(0);
    expect(progress.at(-1)).toMatchObject({ phase: "complete", batches: 3, records: 3 });
    expect(progress.at(-1)?.percent).toBe(100);
    expect(progress.slice(0, -1).every(p => (p.percent ?? 0) < 100)).toBe(true);
    expect(progress.map(p => p.percent)).toEqual(progress.map(p => p.percent).sort((a, b) => a! - b!));
  });
  it("discovers owned workspaces on both centers for all mode", async () => {
    const f = fixture(); await mergeCenters(f.source, f.peer, "all", new AbortController().signal);
    expect(f.order.slice(0, 4)).toEqual(["a:info", "b:info", "a:merge-list", "b:merge-list"]);
  });
  it("requires the new capability without falling back to isolated replicas", async () => {
    const f = fixture(); vi.mocked(f.peer.request).mockResolvedValueOnce({ mode: "manual", origin: f.peer.origin, owner, node: "b" });
    await expect(mergeCenters(f.source, f.peer, workspace, new AbortController().signal)).rejects.toThrow("update both centers");
    expect(f.order).not.toContain("a:merge-export");
  });
  it("rejects credential fields before any relay", () => {
    const bad = structuredClone(bundle);
    Object.assign(bad.records[0]!.fields, { settings: { token: "must-not-leave" } });
    expect(contentBundleSchema.safeParse(bad).success).toBe(false);
    expect(contentMergeRequestSchema.safeParse({ action: "merge-apply", body: { workspace, peer: "https://peer.example.test", bundle: bad } }).success).toBe(false);
  });
  it("rejects mismatched scope and HTTP", () => {
    expect(contentMergeRequestSchema.safeParse({ action: "merge-apply", body: { workspace: owner, peer: "https://peer.example.test", bundle } }).success).toBe(false);
    expect(contentMergeRequestSchema.safeParse({ action: "merge-export", body: { workspace, peer: "http://peer.example.test" } }).success).toBe(false);
  });
  it("stops between requests when cancelled", async () => {
    const f = fixture(), active = new AbortController();
    await expect(mergeCenters(f.source, f.peer, workspace, active.signal, p => { if (p.phase === "pulling") active.abort(); })).rejects.toThrow();
    expect(f.order.some(v => v.endsWith("merge-apply"))).toBe(false);
  });
  it("continues with deduplicated warnings and preserves their origin and workspace", async () => {
    const f = fixture(), originalSource = f.source.request, originalPeer = f.peer.request;
    f.source.request = vi.fn(async (action, body, signal) => {
      const response = await originalSource(action, body, signal);
      return action === "merge-export" ? { ...contentBundleSchema.parse(response), unavailable_attachments: [owner] } : response;
    });
    f.peer.request = vi.fn(async (action, body, signal) => {
      const response = await originalPeer(action, body, signal);
      return action === "merge-export" ? { ...contentBundleSchema.parse(response), unavailable_attachments: undefined } : response;
    });
    const progress: CenterSyncProgress[] = [];
    const result = await mergeCenters(f.source, f.peer, workspace, new AbortController().signal, p => progress.push(p));
    expect(result.warnings).toEqual([{ code: "attachment_unavailable", origin: f.source.origin, workspace, attachment: owner }]);
    expect(progress.at(-1)?.warnings).toEqual(result.warnings);
    expect(f.order.filter(v => v.endsWith("merge-apply"))).toHaveLength(3);
  });
  it("retains an observed warning when a later server request fails", async () => {
    const f = fixture(), sourceRequest = f.source.request, peerRequest = f.peer.request;
    f.source.request = async (action, body, signal) => {
      const response = await sourceRequest(action, body, signal);
      return action === "merge-export" ? { ...contentBundleSchema.parse(response), unavailable_attachments: [owner] } : response;
    };
    f.peer.request = async (action, body, signal) => {
      if (action === "merge-export") throw new Error("fixture request failed");
      return peerRequest(action, body, signal);
    };
    const progress: CenterSyncProgress[] = [];
    await expect(mergeCenters(f.source, f.peer, workspace, new AbortController().signal, p => progress.push(p))).rejects.toThrow("fixture request failed");
    expect(progress.at(-1)?.warnings).toHaveLength(1);
    expect(f.order.some(v => v.endsWith("merge-apply"))).toBe(false);
  });
  it("requires receiving server support before sending an unavailable marker", async () => {
    const f = fixture(), request = f.source.request;
    f.source.request = async (action, body, signal) => {
      const response = await request(action, body, signal);
      return action === "merge-export" ? { ...contentBundleSchema.parse(response), unavailable_attachments: [owner] } : response;
    };
    vi.mocked(f.peer.request).mockResolvedValueOnce({ mode: "manual", origin: f.peer.origin, owner, node: "b", content_merge: 1, attachment_chunks: 1 });
    await expect(mergeCenters(f.source, f.peer, workspace, new AbortController().signal)).rejects.toThrow("Update both centers");
    expect(f.order.some(v => v.endsWith("merge-apply"))).toBe(false);
  });
  it("rejects malformed or contradictory missing-file markers", () => {
    for (const ids of [[owner, owner], ["invalid-id"]]) {
      expect(contentBundleSchema.safeParse({ ...bundle, unavailable_attachments: ids }).success).toBe(false);
    }
    expect(contentBundleSchema.safeParse({ ...bundle, unavailable_attachments: [owner], files: [{ attachment: owner, data: "" }] }).success).toBe(false);
    expect(contentBundleSchema.safeParse({ ...bundle, unavailable_attachments: [owner] }).success).toBe(true);
  });
});
