// @vitest-environment node
import { describe, expect, it } from "vitest";
import { openCenterBackup, sealCenterBackup } from "./center-backup-file";
const data = Buffer.from("504b030468656c6c6f", "hex");
const metadata = { version: 1 as const, sourceUrl: "https://old.example", centerId: "old-center", createdAt: "2026-09-28T00:00:00Z" };
const password = "a portable backup password";
describe("portable center backups", () => {
  it("round trips without a machine-specific key", async () => {
    const sealed = await sealCenterBackup(data, password, metadata);
    expect(sealed.includes(data)).toBe(false);
    const opened = await openCenterBackup(sealed, password);
    expect(opened.data).toEqual(data); expect(opened.metadata).toEqual(metadata);
  });
  it("rejects the wrong password, tampering, truncation and malformed files", async () => {
    const sealed = await sealCenterBackup(data, password, metadata);
    await expect(openCenterBackup(sealed, "a wrong backup password")).rejects.toThrow("Incorrect");
    for (const file of [sealed.subarray(0, 40), Buffer.from("not a backup"), Buffer.from(sealed)]) {
      if (file.length === sealed.length) file[file.length - 1] ^= 1;
      await expect(openCenterBackup(file, password)).rejects.toThrow();
    }
    const tamperedHeader = Buffer.from(sealed); const offset = tamperedHeader.indexOf("old-center"); tamperedHeader[offset] ^= 1;
    await expect(openCenterBackup(tamperedHeader, password)).rejects.toThrow("Incorrect");
  });
});
