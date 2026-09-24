// @vitest-environment node
import { mkdtemp, stat, rm } from "fs/promises";
import { join } from "path";
import { tmpdir } from "os";
import { expect, it } from "vitest";
import { defaultLocalIssueDirectory, resolveLocalIssueDirectory } from "./local-issue-directory";

it("provisions the profile-owned default for empty or whitespace input", async () => {
  const root = await mkdtemp(join(tmpdir(), "multica-local-directory-"));
  try {
    const directory = await resolveLocalIssueDirectory("  ", root);
    expect(directory).toBe(defaultLocalIssueDirectory(root));
    expect((await stat(directory)).isDirectory()).toBe(true);
    expect(await resolveLocalIssueDirectory("", root)).toBe(directory);
  } finally { await rm(root, { recursive: true, force: true }); }
});

it("preserves explicit directories without creating them", async () => {
  const root = await mkdtemp(join(tmpdir(), "multica-local-directory-"));
  try {
    const explicit = join(root, "explicit");
    expect(await resolveLocalIssueDirectory(` ${explicit} `, root)).toBe(explicit);
    await expect(stat(explicit)).rejects.toThrow();
  } finally { await rm(root, { recursive: true, force: true }); }
});
