// @vitest-environment node
import { afterEach, describe, expect, it, vi } from "vitest";
import { mkdtemp, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import { createHash } from "node:crypto";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createCenterRecovery } from "./center-recovery";
import { openCenterBackup, sealCenterBackup } from "./center-backup-file";
const roots: string[] = [];
afterEach(async () => { await Promise.all(roots.splice(0).map(root => rm(root, { recursive: true, force: true }))); });
async function setup() {
  const root = await mkdtemp(join(tmpdir(), "center-buttons-test-")); roots.push(root);
  const file = join(root, "export.multica-backup");
  const dialogs = { save: vi.fn(async (): Promise<string | undefined> => file), open: vi.fn(async (): Promise<string | undefined> => file), confirm: vi.fn(async () => true), confirmTransfer: vi.fn(async () => true) };
  const request = vi.fn<typeof fetch>();
  const controller = createCenterRecovery({ target: () => ({ url: "https://current.example", profile: "desktop-test" }), dialogs, fetch: request, profileRoot: root });
  return { root, file, dialogs, request, controller };
}
const input = { password: "a portable backup password", session: { origin: "https://current.example", token: "desktop-session" } };
const data = Buffer.from("504b030468656c6c6f", "hex");
const jobId = "a".repeat(32);
describe("center export and import", () => {
  it("cancels without contacting the center and preserves an existing file on failure", async () => {
    const { file, dialogs, request, controller } = await setup();
    dialogs.save.mockResolvedValueOnce(undefined);
    expect(await controller.exportData(input)).toEqual({ cancelled: true }); expect(request).not.toHaveBeenCalled();
    await writeFile(file, "existing backup"); request.mockResolvedValueOnce(new Response("unauthorized", { status: 401 }));
    await expect(controller.exportData(input)).rejects.toThrow("Sign in"); expect(await readFile(file, "utf8")).toBe("existing backup");
  });
  it("exports the active center through the native save destination", async () => {
    const { file, request, controller } = await setup();
    request.mockResolvedValueOnce(new Response(data, { headers: { "Content-Type": "application/zip", "X-Multica-Recovery-Center-ID": "source-center" } }));
    expect((await controller.exportData(input)).cancelled).toBe(false);
    const backup = await openCenterBackup(await readFile(file), input.password); expect(backup.data).toEqual(data);
    expect(backup.metadata.sourceUrl).toBe("https://current.example");
    expect(request).toHaveBeenCalledWith("https://current.example/api/center/recovery/desktop/snapshot", expect.objectContaining({ headers: { Authorization: "Bearer desktop-session" }, redirect: "error" }));
    expect(JSON.stringify(request.mock.calls)).not.toContain(input.password);
  });
  it("confirms the destination, tolerates restart, and accepts only matching completion", async () => {
    const { file, dialogs, request, controller } = await setup();
    await writeFile(file, await sealCenterBackup(data, input.password, { version: 1, sourceUrl: "https://old.example", centerId: "old-center", createdAt: new Date().toISOString() }));
    dialogs.confirm.mockResolvedValueOnce(false);
    expect(await controller.importData(input)).toEqual({ cancelled: true }); expect(request).not.toHaveBeenCalled();
    request.mockResolvedValueOnce(Response.json({ job_id: jobId, state: "pending", center_id: "old-center" }, { status: 202 }));
    expect((await controller.importData(input)).jobId).toBe(jobId);
    expect(dialogs.confirm).toHaveBeenCalledWith("https://old.example", "https://current.example");
    request.mockRejectedValueOnce(new TypeError("offline")); expect(await controller.status(jobId)).toEqual({ state: "restarting" });
    request.mockResolvedValueOnce(Response.json({ job_id: jobId, state: "complete", center_id: "old-center" }));
    expect((await controller.status(jobId)).state).toBe("complete");
    expect(request).toHaveBeenLastCalledWith(`https://current.example/api/center/recovery/desktop/import-status?job_id=${jobId}`, expect.objectContaining({ headers: { Authorization: "Bearer desktop-session" }, redirect: "error" }));
    await expect(controller.status(jobId)).rejects.toThrow("Unknown");
  });
  it("does not upload when the password is wrong", async () => {
    const { file, request, controller } = await setup();
    await writeFile(file, await sealCenterBackup(data, input.password, { version: 1, sourceUrl: "https://old.example", centerId: "old-center", createdAt: new Date().toISOString() }));
    await expect(controller.importData({ ...input, password: "a different password" })).rejects.toThrow("Incorrect");
    expect(request).not.toHaveBeenCalled();
  });
  it("requires a login bound to the connected center, not a recovery credential", async () => {
    const { controller, request } = await setup();
    await expect(controller.exportData({ password: input.password, recoveryToken: "a".repeat(64) })).rejects.toThrow("Sign in");
    await expect(controller.exportData({ ...input, session: { ...input.session, token: "" } })).rejects.toThrow("Sign in");
    await expect(controller.exportData({ ...input, session: { ...input.session, origin: "https://another.example" } })).rejects.toThrow("center changed");
    expect(request).not.toHaveBeenCalled();
  });
  it("never falls back to operator recovery when the session is denied", async () => {
    const { controller, request } = await setup();
    request.mockResolvedValueOnce(new Response("forbidden", { status: 403 }));
    await expect(controller.exportData(input)).rejects.toThrow("MULTICA_RECOVERY_OWNER_ID");
    expect(request).toHaveBeenCalledTimes(1);
  });
  it("rejects import acknowledgements for a different center", async () => {
    const { controller, request, file } = await setup();
    await writeFile(file, await sealCenterBackup(data, input.password, { version: 1, sourceUrl: "https://old.example", centerId: "old-center", createdAt: new Date().toISOString() }));
    request.mockResolvedValueOnce(Response.json({ job_id: jobId, state: "pending", center_id: "different-center" }, { status: 202 }));
    await expect(controller.importData(input)).rejects.toThrow("acknowledged");
    expect(request).toHaveBeenCalledTimes(1);
  });
  it("releases a denied or timed-out status session without claiming the restore failed", async () => {
    const { controller, request, file } = await setup();
    await writeFile(file, await sealCenterBackup(data, input.password, { version: 1, sourceUrl: "https://old.example", centerId: "old-center", createdAt: new Date().toISOString() }));
    request.mockResolvedValueOnce(Response.json({ job_id: jobId, state: "pending", center_id: "old-center" }, { status: 202 }));
    await controller.importData(input);
    request.mockResolvedValueOnce(new Response("denied", { status: 403 }));
    await expect(controller.status(jobId)).rejects.toThrow("Sign in");
    await expect(controller.status(jobId)).rejects.toThrow("Unknown");
    request.mockResolvedValueOnce(Response.json({ job_id: jobId, state: "pending", center_id: "old-center" }, { status: 202 }));
    await controller.importData(input);
    vi.useFakeTimers();
    try {
      vi.setSystemTime(Date.now() + 16 * 60 * 1000);
      await expect(controller.status(jobId)).rejects.toThrow("may still be running");
      await expect(controller.status(jobId)).rejects.toThrow("Unknown");
    } finally { vi.useRealTimers(); }
  });
});

const transferInput = { targetUrl: "https://new.example/", sourceRecoveryToken: "s".repeat(64), targetRecoveryToken: "t".repeat(64) };
function snapshotResponse() {
  return new Response(data, { headers: { "Content-Type": "application/zip", "X-Multica-Recovery-Center-ID": "source-center" } });
}
describe("direct center transfer", () => {
  it("loads saved credentials by exact server origin and never borrows the source token for the destination", async () => {
    const { root, controller, request } = await setup();
    const credentials = join(root, "desktop-test", "center-recovery", "sources");
    await mkdir(credentials, { recursive: true });
    const fileFor = (url: string) => join(credentials, createHash("sha256").update(url).digest("hex") + ".json");
    await writeFile(fileFor("https://current.example"), JSON.stringify({ origin: "https://current.example", token: transferInput.sourceRecoveryToken }));
    const input = { ...transferInput, sourceRecoveryToken: "", targetRecoveryToken: "" };
    await expect(controller.transferData(input)).rejects.toThrow("https://new.example");
    expect(request).not.toHaveBeenCalled();
    await writeFile(fileFor("https://new.example"), JSON.stringify({ origin: "https://new.example", token: transferInput.targetRecoveryToken }));
    request.mockResolvedValueOnce(snapshotResponse());
    request.mockResolvedValueOnce(Response.json({ job_id: jobId, state: "pending", center_id: "source-center" }, { status: 202 }));
    request.mockResolvedValueOnce(Response.json({ job_id: jobId, state: "complete", center_id: "source-center" }));
    await expect(controller.transferData(input)).resolves.toEqual({ cancelled: false });
    expect(request).toHaveBeenNthCalledWith(2, "https://new.example/api/center/recovery/import", expect.objectContaining({ headers: expect.objectContaining({ Authorization: `Bearer ${transferInput.targetRecoveryToken}` }) }));
  });
  it("uses separate credentials and waits for destination completion without file dialogs", async () => {
    const { controller, request, dialogs } = await setup();
    request.mockResolvedValueOnce(snapshotResponse());
    request.mockResolvedValueOnce(Response.json({ job_id: jobId, state: "pending", center_id: "source-center" }, { status: 202 }));
    request.mockResolvedValueOnce(Response.json({ job_id: jobId, state: "complete", center_id: "source-center" }));
    expect(await controller.transferData(transferInput)).toEqual({ cancelled: false });
    expect(dialogs.confirmTransfer).toHaveBeenCalledWith("https://current.example", "https://new.example");
    expect(dialogs.open).not.toHaveBeenCalled(); expect(dialogs.save).not.toHaveBeenCalled();
    expect(request).toHaveBeenNthCalledWith(1, "https://current.example/api/center/recovery/snapshot", expect.objectContaining({ headers: { Authorization: `Bearer ${transferInput.sourceRecoveryToken}` }, redirect: "error" }));
    expect(request).toHaveBeenNthCalledWith(2, "https://new.example/api/center/recovery/import", expect.objectContaining({ method: "POST", body: new Uint8Array(data), headers: expect.objectContaining({ Authorization: `Bearer ${transferInput.targetRecoveryToken}`, "X-Multica-Recovery-Confirm": "replace-and-use" }), redirect: "error" }));
    expect(request).toHaveBeenNthCalledWith(3, "https://new.example/api/center/recovery/import-status", expect.objectContaining({ headers: { Authorization: `Bearer ${transferInput.targetRecoveryToken}` }, redirect: "error" }));
  });
  it("rejects the current center, invalid addresses, and invalid credentials before any network request", async () => {
    const { controller, request } = await setup();
    for (const targetUrl of ["https://current.example/", "https://new.example/path", "file:///tmp/a"]) {
      await expect(controller.transferData({ ...transferInput, targetUrl })).rejects.toThrow();
    }
    await expect(controller.transferData({ ...transferInput, targetRecoveryToken: "short" })).rejects.toThrow("token");
    expect(request).not.toHaveBeenCalled();
  });
  it("cancels before exporting and never uploads a failed source snapshot", async () => {
    const { controller, request, dialogs } = await setup();
    dialogs.confirmTransfer.mockResolvedValueOnce(false);
    expect(await controller.transferData(transferInput)).toEqual({ cancelled: true });
    expect(request).not.toHaveBeenCalled();
    request.mockResolvedValueOnce(new Response("", { status: 401 }));
    await expect(controller.transferData(transferInput)).rejects.toThrow("token");
    expect(request).toHaveBeenCalledTimes(1);
  });
  it("does not report success for malformed, mismatched, or failed destination results", async () => {
    const { controller, request } = await setup();
    request.mockResolvedValueOnce(snapshotResponse());
    request.mockResolvedValueOnce(Response.json({ job_id: jobId, state: "pending", center_id: "source-center" }, { status: 202 }));
    request.mockResolvedValueOnce(Response.json({ job_id: jobId, state: "failed", center_id: "source-center", message: "Restore failed" }));
    await expect(controller.transferData(transferInput)).rejects.toThrow("Restore failed");
    request.mockResolvedValueOnce(snapshotResponse());
    request.mockResolvedValueOnce(Response.json({ job_id: jobId, state: "pending", center_id: "wrong-center" }, { status: 202 }));
    await expect(controller.transferData(transferInput)).rejects.toThrow("acknowledged");
    request.mockResolvedValueOnce(snapshotResponse());
    request.mockResolvedValueOnce(Response.json({ state: "complete" }, { status: 202 }));
    await expect(controller.transferData(transferInput)).rejects.toThrow("acknowledged");
  });
  it("keeps other recovery operations locked while the destination restarts", async () => {
    vi.useFakeTimers();
    try {
      const { controller, request } = await setup();
      request.mockResolvedValueOnce(snapshotResponse());
      request.mockResolvedValueOnce(Response.json({ job_id: jobId, state: "pending", center_id: "source-center" }, { status: 202 }));
      request.mockRejectedValueOnce(new TypeError("restarting"));
      request.mockResolvedValueOnce(Response.json({ job_id: jobId, state: "complete", center_id: "source-center" }));
      let completed = false;
      const transfer = controller.transferData(transferInput).then(result => { completed = true; return result; });
      await vi.advanceTimersByTimeAsync(1);
      expect(completed).toBe(false);
      await expect(controller.exportData(input)).rejects.toThrow("already in progress");
      await vi.advanceTimersByTimeAsync(2000);
      expect(await transfer).toEqual({ cancelled: false });
    } finally { vi.useRealTimers(); }
  });
  it("rejects oversized snapshots before uploading and does not retry an unacknowledged upload", async () => {
    const { controller, request } = await setup();
    request.mockResolvedValueOnce(new Response(data, { headers: { "Content-Type": "application/zip", "X-Multica-Recovery-Center-ID": "source-center", "Content-Length": String(513 * 1024 * 1024) } }));
    await expect(controller.transferData(transferInput)).rejects.toThrow("512 MiB");
    expect(request).toHaveBeenCalledTimes(1);
    request.mockResolvedValueOnce(snapshotResponse());
    request.mockRejectedValueOnce(new TypeError("connection lost after upload"));
    await expect(controller.transferData(transferInput)).rejects.toThrow("Check the destination");
    expect(request).toHaveBeenCalledTimes(3);
  });
  it("rejects completion for another import job", async () => {
    const { controller, request } = await setup();
    request.mockResolvedValueOnce(snapshotResponse());
    request.mockResolvedValueOnce(Response.json({ job_id: jobId, state: "pending", center_id: "source-center" }, { status: 202 }));
    request.mockResolvedValueOnce(Response.json({ job_id: "b".repeat(32), state: "complete", center_id: "source-center" }));
    await expect(controller.transferData(transferInput)).rejects.toThrow("unexpected import status");
  });
  it("times out without claiming completion or sending another import", async () => {
    vi.useFakeTimers();
    try {
      const { controller, request } = await setup();
      request.mockResolvedValueOnce(snapshotResponse());
      request.mockResolvedValueOnce(Response.json({ job_id: jobId, state: "pending", center_id: "source-center" }, { status: 202 }));
      request.mockImplementation(async () => new Response("restarting", { status: 503 }));
      const transfer = controller.transferData(transferInput);
      const rejected = expect(transfer).rejects.toThrow("has not reported completion");
      await vi.advanceTimersByTimeAsync(15 * 60 * 1000 + 2000);
      await rejected;
      expect(request.mock.calls.filter(([, options]) => options?.method === "POST")).toHaveLength(1);
    } finally { vi.useRealTimers(); }
  });
});
