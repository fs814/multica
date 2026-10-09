import { z } from "zod";
import { parseWithFallback } from "../api/schema";

export const toolScriptSchema = z.object({ path: z.string(), name: z.string() });
export const toolCatalogSchema = z.object({
  root: z.string(),
  platform: z.string(),
  scripts: z.array(toolScriptSchema).default([]),
});
export const toolRunSchema = z.object({
  id: z.string(),
  path: z.string(),
  status: z.enum(["running", "succeeded", "failed", "stopped"]).catch("failed"),
  startedAt: z.string(),
  finishedAt: z.string().nullable().default(null),
  exitCode: z.number().nullable().default(null),
  pid: z.number().nullable().default(null),
  output: z.string().default(""),
  truncated: z.boolean().default(false),
});
export type ToolScript = z.infer<typeof toolScriptSchema>;
export type ToolCatalog = z.infer<typeof toolCatalogSchema>;
export type ToolRun = z.infer<typeof toolRunSchema>;

export interface LocalToolsBridge {
  catalog(): Promise<unknown>;
  runs(): Promise<unknown>;
  run(path: string): Promise<unknown>;
  output(id: string): Promise<unknown>;
  stop(id: string): Promise<unknown>;
}

export function parseToolCatalog(value: unknown): ToolCatalog {
  const parsed = parseWithFallback<ToolCatalog | null>(value, toolCatalogSchema, null, { endpoint: "local-tools:catalog" });
  if (!parsed) throw new Error("Invalid local script catalog");
  return parsed;
}

export function parseToolRun(value: unknown): ToolRun {
  const parsed = parseWithFallback<ToolRun | null>(value, toolRunSchema, null, { endpoint: "local-tools:run" });
  if (!parsed) throw new Error("Invalid local script run");
  return parsed;
}

export function parseToolRuns(value: unknown): ToolRun[] {
  const parsed = parseWithFallback<ToolRun[] | null>(value, z.array(toolRunSchema), null, { endpoint: "local-tools:runs" });
  if (!parsed) throw new Error("Invalid local script runs");
  return parsed;
}

export interface ToolFolder {
  name: string;
  path: string;
  folders: ToolFolder[];
  scripts: ToolScript[];
}

export function buildToolTree(scripts: ToolScript[], search = ""): ToolFolder {
  const root: ToolFolder = { name: "", path: "", folders: [], scripts: [] };
  for (const script of scripts) {
    if (!script.path.toLowerCase().includes(search.trim().toLowerCase())) continue;
    const parts = script.path.split("/");
    parts.pop();
    let folder = root;
    for (const name of parts) {
      const path = folder.path ? `${folder.path}/${name}` : name;
      let next = folder.folders.find((child) => child.path === path);
      if (!next) {
        next = { name, path, folders: [], scripts: [] };
        folder.folders.push(next);
      }
      folder = next;
    }
    folder.scripts.push(script);
  }
  function sort(folder: ToolFolder) {
    folder.folders.sort((a, b) => a.name.localeCompare(b.name));
    folder.scripts.sort((a, b) => a.name.localeCompare(b.name));
    folder.folders.forEach(sort);
  }
  sort(root);
  return root;
}
