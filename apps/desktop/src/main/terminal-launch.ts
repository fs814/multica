import { spawn } from "node:child_process";
import { accessSync, chmodSync, constants, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { delimiter, join } from "node:path";

/**
 * Opens a command in the user's own terminal (Containers space: `run -it`,
 * `exec -it`, `start -ai`). Multica has no embedded terminal, and these
 * commands need a real TTY.
 *
 * The command is written to a small script whose every argument is quoted,
 * then the script is opened:
 *   macOS    a .command file, opened by Launch Services: Terminal.app, or
 *            whatever terminal the user made the default for .command files
 *   Linux    $TERMINAL, then the usual terminal emulators
 *   Windows  Windows Terminal (wt.exe), else a new cmd window
 * After the command exits the window waits for Enter, so an error stays visible.
 */

/** POSIX single-quote quoting: safe for any byte except NUL. */
export function shQuote(arg: string): string {
  return `'${arg.replace(/'/g, `'\\''`)}'`;
}

/** cmd.exe quoting for arguments already checked against ["%^&|<>]. */
export function cmdQuote(arg: string): string {
  return /^[A-Za-z0-9_./:\\=@+-]+$/.test(arg) ? arg : `"${arg}"`;
}

export function posixScript(argv: string[], title: string): string {
  return [
    "#!/bin/sh",
    `printf '\\033]0;%s\\007' ${shQuote(title)}`,
    `printf '%s\\n\\n' ${shQuote(`$ ${argv.join(" ")}`)}`,
    argv.map(shQuote).join(" "),
    "status=$?",
    `printf '\\n[exited with %s] Press Enter to close.' "$status"`,
    "read _",
    "",
  ].join("\n");
}

export function windowsScript(argv: string[], title: string): string {
  return [
    "@echo off",
    `title ${title.replace(/[^A-Za-z0-9 _.:/-]/g, "")}`,
    argv.map(cmdQuote).join(" "),
    "echo.",
    "echo [exited with %errorlevel%] ",
    "pause",
    "",
  ].join("\r\n");
}

/** Terminal emulators tried on Linux, with how each takes "run this program". */
const LINUX_TERMINALS: [string, (script: string) => string[]][] = [
  ["x-terminal-emulator", (s) => ["-e", "sh", s]],
  ["gnome-terminal", (s) => ["--", "sh", s]],
  ["konsole", (s) => ["-e", "sh", s]],
  ["xfce4-terminal", (s) => ["-x", "sh", s]],
  ["kitty", (s) => ["sh", s]],
  ["alacritty", (s) => ["-e", "sh", s]],
  ["wezterm", (s) => ["start", "--", "sh", s]],
  ["foot", (s) => ["sh", s]],
  ["xterm", (s) => ["-e", "sh", s]],
];

function onPath(name: string, env: NodeJS.ProcessEnv): string | null {
  for (const dir of (env.PATH ?? env.Path ?? "").split(delimiter).filter(Boolean)) {
    const path = join(dir, name);
    try {
      accessSync(path, constants.X_OK);
      return path;
    } catch {
      // keep looking
    }
  }
  return null;
}

export interface TerminalDeps {
  platform: NodeJS.Platform;
  env: NodeJS.ProcessEnv;
  /** Opens a file with its default application (Electron's shell.openPath). */
  openPath: (path: string) => Promise<string>;
  spawnDetached?: (file: string, args: string[]) => void;
  dir?: string;
}

const detached = (file: string, args: string[]) => {
  spawn(file, args, { detached: true, stdio: "ignore", windowsHide: false }).unref();
};

export async function openInTerminal(argv: string[], title: string, deps: TerminalDeps): Promise<void> {
  const { platform, env, openPath, spawnDetached = detached, dir = tmpdir() } = deps;
  const stamp = `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
  if (platform === "win32") {
    const script = join(dir, `multica-${stamp}.cmd`);
    writeFileSync(script, windowsScript(argv, title));
    const wt = onPath("wt.exe", env);
    if (wt) spawnDetached(wt, ["new-tab", "--title", title.slice(0, 60), "cmd", "/c", script]);
    else spawnDetached("cmd.exe", ["/c", "start", "", "cmd", "/c", script]);
    return;
  }
  const script = join(dir, `multica-${stamp}.${platform === "darwin" ? "command" : "sh"}`);
  writeFileSync(script, posixScript(argv, title));
  chmodSync(script, 0o700);
  if (platform === "darwin") {
    const failure = await openPath(script);
    if (failure) throw new Error(failure);
    return;
  }
  const preferred = env.TERMINAL ? onPath(env.TERMINAL, env) ?? (env.TERMINAL.includes("/") ? env.TERMINAL : null) : null;
  if (preferred) {
    spawnDetached(preferred, ["-e", "sh", script]);
    return;
  }
  for (const [name, args] of LINUX_TERMINALS) {
    const path = onPath(name, env);
    if (path) {
      spawnDetached(path, args(script));
      return;
    }
  }
  throw new Error("No terminal emulator found (set $TERMINAL)");
}
