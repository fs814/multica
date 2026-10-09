// @vitest-environment node
import { afterEach, describe, expect, it } from "vitest";
import { mkdtemp, mkdir, rm, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { LocalToolsRunner, defaultToolsRoot, scriptCommand } from "./local-tools-runner";

const directories: string[] = [];
const runners: LocalToolsRunner[] = [];
async function fixture() {
  const dir = await mkdtemp(join(tmpdir(), "multica-tools-"));
  directories.push(dir);
  const runner = new LocalToolsRunner(process.platform, dir);
  runners.push(runner);
  return { dir, runner };
}
afterEach(async () => {
  await Promise.all(runners.splice(0).map((runner) => runner.stopAll()));
  await Promise.all(directories.splice(0).map((dir) => rm(dir, { recursive: true, force: true })));
});

describe("Tools platform selection", () => {
  it("selects the requested platform directory", () => {
    expect(defaultToolsRoot("darwin", "/Users/fs814")).toBe("/Users/fs814/sourcecode/Settings/macbuild");
    expect(defaultToolsRoot("linux", "/home/alice")).toBe("/home/alice/sourcecode/Settings/linuxbuild");
    expect(defaultToolsRoot("win32", "C:\\Users\\alice")).toBe("C:\\sourcecode\\Settings\\winbuild");
    expect(defaultToolsRoot("win32", "", "D:\\Settings")).toBe("D:\\Settings\\winbuild");
  });
  it("preserves shell choice and passes filenames as arguments", () => {
    expect(scriptCommand("darwin", "/scripts/my script.sh", "#!/bin/zsh")).toEqual({ command: "/bin/zsh", args: ["/scripts/my script.sh"] });
    expect(scriptCommand("linux", "/scripts/run.sh", "#!/usr/bin/env bash").command).toBe("/bin/bash");
    expect(scriptCommand("win32", "C:\\a b\\run.ps1", "").args).toEqual(["-NoLogo", "-NoProfile", "-NonInteractive", "-File", "C:\\a b\\run.ps1"]);
    expect(() => scriptCommand("win32", "C:\\%PATH%\\run.cmd", "")).toThrow();
  });
});

describe.skipIf(process.platform === "win32")("Tools local execution", () => {
  it("lists only supported scripts and rejects traversal and symlink escapes", async () => {
    const { dir, runner } = await fixture();
    await mkdir(join(dir, "nested"));
    await writeFile(join(dir, "nested", "run.sh"), "#!/bin/sh\nexit 0\n");
    await writeFile(join(dir, "readme.md"), "not a script");
    await writeFile(join(dir, "run.ps1"), "not for this platform");
    const outside = await mkdtemp(join(tmpdir(), "multica-tools-outside-"));
    directories.push(outside);
    await writeFile(join(outside, "outside.sh"), "exit 0");
    await symlink(join(outside, "outside.sh"), join(dir, "escape.sh"));
    expect((await runner.catalog()).scripts.map((script) => script.path)).toEqual(["nested/run.sh"]);
    await expect(runner.run("../outside.sh")).rejects.toThrow();
    await expect(runner.run(join(dir, "nested/run.sh"))).rejects.toThrow();
    await expect(runner.run("escape.sh")).rejects.toThrow();
  });
  it("reports success, failure, working directory and both output streams", async () => {
    const { dir, runner } = await fixture();
    await writeFile(join(dir, "success.sh"), "#!/bin/sh\npwd\nprintf 'hello\\n'\nprintf 'stderr\\n' >&2\n");
    const success = await runner.run("success.sh");
    await expect.poll(() => runner.output(success.id).status).toBe("succeeded");
    expect(runner.output(success.id)).toMatchObject({ exitCode: 0, output: expect.stringContaining("hello") });
    expect(runner.output(success.id).output).toContain("stderr");
    expect(runner.output(success.id).output).toContain(dir.replace(/^\/var\//, "/private/var/"));
    await writeFile(join(dir, "fail.sh"), "#!/bin/sh\nexit 7\n");
    const failure = await runner.run("fail.sh");
    await expect.poll(() => runner.output(failure.id).status).toBe("failed");
    expect(runner.output(failure.id).exitCode).toBe(7);
  });
  it("keeps running state outside the view, prevents duplicates and stops the process group", async () => {
    const { dir, runner } = await fixture();
    await writeFile(join(dir, "wait.sh"), "#!/bin/sh\nsleep 60 &\nwait\n");
    const run = await runner.run("wait.sh");
    expect((await runner.run("wait.sh")).id).toBe(run.id);
    expect(runner.runs()[0]).toMatchObject({ status: "running", path: "wait.sh" });
    await runner.stop(run.id);
    await expect.poll(() => runner.output(run.id).status).toBe("stopped");
  });
  it("waits for shutdown and rejects further launches", async () => {
    const { dir, runner } = await fixture();
    await writeFile(join(dir, "shutdown.sh"), "#!/bin/sh\nsleep 60 &\nwait\n");
    const run = await runner.run("shutdown.sh");
    await runner.stopAll();
    expect(runner.output(run.id).status).toBe("stopped");
    await expect(runner.run("shutdown.sh")).rejects.toThrow("shutting down");
  });
  it("bounds retained output", async () => {
    const { dir, runner } = await fixture();
    await writeFile(join(dir, "output.sh"), "#!/bin/sh\nhead -c 150000 /dev/zero\n");
    const run = await runner.run("output.sh");
    await expect.poll(() => runner.output(run.id).status).toBe("succeeded");
    expect(runner.output(run.id).truncated).toBe(true);
    expect(runner.output(run.id).output.length).toBe(128 * 1024);
    expect(runner.runs()[0]?.output).toBe("");
  });
});
