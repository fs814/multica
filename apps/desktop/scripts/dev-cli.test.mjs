import assert from "node:assert/strict";
import test from "node:test";
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync } from "node:fs";
import { basename, join } from "node:path";
import { tmpdir } from "node:os";
import { createDevCliPath, devCliSourceDigest } from "./dev-cli.mjs";

test("each dev launch gets an isolated path and never overwrites an existing binary", t => {
  const root = mkdtempSync(join(tmpdir(), "multica-dev-cli-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const first = createDevCliPath(root, "win32");
  writeFileSync(first, "running executable");
  const second = createDevCliPath(root, "win32");
  assert.notEqual(first, second);
  assert.equal(readFileSync(first, "utf8"), "running executable");
  assert.equal(first.startsWith(join(root, ".multica", "desktop-cli")), true);
  assert.equal(first.endsWith("multica.exe"), true);
  assert.equal(basename(createDevCliPath(root, "darwin")), "multica");
});

test("dirty Go and embedded source changes alter the version fingerprint, build output does not", t => {
  const root = mkdtempSync(join(tmpdir(), "multica-dev-source-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  writeFileSync(join(root, "main.go"), "original");
  const original = devCliSourceDigest(root);
  mkdirSync(join(root, "bin"));
  writeFileSync(join(root, "bin", "test.json"), "build output");
  assert.equal(devCliSourceDigest(root), original);
  writeFileSync(join(root, "main.go"), "updated");
  assert.notEqual(devCliSourceDigest(root), original);
  const changed = devCliSourceDigest(root);
  writeFileSync(join(root, "prompt.md"), "new prompt");
  assert.notEqual(devCliSourceDigest(root), changed);
});
