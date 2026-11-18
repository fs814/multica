import { z } from "zod";
import { parseWithFallback } from "../api/schema";

/**
 * Containers space: the container engines on this computer (Docker / OrbStack,
 * Podman, nerdctl) and their containers, images, volumes and networks.
 * Desktop main runs the CLI and normalizes every engine's output into these
 * shapes (apps/desktop/src/main/container-engine.ts); this is the renderer contract.
 */

export const ENGINE_KINDS = ["docker", "podman", "nerdctl"] as const;
export type EngineKind = (typeof ENGINE_KINDS)[number];
export const RESOURCE_KINDS = ["containers", "images", "volumes", "networks"] as const;
export type ResourceKind = (typeof RESOURCE_KINDS)[number];
export const CONTAINER_ACTIONS = ["start", "stop", "restart", "pause", "unpause", "kill"] as const;
export type ContainerAction = (typeof CONTAINER_ACTIONS)[number];
export const CONTAINER_STATES = ["running", "paused", "restarting", "created", "exited", "removing", "dead", "unknown"] as const;
export type ContainerState = (typeof CONTAINER_STATES)[number];

export const engineInfoSchema = z.object({
  kind: z.enum(ENGINE_KINDS),
  path: z.string(),
  version: z.string().nullable().default(null),
  running: z.boolean(),
  error: z.string().nullable().default(null),
});
export type EngineInfo = z.infer<typeof engineInfoSchema>;

export const engineStatusSchema = z.object({
  engines: z.array(engineInfoSchema).default([]),
  active: z.enum(ENGINE_KINDS).nullable().default(null),
});
export type EngineStatus = z.infer<typeof engineStatusSchema>;

export const containerSchema = z.object({
  id: z.string(),
  name: z.string().default(""),
  image: z.string().default(""),
  state: z.enum(CONTAINER_STATES).catch("unknown"),
  status: z.string().default(""),
  ports: z.string().default(""),
  createdAt: z.string().default(""),
  command: z.string().default(""),
  project: z.string().nullable().default(null),
  networks: z.array(z.string()).default([]),
});
export type ContainerInfo = z.infer<typeof containerSchema>;

export const imageSchema = z.object({
  id: z.string(),
  repository: z.string().default("<none>"),
  tag: z.string().default("<none>"),
  ref: z.string(),
  size: z.string().default(""),
  createdAt: z.string().default(""),
  containers: z.number().nullable().default(null),
});
export type ImageInfo = z.infer<typeof imageSchema>;

export const volumeSchema = z.object({
  name: z.string(),
  driver: z.string().default(""),
  mountpoint: z.string().default(""),
  createdAt: z.string().default(""),
});
export type VolumeInfo = z.infer<typeof volumeSchema>;

export const networkSchema = z.object({
  id: z.string().default(""),
  name: z.string(),
  driver: z.string().default(""),
  scope: z.string().default(""),
  internal: z.boolean().default(false),
});
export type NetworkInfo = z.infer<typeof networkSchema>;

export interface ResourceRows {
  containers: ContainerInfo[];
  images: ImageInfo[];
  volumes: VolumeInfo[];
  networks: NetworkInfo[];
}

const listSchemas = {
  containers: z.array(containerSchema),
  images: z.array(imageSchema),
  volumes: z.array(volumeSchema),
  networks: z.array(networkSchema),
} as const;

export function parseEngineStatus(value: unknown): EngineStatus {
  return parseWithFallback<EngineStatus>(value, engineStatusSchema, { engines: [], active: null }, { endpoint: "local-containers:status" });
}

export function parseResourceList<K extends ResourceKind>(resource: K, value: unknown): ResourceRows[K] {
  return parseWithFallback<ResourceRows[K]>(value, listSchemas[resource], [] as unknown as ResourceRows[K], { endpoint: `local-containers:list:${resource}` });
}

export interface RunRequest {
  image: string;
  /** Open in a terminal (`run -it`) rather than in the background (`run -d`). */
  interactive: boolean;
  /** --rm */
  remove: boolean;
  name?: string;
  ports?: string[];
  env?: string[];
  volumes?: string[];
  command?: string[];
}

/** Desktop-only: runs the local container engine's CLI. Every argument is validated in main. */
export interface LocalContainersBridge {
  status(): Promise<unknown>;
  useEngine(kind: EngineKind): Promise<unknown>;
  /** podman machine start, or open OrbStack / Docker Desktop. */
  startEngine(kind: EngineKind): Promise<unknown>;
  list(resource: ResourceKind): Promise<unknown>;
  containerAction(id: string, action: ContainerAction): Promise<unknown>;
  removeContainer(id: string, force: boolean): Promise<unknown>;
  renameContainer(id: string, name: string): Promise<unknown>;
  logs(id: string, tail: number, timestamps: boolean): Promise<unknown>;
  inspect(resource: ResourceKind, id: string): Promise<unknown>;
  /** `exec -it <id> <shell>` in the system terminal. */
  exec(id: string, shell: string): Promise<unknown>;
  /** `start -ai <id>` in the system terminal: start a stopped container interactively. */
  attach(id: string): Promise<unknown>;
  /** Interactive: opens a terminal, resolves true. Background: resolves the new container id. */
  run(options: RunRequest): Promise<unknown>;
  pull(ref: string): Promise<unknown>;
  remove(resource: Exclude<ResourceKind, "containers">, id: string, force: boolean): Promise<unknown>;
  prune(resource: ResourceKind, all: boolean): Promise<unknown>;
}

// -- helpers for the page -----------------------------------------------------

export const isRunning = (c: ContainerInfo) => c.state === "running" || c.state === "restarting";
export const isPaused = (c: ContainerInfo) => c.state === "paused";

/** Case-insensitive match on every text field of a row. */
export function matchesQuery(row: object, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  return Object.values(row).some((v) => (Array.isArray(v) ? v.join(" ") : String(v ?? "")).toLowerCase().includes(q));
}

/** Containers grouped by compose project, ungrouped ones last; running first within a group, then by name. */
export function groupContainers(rows: ContainerInfo[]): { project: string | null; containers: ContainerInfo[] }[] {
  const groups = new Map<string | null, ContainerInfo[]>();
  for (const row of rows) groups.set(row.project, [...(groups.get(row.project) ?? []), row]);
  const order = (c: ContainerInfo) => (isRunning(c) ? 0 : isPaused(c) ? 1 : 2);
  return [...groups.entries()]
    .sort(([a], [b]) => (a === null ? 1 : b === null ? -1 : a.localeCompare(b)))
    .map(([project, containers]) => ({ project, containers: [...containers].sort((a, b) => order(a) - order(b) || a.name.localeCompare(b.name)) }));
}

/** Shell-like split of the run dialog's command field: quotes group words, backslash escapes. */
export function splitCommand(text: string): string[] {
  const out: string[] = [];
  let current = "";
  let quote: "'" | '"' | null = null;
  let started = false;
  for (let i = 0; i < text.length; i++) {
    const ch = text[i]!;
    if (quote) {
      if (ch === quote) quote = null;
      else if (ch === "\\" && quote === '"' && i + 1 < text.length) current += text[++i];
      else current += ch;
    } else if (ch === "'" || ch === '"') {
      quote = ch;
      started = true;
    } else if (ch === "\\" && i + 1 < text.length) {
      current += text[++i];
      started = true;
    } else if (/\s/.test(ch)) {
      if (started || current) out.push(current);
      current = "";
      started = false;
    } else {
      current += ch;
      started = true;
    }
  }
  if (quote) throw new Error("Unclosed quote");
  if (started || current) out.push(current);
  return out;
}

/** One entry per non-empty line (or comma-separated on one line) for ports / env / volumes. */
export function splitEntries(text: string, commas = true): string[] {
  return text.split(commas ? /[\n,]/ : /\n/).map((s) => s.trim()).filter(Boolean);
}

/** Short id for display (Docker's 12-character convention), without a "sha256:" prefix. */
export const shortId = (id: string) => id.replace(/^sha256:/, "").slice(0, 12);
