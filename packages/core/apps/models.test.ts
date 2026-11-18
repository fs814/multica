import { describe, expect, it } from "vitest";
import { groupInstalledApps, parseInstalledApps } from "./models";

const app = (id: string, name: string, category: string, bundleId = "") => ({ id, name, path: `/A/${name}.app`, category, bundleId });

describe("installed apps", () => {
  it("parses with defaults and rejects malformed lists", () => {
    const parsed = parseInstalledApps({ platform: "darwin", apps: [{ id: "1", name: "Xcode", path: "/A/Xcode.app" }] });
    expect(parsed.apps[0]).toMatchObject({ category: "other", icon: null, version: "" });
    expect(() => parseInstalledApps({ platform: "darwin", apps: [{ id: 1 }] })).toThrow("Invalid app list");
  });

  it("groups by category in a stable order, other last, Windows folders after known ones", () => {
    const apps = parseInstalledApps({ platform: "x", apps: [
      app("1", "Zed", "developer"), app("2", "Misc", "other"), app("3", "Visual Studio", "folder:Visual Studio 2022"),
      app("4", "Cursor", "developer"), app("5", "Notion", "productivity"), app("6", "7-Zip", "folder:7-Zip"),
    ] }).apps;
    expect(groupInstalledApps(apps).map((g) => [g.category, g.apps.map((a) => a.name)])).toEqual([
      ["developer", ["Cursor", "Zed"]], ["productivity", ["Notion"]],
      ["folder:7-Zip", ["7-Zip"]], ["folder:Visual Studio 2022", ["Visual Studio"]], ["other", ["Misc"]],
    ]);
  });

  it("filters by name or bundle id", () => {
    const apps = parseInstalledApps({ platform: "x", apps: [app("1", "企业微信", "social", "com.tencent.WeWorkMac"), app("2", "Xcode", "developer")] }).apps;
    expect(groupInstalledApps(apps, "wework").flatMap((g) => g.apps.map((a) => a.name))).toEqual(["企业微信"]);
    expect(groupInstalledApps(apps, "XC").flatMap((g) => g.apps.map((a) => a.name))).toEqual(["Xcode"]);
  });
});
