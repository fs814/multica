// @vitest-environment node
import { describe, expect, it, vi } from "vitest";
import { contentBundleSchema, contentMergeRequestSchema, mergeCenters } from "./center-content-merge";
import type { CenterSyncEndpoint, CenterSyncProgress } from "./center-sync";

const workspace = "12345678-1234-4234-8234-123456789012";
const owner = "22345678-1234-4234-8234-123456789012";
const row = { table: "workspace", fields: { id: workspace, name: "Workspace", slug: "workspace", description: null, context: null, issue_prefix: "MRG", avatar_url: null } };
const bundle = { version: 1 as const, workspace, owner, records: [row], users: [{ id: owner, email: "owner@example.test", name: "Owner", role: "owner" as const }], files: [] };
function fixture() {
  const order: string[] = [];
  let left = structuredClone(bundle), right = { ...structuredClone(bundle), records: [] as typeof bundle.records };
  const make = (name: "a" | "b"): CenterSyncEndpoint => ({ origin: `https://${name}.example.test`, request: vi.fn(async (action, body) => {
    order.push(`${name}:${action}`);
    if (action === "info") return { mode: "manual", origin: `https://${name}.example.test`, owner, node: name, content_merge: 1 };
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
});
