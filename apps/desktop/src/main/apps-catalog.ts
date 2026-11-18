import { execFile } from "node:child_process";
import { createHash } from "node:crypto";
import { readdir, readFile, stat } from "node:fs/promises";
import { basename, delimiter, extname, join, relative, sep } from "node:path";
import type { AppIntro, AppKeymap, InstalledApp, InstalledAppCatalog } from "@multica/core/apps";
import { parseSkhdrc } from "./keymaps";

/**
 * Apps space, main-process half: lists the applications installed on this
 * computer, grouped by category, and launches them.
 *
 * - macOS: `.app` bundles in /Applications (one folder deep), /System/Applications
 *   and ~/Applications; the category is the bundle's LSApplicationCategoryType.
 * - Windows: Start Menu shortcuts; the category is the Start Menu folder.
 * - Linux: freedesktop `.desktop` entries; the category comes from Categories=.
 *
 * Category keys are shared across platforms (see CATEGORY_KEYS) except
 * Windows folders, which come back as `folder:<name>`.
 */

export const CATEGORY_KEYS = [
  "window", "developer", "productivity", "internet", "social", "graphics", "media", "utilities",
  "system", "games", "education", "business", "reference", "news", "lifestyle", "other",
] as const;
type CategoryKey = (typeof CATEGORY_KEYS)[number];

const MAC_CATEGORIES: Record<string, CategoryKey> = {
  "developer-tools": "developer", productivity: "productivity", utilities: "utilities",
  "graphics-design": "graphics", photography: "graphics", music: "media", video: "media", entertainment: "media",
  "social-networking": "social", news: "news", reference: "reference", books: "reference",
  education: "education", business: "business", finance: "business",
  lifestyle: "lifestyle", "health-fitness": "lifestyle", medical: "lifestyle", travel: "lifestyle", sports: "lifestyle",
  "food-and-drink": "lifestyle", weather: "lifestyle", navigation: "lifestyle", shopping: "lifestyle", kids: "lifestyle",
};

/** For apps that declare no category. First match wins; matched against name and bundle id. */
const NAME_RULES: [RegExp, CategoryKey][] = [
  [/wechat|wework|企业微信|微信|\bqq\b|slack|telegram|discord|lark|feishu|飞书|dingtalk|钉钉|zoom|teams|whatsapp|signal|line\b/i, "social"],
  [/chrome|firefox|safari|edge|browser|opera|brave|vivaldi|quark|arc\b|clash|v2ray|vpn|proxy/i, "internet"],
  [/code|visual studio|powershell|stm32|cubemx|keil|arduino|platformio|xcode|terminal|iterm|warp|docker|git|jetbrains|idea|pycharm|webstorm|clion|goland|android studio|cursor|zed|claude|codex|copilot|postman|simulator|emacs|vim|qemu|utm|parallels|virtualbox|vmware/i, "developer"],
  [/office|word|excel|powerpoint|keynote|pages|numbers|notion|obsidian|zotero|evernote|onenote|wps|calendar|notes|reminders|mail|outlook/i, "productivity"],
  [/photo|pixelmator|sketch|figma|affinity|blender|gimp|inkscape|preview/i, "graphics"],
  [/music|spotify|vlc|iina|video|player|tv\b|podcast|audio/i, "media"],
  [/bitwarden|1password|keychain|password|uninstall|cleaner|finder|archive|unarchiver|zip|disk|backup|monitor/i, "utilities"],
];

function guessCategory(name: string, id: string): CategoryKey {
  for (const [re, key] of NAME_RULES) if (re.test(name) || re.test(id)) return key;
  return "other";
}

export function macCategory(lsType: string | undefined, name: string, id: string, system: boolean): CategoryKey {
  const short = (lsType ?? "").replace(/^public\.app-category\./, "");
  if (short) {
    if (short.endsWith("games")) return "games";
    const mapped = MAC_CATEGORIES[short];
    if (mapped) return mapped;
  }
  if (system) return "system";
  return guessCategory(name, id);
}

const LINUX_CATEGORIES: [string, CategoryKey][] = [
  ["InstantMessaging", "social"], ["Chat", "social"], ["Email", "productivity"], ["WebBrowser", "internet"],
  ["Development", "developer"], ["Office", "productivity"], ["Graphics", "graphics"], ["AudioVideo", "media"],
  ["Audio", "media"], ["Video", "media"], ["Network", "internet"], ["Game", "games"], ["Education", "education"],
  ["Science", "education"], ["Utility", "utilities"], ["Settings", "system"], ["System", "system"],
];

export function linuxCategory(categories: string, name: string, id: string): CategoryKey {
  const list = categories.split(";").filter(Boolean);
  for (const [freedesktop, key] of LINUX_CATEGORIES) if (list.includes(freedesktop)) return key;
  return guessCategory(name, id);
}

const run = (cmd: string, args: string[], maxBuffer = 4 << 20, timeout = 10_000) => new Promise<string>((resolve) => {
  execFile(cmd, args, { maxBuffer, timeout }, (error, stdout) => resolve(error ? "" : stdout));
});

/** The plain string value of KEY in an XML plist, or undefined. */
export function plistString(xml: string, key: string): string | undefined {
  const m = new RegExp(`<key>${key}</key>\\s*<string>([^<]*)</string>`).exec(xml);
  if (!m) return undefined;
  return m[1]!.replace(/&lt;/g, "<").replace(/&gt;/g, ">").replace(/&quot;/g, '"').replace(/&apos;/g, "'").replace(/&amp;/g, "&");
}

async function readPlist(path: string): Promise<string> {
  try {
    const head = await readFile(path);
    // Binary plists need plutil; XML ones are read directly.
    if (head.subarray(0, 6).toString("latin1") !== "bplist") return head.toString("utf8");
  } catch {
    return "";
  }
  return run("/usr/bin/plutil", ["-convert", "xml1", "-o", "-", path]);
}

async function children(dir: string): Promise<string[]> {
  try {
    return (await readdir(dir)).filter((n) => !n.startsWith(".")).map((n) => join(dir, n));
  } catch {
    return [];
  }
}

export const appId = (path: string) => createHash("sha1").update(path).digest("hex").slice(0, 16);

async function mapLimit<T, R>(items: T[], limit: number, fn: (item: T) => Promise<R>): Promise<R[]> {
  const out: R[] = new Array(items.length);
  let next = 0;
  await Promise.all(Array.from({ length: Math.min(limit, items.length) }, async () => {
    while (next < items.length) {
      const i = next++;
      out[i] = await fn(items[i]!);
    }
  }));
  return out;
}

export function macRoots(home: string): [string, boolean][] {
  return [
    ["/Applications", false], ["/Applications/Utilities", false], [join(home, "Applications"), false],
    ["/System/Applications", true], ["/System/Applications/Utilities", true],
  ];
}

async function macApps(roots: [string, boolean][]): Promise<InstalledApp[]> {
  const top = roots[0]?.[0];
  const bundles: [string, boolean][] = [];
  for (const [root, system] of roots) {
    for (const path of await children(root)) {
      if (path.endsWith(".app")) bundles.push([path, system]);
      else if (!system && root === top && basename(path) !== "Utilities") {
        // Suites installed as a folder of apps, e.g. /Applications/Microsoft Office/…
        for (const inner of await children(path)) if (inner.endsWith(".app")) bundles.push([inner, false]);
      }
    }
  }
  const apps = await mapLimit(bundles, 8, async ([path, system]): Promise<InstalledApp | null> => {
    const xml = await readPlist(join(path, "Contents", "Info.plist"));
    // Background-only helpers have no UI to open. Menu-bar apps (LSUIElement) stay: Launchpad lists them too.
    if (xml && /<key>LSBackgroundOnly<\/key>\s*(<true\/>|<string>1<\/string>)/.test(xml)) return null;
    const bundleId = (xml && plistString(xml, "CFBundleIdentifier")) || "";
    const fileName = basename(path, ".app");
    // The file name is what Finder and Launchpad show; it is localized more often than CFBundleName.
    const name = fileName;
    return {
      id: appId(path), name, path, category: macCategory(xml ? plistString(xml, "LSApplicationCategoryType") : undefined, name, bundleId, system),
      version: (xml && (plistString(xml, "CFBundleShortVersionString") || plistString(xml, "CFBundleVersion"))) || "",
      bundleId, icon: null, kind: "app", config: null, keymap: false,
    };
  });
  return apps.filter((a): a is InstalledApp => a !== null);
}

async function walk(dir: string, depth: number, out: string[], match: (p: string) => boolean): Promise<void> {
  let entries;
  try {
    entries = await readdir(dir, { withFileTypes: true });
  } catch {
    return;
  }
  for (const e of entries) {
    const p = join(dir, e.name);
    if (e.isDirectory() && depth > 0) await walk(p, depth - 1, out, match);
    else if (e.isFile() && match(p)) out.push(p);
  }
}

const SKIP_SHORTCUT = /uninstall|卸载|readme|help|documentation|website|license|release notes/i;

async function windowsApps(env: Record<string, string | undefined>): Promise<InstalledApp[]> {
  const roots = [
    env.ProgramData && join(env.ProgramData, "Microsoft", "Windows", "Start Menu", "Programs"),
    env.APPDATA && join(env.APPDATA, "Microsoft", "Windows", "Start Menu", "Programs"),
  ].filter((r): r is string => !!r);
  const seen = new Set<string>();
  const apps: InstalledApp[] = [];
  for (const root of roots) {
    const links: string[] = [];
    await walk(root, 3, links, (p) => [".lnk", ".url", ".appref-ms"].includes(extname(p).toLowerCase()));
    for (const path of links) {
      const name = basename(path, extname(path));
      if (SKIP_SHORTCUT.test(name) || seen.has(name.toLowerCase())) continue;
      seen.add(name.toLowerCase());
      const folder = relative(root, path).split(sep).slice(0, -1)[0];
      apps.push({
        id: appId(path), name, path, version: "", bundleId: "", icon: null, kind: "app", config: null, keymap: false,
        category: folder ? `folder:${folder}` : guessCategory(name, ""),
      });
    }
  }
  return apps;
}

/** Fields of the [Desktop Entry] group of a .desktop file. */
export function desktopEntry(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  let inEntry = false;
  for (const line of text.split(/\r?\n/)) {
    if (line.startsWith("[")) { inEntry = line.trim() === "[Desktop Entry]"; continue; }
    if (!inEntry) continue;
    const eq = line.indexOf("=");
    if (eq > 0) out[line.slice(0, eq).trim()] = line.slice(eq + 1).trim();
  }
  return out;
}

async function linuxApps(home: string, env: Record<string, string | undefined>, comments: Map<string, string>): Promise<InstalledApp[]> {
  const dirs = [
    join(home, ".local/share/applications"), "/usr/share/applications", "/usr/local/share/applications",
    "/var/lib/flatpak/exports/share/applications", join(home, ".local/share/flatpak/exports/share/applications"),
    "/var/lib/snapd/desktop/applications",
  ];
  const lang = (env.LANG ?? "").split(".")[0] ?? "";
  const seen = new Set<string>();
  const apps: InstalledApp[] = [];
  for (const dir of dirs) {
    const files: string[] = [];
    await walk(dir, 1, files, (p) => p.endsWith(".desktop"));
    for (const path of files) {
      const desktopId = basename(path);
      if (seen.has(desktopId)) continue; // earlier dirs (the user's) override later ones
      seen.add(desktopId);
      let entry: Record<string, string>;
      try {
        entry = desktopEntry(await readFile(path, "utf8"));
      } catch {
        continue;
      }
      if (entry.Type !== "Application" || entry.NoDisplay === "true" || entry.Hidden === "true") continue;
      const name = entry[`Name[${lang}]`] || entry[`Name[${lang.split("_")[0]}]`] || entry.Name || desktopId;
      const comment = entry[`Comment[${lang}]`] || entry[`Comment[${lang.split("_")[0]}]`] || entry.Comment || entry.GenericName;
      if (comment) comments.set(path, comment);
      apps.push({
        id: appId(path), name, path, version: entry.Version ?? "", bundleId: desktopId, icon: null, kind: "app", config: null, keymap: false,
        category: linuxCategory(entry.Categories ?? "", name, desktopId),
      });
    }
  }
  return apps;
}

/**
 * Command-line window-management services shown next to apps. Opening one
 * opens its config file; "Show keymappings" reads skhd's config, which is where
 * yabai's key bindings live.
 */
const MAC_SERVICES: { name: string; configs: string[] }[] = [
  { name: "yabai", configs: [".config/yabai/yabairc", ".yabairc"] },
  { name: "skhd", configs: [".config/skhd/skhdrc", ".skhdrc"] },
];
const SKHD_CONFIGS = [".config/skhd/skhdrc", ".skhdrc"];

const exists = (path: string) => stat(path).then(() => true, () => false);

async function firstExisting(paths: string[]): Promise<string | null> {
  for (const path of paths) if (await exists(path)) return path;
  return null;
}

export function serviceDirs(env: Record<string, string | undefined>): string[] {
  return [...new Set(["/opt/homebrew/bin", "/usr/local/bin", ...(env.PATH ?? "").split(delimiter).filter(Boolean)])];
}

async function macServices(home: string, dirs: string[]): Promise<InstalledApp[]> {
  const keymap = await firstExisting(SKHD_CONFIGS.map((c) => join(home, c)));
  const out: InstalledApp[] = [];
  for (const service of MAC_SERVICES) {
    const bin = await firstExisting(dirs.map((d) => join(d, service.name)));
    if (!bin) continue;
    const version = (await run(bin, ["--version"])).trim().replace(new RegExp(`^${service.name}-?v?`), "");
    out.push({
      id: appId(bin), name: service.name, path: bin, category: "window", version, bundleId: "", icon: null,
      kind: "service", config: await firstExisting(service.configs.map((c) => join(home, c))), keymap: keymap !== null,
    });
  }
  return out;
}

/** Descriptions of what Homebrew installed: casks keyed by their .app name, formulae by name. */
export interface BrewIndex {
  casks: Map<string, { token: string; desc: string | null; homepage: string | null }>;
  formulae: Map<string, { desc: string | null; homepage: string | null }>;
}

export function parseBrewInfo(json: string): BrewIndex {
  const index: BrewIndex = { casks: new Map(), formulae: new Map() };
  let data: { casks?: unknown[]; formulae?: unknown[] };
  try {
    data = JSON.parse(json);
  } catch {
    return index;
  }
  for (const raw of data.casks ?? []) {
    const cask = raw as { token?: string; desc?: string | null; homepage?: string | null; artifacts?: unknown[] };
    for (const artifact of cask.artifacts ?? []) {
      const apps = (artifact as { app?: unknown[] } | null)?.app;
      for (const app of Array.isArray(apps) ? apps : []) {
        if (typeof app === "string") index.casks.set(app, { token: cask.token ?? "", desc: cask.desc ?? null, homepage: cask.homepage ?? null });
      }
    }
  }
  for (const raw of data.formulae ?? []) {
    const formula = raw as { name?: string; desc?: string | null; homepage?: string | null };
    if (formula.name) index.formulae.set(formula.name, { desc: formula.desc ?? null, homepage: formula.homepage ?? null });
  }
  return index;
}

async function loadBrewIndex(): Promise<BrewIndex> {
  const brew = await firstExisting(["/opt/homebrew/bin/brew", "/usr/local/bin/brew", "/home/linuxbrew/.linuxbrew/bin/brew"]);
  if (!brew) return { casks: new Map(), formulae: new Map() };
  return parseBrewInfo(await run(brew, ["info", "--json=v2", "--installed"], 128 << 20, 60_000));
}

/** Spotlight's "date added" and "last opened" for a path, as ISO strings. */
async function spotlightDates(path: string): Promise<{ addedAt: string | null; lastUsedAt: string | null }> {
  const out = await run("/usr/bin/mdls", ["-raw", "-name", "kMDItemDateAdded", "-name", "kMDItemLastUsedDate", path]);
  const [added, used] = out.split("\0");
  const iso = (v: string | undefined) => {
    if (!v || v === "(null)") return null;
    const date = new Date(v.replace(" ", "T").replace(" +", "+").replace(/([+-]\d\d)(\d\d)$/, "$1:$2"));
    return Number.isNaN(date.getTime()) ? null : date.toISOString();
  };
  return { addedAt: iso(added), lastUsedAt: iso(used) };
}

export interface AppsCatalogOptions {
  platform: string;
  home: string;
  env: Record<string, string | undefined>;
  /** Data URL of the app's icon, or null. */
  iconFor?: (path: string) => Promise<string | null>;
  /** macOS folders to scan as [path, isSystem]; the first one is also searched one folder deep. */
  macRoots?: [string, boolean][];
  /** Folders searched for yabai / skhd binaries (default: Homebrew dirs plus PATH). */
  serviceDirs?: string[];
  /** Homebrew descriptions (default: `brew info --json=v2 --installed`, cached per list). */
  brewIndex?: () => Promise<BrewIndex>;
  /** Spotlight dates (default: mdls on macOS). */
  spotlight?: (path: string) => Promise<{ addedAt: string | null; lastUsedAt: string | null }>;
}

export class AppsCatalog {
  private apps = new Map<string, InstalledApp>();
  private readonly comments = new Map<string, string>();
  private brew: Promise<BrewIndex> | null = null;

  constructor(private readonly options: AppsCatalogOptions) {}

  async list(): Promise<InstalledAppCatalog> {
    const { platform, home, env, iconFor } = this.options;
    const found = platform === "darwin"
      ? [...await macApps(this.options.macRoots ?? macRoots(home)), ...await macServices(home, this.options.serviceDirs ?? serviceDirs(env))]
      : platform === "win32" ? await windowsApps(env)
      : await linuxApps(home, env, this.comments);
    if (iconFor) {
      await mapLimit(found.filter((a) => a.kind === "app"), 8, async (app) => {
        app.icon = await iconFor(app.path).catch(() => null);
      });
    }
    found.sort((a, b) => a.name.localeCompare(b.name));
    this.apps = new Map(found.map((a) => [a.id, a]));
    this.brew = null; // re-read Homebrew on the next intro after a refresh
    return { platform, apps: found };
  }

  pathOf(id: string): string {
    const app = this.apps.get(id);
    if (!app) throw new Error("This app is no longer listed; refresh the list");
    return app.path;
  }

  /**
   * What to open for this entry: the app itself, or a service's config file.
   * Checks it still exists, so a removed app gives a clear error.
   */
  async launchable(id: string): Promise<string> {
    const app = this.apps.get(id);
    if (app?.kind === "service") {
      if (!app.config) throw new Error(`No config file found for ${app.name}`);
      return app.config;
    }
    const path = this.pathOf(id);
    await stat(path).catch(() => {
      throw new Error("This app is no longer installed; refresh the list");
    });
    return path;
  }

  /** What an app is, from Homebrew, the bundle, Spotlight or the .desktop entry. */
  async intro(id: string): Promise<AppIntro> {
    const app = this.apps.get(id);
    if (!app) throw new Error("This app is no longer listed; refresh the list");
    const { platform } = this.options;
    const intro: AppIntro = {
      id: app.id, name: app.name, version: app.version, category: app.category, kind: app.kind, path: app.path,
      bundleId: app.bundleId, description: null, homepage: null, source: "manual", sourceDetail: "", copyright: null,
      addedAt: null, lastUsedAt: null, config: app.config,
    };
    if (platform === "win32") return { ...intro, source: "start-menu" };
    if (platform !== "darwin") return { ...intro, source: "desktop-entry", description: this.comments.get(app.path) ?? null };

    this.brew ??= (this.options.brewIndex ?? loadBrewIndex)().catch(() => ({ casks: new Map(), formulae: new Map() }));
    const brew = await this.brew;
    if (app.kind === "service") {
      const formula = brew.formulae.get(app.name);
      return formula
        ? { ...intro, description: formula.desc, homepage: formula.homepage, source: "homebrew-formula", sourceDetail: app.name }
        : intro;
    }
    const xml = await readPlist(join(app.path, "Contents", "Info.plist"));
    intro.copyright = (xml && plistString(xml, "NSHumanReadableCopyright")) || null;
    Object.assign(intro, await (this.options.spotlight ?? spotlightDates)(app.path).catch(() => ({})));
    const cask = brew.casks.get(basename(app.path));
    if (app.path.startsWith("/System/")) intro.source = "system";
    else if (cask) Object.assign(intro, { source: "homebrew-cask", sourceDetail: cask.token, description: cask.desc, homepage: cask.homepage });
    else if (await exists(join(app.path, "Contents", "_MASReceipt", "receipt"))) intro.source = "app-store";
    return intro;
  }

  /** Key bindings for an entry with `keymap` (yabai / skhd: skhd's config). */
  async keymap(id: string): Promise<AppKeymap> {
    const app = this.apps.get(id);
    if (!app) throw new Error("This app is no longer listed; refresh the list");
    if (!app.keymap) throw new Error(`No keymappings are known for ${app.name}`);
    const source = await firstExisting(SKHD_CONFIGS.map((c) => join(this.options.home, c)));
    if (!source) throw new Error("skhd's config file is gone; refresh the list");
    return parseSkhdrc(await readFile(source, "utf8"), source);
  }
}
