import { z } from "zod";
import { parseWithFallback } from "../api/schema";

/**
 * Apps space: the applications installed on this computer, grouped by category.
 * Desktop main builds the list; this is the renderer contract.
 *
 * `category` is one of APP_CATEGORY_KEYS, or `folder:<name>` for a Windows
 * Start Menu folder (shown as-is).
 */
export const APP_CATEGORY_KEYS = [
  "window", "developer", "productivity", "internet", "social", "graphics", "media", "utilities",
  "system", "games", "education", "business", "reference", "news", "lifestyle", "other",
] as const;
export type AppCategoryKey = (typeof APP_CATEGORY_KEYS)[number];

export const installedAppSchema = z.object({
  id: z.string(),
  name: z.string(),
  path: z.string(),
  category: z.string().default("other"),
  version: z.string().default(""),
  bundleId: z.string().default(""),
  /** data: URL of the app icon, when the OS provides one. */
  icon: z.string().nullable().default(null),
  /** "service": a background command-line tool (e.g. yabai); opening it opens its config file. */
  kind: z.enum(["app", "service"]).catch("app").default("app"),
  /** Config file opened for a service. */
  config: z.string().nullable().default(null),
  /** Whether "Show keymappings" has something to show. */
  keymap: z.boolean().default(false),
});

export const keymapBindingSchema = z.object({
  keys: z.string(),
  raw: z.string(),
  command: z.string(),
  /** Set when the binding only applies in one app. */
  app: z.string().nullable().default(null),
});
export const appKeymapSchema = z.object({
  source: z.string(),
  sections: z.array(z.object({ title: z.string(), bindings: z.array(keymapBindingSchema) })).default([]),
});
export type KeymapBinding = z.infer<typeof keymapBindingSchema>;
export type AppKeymap = z.infer<typeof appKeymapSchema>;

export const APP_SOURCES = ["homebrew-cask", "homebrew-formula", "app-store", "system", "manual", "start-menu", "desktop-entry"] as const;

export const appIntroSchema = z.object({
  id: z.string(),
  name: z.string(),
  version: z.string().default(""),
  category: z.string().default("other"),
  kind: z.enum(["app", "service"]).catch("app").default("app"),
  path: z.string(),
  bundleId: z.string().default(""),
  /** One-line description (Homebrew, or a Linux .desktop Comment), when one exists. */
  description: z.string().nullable().default(null),
  homepage: z.string().nullable().default(null),
  source: z.enum(APP_SOURCES).catch("manual").default("manual"),
  /** e.g. the Homebrew cask or formula name. */
  sourceDetail: z.string().default(""),
  copyright: z.string().nullable().default(null),
  addedAt: z.string().nullable().default(null),
  lastUsedAt: z.string().nullable().default(null),
  config: z.string().nullable().default(null),
});
export type AppIntro = z.infer<typeof appIntroSchema>;

export function parseAppIntro(value: unknown): AppIntro {
  const parsed = parseWithFallback<AppIntro | null>(value, appIntroSchema, null, { endpoint: "local-apps:intro" });
  if (!parsed) throw new Error("Invalid app intro");
  return parsed;
}

export function parseAppKeymap(value: unknown): AppKeymap {
  const parsed = parseWithFallback<AppKeymap | null>(value, appKeymapSchema, null, { endpoint: "local-apps:keymap" });
  if (!parsed) throw new Error("Invalid keymap");
  return parsed;
}

/** Sections whose bindings match QUERY (keys, raw keys, command, section title or app). */
export function filterKeymap(keymap: AppKeymap, query: string): AppKeymap["sections"] {
  const q = query.trim().toLowerCase();
  if (!q) return keymap.sections;
  return keymap.sections.flatMap((section) => {
    const hit = section.title.toLowerCase().includes(q);
    const bindings = hit ? section.bindings : section.bindings.filter((b) =>
      [b.keys, b.raw, b.command, b.app ?? ""].some((v) => v.toLowerCase().includes(q)));
    return bindings.length ? [{ ...section, bindings }] : [];
  });
}

export const installedAppCatalogSchema = z.object({
  platform: z.string(),
  apps: z.array(installedAppSchema).default([]),
});

export type InstalledApp = z.infer<typeof installedAppSchema>;
export type InstalledAppCatalog = z.infer<typeof installedAppCatalogSchema>;

/** Window bridge exposed by Desktop preload as `desktopAPI.apps`. */
export interface LocalAppsBridge {
  list(): Promise<unknown>;
  /** Launch an app by id from the last list(). */
  launch(id: string): Promise<unknown>;
  /** Show the app (bundle / shortcut / .desktop file) in Finder or Explorer. */
  reveal(id: string): Promise<unknown>;
  /** Parsed key bindings for an app whose `keymap` is true. */
  keymap(id: string): Promise<unknown>;
  /** What the app is: description, source, dates (see AppIntro). */
  intro(id: string): Promise<unknown>;
}

export function parseInstalledApps(value: unknown): InstalledAppCatalog {
  const parsed = parseWithFallback<InstalledAppCatalog | null>(value, installedAppCatalogSchema, null, { endpoint: "local-apps:list" });
  if (!parsed) throw new Error("Invalid app list");
  return parsed;
}

export function isKnownAppCategory(category: string): category is AppCategoryKey {
  return (APP_CATEGORY_KEYS as readonly string[]).includes(category);
}

export interface AppGroup { category: string; apps: InstalledApp[] }

/**
 * Apps matching SEARCH (name or bundle id), grouped by category: known
 * categories in APP_CATEGORY_KEYS order with "other" last, then Windows
 * folders alphabetically.
 */
export function groupInstalledApps(apps: readonly InstalledApp[], search = ""): AppGroup[] {
  const q = search.trim().toLowerCase();
  const groups = new Map<string, InstalledApp[]>();
  for (const app of apps) {
    if (q && !app.name.toLowerCase().includes(q) && !app.bundleId.toLowerCase().includes(q)) continue;
    const list = groups.get(app.category) ?? [];
    list.push(app);
    groups.set(app.category, list);
  }
  const rank = (c: string) => {
    if (c === "other") return 10_000;
    const known = APP_CATEGORY_KEYS.indexOf(c as AppCategoryKey);
    return known >= 0 ? known : 1_000;
  };
  return [...groups]
    .sort(([a], [b]) => rank(a) - rank(b) || a.localeCompare(b))
    .map(([category, list]) => ({ category, apps: list.sort((x, y) => x.name.localeCompare(y.name)) }));
}
