// @vitest-environment node
import { describe, expect, it } from "vitest";
import { buildToolTree, parseToolCatalog, parseToolRun, parseToolRuns } from "./models";

describe("local Tools data", () => {
  it("builds and filters a sorted directory hierarchy", () => {
    const scripts = [
      { path: "game/engine/run.sh", name: "run.sh" },
      { path: "ai/build.sh", name: "build.sh" },
      { path: "setup.sh", name: "setup.sh" },
    ];
    expect(buildToolTree(scripts).folders.map((folder) => folder.name)).toEqual(["ai", "game"]);
    const filtered = buildToolTree(scripts, "ENGINE");
    expect(filtered.scripts).toEqual([]);
    expect(filtered.folders[0]?.folders[0]?.scripts).toEqual([scripts[0]]);
    expect(buildToolTree(scripts, "missing").folders).toEqual([]);
  });

  it("rejects malformed IPC payloads and defaults optional fields", () => {
    expect(() => parseToolCatalog({ scripts: "wrong" })).toThrow();
    expect(() => parseToolRuns({})).toThrow();
    expect(() => parseToolRun(null)).toThrow();
    expect(parseToolCatalog({ root: "/Settings/macbuild", platform: "darwin" }).scripts).toEqual([]);
    expect(parseToolRun({ id: "1", path: "run.sh", status: "future-state", startedAt: "now" })).toMatchObject({ status: "failed", output: "", exitCode: null });
  });
});
