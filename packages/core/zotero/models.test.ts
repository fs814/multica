import { describe, expect, it } from "vitest";
import { countZoteroSources, filterZoteroFiles, parseZoteroCatalog, zoteroFileKind } from "./models";

const file = (over: Record<string, unknown>) => ({
  id: "1", name: "paper.pdf", ext: "pdf", size: 1, modifiedAt: "2026-01-01T00:00:00Z", month: "2026-01",
  source: "wecom", path: "/x/paper.pdf", ...over,
});

describe("zotero models", () => {
  it("parses a catalog and defaults imported to null", () => {
    const catalog = parseZoteroCatalog({ roots: [{ source: "downloads", path: "/Users/me/Downloads" }], files: [file({})] });
    expect(catalog.roots[0]!.account).toBe("");
    expect(catalog.files[0]!.imported).toBeNull();
  });

  it("rejects a malformed catalog", () => {
    expect(() => parseZoteroCatalog({ files: [{ id: 1 }] })).toThrow("Invalid Zotero response");
  });

  it("maps extensions to kinds", () => {
    expect(zoteroFileKind("PPTX")).toBe("slides");
    expect(zoteroFileKind("docx")).toBe("word");
    expect(zoteroFileKind("caj")).toBe("other");
  });

  it("filters by source, kind, name and import state", () => {
    const files = parseZoteroCatalog({
      files: [
        file({ id: "a" }),
        file({ id: "b", name: "Deck.pptx", ext: "pptx", source: "downloads" }),
        file({ id: "c", name: "old.pdf", source: "wechat", imported: { key: "K", collection: "Papers", at: "now" } }),
      ],
    }).files;
    const base = { source: "all", kind: "all", search: "", hideImported: false } as const;
    expect(filterZoteroFiles(files, { ...base, source: "downloads" }).map((f) => f.id)).toEqual(["b"]);
    expect(filterZoteroFiles(files, { ...base, kind: "pdf" }).map((f) => f.id)).toEqual(["a", "c"]);
    expect(filterZoteroFiles(files, { ...base, search: "DECK" }).map((f) => f.id)).toEqual(["b"]);
    expect(filterZoteroFiles(files, { ...base, hideImported: true }).map((f) => f.id)).toEqual(["a", "b"]);
    expect(countZoteroSources(files)).toEqual({ all: 3, wecom: 1, wechat: 1, downloads: 1 });
  });
});
