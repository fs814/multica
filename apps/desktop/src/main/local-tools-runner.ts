import { spawn, type ChildProcess } from "node:child_process";
import { randomUUID } from "node:crypto";
import { open, readdir, realpath, stat } from "node:fs/promises";
import { homedir } from "node:os";
import { basename, dirname, extname, isAbsolute, join, relative, resolve, sep, win32 } from "node:path";
import type { ToolCatalog, ToolRun } from "@multica/core/tools";

const OUTPUT_LIMIT = 128 * 1024;
const RUN_LIMIT = 100;
const CONCURRENCY_LIMIT = 8;
const SKIP_DIRECTORIES = new Set(["node_modules"]);

export function defaultToolsRoot(platform: string, home: string, settingsDirectory?: string): string {
  const paths = platform === "win32" ? win32 : { join };
  const settings = settingsDirectory || (platform === "win32" ? "C:\\sourcecode\\Settings" : join(home, "sourcecode", "Settings"));
  return paths.join(settings, platform === "darwin" ? "macbuild" : platform === "win32" ? "winbuild" : "linuxbuild");
}

function supportedScript(path: string, platform: string): boolean {
  return (platform === "win32" ? [".ps1", ".bat", ".cmd"] : [".sh", ".bash", ".zsh"]).includes(extname(path).toLowerCase());
}

function contained(root: string, path: string): boolean {
  const rel = relative(root, path);
  return !!rel && !isAbsolute(rel) && rel !== ".." && !rel.startsWith(`..${sep}`);
}

export function scriptCommand(platform: string, path: string, firstLine: string): { command: string; args: string[] } {
  const extension = extname(path).toLowerCase();
  if (platform === "win32") {
    if (extension === ".ps1") return { command: "powershell.exe", args: ["-NoLogo", "-NoProfile", "-NonInteractive", "-File", path] };
    // cmd expands these even inside quotes. Reject them rather than treating a
    // filename from the catalog as shell syntax.
    if (/["%!?&|<>^\r\n]/.test(path)) throw new Error("This batch script path contains unsupported command characters");
    return { command: "cmd.exe", args: ["/d", "/s", "/c", `""${path}""`] };
  }
  const shebang = firstLine.match(/^#!\s*(\S+)(?:\s+(.*))?$/);
  let shell = extension === ".zsh" ? "zsh" : "bash";
  if (shebang) {
    shell = basename(shebang[1]!);
    if (shell === "env") shell = shebang[2]?.trim().split(/\s+/)[0] ?? "";
    if (!["sh", "bash", "zsh"].includes(shell)) throw new Error("Unsupported script interpreter");
  }
  return { command: `/bin/${shell}`, args: [path] };
}

interface ActiveRun { run: ToolRun; child: ChildProcess; stopping: boolean; killTimer?: ReturnType<typeof setTimeout> }

export class LocalToolsRunner {
  readonly root: string;
  private closing = false;
  private readonly active = new Map<string, ActiveRun>();
  private readonly history = new Map<string, ToolRun>();
  private readonly starting = new Set<string>();

  constructor(
    private readonly platform = process.platform,
    root = defaultToolsRoot(platform, homedir(), process.env.MULTICA_SETTINGS_DIR),
  ) { this.root = resolve(root); }

  async catalog(): Promise<ToolCatalog> {
    const root = await realpath(this.root);
    const scripts: ToolCatalog["scripts"] = [];
    const walk = async (directory: string): Promise<void> => {
      for (const entry of await readdir(directory, { withFileTypes: true })) {
        if (entry.name.startsWith(".") || entry.isSymbolicLink()) continue;
        const path = join(directory, entry.name);
        if (entry.isDirectory() && !SKIP_DIRECTORIES.has(entry.name)) await walk(path);
        else if (entry.isFile() && supportedScript(path, this.platform)) {
          scripts.push({ path: relative(root, path).split(sep).join("/"), name: entry.name });
        }
      }
    };
    await walk(root);
    scripts.sort((a, b) => a.path.localeCompare(b.path));
    return { root: this.root, platform: this.platform, scripts };
  }

  runs(): ToolRun[] {
    return [...this.history.values()].reverse().map((run) => ({ ...run, output: "" }));
  }

  output(id: string): ToolRun {
    const run = this.history.get(id);
    if (!run) throw new Error("Script run no longer exists");
    return { ...run };
  }

  async run(path: string): Promise<ToolRun> {
    if (this.closing) throw new Error("Tools is shutting down");
    if (typeof path !== "string" || !path || path.includes("\\") || path.includes("\0") || path.split("/").some((part) => !part || part === "." || part === "..") || isAbsolute(path)) {
      throw new Error("Invalid script path");
    }
    const existing = [...this.active.values()].find((entry) => entry.run.path === path);
    if (existing) return { ...existing.run };
    if (this.starting.has(path)) throw new Error("Script is already starting");
    if (this.active.size + this.starting.size >= CONCURRENCY_LIMIT) throw new Error("Too many scripts are running");
    this.starting.add(path);
    try {
      const root = await realpath(this.root);
      const script = await realpath(resolve(root, path));
      if (!contained(root, script) || !supportedScript(script, this.platform) || !(await stat(script)).isFile()) throw new Error("Script is outside the Tools directory or unsupported");
      // Require catalog membership, including its symlink and directory rules.
      if (!(await this.catalog()).scripts.some((entry) => entry.path === path)) throw new Error("Script is not in the Tools catalog");
      const handle = await open(script, "r");
      let firstLine: string;
      try {
        const buffer = Buffer.alloc(1024);
        const { bytesRead } = await handle.read(buffer, 0, buffer.length, 0);
        firstLine = buffer.subarray(0, bytesRead).toString("utf8").split(/\r?\n/)[0] ?? "";
      } finally { await handle.close(); }
      const command = scriptCommand(this.platform, script, firstLine);
      const run: ToolRun = {
        id: randomUUID(), path, status: "running", startedAt: new Date().toISOString(),
        finishedAt: null, exitCode: null, pid: null, output: "", truncated: false,
      };
      if (this.closing) throw new Error("Tools is shutting down");
      const child = spawn(command.command, command.args, {
        cwd: dirname(script), env: process.env, shell: false,
        detached: this.platform !== "win32", windowsHide: true,
        windowsVerbatimArguments: this.platform === "win32" && extname(script).toLowerCase() !== ".ps1",
        stdio: ["ignore", "pipe", "pipe"],
      });
      run.pid = child.pid ?? null;
      const entry: ActiveRun = { run, child, stopping: false };
      this.active.set(run.id, entry);
      this.history.set(run.id, run);
      const append = (chunk: string) => {
        run.output += chunk;
        if (run.output.length > OUTPUT_LIMIT) {
          run.output = run.output.slice(-OUTPUT_LIMIT);
          run.truncated = true;
        }
      };
      child.stdout?.setEncoding("utf8").on("data", append);
      child.stderr?.setEncoding("utf8").on("data", append);
      const finish = (status: ToolRun["status"], code: number | null) => {
        if (run.status !== "running") return;
        run.status = status;
        run.exitCode = code;
        run.finishedAt = new Date().toISOString();
        if (entry.killTimer) clearTimeout(entry.killTimer);
        this.active.delete(run.id);
      };
      child.on("error", (error) => { append(`${error.message}\n`); finish("failed", null); });
      child.on("close", (code) => finish(entry.stopping ? "stopped" : code === 0 ? "succeeded" : "failed", code));
      for (const [id, previous] of this.history) {
        if (this.history.size <= RUN_LIMIT) break;
        if (previous.status !== "running") this.history.delete(id);
      }
      return { ...run };
    } finally { this.starting.delete(path); }
  }

  async stop(id: string): Promise<ToolRun> {
    const entry = this.active.get(id);
    if (!entry) return this.output(id);
    if (entry.stopping) return this.output(id);
    const pid = entry.child.pid;
    if (!pid) return this.output(id);
    entry.stopping = true;
    try {
      if (this.platform === "win32") {
        await new Promise<void>((resolveStop, reject) => {
          const kill = spawn("taskkill.exe", ["/pid", String(pid), "/T", "/F"], { windowsHide: true });
          kill.on("error", reject);
          kill.on("close", (code) => code === 0 ? resolveStop() : reject(new Error("Unable to stop script process tree")));
        });
      } else {
        process.kill(-pid, "SIGTERM");
        entry.killTimer = setTimeout(() => {
          try { process.kill(-pid, "SIGKILL"); } catch { /* Process already exited. */ }
        }, 3000);
        entry.killTimer.unref();
      }
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== "ESRCH") {
        entry.stopping = false;
        throw error;
      }
    }
    return this.output(id);
  }

  async stopAll(): Promise<void> {
    this.closing = true;
    const entries = [...this.active.values()];
    // Wait for close before allowing Electron to exit, so the escalation timer
    // can also terminate children that ignore SIGTERM.
    const closed = entries.map(({ child }) => new Promise<void>((done) => {
      child.once("close", () => done());
    }));
    await Promise.all(entries.map(({ run }) => this.stop(run.id)));
    await Promise.all(closed);
  }
}
