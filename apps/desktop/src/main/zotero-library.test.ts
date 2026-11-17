// @vitest-environment node
import { createHash } from "node:crypto";
import { mkdtemp, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { resolveRoots, rootPatterns, ZoteroLibrary } from "./zotero-library";

/** Just enough of Zotero's local API v3 for list -> import -> verify. */
function fakeZotero() {
  const state = {
    collections: new Map<string, { name: string; parentCollection: string | false }>([["AAAAAAAA", { name: "Papers", parentCollection: false }]]),
    items: new Map<string, Record<string, unknown>>(),
    uploads: new Map<string, { md5: string; multipart: boolean; data?: Buffer }>(),
    enabled: true,
    failUploads: false,
  };
  const body = (req: IncomingMessage) => new Promise<Buffer>((resolve) => {
    const chunks: Buffer[] = [];
    req.on("data", (c: Buffer) => chunks.push(c)).on("end", () => resolve(Buffer.concat(chunks)));
  });
  const send = (res: ServerResponse, code: number, payload: unknown = "") => {
    res.writeHead(code, { "Content-Type": "application/json", "Zotero-Server-ID": "srv1" });
    res.end(typeof payload === "string" ? payload : JSON.stringify(payload));
  };
  let n = 0;
  const server: Server = createServer(async (req, res) => {
    const url = new URL(req.url!, "http://x");
    const path = url.pathname;
    const data = await body(req);
    if (!state.enabled) return send(res, 403, "Local API is not enabled");
    if (req.method === "GET" && path === "/api/") return send(res, 200, "ok");
    if (req.method === "GET" && path === "/api/users/0/collections") {
      return send(res, 200, [...state.collections].map(([key, d]) => ({ key, data: d })));
    }
    if (req.method === "GET" && path.startsWith("/api/users/0/items/")) {
      const item = state.items.get(path.split("/").pop()!);
      return item ? send(res, 200, { data: item }) : send(res, 404);
    }
    if (path === "/api/local/authorize") {
      if (req.headers["zotero-server-id"] !== "srv1") return send(res, 428, "Zotero-Server-ID not provided");
      return send(res, 200, { key: "LOCALKEY", remember: true });
    }
    if (path === "/api/local/uploads/missing") return send(res, 500, "Upload failed: undefined");
    if (path.startsWith("/api/local/uploads/")) {
      const up = state.uploads.get(path.split("/").pop()!)!;
      const type = req.headers["content-type"] ?? "";
      if (up.multipart !== type.startsWith("multipart/form-data")) return send(res, 400, "wrong upload form");
      if (up.multipart) {
        // Same parsing as Zotero's _decodeMultipartData: split on the boundary, body after the blank line.
        const boundary = `--${/boundary=(\S+)/.exec(type)![1]}`;
        const text = data.toString("latin1");
        const field = text.split(boundary).slice(1, -1).find((f) => f.includes('name="file"'))!;
        up.data = Buffer.from(field.slice(field.indexOf("\r\n\r\n") + 4).replace(/\r\n$/, ""), "latin1");
      } else {
        up.data = data;
      }
      return send(res, 201);
    }
    if (req.headers["zotero-api-key"] !== "LOCALKEY") return send(res, 401, "bad key");
    if (req.method === "DELETE" && path.startsWith("/api/users/0/items/")) {
      if (!req.headers["if-unmodified-since-version"]) return send(res, 428, "If-Unmodified-Since-Version not provided");
      state.items.delete(path.split("/").pop()!);
      return send(res, 204);
    }
    if (req.headers["zotero-server-id"] !== "srv1") return send(res, 428, "no server id");
    if (path === "/api/users/0/collections") {
      const key = `C${String(++n).padStart(7, "0")}`;
      state.collections.set(key, JSON.parse(data.toString())[0]);
      return send(res, 200, { success: { 0: key } });
    }
    if (path === "/api/users/0/items") {
      const key = `I${String(++n).padStart(7, "0")}`;
      state.items.set(key, JSON.parse(data.toString())[0]);
      return send(res, 200, { success: { 0: key } });
    }
    if (path.endsWith("/file")) {
      const key = path.split("/").at(-2)!;
      const form = new URLSearchParams(data.toString());
      const uploadKey = form.get("upload");
      if (uploadKey) {
        const up = state.uploads.get(uploadKey)!;
        expect(createHash("md5").update(up.data!).digest("hex")).toBe(up.md5);
        state.items.get(key)!.md5 = up.md5;
        return send(res, 204);
      }
      const uk = `u${++n}`;
      const multipart = form.get("params") === "1";
      state.uploads.set(uk, { md5: form.get("md5")!, multipart });
      if (state.failUploads) return send(res, 200, { url: `http://${req.headers.host}/api/local/uploads/missing`, uploadKey: uk });
      const url = `http://${req.headers.host}/api/local/uploads/${uk}`;
      return send(res, 200, multipart ? { url, uploadKey: uk, params: { key: form.get("md5") } }
        : { url, uploadKey: uk, contentType: "application/octet-stream", prefix: "", suffix: "" });
    }
    send(res, 404);
  });
  return { server, state };
}

let tmp: string;
let zotero: ReturnType<typeof fakeZotero>;
let baseUrl: string;

beforeEach(async () => {
  tmp = await mkdtemp(join(tmpdir(), "zotero-lib-"));
  zotero = fakeZotero();
  await new Promise<void>((r) => zotero.server.listen(0, "127.0.0.1", r));
  baseUrl = `http://127.0.0.1:${(zotero.server.address() as AddressInfo).port}`;
});
afterEach(async () => {
  await new Promise<void>((r) => zotero.server.close(() => r()));
  await rm(tmp, { recursive: true, force: true });
});

async function put(path: string, text: string) {
  await mkdir(join(path, ".."), { recursive: true });
  await writeFile(path, text);
}

async function setupHome() {
  const home = join(tmp, "home");
  const wecom = join(home, "Library/Containers/com.tencent.WeWorkMac/Data/Documents/Profiles/PROFILE1/Caches/Files");
  await put(join(wecom, "2025-03/abc/paper.pdf"), "%PDF paper");
  await put(join(wecom, "2025-03/abc/notes.zip"), "zip");
  await put(join(home, "Downloads/deck.pptx"), "pptx bytes");
  await put(join(home, "Downloads/sub/deeper/deepest/too-deep.pdf"), "x");
  await put(join(home, "Downloads/Some.app/Contents/readme.pdf"), "x");
  await put(join(home, "Downloads/~$lock.docx"), "x");
  return home;
}

function makeLibrary(home: string, trash = vi.fn(async () => {}), limits?: { multipartFrom: number; max: number }) {
  return new ZoteroLibrary({
    platform: "darwin", home, env: {}, baseUrl, trash, limits,
    stateFile: join(tmp, "state.json"), keyFile: join(home, ".chat-files-mcp/zotero.json"),
  });
}

describe("Zotero library", () => {
  it("finds WeCom and Downloads documents, skipping bundles, lock files, other types and deep folders", async () => {
    const home = await setupHome();
    const catalog = await makeLibrary(home).list();
    expect(catalog.roots.map((r) => [r.source, r.account])).toEqual([["wecom", "PROFILE1"], ["downloads", ""]]);
    expect(catalog.files.map((f) => [f.source, f.name, f.month]).sort()).toEqual([
      ["downloads", "deck.pptx", expect.stringMatching(/^\d{4}-\d\d$/)],
      ["wecom", "paper.pdf", "2025-03"],
    ]);
  });

  it("reads extra roots from CHAT_FILES_ROOTS with * segments", async () => {
    await put(join(tmp, "D/xwechat_files/wxid_1/msg/file/2026-01/a.pdf"), "a");
    const patterns = rootPatterns("linux", join(tmp, "nohome"), { CHAT_FILES_ROOTS: `wechat=${tmp}/D/xwechat_files/*/msg/file:junk=/x` });
    const roots = await resolveRoots(patterns);
    expect(roots.map((r) => [r.source, r.account])).toEqual([["wechat", "wxid_1"]]);
  });

  it("reports Zotero state: disabled, needs a key, ready after authorize", async () => {
    const home = await setupHome();
    zotero.state.enabled = false;
    expect((await makeLibrary(home).client.status()).state).toBe("disabled");
    zotero.state.enabled = true;
    const lib = makeLibrary(home);
    expect((await lib.client.status()).state).toBe("needs_key");
    await lib.client.authorize();
    expect(JSON.parse(await readFile(join(home, ".chat-files-mcp/zotero.json"), "utf8"))).toEqual({ ZOTERO_LOCAL_API_KEY: "LOCALKEY" });
    expect((await makeLibrary(home).client.status()).state).toBe("ready");
  });

  it("imports into a (new nested) collection, verifies, remembers, and trashes only on request", async () => {
    const home = await setupHome();
    const trash = vi.fn(async () => {});
    const lib = makeLibrary(home, trash);
    const { files } = await lib.list();
    const paper = files.find((f) => f.name === "paper.pdf")!;
    const deck = files.find((f) => f.name === "deck.pptx")!;

    expect((await lib.importFile(paper.id, "AAAAAAAA", false)).error).toMatch(/Authorize/);
    await lib.client.authorize();
    await lib.collections();
    const llm = await lib.ensureCollection("Papers/LLM");
    expect(llm.key).not.toBe("AAAAAAAA");
    expect(zotero.state.collections.get(llm.key)).toEqual({ name: "LLM", parentCollection: "AAAAAAAA" });

    const first = await lib.importFile(paper.id, llm.key, false);
    expect(first).toMatchObject({ status: "imported", collection: "Papers/LLM", trashed: false });
    const item = zotero.state.items.get(first.key!)!;
    expect(item).toMatchObject({ itemType: "attachment", linkMode: "imported_file", filename: "paper.pdf", collections: [llm.key] });
    expect(item.tags).toEqual([{ tag: "multica" }, { tag: "wecom" }]);
    expect(trash).not.toHaveBeenCalled();

    // A fresh list knows it is already there; importing again does not duplicate.
    const again = makeLibrary(home, trash);
    await again.client.authorize();
    const relisted = (await again.list()).files.find((f) => f.name === "paper.pdf")!;
    expect(relisted.imported).toMatchObject({ key: first.key, collection: "Papers/LLM" });
    expect(await again.importFile(relisted.id, llm.key, true)).toMatchObject({ status: "already_imported", trashed: true });
    expect(zotero.state.items.size).toBe(1);
    expect(trash).toHaveBeenCalledWith(paper.path);

    const second = await lib.importFile(deck.id, "AAAAAAAA", true);
    expect(second).toMatchObject({ status: "imported", trashed: true });
    expect(await lib.importFile(deck.id, "AAAAAAAA", false)).toMatchObject({ status: "failed", error: expect.stringMatching(/no longer listed/) });
  });

  it("sends large files as multipart, refuses oversized ones, and removes the item when an upload fails", async () => {
    const home = await setupHome();
    // Bytes that a text decoder would mangle, plus CRLFs, to prove the body arrives byte-exact.
    await writeFile(join(home, "Downloads/big.pdf"), Buffer.from([0, 255, 13, 10, 13, 10, 128, 45, 45, 10, 10, 200]));
    const lib = makeLibrary(home, undefined, { multipartFrom: 8, max: 1000 });
    await lib.client.authorize();
    const files = (await lib.list()).files;
    const big = files.find((f) => f.name === "big.pdf")!;
    const small = files.find((f) => f.name === "deck.pptx")!;
    expect(await lib.importFile(big.id, "AAAAAAAA", false)).toMatchObject({ status: "imported" });
    const sent = [...zotero.state.uploads.values()].at(-1)!;
    expect(sent.multipart).toBe(true);
    expect(sent.data).toEqual(Buffer.from([0, 255, 13, 10, 13, 10, 128, 45, 45, 10, 10, 200]));

    const tiny = makeLibrary(home, undefined, { multipartFrom: 8, max: 5 });
    await tiny.client.authorize();
    await tiny.list();
    expect(await tiny.importFile(small.id, "AAAAAAAA", false)).toMatchObject({ status: "failed", error: expect.stringMatching(/Too large/) });
    expect(zotero.state.items.size).toBe(1);

    zotero.state.failUploads = true;
    expect(await lib.importFile(small.id, "AAAAAAAA", false)).toMatchObject({ status: "failed", error: expect.stringMatching(/HTTP 500/) });
    expect(zotero.state.items.size).toBe(1); // the empty attachment was erased again
  });

  it("renames in place, keeps the import record, and refuses unsafe or clashing names", async () => {
    const home = await setupHome();
    await put(join(home, "Downloads/other.pdf"), "other");
    const lib = makeLibrary(home);
    await lib.client.authorize();
    const files = (await lib.list()).files;
    const deck = files.find((f) => f.name === "deck.pptx")!;
    expect((await lib.importFile(deck.id, "AAAAAAAA", false)).status).toBe("imported");

    for (const bad of ["", "  ", "..", "a/b.pptx", "a\\b.pptx", "x:y.pptx", "nul\u0000.pptx"]) {
      await expect(lib.renameFile(deck.id, bad)).rejects.toThrow(/not a valid file name/);
    }
    await expect(lib.renameFile(deck.id, "other.pdf")).rejects.toThrow(/already exists/);
    expect(await lib.renameFile(deck.id, "deck.pptx")).toMatchObject({ id: deck.id });

    const renamed = await lib.renameFile(deck.id, " Talk 2026.pptx ");
    expect(renamed).toMatchObject({ name: "Talk 2026.pptx", ext: "pptx", path: join(home, "Downloads/Talk 2026.pptx") });
    expect(renamed.id).not.toBe(deck.id);
    expect(await readFile(renamed.path, "utf8")).toBe("pptx bytes");
    await expect(readFile(deck.path)).rejects.toThrow();
    await expect(lib.renameFile(deck.id, "again.pptx")).rejects.toThrow(/no longer listed/);
    // The "already in Zotero" mark follows the file to its new name.
    const relisted = (await makeLibrary(home).list()).files.find((f) => f.name === "Talk 2026.pptx")!;
    expect(relisted.imported).not.toBeNull();
    // A case-only rename works on case-insensitive disks too.
    expect((await lib.renameFile(renamed.id, "talk 2026.pptx")).name).toBe("talk 2026.pptx");
  });

  it("moves a listed file to Trash and forgets it", async () => {
    const home = await setupHome();
    const trash = vi.fn(async () => {});
    const lib = makeLibrary(home, trash);
    const deck = (await lib.list()).files.find((f) => f.name === "deck.pptx")!;
    expect(await lib.trashFile(deck.id)).toEqual({ id: deck.id, trashed: true });
    expect(trash).toHaveBeenCalledWith(deck.path);
    await expect(lib.trashFile(deck.id)).rejects.toThrow(/no longer listed/);
    await expect(lib.trashFile("never-listed")).rejects.toThrow(/no longer listed/);
  });

  it("rejects ids it never listed and malformed collection keys", async () => {
    const home = await setupHome();
    const lib = makeLibrary(home);
    expect((await lib.importFile("nope", "AAAAAAAA", false)).error).toMatch(/no longer listed/);
    const { files } = await lib.list();
    expect((await lib.importFile(files[0]!.id, "../x", false)).error).toMatch(/collection/);
    expect(() => lib.pathOf("nope")).toThrow(/no longer listed/);
  });
});
