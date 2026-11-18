import { spawn } from "node:child_process";
import { accessSync, constants, realpathSync, statSync } from "node:fs";
import { delimiter, join } from "node:path";

/**
 * Containers space, main-process side: finds the container engines on this
 * computer (Docker / OrbStack, Podman, nerdctl), and runs their CLI.
 *
 * Every engine-specific output is normalized here into the shapes in
 * @multica/core/containers, so the renderer never sees a CLI format. Docker
 * and nerdctl print one JSON object per line (`--format '{{json .}}'`);
 * Podman prints a JSON array (`--format json`) with different field names.
 *
 * Nothing here builds a shell command line: arguments are passed as argv to
 * the CLI, and every value coming from the renderer is validated first.
 */

export const ENGINE_KINDS = ["docker", "podman", "nerdctl"] as const;
export type EngineKind = (typeof ENGINE_KINDS)[number];
export const RESOURCE_KINDS = ["containers", "images", "volumes", "networks"] as const;
export type ResourceKind = (typeof RESOURCE_KINDS)[number];
export const CONTAINER_ACTIONS = ["start", "stop", "restart", "pause", "unpause", "kill"] as const;
export type ContainerAction = (typeof CONTAINER_ACTIONS)[number];

export interface ExecResult { code: number; stdout: string; stderr: string; output: string }
/** Runs a program with argv (no shell). `output` is stdout and stderr interleaved in arrival order. */
export type Exec = (file: string, args: string[], opts: { timeoutMs: number; env?: NodeJS.ProcessEnv }) => Promise<ExecResult>;

export interface EngineInfo {
  kind: EngineKind;
  path: string;
  version: string | null;
  running: boolean;
  /** Why it is not running (first line of the CLI's error), when known. */
  error: string | null;
}

export interface ContainerRow {
  id: string;
  name: string;
  image: string;
  state: string;
  status: string;
  ports: string;
  createdAt: string;
  command: string;
  /** docker compose / podman-compose project, from the container labels. */
  project: string | null;
  networks: string[];
}

export interface ImageRow {
  id: string;
  repository: string;
  tag: string;
  /** What to pass to `run` / `rmi`: repo:tag, or the id for untagged images. */
  ref: string;
  size: string;
  createdAt: string;
  containers: number | null;
}

export interface VolumeRow { name: string; driver: string; mountpoint: string; createdAt: string }
export interface NetworkRow { id: string; name: string; driver: string; scope: string; internal: boolean }

export interface RunOptions {
  image: string;
  /** Open an interactive terminal (`run -it`) instead of starting in the background (`run -d`). */
  interactive: boolean;
  /** --rm: remove the container when it exits. */
  remove: boolean;
  name?: string;
  ports?: string[];
  env?: string[];
  volumes?: string[];
  command?: string[];
}

// -- validation -------------------------------------------------------------

/** Container / image / volume / network identifiers and image references; no leading dash (option injection). */
const REF = /^[A-Za-z0-9][A-Za-z0-9_.:/@+-]{0,254}$/;
const NAME = /^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$/;
const PORT = /^((\d{1,3}\.){3}\d{1,3}:)?(\d{1,5}(-\d{1,5})?:)?\d{1,5}(-\d{1,5})?(\/(tcp|udp|sctp))?$/;
const ENV = /^[A-Za-z_][A-Za-z0-9_]*=[^\0\r\n]*$/;
const VOLUME = /^([A-Za-z]:)?[^\0\r\n:]+:[^\0\r\n:]+(:(ro|rw|z|Z|ro,z|ro,Z))?$/;
const SHELL = /^[A-Za-z0-9_./-]{1,64}$/;

export function validRef(value: unknown, what = "id"): string {
  if (typeof value !== "string" || !REF.test(value)) throw new Error(`Invalid ${what}`);
  return value;
}

function validList(values: unknown, pattern: RegExp, what: string, max = 32): string[] {
  if (values === undefined || values === null) return [];
  if (!Array.isArray(values) || values.length > max) throw new Error(`Invalid ${what}`);
  return values.map((v) => {
    if (typeof v !== "string" || !pattern.test(v.trim())) throw new Error(`Invalid ${what}: ${String(v).slice(0, 80)}`);
    return v.trim();
  });
}

/** Characters cmd.exe would interpret inside the interactive-terminal script. */
const WINDOWS_UNSAFE = /["%^&|<>]/;

export function validRunOptions(value: unknown, platform: NodeJS.Platform): RunOptions {
  if (!value || typeof value !== "object") throw new Error("Invalid run options");
  const o = value as Record<string, unknown>;
  const image = validRef(o.image, "image");
  if (o.name !== undefined && o.name !== "" && (typeof o.name !== "string" || !NAME.test(o.name))) throw new Error("Invalid container name");
  const command = o.command === undefined ? [] : (() => {
    if (!Array.isArray(o.command) || o.command.length > 32) throw new Error("Invalid command");
    return o.command.map((part) => {
      if (typeof part !== "string" || part.length === 0 || part.length > 512 || /[\0\r\n]/.test(part)) throw new Error("Invalid command");
      return part;
    });
  })();
  const options: RunOptions = {
    image,
    interactive: o.interactive === true,
    remove: o.remove === true,
    name: typeof o.name === "string" && o.name ? o.name : undefined,
    ports: validList(o.ports, PORT, "port mapping"),
    env: validList(o.env, ENV, "environment variable"),
    volumes: validList(o.volumes, VOLUME, "volume"),
    command,
  };
  if (platform === "win32" && options.interactive) {
    for (const part of [...command, ...options.env!, ...options.volumes!]) {
      if (WINDOWS_UNSAFE.test(part)) throw new Error(`Unsupported character in: ${part.slice(0, 80)}`);
    }
  }
  return options;
}

export function validShell(value: unknown): string {
  if (typeof value !== "string" || !SHELL.test(value)) throw new Error("Invalid shell");
  return value;
}

// -- argv builders (pure, tested) ---------------------------------------------

export function runArgs(o: RunOptions): string[] {
  const args = ["run", o.interactive ? "-it" : "-d"];
  if (o.remove) args.push("--rm");
  if (o.name) args.push("--name", o.name);
  for (const p of o.ports ?? []) args.push("-p", p);
  for (const e of o.env ?? []) args.push("-e", e);
  for (const v of o.volumes ?? []) args.push("-v", v);
  args.push(o.image, ...(o.command ?? []));
  return args;
}

export function listArgs(kind: EngineKind, resource: ResourceKind): string[] {
  const json = kind === "podman" ? ["--format", "json"] : ["--format", "{{json .}}"];
  switch (resource) {
    case "containers": return ["ps", "-a", "--no-trunc", ...json];
    case "images": return kind === "podman" ? ["images", ...json] : ["images", "--no-trunc", ...json];
    case "volumes": return ["volume", "ls", ...json];
    case "networks": return kind === "podman" ? ["network", "ls", ...json] : ["network", "ls", "--no-trunc", ...json];
  }
}

// -- output normalization ---------------------------------------------------

/** A JSON array (Podman) or one JSON object per line (Docker, nerdctl). */
export function parseJsonRecords(text: string): Record<string, unknown>[] {
  const trimmed = text.trim();
  if (!trimmed) return [];
  if (trimmed.startsWith("[")) {
    const parsed = JSON.parse(trimmed) as unknown;
    return Array.isArray(parsed) ? (parsed.filter((x) => x && typeof x === "object") as Record<string, unknown>[]) : [];
  }
  return trimmed.split(/\r?\n/).filter((l) => l.trim()).map((l) => JSON.parse(l) as Record<string, unknown>);
}

const str = (v: unknown): string => (typeof v === "string" ? v : typeof v === "number" ? String(v) : "");
const unixToIso = (v: unknown): string => (typeof v === "number" && v > 0 ? new Date(v * 1000).toISOString() : "");

/** Docker labels are "k=v,k2=v2"; Podman's are an object. */
function labels(v: unknown): Record<string, string> {
  if (v && typeof v === "object" && !Array.isArray(v)) {
    return Object.fromEntries(Object.entries(v as Record<string, unknown>).map(([k, x]) => [k, str(x)]));
  }
  const out: Record<string, string> = {};
  for (const pair of str(v).split(",")) {
    const i = pair.indexOf("=");
    if (i > 0) out[pair.slice(0, i)] = pair.slice(i + 1);
  }
  return out;
}

function podmanPorts(v: unknown): string {
  if (!Array.isArray(v)) return str(v);
  return v.map((p) => {
    const port = p as Record<string, unknown>;
    const proto = str(port.protocol) || "tcp";
    const range = Number(port.range) > 1 ? `-${Number(port.container_port) + Number(port.range) - 1}` : "";
    const host = port.host_port ? `${str(port.host_ip) || "0.0.0.0"}:${port.host_port}${range ? `-${Number(port.host_port) + Number(port.range) - 1}` : ""}->` : "";
    return `${host}${port.container_port}${range}/${proto}`;
  }).join(", ");
}

const KNOWN_STATES = ["running", "paused", "exited", "created", "restarting", "removing", "dead"];

function stateOf(state: string, status: string): string {
  const s = state.toLowerCase();
  if (KNOWN_STATES.includes(s)) return s;
  if (s === "stopped" || s === "configured") return s === "stopped" ? "exited" : "created";
  // Old Docker has no State column: derive it from Status ("Up 3 minutes (Paused)", "Exited (0) ...").
  const st = status.toLowerCase();
  if (st.startsWith("up")) return st.includes("paused") ? "paused" : "running";
  if (st.startsWith("exited")) return "exited";
  if (st.startsWith("created")) return "created";
  if (st.startsWith("restarting")) return "restarting";
  return s || "unknown";
}

export function normalizeContainers(kind: EngineKind, records: Record<string, unknown>[]): ContainerRow[] {
  return records.map((r) => {
    if (kind === "podman") {
      const lab = labels(r.Labels);
      const names = Array.isArray(r.Names) ? r.Names.map(str) : [str(r.Names)];
      return {
        id: str(r.Id || r.ID),
        name: names[0] ?? "",
        image: str(r.Image),
        state: stateOf(str(r.State), str(r.Status)),
        status: str(r.Status),
        ports: podmanPorts(r.Ports),
        createdAt: str(r.CreatedAt) || unixToIso(r.Created),
        command: Array.isArray(r.Command) ? r.Command.map(str).join(" ") : str(r.Command),
        project: lab["com.docker.compose.project"] || lab["io.podman.compose.project"] || (str(r.PodName) || null),
        networks: Array.isArray(r.Networks) ? r.Networks.map(str) : [],
      };
    }
    const lab = labels(r.Labels);
    return {
      id: str(r.ID),
      name: str(r.Names).split(",")[0] ?? "",
      image: str(r.Image),
      state: stateOf(str(r.State), str(r.Status)),
      status: str(r.Status),
      ports: str(r.Ports),
      createdAt: str(r.CreatedAt),
      command: str(r.Command).replace(/^"|"$/g, ""),
      project: lab["com.docker.compose.project"] || null,
      networks: str(r.Networks).split(",").filter(Boolean),
    };
  });
}

function humanSize(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return "";
  const units = ["B", "kB", "MB", "GB", "TB"];
  let n = bytes;
  let i = 0;
  while (n >= 1000 && i < units.length - 1) { n /= 1000; i++; }
  return `${n >= 100 || i === 0 ? Math.round(n) : n.toFixed(1)}${units[i]}`;
}

export function normalizeImages(kind: EngineKind, records: Record<string, unknown>[]): ImageRow[] {
  if (kind === "podman") {
    return records.flatMap((r) => {
      const id = str(r.Id || r.ID);
      const tags = Array.isArray(r.RepoTags) && r.RepoTags.length ? r.RepoTags.map(str) : (Array.isArray(r.Names) && r.Names.length ? r.Names.map(str) : []);
      const base = {
        id, size: typeof r.Size === "number" ? humanSize(r.Size) : str(r.Size),
        createdAt: str(r.CreatedAt) || unixToIso(r.Created),
        containers: typeof r.Containers === "number" ? r.Containers : null,
      };
      if (!tags.length) return [{ ...base, repository: "<none>", tag: "<none>", ref: id }];
      return tags.map((t) => {
        const i = t.lastIndexOf(":");
        const repository = i > t.lastIndexOf("/") ? t.slice(0, i) : t;
        const tag = i > t.lastIndexOf("/") ? t.slice(i + 1) : "latest";
        return { ...base, repository, tag, ref: t };
      });
    });
  }
  return records.map((r) => {
    const repository = str(r.Repository) || "<none>";
    const tag = str(r.Tag) || "<none>";
    const id = str(r.ID);
    const containers = Number.parseInt(str(r.Containers), 10);
    return {
      id, repository, tag,
      ref: repository !== "<none>" && tag !== "<none>" ? `${repository}:${tag}` : id,
      size: str(r.Size), createdAt: str(r.CreatedAt),
      containers: Number.isFinite(containers) ? containers : null,
    };
  });
}

export function normalizeVolumes(records: Record<string, unknown>[]): VolumeRow[] {
  return records.map((r) => ({ name: str(r.Name), driver: str(r.Driver), mountpoint: str(r.Mountpoint), createdAt: str(r.CreatedAt) }));
}

export function normalizeNetworks(kind: EngineKind, records: Record<string, unknown>[]): NetworkRow[] {
  return records.map((r) => (kind === "podman"
    ? { id: str(r.id || r.ID), name: str(r.name || r.Name), driver: str(r.driver || r.Driver), scope: str(r.Scope) || "local", internal: r.internal === true }
    : { id: str(r.ID), name: str(r.Name), driver: str(r.Driver), scope: str(r.Scope), internal: str(r.Internal) === "true" }));
}

/** First meaningful line of a CLI failure. */
export function cliError(result: ExecResult, fallback: string): Error {
  const line = (result.stderr || result.stdout).split(/\r?\n/).map((l) => l.trim()).find(Boolean);
  return new Error(line ? line.replace(/^Error(:| response from daemon:)\s*/i, "") : fallback);
}

// -- engine discovery and execution -----------------------------------------

export const defaultExec: Exec = (file, args, { timeoutMs, env }) => new Promise((resolve) => {
  const child = spawn(file, args, { env, windowsHide: true });
  let stdout = "";
  let stderr = "";
  let output = "";
  const timer = setTimeout(() => { child.kill(); }, timeoutMs);
  child.stdout.on("data", (d: Buffer) => { const s = d.toString(); stdout += s; output += s; });
  child.stderr.on("data", (d: Buffer) => { const s = d.toString(); stderr += s; output += s; });
  child.on("error", (error) => { clearTimeout(timer); resolve({ code: -1, stdout, stderr: stderr || error.message, output }); });
  child.on("close", (code) => { clearTimeout(timer); resolve({ code: code ?? -1, stdout, stderr: code === null && !stderr ? `Timed out after ${timeoutMs / 1000}s` : stderr, output }); });
});

export interface ContainerEngineOptions {
  platform: NodeJS.Platform;
  home: string;
  env: NodeJS.ProcessEnv;
  exec?: Exec;
  /** Extra directories to look in before the defaults (tests). */
  searchDirs?: string[];
}

/** Where engines install their CLI without always putting it on PATH. */
function extraDirs(platform: NodeJS.Platform, home: string): string[] {
  if (platform === "darwin") {
    return [
      join(home, ".orbstack/bin"), "/Applications/OrbStack.app/Contents/MacOS/xbin",
      "/opt/homebrew/bin", "/usr/local/bin", join(home, ".rd/bin"),
      "/Applications/Docker.app/Contents/Resources/bin", "/opt/podman/bin",
    ];
  }
  if (platform === "win32") {
    return [
      "C:\\Program Files\\Docker\\Docker\\resources\\bin", "C:\\Program Files\\RedHat\\Podman",
      join(home, "AppData\\Local\\Programs\\Rancher Desktop\\resources\\resources\\win32\\bin"),
    ];
  }
  return ["/usr/bin", "/usr/local/bin", join(home, ".local/bin"), join(home, ".rd/bin"), "/home/linuxbrew/.linuxbrew/bin"];
}

const TIMEOUT = { probe: 8_000, list: 30_000, action: 60_000, pull: 15 * 60_000, machine: 5 * 60_000 };

export class ContainerEngine {
  private readonly exec: Exec;
  private selected: EngineKind | null = null;
  private engines: EngineInfo[] | null = null;

  constructor(private readonly opts: ContainerEngineOptions) {
    this.exec = opts.exec ?? defaultExec;
  }

  /** Executable CLIs per engine, first match wins; dangling links (an uninstalled Docker.app) are skipped. */
  find(): { kind: EngineKind; path: string }[] {
    const { platform, home, env, searchDirs = [] } = this.opts;
    const dirs = [...searchDirs, ...(env.PATH ?? env.Path ?? "").split(delimiter).filter(Boolean), ...extraDirs(platform, home)];
    const found: { kind: EngineKind; path: string }[] = [];
    for (const kind of ENGINE_KINDS) {
      for (const dir of dirs) {
        const path = join(dir, platform === "win32" ? `${kind}.exe` : kind);
        try {
          accessSync(path, constants.X_OK);
          if (!statSync(realpathSync(path)).isFile()) continue;
          found.push({ kind, path });
          break;
        } catch {
          // not here
        }
      }
    }
    return found;
  }

  /** Probes every engine: version, and whether it answers (`ps -q`). */
  async status(): Promise<{ engines: EngineInfo[]; active: EngineKind | null }> {
    const engines = await Promise.all(this.find().map(async ({ kind, path }) => {
      const [version, probe] = await Promise.all([
        this.run(path, ["--version"], TIMEOUT.probe),
        this.run(path, ["ps", "-q"], TIMEOUT.probe),
      ]);
      return {
        kind, path,
        version: version.code === 0 ? version.stdout.trim().split(/\r?\n/)[0] ?? null : null,
        running: probe.code === 0,
        error: probe.code === 0 ? null : cliError(probe, "Not running").message,
      } satisfies EngineInfo;
    }));
    this.engines = engines;
    if (!this.selected || !engines.some((e) => e.kind === this.selected)) {
      this.selected = (engines.find((e) => e.running) ?? engines[0])?.kind ?? null;
    }
    return { engines, active: this.selected };
  }

  use(kind: EngineKind): void {
    if (!this.engines?.some((e) => e.kind === kind)) throw new Error(`${kind} is not installed`);
    this.selected = kind;
  }

  /** The active engine's CLI (probing on first use). */
  async active(): Promise<{ kind: EngineKind; path: string }> {
    if (!this.engines) await this.status();
    const engine = this.engines!.find((e) => e.kind === this.selected);
    if (!engine) throw new Error("No container engine found (install Docker, OrbStack, Podman or nerdctl)");
    return { kind: engine.kind, path: engine.path };
  }

  private run(path: string, args: string[], timeoutMs: number): Promise<ExecResult> {
    return this.exec(path, args, { timeoutMs, env: this.opts.env });
  }

  /** Runs the active engine's CLI; throws its error message on failure. */
  async cli(args: string[], timeoutMs = TIMEOUT.action): Promise<ExecResult> {
    const { path } = await this.active();
    const result = await this.run(path, args, timeoutMs);
    if (result.code !== 0) throw cliError(result, `${args[0]} failed`);
    return result;
  }

  async list(resource: ResourceKind): Promise<ContainerRow[] | ImageRow[] | VolumeRow[] | NetworkRow[]> {
    const { kind } = await this.active();
    const records = parseJsonRecords((await this.cli(listArgs(kind, resource), TIMEOUT.list)).stdout);
    switch (resource) {
      case "containers": return normalizeContainers(kind, records);
      case "images": return normalizeImages(kind, records);
      case "volumes": return normalizeVolumes(records);
      case "networks": return normalizeNetworks(kind, records);
    }
  }

  async containerAction(id: string, action: ContainerAction): Promise<void> {
    await this.cli([action, id]);
  }

  async removeContainer(id: string, force: boolean): Promise<void> {
    await this.cli(["rm", ...(force ? ["-f"] : []), id]);
  }

  async rename(id: string, name: string): Promise<void> {
    if (!NAME.test(name)) throw new Error("Invalid container name");
    await this.cli(["rename", id, name]);
  }

  async logs(id: string, tail: number, timestamps: boolean): Promise<string> {
    const result = await this.cli(["logs", "--tail", String(tail), ...(timestamps ? ["--timestamps"] : []), id], TIMEOUT.list);
    return result.output;
  }

  async inspect(resource: ResourceKind, id: string): Promise<string> {
    const sub = { containers: ["container", "inspect"], images: ["image", "inspect"], volumes: ["volume", "inspect"], networks: ["network", "inspect"] }[resource];
    const text = (await this.cli([...sub, id], TIMEOUT.list)).stdout;
    try {
      return JSON.stringify(JSON.parse(text), null, 2);
    } catch {
      return text;
    }
  }

  /** Background run (`run -d`): the new container's id. */
  async runDetached(options: RunOptions): Promise<string> {
    const result = await this.cli(runArgs({ ...options, interactive: false }), TIMEOUT.pull);
    return result.stdout.trim().split(/\r?\n/).pop() ?? "";
  }

  async pull(ref: string): Promise<string> {
    return (await this.cli(["pull", ref], TIMEOUT.pull)).output;
  }

  async remove(resource: Exclude<ResourceKind, "containers">, id: string, force: boolean): Promise<void> {
    const args = { images: ["rmi", ...(force ? ["-f"] : []), id], volumes: ["volume", "rm", ...(force ? ["-f"] : []), id], networks: ["network", "rm", id] }[resource];
    await this.cli(args);
  }

  /** Removes stopped containers / dangling (or, with all, unused) images / unused volumes / unused networks. */
  async prune(resource: ResourceKind, all: boolean): Promise<string> {
    const sub = { containers: "container", images: "image", volumes: "volume", networks: "network" }[resource];
    return (await this.cli([sub, "prune", "-f", ...(all && resource === "images" ? ["-a"] : [])], TIMEOUT.pull)).output;
  }

  /** argv for an interactive terminal: the CLI path first. */
  async terminalArgv(args: string[]): Promise<string[]> {
    const { path } = await this.active();
    return [path, ...args];
  }

  /** Podman: `podman machine start` (macOS / Windows VMs). Docker apps are opened by the caller. */
  async startPodmanMachine(): Promise<string> {
    const podman = this.find().find((e) => e.kind === "podman");
    if (!podman) throw new Error("podman is not installed");
    const result = await this.run(podman.path, ["machine", "start"], TIMEOUT.machine);
    if (result.code !== 0) throw cliError(result, "podman machine start failed");
    return result.output;
  }
}
