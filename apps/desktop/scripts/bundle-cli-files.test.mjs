import assert from "node:assert/strict";
import test from "node:test";
import { mkdtemp, readFile, writeFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { replaceBundledCli, shouldKeepBundledCli } from "./bundle-cli-files.mjs";

test("without Go an existing bundle is preserved, even if a stale source exists", () => {
  assert.equal(shouldKeepBundledCli(false, true), true);
  assert.equal(shouldKeepBundledCli(true, true), false);
  assert.equal(shouldKeepBundledCli(false, false), false);
});

test("replacement preserves an unrelated bundle resource", async (t) => {
  const dir = await mkdtemp(join(tmpdir(), "multica-bundle-"));
  t.after(() => rm(dir, { recursive: true, force: true }));
  const source = join(dir, "source");
  const destination = join(dir, "multica.exe");
  const other = join(dir, "keep.txt");
  await writeFile(source, "new");
  await writeFile(destination, "old");
  await writeFile(other, "keep");
  assert.equal(await replaceBundledCli(source, destination), true);
  assert.equal(await readFile(destination, "utf8"), "new");
  assert.equal(await readFile(other, "utf8"), "keep");
});

for (const allowLockedDestination of [false, true]) {
  test(`locked Windows destination is preserved (compile-only=${allowLockedDestination})`, async () => {
    const calls = [];
    const opts = {
      platform: "win32", allowLockedDestination,
      copy: async (...args) => calls.push(["copy", ...args]),
      move: async () => { throw Object.assign(new Error("locked"), { code: "EPERM" }); },
      remove: async (path) => calls.push(["remove", path]), warn: () => {},
    };
    if (allowLockedDestination) assert.equal(await replaceBundledCli("new", "multica.exe", opts), false);
    else await assert.rejects(replaceBundledCli("new", "multica.exe", opts), /existing binary was preserved/);
    assert.equal(calls.filter(([op]) => op === "remove").length, 1);
    assert.match(calls.at(-1)[1], /^multica\.exe\..*\.tmp$/);
  });
}

test("unexpected copy errors remain fatal and only the staged file is cleaned", async () => {
  const removed = [];
  await assert.rejects(replaceBundledCli("new", "multica.exe", {
    copy: async () => { throw new Error("disk full"); },
    remove: async (path) => removed.push(path),
  }), /disk full/);
  assert.match(removed[0], /\.tmp$/);
});
