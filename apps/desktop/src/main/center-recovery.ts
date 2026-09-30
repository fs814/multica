import { createHash, randomUUID } from "node:crypto";
import { readFile, writeFile, rename, rm, stat } from "node:fs/promises";
import { join, dirname } from "node:path";
import { homedir } from "node:os";
import { parseCenterImportStatus } from "@multica/core/api/center-recovery-schema";
import { parseCenterSettings } from "../shared/center-settings";
import { parseRecoveryRequest, parseTransferRequest, type CenterRecoveryResult, type CenterRecoveryProgress } from "../shared/center-recovery";
import { MAX_CENTER_BACKUP, openCenterBackup, sealCenterBackup } from "./center-backup-file";

interface Target { url: string; profile: string }
interface Dialogs {
  save(): Promise<string | undefined>;
  open(): Promise<string | undefined>;
  confirm(source: string, target: string): Promise<boolean>;
  confirmTransfer(source: string, target: string): Promise<boolean>;
}
interface Options { target(): Target; dialogs: Dialogs; fetch?: typeof fetch; profileRoot?: string }

async function boundedBody(response: Response): Promise<Buffer> {
  if (Number(response.headers.get("Content-Length")) > MAX_CENTER_BACKUP || !response.body) throw new Error("Backup exceeds the 512 MiB limit");
  const reader = response.body.getReader(); const chunks: Uint8Array[] = []; let size = 0;
  try {
    for (;;) { const part = await reader.read(); if (part.done) break; size += part.value.length;
      if (size > MAX_CENTER_BACKUP) throw new Error("Backup exceeds the 512 MiB limit"); chunks.push(part.value); }
    return Buffer.concat(chunks);
  } finally { await reader.cancel(); }
}
function responseError(response: Response, desktop = false): Error {
  if (response.status === 404) return new Error("This center needs the Multica recovery update and operator configuration before exporting or importing data.");
  if (response.status === 401 || response.status === 403) return new Error(desktop ? "Sign in as the account configured by MULTICA_RECOVERY_OWNER_ID on this center." : "The center recovery token is missing or incorrect.");
  if (response.status === 409) return new Error("Import is unavailable or another import is pending. Enable managed recovery on a single-node center with local uploads.");
  return new Error(`Center recovery failed (HTTP ${response.status}). Check the center's recovery configuration and PostgreSQL tools.`);
}

export function createCenterRecovery(options: Options) {
  const request = options.fetch ?? fetch;
  let busy = false;
  const jobs = new Map<string, { target: Target; token: string; centerId?: string; desktop?: boolean; deadline?: number }>();
  const target = (): Target => {
    const value = options.target();
    const parsed = parseCenterSettings({ version: 1, ...value });
    return { url: parsed.url, profile: parsed.profile };
  };
  async function credential(t: Target, supplied: string): Promise<string> {
    if (supplied) { if (supplied.length < 32) throw new Error("Recovery token must contain at least 32 characters"); return supplied; }
    try {
      const root = options.profileRoot ?? join(homedir(), ".multica", "profiles");
      const hash = createHash("sha256").update(t.url).digest("hex");
      const value: unknown = JSON.parse(await readFile(join(root, t.profile, "center-recovery", "sources", hash + ".json"), "utf8"));
      if (value && typeof value === "object" && "origin" in value && value.origin === t.url && "token" in value && typeof value.token === "string" && value.token.length >= 32 && !/\s/.test(value.token)) return value.token;
    } catch { /* Configuration is optional; never expose its contents. */ }
    throw new Error(`Enter the operator recovery token for ${t.url}. A normal login or provider API key cannot transfer the full center.`);
  }
  async function exclusive<T>(work: () => Promise<T>): Promise<T> {
    if (busy) throw new Error("A center recovery operation is already in progress"); busy = true;
    try { return await work(); } finally { busy = false; }
  }
  const headers = (token: string) => ({ Authorization: `Bearer ${token}` });
  const desktopPrefix = "/api/center/recovery/desktop";
  function sessionCredential(t: Target, session: { origin: string; token: string }): string {
    if (session.origin !== t.url) throw new Error("The center changed. Sign in to the current center before exporting or importing data.");
    return session.token;
  }
  async function snapshot(t: Target, token: string, desktop = false) {
    const response = await request(t.url + (desktop ? desktopPrefix : "/api/center/recovery") + "/snapshot", { headers: headers(token), redirect: "error", signal: AbortSignal.timeout(360_000) });
    if (!response.ok) throw responseError(response, desktop);
    const centerId = response.headers.get("X-Multica-Recovery-Center-ID");
    if (!centerId || response.headers.get("Content-Type") !== "application/zip") throw new Error("The center returned an unsupported backup response. Update the center and retry.");
    return { data: await boundedBody(response), centerId };
  }
  async function status(jobId: unknown): Promise<CenterRecoveryProgress> {
    if (typeof jobId !== "string" || !jobs.has(jobId)) throw new Error("Unknown import operation");
    const job = jobs.get(jobId)!;
    if (job.deadline && Date.now() >= job.deadline) {
      jobs.delete(jobId);
      throw new Error("Import has not reported completion. Check the center's import status before retrying; it may still be running.");
    }
    let response: Response;
    try { response = await request(job.target.url + (job.desktop ? desktopPrefix + "/import-status?job_id=" + jobId : "/api/center/recovery/import-status"), { headers: headers(job.token), redirect: "error", signal: AbortSignal.timeout(5000) }); }
    catch { return { state: "restarting" }; }
    if (response.status >= 500) return { state: "restarting" };
    try {
      if (!response.ok) throw responseError(response, job.desktop);
      const parsed = parseCenterImportStatus(await response.json());
      if (!parsed || parsed.jobId !== jobId || (job.centerId && parsed.centerId !== job.centerId)) throw new Error("The center returned an unexpected import status");
      if (!["pending", "activating", "complete", "failed"].includes(parsed.state)) throw new Error("The center returned an unsupported import state");
      if (parsed.state === "complete" || parsed.state === "failed") jobs.delete(jobId);
      return { state: parsed.state, message: parsed.message };
    } catch (error) {
      jobs.delete(jobId);
      throw error;
    }
  }
  return {
    exportData: (raw: unknown): Promise<CenterRecoveryResult> => exclusive(async () => {
      const input = parseRecoveryRequest(raw), t = target();
      const destination = await options.dialogs.save(); if (!destination) return { cancelled: true };
      const token = sessionCredential(t, input.session);
      const { data, centerId } = await snapshot(t, token, true);
      const encrypted = await sealCenterBackup(data, input.password, { version: 1, sourceUrl: t.url, centerId, createdAt: new Date().toISOString() });
      const temporary = join(dirname(destination), ".multica-backup-" + randomUUID());
      try { await writeFile(temporary, encrypted, { mode: 0o600, flag: "wx" }); await rename(temporary, destination); }
      finally { await rm(temporary, { force: true }); }
      return { cancelled: false, filePath: destination };
    }),
    importData: (raw: unknown): Promise<CenterRecoveryResult> => exclusive(async () => {
      const input = parseRecoveryRequest(raw), t = target();
      const file = await options.dialogs.open(); if (!file) return { cancelled: true };
      if ((await stat(file)).size > MAX_CENTER_BACKUP + 8192) throw new Error("Backup exceeds the size limit");
      const backup = await openCenterBackup(await readFile(file), input.password);
      if (!await options.dialogs.confirm(backup.metadata.sourceUrl, t.url)) return { cancelled: true };
      const token = sessionCredential(t, input.session);
      const response = await request(t.url + desktopPrefix + "/import", { method: "POST", headers: { ...headers(token), "Content-Type": "application/zip", "X-Multica-Recovery-Confirm": "replace-and-use" }, body: new Uint8Array(backup.data), redirect: "error", signal: AbortSignal.timeout(360_000) });
      if (response.status !== 202) throw responseError(response, true);
      const status = parseCenterImportStatus(await response.json());
      if (!status || status.state !== "pending" || status.centerId !== backup.metadata.centerId) throw new Error("Import was not acknowledged by the center. Check its status before retrying.");
      jobs.set(status.jobId, { target: t, token, desktop: true, centerId: backup.metadata.centerId, deadline: Date.now() + 15 * 60 * 1000 });
      return { cancelled: false, jobId: status.jobId };
    }),
    transferData: (raw: unknown): Promise<CenterRecoveryResult> => exclusive(async () => {
      const input = parseTransferRequest(raw), source = target();
      const destination = { url: input.targetUrl, profile: source.profile };
      if (destination.url === source.url) throw new Error("Choose a different center server for the transfer");
      const sourceToken = await credential(source, input.sourceRecoveryToken);
      const destinationToken = await credential(destination, input.targetRecoveryToken);
      if (!await options.dialogs.confirmTransfer(source.url, destination.url)) return { cancelled: true };
      const backup = await snapshot(source, sourceToken);
      let response: Response;
      try {
        response = await request(destination.url + "/api/center/recovery/import", { method: "POST", headers: { ...headers(destinationToken), "Content-Type": "application/zip", "X-Multica-Recovery-Confirm": "replace-and-use" }, body: new Uint8Array(backup.data), redirect: "error", signal: AbortSignal.timeout(360_000) });
      } catch { throw new Error("Transfer was not acknowledged. Check the destination center's import status before retrying; it may still be running."); }
      if (response.status !== 202) throw responseError(response);
      const accepted = parseCenterImportStatus(await response.json().catch(() => null));
      if (!accepted || accepted.state !== "pending" || accepted.centerId !== backup.centerId) throw new Error("Transfer was not acknowledged by the expected center. Check the destination's import status before retrying.");
      jobs.set(accepted.jobId, { target: destination, token: destinationToken, centerId: backup.centerId });
      const deadline = Date.now() + 15 * 60 * 1000;
      try {
        for (;;) {
          const progress = await status(accepted.jobId);
          if (progress.state === "failed") throw new Error(progress.message || "The destination center could not restore the transferred data");
          if (progress.state === "complete") return { cancelled: false };
          if (Date.now() >= deadline) throw new Error("The destination has not reported completion. Check its import status before retrying; the transfer may still be running.");
          await new Promise(resolve => setTimeout(resolve, 2000));
        }
      } finally { jobs.delete(accepted.jobId); }
    }),
    status,
  };
}
