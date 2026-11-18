// @vitest-environment node
import { execFileSync } from "node:child_process";
import { chmodSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cmdQuote, openInTerminal, posixScript, shQuote, windowsScript } from "./terminal-launch";

let dir: string;
beforeEach(() => { dir = mkdtempSync(join(tmpdir(), "term-")); });
afterEach(() => rmSync(dir, { recursive: true, force: true }));

describe("terminal scripts", () => {
  it("quotes every argument for sh, so the command runs exactly as argv", () => {
    expect(shQuote("it's")).toBe(`'it'\\''s'`);
    const script = join(dir, "s.sh");
    // Replace the trailing `read` so the script runs non-interactively here.
    writeFileSync(script, posixScript(["printf", "%s|", "a b", "it's", "$HOME", "`id`"], "t").replace("read _", ":"));
    chmodSync(script, 0o700);
    expect(execFileSync("sh", [script]).toString()).toContain("a b|it's|$HOME|`id`|");
  });

  it("writes a cmd script with quoted arguments", () => {
    expect(cmdQuote("C:\\Program Files\\Docker\\docker.exe")).toBe('"C:\\Program Files\\Docker\\docker.exe"');
    expect(windowsScript(["docker.exe", "run", "-it", "alpine"], "alpine")).toContain("docker.exe run -it alpine\r\n");
  });
});

describe("openInTerminal", () => {
  it("macOS: opens a .command file with its default terminal", async () => {
    const openPath = vi.fn(async () => "");
    await openInTerminal(["/usr/local/bin/docker", "exec", "-it", "web", "sh"], "web sh", { platform: "darwin", env: {}, openPath, dir });
    const file = openPath.mock.calls[0]![0] as string;
    expect(file).toMatch(/\.command$/);
    expect(readFileSync(file, "utf8")).toContain("'/usr/local/bin/docker' 'exec' '-it' 'web' 'sh'");
    openPath.mockResolvedValueOnce("No application knows how to open it.");
    await expect(openInTerminal(["x"], "t", { platform: "darwin", env: {}, openPath, dir })).rejects.toThrow("No application");
  });

  it("Linux: uses $TERMINAL, else the first emulator on PATH", async () => {
    const bin = join(dir, "bin");
    execFileSync("mkdir", [bin]);
    for (const name of ["konsole", "xterm"]) { writeFileSync(join(bin, name), ""); chmodSync(join(bin, name), 0o755); }
    const spawnDetached = vi.fn();
    await openInTerminal(["podman", "run", "-it", "alpine"], "alpine", { platform: "linux", env: { PATH: bin }, openPath: vi.fn(), spawnDetached, dir });
    expect(spawnDetached.mock.calls[0]![0]).toBe(join(bin, "konsole"));
    expect(spawnDetached.mock.calls[0]![1].slice(0, 2)).toEqual(["-e", "sh"]);
    await openInTerminal(["podman"], "t", { platform: "linux", env: { PATH: bin, TERMINAL: "xterm" }, openPath: vi.fn(), spawnDetached, dir });
    expect(spawnDetached.mock.calls[1]![0]).toBe(join(bin, "xterm"));
    await expect(openInTerminal(["podman"], "t", { platform: "linux", env: { PATH: join(dir, "none") }, openPath: vi.fn(), spawnDetached, dir }))
      .rejects.toThrow("No terminal emulator found");
  });

  it("Windows: prefers Windows Terminal", async () => {
    const bin = join(dir, "win");
    execFileSync("mkdir", [bin]);
    writeFileSync(join(bin, "wt.exe"), "");
    chmodSync(join(bin, "wt.exe"), 0o755);
    const spawnDetached = vi.fn();
    await openInTerminal(["docker.exe", "run", "-it", "alpine"], "alpine", { platform: "win32", env: { PATH: bin }, openPath: vi.fn(), spawnDetached, dir });
    expect(spawnDetached.mock.calls[0]![0]).toBe(join(bin, "wt.exe"));
    expect(spawnDetached.mock.calls[0]![1]).toEqual(expect.arrayContaining(["new-tab", "cmd", "/c"]));
    await openInTerminal(["docker.exe"], "t", { platform: "win32", env: { PATH: join(dir, "none") }, openPath: vi.fn(), spawnDetached, dir });
    expect(spawnDetached.mock.calls[1]!.slice(0, 1)).toEqual(["cmd.exe"]);
  });
});
