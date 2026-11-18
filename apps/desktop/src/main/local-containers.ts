import { existsSync } from "node:fs";
import { homedir } from "node:os";
import { ipcMain, shell, type BrowserWindow, type IpcMainInvokeEvent } from "electron";
import {
  CONTAINER_ACTIONS, ContainerEngine, ENGINE_KINDS, RESOURCE_KINDS,
  runArgs, validRef, validRunOptions, validShell, type ContainerAction, type EngineKind, type ResourceKind,
} from "./container-engine";
import { openInTerminal } from "./terminal-launch";

/** Desktop apps that run the Docker engine, tried in order when Docker is not running. */
function dockerApps(platform: NodeJS.Platform, home: string): string[] {
  if (platform === "darwin") return ["/Applications/OrbStack.app", `${home}/Applications/OrbStack.app`, "/Applications/Docker.app", "/Applications/Rancher Desktop.app"];
  if (platform === "win32") return ["C:\\Program Files\\Docker\\Docker\\Docker Desktop.exe"];
  return [];
}

function oneOf<T extends string>(values: readonly T[], value: unknown, what: string): T {
  if (typeof value !== "string" || !(values as readonly string[]).includes(value)) throw new Error(`Invalid ${what}`);
  return value as T;
}

export function setupLocalContainers(windowGetter: () => BrowserWindow | null, engine = new ContainerEngine({ platform: process.platform, home: homedir(), env: process.env })): void {
  function requireMainRenderer(event: IpcMainInvokeEvent) {
    const main = windowGetter()?.webContents;
    if (!main || event.sender !== main || event.senderFrame !== main.mainFrame) {
      throw new Error("Containers requires the main Desktop window");
    }
  }
  const handle = (channel: string, fn: (...args: unknown[]) => unknown) => {
    // Validation runs synchronously, so a bad argument throws before any CLI call.
    ipcMain.handle(`local-containers:${channel}`, (event, ...args: unknown[]) => {
      requireMainRenderer(event);
      return fn(...args);
    });
  };
  const terminal = async (args: string[], title: string) => {
    await openInTerminal(await engine.terminalArgv(args), title, { platform: process.platform, env: process.env, openPath: (p) => shell.openPath(p) });
    return true;
  };
  const tailOf = (value: unknown) => {
    const n = typeof value === "number" ? Math.floor(value) : NaN;
    if (!(n >= 1 && n <= 10_000)) throw new Error("Invalid line count");
    return n;
  };

  handle("status", () => engine.status());
  handle("use-engine", (kind) => {
    engine.use(oneOf(ENGINE_KINDS, kind, "engine"));
    return true;
  });
  handle("start-engine", (kind) => {
    const k: EngineKind = oneOf(ENGINE_KINDS, kind, "engine");
    if (k === "podman") return engine.startPodmanMachine();
    return (async () => {
      const appPath = dockerApps(process.platform, homedir()).find((p) => existsSync(p));
      if (!appPath) {
        throw new Error(process.platform === "linux"
          ? "Start the Docker service: sudo systemctl start docker"
          : "No Docker app found (install OrbStack or Docker Desktop)");
      }
      const failure = await shell.openPath(appPath);
      if (failure) throw new Error(failure);
      return `Opening ${appPath}`;
    })();
  });
  handle("list", (resource) => engine.list(oneOf(RESOURCE_KINDS, resource, "resource")));
  handle("container-action", (id, action) => {
    const ref = validRef(id);
    const a: ContainerAction = oneOf(CONTAINER_ACTIONS, action, "action");
    return engine.containerAction(ref, a).then(() => true);
  });
  handle("remove-container", (id, force) => {
    const ref = validRef(id);
    return engine.removeContainer(ref, force === true).then(() => true);
  });
  handle("rename-container", (id, name) => {
    const ref = validRef(id);
    if (typeof name !== "string") throw new Error("Invalid container name");
    return engine.rename(ref, name).then(() => true);
  });
  handle("logs", (id, tail, timestamps) => {
    const ref = validRef(id);
    return engine.logs(ref, tailOf(tail), timestamps === true);
  });
  handle("inspect", (resource, id) => {
    const r: ResourceKind = oneOf(RESOURCE_KINDS, resource, "resource");
    return engine.inspect(r, validRef(id));
  });
  handle("exec", (id, shellName) => {
    const ref = validRef(id);
    const sh = validShell(shellName);
    return terminal(["exec", "-it", ref, sh], `${ref.slice(0, 12)} ${sh}`);
  });
  handle("attach", (id) => {
    // Starts a stopped container with its TTY attached (`start -ai`), or attaches to a running one.
    const ref = validRef(id);
    return terminal(["start", "-ai", ref], ref.slice(0, 24));
  });
  handle("run", (options) => {
    const o = validRunOptions(options, process.platform);
    return o.interactive ? terminal(runArgs(o), o.image) : engine.runDetached(o);
  });
  handle("pull", (ref) => engine.pull(validRef(ref, "image")));
  handle("remove", (resource, id, force) => {
    const r = oneOf(["images", "volumes", "networks"] as const, resource, "resource");
    const ref = validRef(id);
    return engine.remove(r, ref, force === true).then(() => true);
  });
  handle("prune", (resource, all) => engine.prune(oneOf(RESOURCE_KINDS, resource, "resource"), all === true));
}
