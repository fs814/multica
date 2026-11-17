import { z } from "zod";
import { parseWithFallback } from "../api/schema";

/**
 * Zotero space: documents kept by WeCom / WeChat chat history and the
 * Downloads folder, transferred into the local Zotero desktop library.
 * Desktop main does the file and Zotero work; this is the renderer contract.
 */

export const ZOTERO_SOURCES = ["wecom", "wechat", "downloads"] as const;
export type ZoteroSource = (typeof ZOTERO_SOURCES)[number];

export const ZOTERO_KINDS = ["pdf", "word", "slides", "sheet", "other"] as const;
export type ZoteroFileKind = (typeof ZOTERO_KINDS)[number];

export const zoteroRootSchema = z.object({
  source: z.enum(ZOTERO_SOURCES),
  account: z.string().default(""),
  path: z.string(),
});

export const zoteroFileSchema = z.object({
  id: z.string(),
  name: z.string(),
  ext: z.string(),
  size: z.number(),
  modifiedAt: z.string(),
  month: z.string(),
  source: z.enum(ZOTERO_SOURCES),
  account: z.string().default(""),
  path: z.string(),
  imported: z
    .object({ key: z.string(), collection: z.string().default(""), at: z.string() })
    .nullable()
    .default(null),
});

export const zoteroCatalogSchema = z.object({
  roots: z.array(zoteroRootSchema).default([]),
  files: z.array(zoteroFileSchema).default([]),
  errors: z.array(z.string()).default([]),
});

export const zoteroStatusSchema = z.object({
  state: z.enum(["ready", "needs_key", "disabled", "not_running"]).catch("not_running"),
  url: z.string(),
  message: z.string().default(""),
});

export const zoteroCollectionSchema = z.object({ key: z.string(), path: z.string() });

export const zoteroImportResultSchema = z.object({
  id: z.string(),
  status: z.enum(["imported", "already_imported", "failed"]).catch("failed"),
  key: z.string().nullable().default(null),
  collection: z.string().default(""),
  trashed: z.boolean().default(false),
  error: z.string().default(""),
});

export type ZoteroRoot = z.infer<typeof zoteroRootSchema>;
export type ZoteroFile = z.infer<typeof zoteroFileSchema>;
export type ZoteroCatalog = z.infer<typeof zoteroCatalogSchema>;
export type ZoteroStatus = z.infer<typeof zoteroStatusSchema>;
export type ZoteroCollection = z.infer<typeof zoteroCollectionSchema>;
export type ZoteroImportResult = z.infer<typeof zoteroImportResultSchema>;

/** Window bridge exposed by Desktop preload as `desktopAPI.zotero`. */
export interface LocalZoteroBridge {
  /** Scan WeCom, WeChat and Downloads for documents. */
  list(): Promise<unknown>;
  status(): Promise<unknown>;
  /** Ask Zotero for a local API write key; resolves when the user answers Zotero's dialog. */
  authorize(): Promise<unknown>;
  collections(): Promise<unknown>;
  /** Key of collection "A/B", created when missing. */
  ensureCollection(path: string): Promise<unknown>;
  /** Import one listed file (by id from the last list()) into a collection. */
  importFile(id: string, collectionKey: string, trashOriginal: boolean): Promise<unknown>;
  /** Show the file in Finder / Explorer. */
  reveal(id: string): Promise<unknown>;
  /** Preview the file: Quick Look on macOS, the default app elsewhere. */
  preview(id: string): Promise<unknown>;
  /** Move the file to the system Trash / Recycle Bin. */
  trash(id: string): Promise<unknown>;
  /** Rename the file in its folder; resolves to the updated file entry. */
  rename(id: string, name: string): Promise<unknown>;
}

function parse<T>(value: unknown, schema: z.ZodType, endpoint: string): T {
  const parsed = parseWithFallback<T | null>(value, schema, null, { endpoint });
  if (!parsed) throw new Error(`Invalid Zotero response (${endpoint})`);
  return parsed;
}

export const parseZoteroCatalog = (v: unknown) => parse<ZoteroCatalog>(v, zoteroCatalogSchema, "local-zotero:list");
export const parseZoteroStatus = (v: unknown) => parse<ZoteroStatus>(v, zoteroStatusSchema, "local-zotero:status");
export const parseZoteroCollections = (v: unknown) =>
  parse<ZoteroCollection[]>(v, z.array(zoteroCollectionSchema), "local-zotero:collections");
export const parseZoteroCollection = (v: unknown) =>
  parse<ZoteroCollection>(v, zoteroCollectionSchema, "local-zotero:ensure-collection");
export const parseZoteroFile = (v: unknown) => parse<ZoteroFile>(v, zoteroFileSchema, "local-zotero:rename");
export const parseZoteroImportResult = (v: unknown) =>
  parse<ZoteroImportResult>(v, zoteroImportResultSchema, "local-zotero:import");

const KIND_BY_EXT: Record<string, ZoteroFileKind> = {
  pdf: "pdf",
  doc: "word", docx: "word", rtf: "word", odt: "word", pages: "word", wps: "word",
  ppt: "slides", pptx: "slides", pps: "slides", ppsx: "slides", key: "slides", odp: "slides", dps: "slides",
  xls: "sheet", xlsx: "sheet", ods: "sheet", numbers: "sheet", et: "sheet", csv: "sheet",
};

export function zoteroFileKind(ext: string): ZoteroFileKind {
  return KIND_BY_EXT[ext.toLowerCase()] ?? "other";
}

export interface ZoteroFileFilter {
  source: ZoteroSource | "all";
  kind: ZoteroFileKind | "all";
  search: string;
  hideImported: boolean;
}

export function filterZoteroFiles(files: readonly ZoteroFile[], filter: ZoteroFileFilter): ZoteroFile[] {
  const q = filter.search.trim().toLowerCase();
  return files.filter(
    (f) =>
      (filter.source === "all" || f.source === filter.source) &&
      (filter.kind === "all" || zoteroFileKind(f.ext) === filter.kind) &&
      (!filter.hideImported || !f.imported) &&
      (!q || f.name.toLowerCase().includes(q)),
  );
}

export function countZoteroSources(files: readonly ZoteroFile[]): Record<ZoteroSource | "all", number> {
  const counts = { all: files.length, wecom: 0, wechat: 0, downloads: 0 };
  for (const f of files) counts[f.source] += 1;
  return counts;
}
