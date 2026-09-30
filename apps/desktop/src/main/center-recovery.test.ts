// @vitest-environment node
import { afterEach, describe, expect, it, vi } from "vitest";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createCenterRecovery } from "./center-recovery";
import { openCenterBackup, sealCenterBackup } from "./center-backup-file";
const roots: string[] = [];
afterEach(async () => { await Promise.all(roots.splice(0).map(root => rm(root, { recursive: true, force: true }))); });
async function setup() {
  const root = await mkdtemp(join(tmpdir(), "center-buttons-test-")); roots.push(root);
  const file = join(root, "export.multica-backup");
  const dialogs = { save: vi.fn(async (): Promise<string | undefined> => file), open: vi.fn(async (): Promise<string | undefined> => file), confirm: vi.fn(async () => true) };
  const request = vi.fn<typeof fetch>();
  const controller = createCenterRecovery({ target: () => ({ url: "https://current.example", profile: "desktop-test" }), dialogs, fetch: request, profileRoot: root });
  return { file, dialogs, request, controller };
}
const input = { password: "a portable backup password", recoveryToken: "a".repeat(64) };
const data = Buffer.from("504b030468656c6c6f", "hex");
const jobId = "a".repeat(32);
describe("center export and import", () => {
  it("cancels without contacting the center and preserves an existing file on failure", async () => {
    const { file, dialogs, request, controller } = await setup();
    dialogs.save.mockResolvedValueOnce(undefined);
    expect(await controller.exportData(input)).toEqual({ cancelled: true }); expect(request).not.toHaveBeenCalled();
    await writeFile(file, "existing backup"); request.mockResolvedValueOnce(new Response("unauthorized", { status: 401 }));
    await expect(controller.exportData(input)).rejects.toThrow("token"); expect(await readFile(file, "utf8")).toBe("existing backup");
  });
  it("exports the active center through the native save destination", async () => {
    const { file, request, controller } = await setup();
    request.mockResolvedValueOnce(new Response(data, { headers: { "Content-Type": "application/zip", "X-Multica-Recovery-Center-ID": "source-center" } }));
    expect((await controller.exportData(input)).cancelled).toBe(false);
    const backup = await openCenterBackup(await readFile(file), input.password); expect(backup.data).toEqual(data);
    expect(backup.metadata.sourceUrl).toBe("https://current.example");
    expect(request).toHaveBeenCalledWith("https://current.example/api/center/recovery/snapshot", expect.objectContaining({ redirect: "error" }));
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
    await expect(controller.status(jobId)).rejects.toThrow("Unknown");
  });
  it("does not upload when the password is wrong", async () => {
    const { file, request, controller } = await setup();
    await writeFile(file, await sealCenterBackup(data, input.password, { version: 1, sourceUrl: "https://old.example", centerId: "old-center", createdAt: new Date().toISOString() }));
    await expect(controller.importData({ ...input, password: "a different password" })).rejects.toThrow("Incorrect");
    expect(request).not.toHaveBeenCalled();
  });
});
