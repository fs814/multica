// @vitest-environment node
import { describe, expect, it, vi } from "vitest";
import { syncCenters, validateCenterSyncSourceRequest, type CenterSyncEndpoint } from "./center-sync";

const id = "12345678-1234-4234-8234-123456789012";
const scope = { workspace: id, group: id, epoch: id };
const principal = { Account: id, Actor: id, Node: `manual-center-${"b".repeat(64)}` };
const prepared = { scope, principal };
const record = { kind: "issue", id, version: 1, deleted: false, fields: { title: "Fixture" } };

function fixture() {
  let state = { schema: 1, ...prepared, initialized: false, cursor: 0, records: {}, outbox: null, review: null };
  const order: string[] = [];
  const source: CenterSyncEndpoint = { origin: "https://source.example", request: vi.fn(async (action, body) => {
    order.push(`source:${action}`);
    if (action === "info") return { schema: 1, mode: "manual", owner: id, origin: source.origin, node: `manual-center-${"a".repeat(64)}` };
    if (action === "prepare") return prepared;
    if (action === "pull") {
      const parsed = validateCenterSyncSourceRequest({ action, body });
      if (parsed.action !== "pull") throw new Error("unexpected action");
      return { schema: 1, scope, from: parsed.body.cursor, cursor: 1, snapshot: parsed.body.snapshot, records: parsed.body.snapshot ? [record] : [], digest: "a".repeat(64) };
    }
    throw new Error(`Unexpected source action ${action}`);
  }) };
  const destination: CenterSyncEndpoint = { origin: "https://destination.example", request: vi.fn(async action => {
    order.push(`destination:${action}`);
    if (action === "info") return { schema: 1, mode: "manual", owner: id, origin: destination.origin, node: principal.Node };
    if (action === "replica") return state;
    if (action === "apply") { state = { ...state, initialized: true, cursor: 1, records: { [`issue/${id}`]: record } }; return state; }
    throw new Error(`Unexpected destination action ${action}`);
  }) };
  return { source, destination, order };
}

describe("manual center sync coordinator", () => {
  it("does nothing until called, then commits the snapshot and verifies the incremental boundary", async () => {
    const { source, destination, order } = fixture();
    expect(order).toEqual([]);
    const result = await syncCenters(source, destination, id, new AbortController().signal);
    expect(result).toEqual({ cursor: 1, records: [record], pending: 0, conflicts: 0 });
    expect(order).toEqual(["source:info", "destination:info", "source:prepare", "destination:replica", "source:pull", "destination:apply", "source:pull", "destination:apply"]);
  });

  it.each(["http://source.example", "https://destination.example"])("rejects unsafe source %s before requests", async origin => {
    const { source, destination, order } = fixture(); source.origin = origin;
    await expect(syncCenters(source, destination, id, new AbortController().signal)).rejects.toThrow();
    expect(order).toEqual([]);
  });

  it("does not continue after cancellation, even when a transport returns late", async () => {
    const { source, destination, order } = fixture();
    const controller = new AbortController();
    source.request = vi.fn(async () => { controller.abort(); return {}; });
    await expect(syncCenters(source, destination, id, controller.signal)).rejects.toThrow();
    expect(order).toEqual([]);
  });

  it.each([{}, { schema: 1, mode: "background", owner: id }, null])("rejects malformed info without reading workspace data", async response => {
    const { source, destination } = fixture();
    source.request = vi.fn(async () => response);
    await expect(syncCenters(source, destination, id, new AbortController().signal)).rejects.toThrow("Invalid sync response");
    expect(destination.request).not.toHaveBeenCalled();
  });

  it("rejects a mismatched destination origin before preparing a workspace", async () => {
    const { source, destination } = fixture();
    destination.request = vi.fn(async () => ({ schema: 1, mode: "manual", owner: id, origin: "https://wrong.example", node: principal.Node }));
    await expect(syncCenters(source, destination, id, new AbortController().signal)).rejects.toThrow("identity mismatch");
    expect(source.request).toHaveBeenCalledTimes(1);
  });

  it("rejects arbitrary source actions and unsafe edit fields", () => {
    expect(() => validateCenterSyncSourceRequest({ action: "edit", body: {} })).toThrow();
    expect(() => validateCenterSyncSourceRequest({ action: "info", body: { secret: "not allowed" } })).toThrow();
    expect(() => validateCenterSyncSourceRequest({ action: "push", body: { ...prepared, operation: { id, node: principal.Node, incarnation: id, sequence: 1, actor: id, scope, kind: "issue", entity: id, base: 1, patch: { status: "done" } } } })).toThrow();
  });

  it("pushes durable operations once, saves conflict receipts and pulls the resulting boundary", async () => {
    const operation = { id, node: principal.Node, incarnation: id, sequence: 1, actor: id, scope, kind: "issue", entity: id, base: 1, patch: { title: "local edit" } };
    const { source, destination, order } = fixture();
    let acknowledged = false;
    const originalSource = source.request;
    const originalDestination = destination.request;
    source.request = vi.fn(async (action, body, signal) => {
      if (action !== "push") return originalSource(action, body, signal);
      order.push("source:push");
      expect(validateCenterSyncSourceRequest({ action, body }).body).toHaveProperty("operation", operation);
      return { operation: id, status: "conflict", record, conflicts: [{ field: "title", base: "base", local: "local edit", center: "Fixture" }] };
    });
    destination.request = vi.fn(async (action, body, signal) => {
      if (action === "info") return originalDestination(action, body, signal);
      order.push(`destination:${action}`);
      if (action === "acknowledge") acknowledged = true;
      return { schema: 1, ...prepared, initialized: true, cursor: 1, records: { [`issue/${id}`]: record }, outbox: acknowledged ? [] : [operation], review: acknowledged ? [{ operation }] : [] };
    });
    const result = await syncCenters(source, destination, id, new AbortController().signal);
    expect(result.conflicts).toBe(1);
    expect(result.pending).toBe(0);
    expect(order.filter(item => item === "source:push")).toHaveLength(1);
    expect(order.slice(-4)).toEqual(["source:push", "destination:acknowledge", "source:pull", "destination:apply"]);
  });

  it("does not acknowledge a failed push or retry it in the same run", async () => {
    const { source, destination } = fixture();
    const originalSource = source.request;
    const originalDestination = destination.request;
    source.request = vi.fn(async (action, body, signal) => {
      if (action === "push") throw new Error("response lost");
      return originalSource(action, body, signal);
    });
    destination.request = vi.fn(async (action, body, signal) => {
      if (action === "info") return originalDestination(action, body, signal);
      return { schema: 1, ...prepared, initialized: true, cursor: 1, records: {}, review: [], outbox: [{ id, node: principal.Node, incarnation: id, sequence: 1, actor: id, scope, kind: "issue", entity: id, base: 1, patch: { title: "local edit" } }] };
    });
    await expect(syncCenters(source, destination, id, new AbortController().signal)).rejects.toThrow("response lost");
    expect(vi.mocked(source.request).mock.calls.filter(([action]) => action === "push")).toHaveLength(1);
    expect(vi.mocked(destination.request).mock.calls.some(([action]) => action === "acknowledge")).toBe(false);
  });

  it("rejects malformed batches before copying their data", async () => {
    const { source, destination } = fixture();
    const original = source.request;
    source.request = vi.fn(async (action, body, signal) => action === "pull" ? { cursor: "broken" } : original(action, body, signal));
    await expect(syncCenters(source, destination, id, new AbortController().signal)).rejects.toThrow("Invalid sync response: pull");
    expect(vi.mocked(destination.request).mock.calls.some(([action]) => action === "apply")).toBe(false);
  });

  it("rejects unselected fields before sending them to the destination", async () => {
    const { source, destination } = fixture();
    const original = source.request;
    source.request = vi.fn(async (action, body, signal) => action === "pull" ? {
      schema: 1, scope, from: 0, cursor: 1, snapshot: true, digest: "a".repeat(64),
      records: [{ ...record, fields: { ...record.fields, credential: "must-not-cross-origins" } }],
    } : original(action, body, signal));
    await expect(syncCenters(source, destination, id, new AbortController().signal)).rejects.toThrow("Invalid sync response: pull");
    expect(vi.mocked(destination.request).mock.calls.some(([action]) => action === "apply")).toBe(false);
  });
});
