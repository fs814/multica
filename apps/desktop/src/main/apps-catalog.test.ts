// @vitest-environment node
import { mkdtemp, mkdir, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { AppsCatalog, desktopEntry, linuxCategory, macCategory, parseBrewInfo, plistString } from "./apps-catalog";

let tmp: string;
beforeEach(async () => { tmp = await mkdtemp(join(tmpdir(), "apps-catalog-")); });
afterEach(async () => { await rm(tmp, { recursive: true, force: true }); });

async function put(path: string, text: string) {
  await mkdir(join(path, ".."), { recursive: true });
  await writeFile(path, text);
}

const plist = (fields: Record<string, string>, extra = "") => `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
${Object.entries(fields).map(([k, v]) => `<key>${k}</key>\n<string>${v}</string>`).join("\n")}
${extra}
</dict></plist>`;

describe("category rules", () => {
  it("maps macOS LSApplicationCategoryType, then system folders, then names", () => {
    expect(macCategory("public.app-category.developer-tools", "X", "", false)).toBe("developer");
    expect(macCategory("public.app-category.puzzle-games", "X", "", false)).toBe("games");
    expect(macCategory(undefined, "Calculator", "com.apple.calculator", true)).toBe("system");
    expect(macCategory(undefined, "企业微信", "com.tencent.WeWorkMac", false)).toBe("social");
    expect(macCategory(undefined, "Clash Verge", "", false)).toBe("internet");
    expect(macCategory(undefined, "Something", "com.example.thing", false)).toBe("other");
  });

  it("maps freedesktop categories, preferring the specific ones", () => {
    expect(linuxCategory("Network;InstantMessaging;", "Telegram", "")).toBe("social");
    expect(linuxCategory("Network;WebBrowser;", "Firefox", "")).toBe("internet");
    expect(linuxCategory("Development;IDE;", "VS Code", "")).toBe("developer");
    expect(linuxCategory("", "Mystery", "")).toBe("other");
  });

  it("reads plist strings and .desktop entries", () => {
    expect(plistString(plist({ CFBundleIdentifier: "a&amp;b" }), "CFBundleIdentifier")).toBe("a&b");
    expect(plistString(plist({}), "CFBundleIdentifier")).toBeUndefined();
    expect(desktopEntry("[Desktop Entry]\nName=Foo\nName[zh_CN]=福\n[Desktop Action new]\nName=New window\n")).toEqual({ Name: "Foo", "Name[zh_CN]": "福" });
  });
});

describe("AppsCatalog", () => {
  it("lists macOS bundles one folder deep, skips background-only helpers, and resolves ids", async () => {
    const apps = join(tmp, "Applications");
    const system = join(tmp, "System");
    await put(join(apps, "Xcode.app/Contents/Info.plist"), plist({ CFBundleIdentifier: "com.apple.dt.Xcode", LSApplicationCategoryType: "public.app-category.developer-tools", CFBundleShortVersionString: "26.1" }));
    await put(join(apps, "Office/Word.app/Contents/Info.plist"), plist({ CFBundleIdentifier: "com.microsoft.Word", LSApplicationCategoryType: "public.app-category.productivity" }));
    await put(join(apps, "Helper.app/Contents/Info.plist"), plist({ CFBundleIdentifier: "x.helper" }, "<key>LSBackgroundOnly</key><true/>"));
    await put(join(apps, "Menu.app/Contents/Info.plist"), plist({ CFBundleIdentifier: "x.menu" }, "<key>LSUIElement</key><true/>"));
    await put(join(system, "Calculator.app/Contents/Info.plist"), plist({ CFBundleIdentifier: "com.apple.calculator" }));
    await put(join(apps, "notes.txt"), "not an app");
    const catalog = new AppsCatalog({ platform: "darwin", home: tmp, env: {}, macRoots: [[apps, false], [system, true]], serviceDirs: [], iconFor: async (p) => `icon:${p.split("/").pop()}` });
    const list = await catalog.list();
    expect(list.apps.map((a) => [a.name, a.category, a.version])).toEqual([
      ["Calculator", "system", ""], ["Menu", "other", ""], ["Word", "productivity", ""], ["Xcode", "developer", "26.1"],
    ]);
    expect(list.apps[0]!.icon).toBe("icon:Calculator.app");
    const xcode = list.apps.find((a) => a.name === "Xcode")!;
    expect(await catalog.launchable(xcode.id)).toBe(join(apps, "Xcode.app"));
    await rm(join(apps, "Xcode.app"), { recursive: true });
    await expect(catalog.launchable(xcode.id)).rejects.toThrow(/no longer installed/);
    expect(() => catalog.pathOf("nope")).toThrow(/no longer listed/);
  });

  it("lists Windows Start Menu shortcuts by folder, skipping uninstallers and duplicates", async () => {
    const programs = join(tmp, "ProgramData/Microsoft/Windows/Start Menu/Programs");
    const user = join(tmp, "AppData/Microsoft/Windows/Start Menu/Programs");
    await put(join(programs, "Visual Studio 2022.lnk"), "");
    await put(join(programs, "7-Zip/7-Zip File Manager.lnk"), "");
    await put(join(programs, "7-Zip/Uninstall 7-Zip.lnk"), "");
    await put(join(user, "Telegram.lnk"), "");
    await put(join(user, "7-Zip File Manager.lnk"), "");
    const list = await new AppsCatalog({ platform: "win32", home: tmp, env: { ProgramData: join(tmp, "ProgramData"), APPDATA: join(tmp, "AppData") } }).list();
    expect(list.apps.map((a) => [a.name, a.category])).toEqual([
      ["7-Zip File Manager", "folder:7-Zip"], ["Telegram", "social"], ["Visual Studio 2022", "developer"],
    ]);
  });

  it("lists Linux .desktop apps with localized names, honouring NoDisplay and user overrides", async () => {
    const local = join(tmp, ".local/share/applications");
    await put(join(local, "code.desktop"), "[Desktop Entry]\nType=Application\nName=Code\nName[zh_CN]=代码\nCategories=Development;IDE;\n");
    await put(join(local, "hidden.desktop"), "[Desktop Entry]\nType=Application\nName=Hidden\nNoDisplay=true\n");
    await put(join(local, "link.desktop"), "[Desktop Entry]\nType=Link\nName=Docs\n");
    const list = await new AppsCatalog({ platform: "linux", home: tmp, env: { LANG: "zh_CN.UTF-8" } }).list();
    const mine = list.apps.filter((a) => a.path.startsWith(tmp));
    expect(mine.map((a) => [a.name, a.category, a.bundleId])).toEqual([["代码", "developer", "code.desktop"]]);
  });

  it("adds yabai and skhd as window-management services that open their config and show skhd's keymap", async () => {
    const bin = join(tmp, "bin");
    await put(join(bin, "yabai"), "#!/bin/sh\necho yabai-v7.1.25\n");
    await put(join(bin, "skhd"), "#!/bin/sh\necho skhd-v0.3.9\n");
    const { chmod } = await import("node:fs/promises");
    await chmod(join(bin, "yabai"), 0o755);
    await chmod(join(bin, "skhd"), 0o755);
    await put(join(tmp, ".yabairc"), "yabai -m config layout bsp\n");
    await put(join(tmp, ".config/skhd/skhdrc"), "# focus\nalt + shift - h : yabai -m window --focus west\n");
    const catalog = new AppsCatalog({ platform: "darwin", home: tmp, env: {}, serviceDirs: [bin], macRoots: [[join(tmp, "none"), false]] });
    const services = (await catalog.list()).apps.filter((a) => a.kind === "service");
    expect(services.map((a) => [a.name, a.category, a.version, a.config, a.keymap])).toEqual([
      ["skhd", "window", "0.3.9", join(tmp, ".config/skhd/skhdrc"), true],
      ["yabai", "window", "7.1.25", join(tmp, ".yabairc"), true],
    ]);
    const yabai = services.find((a) => a.name === "yabai")!;
    expect(await catalog.launchable(yabai.id)).toBe(join(tmp, ".yabairc"));
    const keymap = await catalog.keymap(yabai.id);
    expect(keymap.sections[0]!.bindings[0]!.keys).toBe("⌥ ⇧ H");
    await rm(join(tmp, ".config/skhd/skhdrc"));
    await expect(catalog.keymap(yabai.id)).rejects.toThrow(/config file is gone/);
  });

  it("builds intros from Homebrew, the bundle, Spotlight and the App Store receipt", async () => {
    const apps = join(tmp, "Applications");
    await put(join(apps, "Zotero.app/Contents/Info.plist"), plist({ CFBundleIdentifier: "org.zotero.zotero", NSHumanReadableCopyright: "© Zotero" }));
    await put(join(apps, "Things.app/Contents/Info.plist"), plist({ CFBundleIdentifier: "com.culturedcode.Things" }));
    await put(join(apps, "Things.app/Contents/_MASReceipt/receipt"), "x");
    await put(join(apps, "Mine.app/Contents/Info.plist"), plist({ CFBundleIdentifier: "me.mine" }));
    const brew = parseBrewInfo(JSON.stringify({
      casks: [{ token: "zotero", desc: "Collect, organise, cite, and share research sources", homepage: "https://www.zotero.org/", artifacts: [{ app: ["Zotero.app"] }, { zap: [] }] }],
      formulae: [{ name: "yabai", desc: "A tiling window manager", homepage: "https://github.com/asmvik/yabai" }],
    }));
    const catalog = new AppsCatalog({
      platform: "darwin", home: tmp, env: {}, macRoots: [[apps, false]], serviceDirs: [],
      brewIndex: async () => brew, spotlight: async () => ({ addedAt: "2026-11-09T08:28:53.000Z", lastUsedAt: null }),
    });
    const list = (await catalog.list()).apps;
    const intro = (name: string) => catalog.intro(list.find((a) => a.name === name)!.id);
    expect(await intro("Zotero")).toMatchObject({
      source: "homebrew-cask", sourceDetail: "zotero", description: "Collect, organise, cite, and share research sources",
      homepage: "https://www.zotero.org/", copyright: "© Zotero", addedAt: "2026-11-09T08:28:53.000Z", bundleId: "org.zotero.zotero",
    });
    expect(await intro("Things")).toMatchObject({ source: "app-store", description: null });
    expect(await intro("Mine")).toMatchObject({ source: "manual", description: null, homepage: null });
    await expect(catalog.intro("nope")).rejects.toThrow(/no longer listed/);
    expect(parseBrewInfo("not json").casks.size).toBe(0);
  });

  it("uses the .desktop Comment as the Linux intro", async () => {
    await put(join(tmp, ".local/share/applications/code.desktop"), "[Desktop Entry]\nType=Application\nName=Code\nComment=Code editing. Redefined.\nCategories=Development;\n");
    const catalog = new AppsCatalog({ platform: "linux", home: tmp, env: {} });
    const code = (await catalog.list()).apps.find((a) => a.name === "Code")!;
    expect(await catalog.intro(code.id)).toMatchObject({ source: "desktop-entry", description: "Code editing. Redefined." });
  });
});
