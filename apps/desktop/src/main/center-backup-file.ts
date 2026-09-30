import { createCipheriv, createDecipheriv, randomBytes, scrypt } from "node:crypto";
import { promisify } from "node:util";

const derive = promisify(scrypt);
const magic = Buffer.from("MULTICA-DESKTOP-BACKUP-1\n");
export const MAX_CENTER_BACKUP = 512 * 1024 * 1024;
export interface BackupMetadata { version: 1; sourceUrl: string; centerId: string; createdAt: string }

function metadata(value: unknown): BackupMetadata {
  if (!value || typeof value !== "object") throw new Error("Invalid backup metadata");
  const v = value as Record<string, unknown>;
  if (v.version !== 1 || typeof v.sourceUrl !== "string" || v.sourceUrl.length > 2048 ||
      typeof v.centerId !== "string" || !v.centerId || v.centerId.length > 512 ||
      typeof v.createdAt !== "string" || !Number.isFinite(Date.parse(v.createdAt))) throw new Error("Invalid backup metadata");
  return { version: 1, sourceUrl: v.sourceUrl, centerId: v.centerId, createdAt: v.createdAt };
}
function checkPassword(password: string): void {
  if (password.length < 12 || password.length > 1024) throw new Error("Backup password must contain 12–1024 characters");
}
export async function sealCenterBackup(data: Buffer, password: string, info: BackupMetadata): Promise<Buffer> {
  checkPassword(password);
  if (data.length > MAX_CENTER_BACKUP || data.subarray(0, 4).toString("hex") !== "504b0304") throw new Error("Invalid center snapshot");
  const raw = Buffer.from(JSON.stringify(metadata(info)));
  const size = Buffer.alloc(4); size.writeUInt32BE(raw.length);
  const salt = randomBytes(16), nonce = randomBytes(12);
  const header = Buffer.concat([magic, size, raw, salt, nonce]);
  const key = await derive(password, salt, 32) as Buffer;
  try {
    const cipher = createCipheriv("aes-256-gcm", key, nonce); cipher.setAAD(header);
    return Buffer.concat([header, cipher.update(data), cipher.final(), cipher.getAuthTag()]);
  } finally { key.fill(0); }
}
export async function openCenterBackup(file: Buffer, password: string): Promise<{ data: Buffer; metadata: BackupMetadata }> {
  checkPassword(password);
  if (file.length > MAX_CENTER_BACKUP + 8192 || file.length < magic.length + 48 || !file.subarray(0, magic.length).equals(magic)) throw new Error("Select a Multica Desktop backup (.multica-backup)");
  const size = file.readUInt32BE(magic.length);
  const offset = magic.length + 4 + size;
  if (size > 4096 || file.length < offset + 44) throw new Error("Invalid or incomplete backup");
  const salt = file.subarray(offset, offset + 16), nonce = file.subarray(offset + 16, offset + 28);
  const key = await derive(password, salt, 32) as Buffer;
  try {
    const decipher = createDecipheriv("aes-256-gcm", key, nonce);
    decipher.setAAD(file.subarray(0, offset + 28)); decipher.setAuthTag(file.subarray(-16));
    const data = Buffer.concat([decipher.update(file.subarray(offset + 28, -16)), decipher.final()]);
    return { data, metadata: metadata(JSON.parse(file.subarray(magic.length + 4, offset).toString("utf8"))) };
  } catch { throw new Error("Incorrect backup password or damaged backup"); }
  finally { key.fill(0); }
}
