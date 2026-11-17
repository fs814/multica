import { createHash, randomBytes } from "node:crypto";
import { createReadStream } from "node:fs";
import { mkdir, readFile, readdir, rename, stat, writeFile } from "node:fs/promises";
import { dirname, join, relative, sep } from "node:path";
import type {
  ZoteroCatalog, ZoteroCollection, ZoteroFile, ZoteroImportResult, ZoteroRoot, ZoteroSource, ZoteroStatus,
} from "@multica/core/zotero";

/**
 * Zotero space, main-process half: finds the documents WeCom / WeChat keep
 * from chat history plus those in Downloads, and imports chosen ones into the
 * local Zotero desktop library through its local API (Zotero 7.1+, port 23119).
 *
 * The renderer only ever passes ids from the last list(); paths never cross
 * the IPC boundary inbound. The local API write key is shared with
 * examples/mcp-servers/chat-files-mcp (~/.chat-files-mcp/zotero.json), so
 * authorizing either one authorizes both.
 */

export const DOC_EXTS = new Set(
  ("pdf caj kdh nh epub djvu ppt pptx pps ppsx key odp dps doc docx rtf odt pages wps " +
    "xls xlsx ods numbers et csv").split(" "),
);
const SKIP_DIRS = new Set(["node_modules", ".git", "__MACOSX"]);
const MAX_FILES = 20_000;
const MONTH = /^(19|20)\d\d-(0[1-9]|1[0-2])$/;
const KEY = /^[A-Z0-9]{8}$/;
/**
 * Zotero's local upload receiver reads a raw body into a JS array, one element
 * per byte, which fails past ~256 MiB (SpiderMonkey's dense-array limit). Larger
 * files go as multipart/form-data, which it reads into a string instead
 * (capped near 1 GiB by the engine's maximum string length).
 */
export const MULTIPART_FROM_BYTES = 128 * 1024 * 1024;
export const MAX_UPLOAD_BYTES = 900 * 1024 * 1024;

interface RootPattern {
  source: ZoteroSource;
  base: string;
  /** Path segments below base; "*" matches any non-hidden directory. */
  segments: string[];
  /** Which "*" names the account (0 = first). */
  accountStar?: number;
  maxDepth: number;
}

export function rootPatterns(platform: string, home: string, env: Record<string, string | undefined>): RootPattern[] {
  const deep = 8;
  const list: RootPattern[] = [];
  if (platform === "darwin") {
    const c = join(home, "Library", "Containers");
    list.push(
      { source: "wecom", base: join(c, "com.tencent.WeWorkMac/Data/Documents/Profiles"), segments: ["*", "Caches", "Files"], maxDepth: deep },
      { source: "wechat", base: join(c, "com.tencent.xinWeChat/Data/Documents/xwechat_files"), segments: ["*", "msg", "file"], maxDepth: deep },
      {
        source: "wechat",
        base: join(c, "com.tencent.xinWeChat/Data/Library/Application Support/com.tencent.xinWeChat"),
        segments: ["*", "*", "Message", "MessageTemp", "*", "File"], accountStar: 1, maxDepth: deep,
      },
    );
  } else if (platform === "win32") {
    const docs = join(home, "Documents");
    const wechat3 = [docs];
    if (env.WECHAT3_FILE_ROOT) wechat3.unshift(env.WECHAT3_FILE_ROOT);
    for (const root of wechat3) {
      list.push({ source: "wechat", base: join(root, "WeChat Files"), segments: ["*", "FileStorage", "File"], maxDepth: deep });
    }
    list.push(
      { source: "wechat", base: join(docs, "xwechat_files"), segments: ["*", "msg", "file"], maxDepth: deep },
      { source: "wecom", base: join(docs, "WXWork"), segments: ["*", "Cache", "File"], maxDepth: deep },
    );
  } else {
    for (const base of [join(home, "Documents", "xwechat_files"), join(home, "xwechat_files")]) {
      list.push({ source: "wechat", base, segments: ["*", "msg", "file"], maxDepth: deep });
    }
  }
  list.push({ source: "downloads", base: join(home, "Downloads"), segments: [], maxDepth: 3 });
  // Same variable as chat-files-mcp: entries "app=path" separated by the path
  // delimiter; "*" segments allowed. Only our three sources are used here.
  const delimiter = platform === "win32" ? ";" : ":";
  for (const raw of (env.CHAT_FILES_ROOTS ?? "").split(delimiter)) {
    const at = raw.indexOf("=");
    const source = raw.slice(0, at).trim().toLowerCase();
    if (at < 0 || !["wecom", "wechat", "downloads"].includes(source)) continue;
    const parts = raw.slice(at + 1).trim().split(/[\\/]+/);
    const star = parts.indexOf("*");
    const base = (star < 0 ? parts : parts.slice(0, star)).join(sep) || sep;
    list.push({ source: source as ZoteroSource, base, segments: star < 0 ? [] : parts.slice(star), maxDepth: deep });
  }
  return list;
}

async function dirs(path: string): Promise<string[]> {
  try {
    return (await readdir(path, { withFileTypes: true }))
      .filter((d) => d.isDirectory() && !d.name.startsWith("."))
      .map((d) => d.name);
  } catch {
    return [];
  }
}

async function isDir(path: string): Promise<boolean> {
  try {
    return (await stat(path)).isDirectory();
  } catch {
    return false;
  }
}

export async function resolveRoots(patterns: RootPattern[]): Promise<(ZoteroRoot & { maxDepth: number })[]> {
  const out: (ZoteroRoot & { maxDepth: number })[] = [];
  const seen = new Set<string>();
  for (const p of patterns) {
    let frontier: { path: string; stars: string[] }[] = [{ path: p.base, stars: [] }];
    for (const segment of p.segments) {
      const next: typeof frontier = [];
      for (const f of frontier) {
        if (segment === "*") for (const d of await dirs(f.path)) next.push({ path: join(f.path, d), stars: [...f.stars, d] });
        else next.push({ path: join(f.path, segment), stars: f.stars });
      }
      frontier = next;
    }
    for (const f of frontier) {
      if (seen.has(f.path) || !(await isDir(f.path))) continue;
      seen.add(f.path);
      out.push({ source: p.source, account: f.stars[p.accountStar ?? 0] ?? "", path: f.path, maxDepth: p.maxDepth });
    }
  }
  return out;
}

export function extOf(name: string): string {
  const dot = name.lastIndexOf(".");
  return dot > 0 ? name.slice(dot + 1).toLowerCase() : "";
}

export function fileId(path: string): string {
  return createHash("sha1").update(path).digest("hex").slice(0, 16);
}

async function walk(root: ZoteroRoot & { maxDepth: number }, found: ZoteroFile[], errors: string[]): Promise<void> {
  const queue: { path: string; depth: number }[] = [{ path: root.path, depth: 0 }];
  while (queue.length && found.length < MAX_FILES) {
    const { path, depth } = queue.shift()!;
    let entries;
    try {
      entries = await readdir(path, { withFileTypes: true });
    } catch (error) {
      errors.push(`${path}: ${(error as Error).message}`);
      continue;
    }
    for (const entry of entries) {
      if (entry.name.startsWith(".") || entry.name.startsWith("~$")) continue;
      const full = join(path, entry.name);
      if (entry.isDirectory()) {
        if (depth + 1 < root.maxDepth && !SKIP_DIRS.has(entry.name) && !entry.name.endsWith(".app")) {
          queue.push({ path: full, depth: depth + 1 });
        }
        continue;
      }
      const ext = extOf(entry.name);
      if (!entry.isFile() || !DOC_EXTS.has(ext)) continue;
      try {
        const st = await stat(full);
        const month = relative(root.path, path).split(sep).find((part) => MONTH.test(part))
          ?? st.mtime.toISOString().slice(0, 7);
        found.push({
          id: fileId(full), name: entry.name, ext, size: st.size, modifiedAt: st.mtime.toISOString(), month,
          source: root.source, account: root.account, path: full, imported: null,
        });
      } catch (error) {
        errors.push(`${full}: ${(error as Error).message}`);
      }
    }
  }
}

export function md5File(path: string): Promise<string> {
  return new Promise((resolve, reject) => {
    const hash = createHash("md5");
    createReadStream(path).on("data", (chunk) => hash.update(chunk)).on("error", reject).on("end", () => resolve(hash.digest("hex")));
  });
}

const EXTRA_TYPES: Record<string, string> = {
  pdf: "application/pdf", caj: "application/caj", epub: "application/epub+zip", djvu: "image/vnd.djvu",
  doc: "application/msword", docx: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
  ppt: "application/vnd.ms-powerpoint", pptx: "application/vnd.openxmlformats-officedocument.presentationml.presentation",
  xls: "application/vnd.ms-excel", xlsx: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
  rtf: "application/rtf", csv: "text/csv", odt: "application/vnd.oasis.opendocument.text",
  odp: "application/vnd.oasis.opendocument.presentation", ods: "application/vnd.oasis.opendocument.spreadsheet",
};
export const contentType = (name: string) => EXTRA_TYPES[extOf(name)] ?? "application/octet-stream";

async function writePrivate(path: string, text: string): Promise<void> {
  await mkdir(dirname(path), { recursive: true, mode: 0o700 });
  const part = `${path}.part`;
  await writeFile(part, text, { mode: 0o600 });
  await rename(part, path);
}

async function readJson<T>(path: string, fallback: T): Promise<T> {
  try {
    return JSON.parse(await readFile(path, "utf8")) as T;
  } catch {
    return fallback;
  }
}

class ZoteroHttpError extends Error {
  constructor(readonly status: number, message: string) { super(message); }
}

/** Client for Zotero desktop's local API (v3, same shapes as api.zotero.org). */
export class ZoteroLocalClient {
  private serverId: string | null = null;
  private key: string | null = null;
  private keyLoaded = false;

  constructor(
    readonly baseUrl: string,
    private readonly keyFile: string,
    private readonly fetchImpl: typeof fetch = fetch,
    readonly limits = { multipartFrom: MULTIPART_FROM_BYTES, max: MAX_UPLOAD_BYTES },
  ) {}

  private get api() { return `${this.baseUrl}/api`; }

  private async loadKey(): Promise<string | null> {
    if (!this.keyLoaded) {
      this.key = (await readJson<Record<string, string>>(this.keyFile, {})).ZOTERO_LOCAL_API_KEY ?? null;
      this.keyLoaded = true;
    }
    return this.key;
  }

  private async saveKey(key: string | null): Promise<void> {
    const saved = await readJson<Record<string, string>>(this.keyFile, {});
    if (key) saved.ZOTERO_LOCAL_API_KEY = key;
    else delete saved.ZOTERO_LOCAL_API_KEY;
    await writePrivate(this.keyFile, JSON.stringify(saved, null, 1));
  }

  private async request(method: string, url: string, init: { body?: BodyInit; headers?: Record<string, string>; write?: boolean; timeoutMs?: number } = {}) {
    const headers: Record<string, string> = { "Zotero-API-Version": "3", ...init.headers };
    if (init.write) {
      const key = await this.loadKey();
      if (!key) throw new Error("Zotero has not authorized Multica yet: click Authorize");
      headers["Zotero-API-Key"] = key;
      headers["Zotero-Server-ID"] = await this.getServerId();
    }
    let response: Response;
    try {
      response = await this.fetchImpl(url, { method, body: init.body, headers, signal: AbortSignal.timeout(init.timeoutMs ?? 120_000) });
    } catch (error) {
      throw new ZoteroHttpError(0, `Zotero is not reachable at ${this.baseUrl} (${(error as Error).message})`);
    }
    if (response.status === 401 && init.write) {
      this.key = null;
      await this.saveKey(null);
      throw new ZoteroHttpError(401, "Zotero rejected the saved key: click Authorize again");
    }
    return response;
  }

  private async expect(response: Response, ok: number[], what: string): Promise<Response> {
    if (ok.includes(response.status)) return response;
    const text = (await response.text()).slice(0, 300);
    throw new ZoteroHttpError(response.status, `${what}: HTTP ${response.status} ${text}`);
  }

  async getServerId(): Promise<string> {
    if (this.serverId) return this.serverId;
    const response = await this.expect(await this.request("GET", `${this.api}/`), [200], "Zotero local API");
    const id = response.headers.get("Zotero-Server-ID");
    if (!id) throw new Error("This Zotero can't accept files from other apps; update to Zotero 7.1 or newer");
    this.serverId = id;
    return id;
  }

  async status(): Promise<ZoteroStatus> {
    const url = this.baseUrl;
    try {
      await this.getServerId();
    } catch (error) {
      const status = error instanceof ZoteroHttpError ? error.status : -1;
      if (status === 0) return { state: "not_running", url, message: "" };
      if (status === 403) return { state: "disabled", url, message: "" };
      return { state: "not_running", url, message: (error as Error).message };
    }
    return { state: (await this.loadKey()) ? "ready" : "needs_key", url, message: "" };
  }

  async authorize(): Promise<{ remembered: boolean }> {
    // Every local-API POST, this one included, must name the server it expects.
    const response = await this.request("POST", `${this.api}/local/authorize`, {
      body: JSON.stringify({ appName: "Multica" }),
      headers: { "Content-Type": "application/json", "Zotero-Server-ID": await this.getServerId() },
      timeoutMs: 10 * 60_000,
    });
    if (response.status === 403) throw new Error("Authorization was denied in Zotero");
    const got = (await (await this.expect(response, [200], "Authorize")).json()) as { key: string; remember?: boolean };
    this.key = got.key;
    this.keyLoaded = true;
    // A one-time key ("Allow") is consumed by the first write: keep it in memory only.
    if (got.remember) await this.saveKey(got.key);
    return { remembered: !!got.remember };
  }

  async collections(): Promise<ZoteroCollection[]> {
    const response = await this.expect(await this.request("GET", `${this.api}/users/0/collections`), [200], "List collections");
    const raw = (await response.json()) as { key: string; data: { name: string; parentCollection?: string | false } }[];
    const byKey = new Map(raw.map((c) => [c.key, c.data]));
    const pathOf = (key: string) => {
      const parts: string[] = [];
      const seen = new Set<string>();
      for (let k: string | false | undefined = key; k && byKey.has(k) && !seen.has(k); k = byKey.get(k)!.parentCollection) {
        seen.add(k);
        parts.unshift(byKey.get(k)!.name);
      }
      return parts.join("/");
    };
    return raw.map((c) => ({ key: c.key, path: pathOf(c.key) })).sort((a, b) => a.path.localeCompare(b.path));
  }

  private async postJson(path: string, payload: unknown, what: string): Promise<string> {
    const response = await this.expect(await this.request("POST", `${this.api}/users/0/${path}`, {
      body: JSON.stringify(payload), headers: { "Content-Type": "application/json" }, write: true,
    }), [200], what);
    const result = (await response.json()) as { success?: Record<string, string>; failed?: Record<string, { message?: string }> };
    const key = result.success?.["0"];
    if (!key) throw new Error(`${what}: ${result.failed?.["0"]?.message ?? "failed"}`);
    return key;
  }

  async ensureCollection(path: string): Promise<ZoteroCollection> {
    const names = path.split("/").map((n) => n.trim()).filter(Boolean);
    if (!names.length) throw new Error("Collection name is empty");
    const known = new Map((await this.collections()).map((c) => [c.path, c.key]));
    let parent: string | false = false;
    let sofar = "";
    for (const name of names) {
      sofar = sofar ? `${sofar}/${name}` : name;
      let key = known.get(sofar);
      if (!key) {
        key = await this.postJson("collections", [{ name, parentCollection: parent }], `Create collection ${sofar}`);
        known.set(sofar, key);
      }
      parent = key;
    }
    return { key: parent as string, path: names.join("/") };
  }

  async createAttachment(name: string, collectionKey: string, tags: string[]): Promise<string> {
    return this.postJson("items", [{
      itemType: "attachment", linkMode: "imported_file", title: name, filename: name, contentType: contentType(name),
      charset: "", collections: [collectionKey], tags: tags.map((tag) => ({ tag })),
    }], `Create item ${name}`);
  }

  async upload(key: string, path: string, name: string, md5: string): Promise<void> {
    const st = await stat(path);
    const url = `${this.api}/users/0/items/${key}/file`;
    const formHeaders = { "Content-Type": "application/x-www-form-urlencoded", "If-None-Match": "*" };
    const multipart = st.size >= this.limits.multipartFrom;
    const form = new URLSearchParams({
      md5, filename: name, filesize: String(st.size), mtime: String(Math.floor(st.mtimeMs)), contentType: contentType(name),
      ...(multipart ? { params: "1" } : {}),
    });
    const auth = (await (await this.expect(await this.request("POST", url, { body: form.toString(), headers: formHeaders, write: true }), [200], "Authorize upload")).json()) as
      { exists?: number; url: string; contentType?: string; prefix?: string; suffix?: string; uploadKey: string };
    if (auth.exists) return;
    const bytes = await readFile(path);
    if (multipart) {
      const boundary = `----multica${randomBytes(16).toString("hex")}`;
      const head = `--${boundary}\r\nContent-Disposition: form-data; name="file"; filename="upload"\r\nContent-Type: application/octet-stream\r\n\r\n`;
      const body = Buffer.concat([Buffer.from(head, "latin1"), bytes, Buffer.from(`\r\n--${boundary}--\r\n`, "latin1")]);
      await this.expect(await this.request("POST", auth.url, {
        body, headers: { "Content-Type": `multipart/form-data; boundary=${boundary}` }, timeoutMs: 10 * 60_000,
      }), [200, 201, 204], "Upload");
    } else {
      const body = Buffer.concat([Buffer.from(auth.prefix ?? "", "latin1"), bytes, Buffer.from(auth.suffix ?? "", "latin1")]);
      await this.expect(await this.request("POST", auth.url, {
        body, headers: { "Content-Type": auth.contentType ?? "application/octet-stream" }, timeoutMs: 10 * 60_000,
      }), [200, 201, 204], "Upload");
    }
    await this.expect(await this.request("POST", url, {
      body: new URLSearchParams({ upload: auth.uploadKey }).toString(), headers: formHeaders, write: true,
    }), [204], "Register upload");
  }

  /** Erase an item (used to remove an attachment whose file never arrived). */
  async deleteItem(key: string): Promise<void> {
    const url = `${this.api}/users/0/items/${key}`;
    const current = await this.request("GET", url);
    if (current.status === 404) return;
    await this.expect(current, [200], "Read item");
    const version = current.headers.get("Last-Modified-Version") ?? "0";
    await this.expect(await this.request("DELETE", url, { headers: { "If-Unmodified-Since-Version": version }, write: true }), [204], "Delete item");
  }

  async attachmentMd5(key: string): Promise<string | null> {
    const response = await this.request("GET", `${this.api}/users/0/items/${key}`);
    if (response.status === 404) return null;
    const item = (await (await this.expect(response, [200], "Read item")).json()) as { data: { md5?: string | null } };
    return item.data.md5 ?? null;
  }
}

interface ImportRecord { serverId: string; key: string; collection: string; at: string }
interface ImportState {
  version: 1;
  /** path -> the file version whose md5 we know. */
  hashes: Record<string, { size: number; mtimeMs: number; md5: string }>;
  /** md5 -> where that content went. */
  imports: Record<string, ImportRecord>;
}

export interface ZoteroLibraryOptions {
  platform: string;
  home: string;
  env: Record<string, string | undefined>;
  stateFile: string;
  keyFile: string;
  baseUrl?: string;
  trash: (path: string) => Promise<void>;
  fetchImpl?: typeof fetch;
  limits?: { multipartFrom: number; max: number };
}

export class ZoteroLibrary {
  readonly client: ZoteroLocalClient;
  private files = new Map<string, ZoteroFile>();
  private collectionPaths = new Map<string, string>();

  constructor(private readonly options: ZoteroLibraryOptions) {
    this.client = new ZoteroLocalClient(options.baseUrl ?? "http://127.0.0.1:23119", options.keyFile, options.fetchImpl, options.limits);
  }

  private state(): Promise<ImportState> {
    return readJson<ImportState>(this.options.stateFile, { version: 1, hashes: {}, imports: {} });
  }

  async list(): Promise<ZoteroCatalog> {
    const { platform, home, env } = this.options;
    const roots = await resolveRoots(rootPatterns(platform, home, env));
    const files: ZoteroFile[] = [];
    const errors: string[] = [];
    for (const root of roots) await walk(root, files, errors);
    const state = await this.state();
    for (const f of files) {
      const known = state.hashes[f.path];
      const record = known && known.size === f.size && known.mtimeMs === Date.parse(f.modifiedAt) ? state.imports[known.md5] : undefined;
      if (record) f.imported = { key: record.key, collection: record.collection, at: record.at };
    }
    files.sort((a, b) => b.modifiedAt.localeCompare(a.modifiedAt));
    this.files = new Map(files.map((f) => [f.id, f]));
    return { roots: roots.map(({ source, account, path }) => ({ source, account, path })), files, errors: errors.slice(0, 20) };
  }

  async collections(): Promise<ZoteroCollection[]> {
    const list = await this.client.collections();
    this.collectionPaths = new Map(list.map((c) => [c.key, c.path]));
    return list;
  }

  async ensureCollection(path: string): Promise<ZoteroCollection> {
    const c = await this.client.ensureCollection(path);
    this.collectionPaths.set(c.key, c.path);
    return c;
  }

  async importFile(id: string, collectionKey: string, trashOriginal: boolean): Promise<ZoteroImportResult> {
    const file = this.files.get(id);
    const result: ZoteroImportResult = { id, status: "failed", key: null, collection: "", trashed: false, error: "" };
    try {
      if (!file) throw new Error("This file is no longer listed; refresh the list");
      if (!KEY.test(collectionKey)) throw new Error("Choose a Zotero collection");
      result.collection = this.collectionPaths.get(collectionKey) ?? "";
      const st = await stat(file.path);
      const md5 = await md5File(file.path);
      const serverId = await this.client.getServerId();
      const state = await this.state();
      const previous = state.imports[md5];
      if (previous && previous.serverId === serverId && (await this.client.attachmentMd5(previous.key)) === md5) {
        Object.assign(result, { status: "already_imported", key: previous.key, collection: previous.collection });
      } else {
        const { max } = this.client.limits;
        if (st.size > max) {
          throw new Error(`Too large for Zotero's local API (${Math.round(st.size / 1048576)} MB, limit ${Math.round(max / 1048576)} MB): drag it into Zotero instead`);
        }
        const key = await this.client.createAttachment(file.name, collectionKey, ["multica", file.source]);
        try {
          await this.client.upload(key, file.path, file.name, md5);
          if ((await this.client.attachmentMd5(key)) !== md5) throw new Error(`Zotero item ${key} does not match the file after upload`);
        } catch (error) {
          // Don't leave an empty attachment behind in Zotero.
          await this.client.deleteItem(key).catch(() => undefined);
          throw error;
        }
        Object.assign(result, { status: "imported", key });
        state.imports[md5] = { serverId, key, collection: result.collection, at: new Date().toISOString() };
      }
      state.hashes[file.path] = { size: st.size, mtimeMs: st.mtime.getTime(), md5 };
      await writePrivate(this.options.stateFile, JSON.stringify(state));
      const record = state.imports[md5]!;
      file.imported = { key: record.key, collection: record.collection, at: record.at };
      if (trashOriginal) {
        // Only after Zotero holds a verified copy.
        await this.options.trash(file.path);
        result.trashed = true;
        this.files.delete(id);
      }
    } catch (error) {
      result.error = (error as Error).message;
    }
    return result;
  }

  /**
   * Rename a listed file in place (same folder). Returns the updated entry; its
   * id changes with the path. Refuses separators, control characters and names
   * that would overwrite another file.
   */
  async renameFile(id: string, newName: string): Promise<ZoteroFile> {
    const file = this.files.get(id);
    if (!file) throw new Error("This file is no longer listed; refresh the list");
    const name = newName.trim();
    const control = [...name].some((ch) => ch.charCodeAt(0) < 0x20);
    if (!name || name === "." || name === ".." || control || /[\\/:*?"<>|]/.test(name)) {
      throw new Error(`"${name}" is not a valid file name`);
    }
    if (Buffer.byteLength(name) > 255) throw new Error("That file name is too long");
    if (name === file.name) return file;
    const target = join(dirname(file.path), name);
    // A case-only rename finds "itself" on case-insensitive disks; anything else is a clash.
    if (name.toLowerCase() !== file.name.toLowerCase() && (await stat(target).then(() => true, () => false))) {
      throw new Error(`A file named "${name}" already exists in that folder`);
    }
    await rename(file.path, target);
    const state = await this.state();
    if (state.hashes[file.path]) {
      state.hashes[target] = state.hashes[file.path]!;
      delete state.hashes[file.path];
      await writePrivate(this.options.stateFile, JSON.stringify(state));
    }
    const renamed: ZoteroFile = { ...file, id: fileId(target), name, ext: extOf(name), path: target };
    this.files.delete(id);
    this.files.set(renamed.id, renamed);
    return renamed;
  }

  /** Move a listed file to the system Trash (recoverable) and drop it from the list. */
  async trashFile(id: string): Promise<{ id: string; trashed: true }> {
    const path = this.pathOf(id);
    await this.options.trash(path);
    this.files.delete(id);
    return { id, trashed: true };
  }

  pathOf(id: string): string {
    const file = this.files.get(id);
    if (!file) throw new Error("This file is no longer listed; refresh the list");
    return file.path;
  }
}

export const sharedKeyFile = (home: string) => join(home, ".chat-files-mcp", "zotero.json");
