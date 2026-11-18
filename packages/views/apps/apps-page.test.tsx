import type { ReactNode } from "react";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import { useAppsViewStore } from "@multica/core/apps";
import type { LocalAppsBridge } from "@multica/core/apps";
import enApps from "../locales/en/apps.json";
import { AppsPage } from "./apps-page";

const external = vi.hoisted(() => vi.fn());
vi.mock("../platform/open-external", () => ({ openExternal: external }));
vi.mock("../layout/page-header", () => ({ PageHeader: ({ children }: { children: ReactNode }) => <header>{children}</header>, PAGE_GUTTER: "" }));
vi.mock("@multica/ui/components/ui/dialog", () => ({
  Dialog: ({ open, children, onOpenChange }: { open: boolean; children: ReactNode; onOpenChange: (open: boolean) => void }) =>
    (open ? <div role="dialog">{children}<button onClick={() => onOpenChange(false)}>close dialog</button></div> : null),
  DialogContent: ({ children }: { children: ReactNode }) => <>{children}</>,
  DialogHeader: ({ children }: { children: ReactNode }) => <>{children}</>,
  DialogTitle: ({ children }: { children: ReactNode }) => <h2>{children}</h2>,
  DialogDescription: ({ children }: { children: ReactNode }) => <p>{children}</p>,
}));
vi.mock("@multica/ui/components/ui/context-menu", () => ({
  ContextMenu: ({ open, children }: { open: boolean; children: ReactNode }) => (open ? <div role="menu">{children}</div> : null),
  ContextMenuContent: ({ children }: { children: ReactNode }) => <>{children}</>,
  ContextMenuSeparator: () => null,
  ContextMenuItem: ({ children, onClick, disabled }: { children: ReactNode; onClick?: () => void; disabled?: boolean }) => <button role="menuitem" disabled={disabled} onClick={onClick}>{children}</button>,
}));

const app = (id: string, name: string, category: string, icon: string | null = null) => ({ id, name, path: `/Applications/${name}.app`, category, icon });

function makeBridge(over: Partial<LocalAppsBridge> = {}) {
  return {
    list: vi.fn().mockResolvedValue({ platform: "darwin", apps: [
      app("1", "Xcode", "developer"), app("2", "企业微信", "social", "data:image/png;base64,AA=="),
      app("3", "Visual Studio 2022", "folder:Visual Studio 2022"), app("4", "Cursor", "developer"), app("5", "Odd", "other"),
      { id: "6", name: "yabai", path: "/opt/homebrew/bin/yabai", category: "window", kind: "service", config: "/Users/me/.yabairc", keymap: true },
    ] }),
    launch: vi.fn().mockResolvedValue(true),
    reveal: vi.fn().mockResolvedValue(true),
    keymap: vi.fn().mockResolvedValue({ source: "/Users/me/.config/skhd/skhdrc", sections: [
      { title: "focus window", bindings: [{ keys: "⌥ ⇧ H", raw: "alt + shift - h", command: "yabai -m window --focus west" }] },
      { title: "app help", bindings: [{ keys: "⌘ H", raw: "cmd - h", command: "fslauncher keyhelp cmux", app: "cmux" }] },
    ] }),
    intro: vi.fn(async (id: string) => (id === "6"
      ? { id, name: "yabai", version: "7.1.25", category: "window", kind: "service", path: "/opt/homebrew/bin/yabai",
          description: "A tiling window manager for macOS based on binary space partitioning.", homepage: "https://github.com/asmvik/yabai",
          source: "homebrew-formula", sourceDetail: "yabai", config: "/Users/me/.yabairc" }
      : { id, name: "Odd", path: "/Applications/Odd.app", category: "other", source: "manual", copyright: "© Odd Co.",
          addedAt: "2026-11-09T08:28:53.000Z" })),
    ...over,
  } satisfies LocalAppsBridge;
}

function mount(bridge?: LocalAppsBridge) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(<QueryClientProvider client={client}><I18nProvider locale="en" resources={{ en: { apps: enApps } }}><AppsPage bridge={bridge} /></I18nProvider></QueryClientProvider>);
}

describe("Apps page", () => {
  it("explains that it needs Desktop", () => {
    mount();
    expect(screen.getByText(enApps.desktop_required)).toBeVisible();
  });

  it("lists installed apps by category, with Windows folders shown as-is and Other last", async () => {
    mount(makeBridge());
    await screen.findByText("Xcode");
    expect(screen.getAllByRole("heading", { level: 2 }).map((h) => h.textContent)).toEqual([
      "Window management 1", "Developer tools 2", "Chat & social 1", "Visual Studio 2022 1", "Other 1",
    ]);
    expect(within(screen.getByRole("region", { name: "Developer tools" })).getAllByRole("listitem").map((b) => b.textContent)).toEqual(["Cursor", "Xcode"]);
    expect(screen.getByText("6 apps")).toBeVisible();
  });

  it("searches, launches on double-click or Enter (not single click), and reports a failed launch", async () => {
    const bridge = makeBridge({ launch: vi.fn().mockRejectedValueOnce(new Error("The application can’t be opened.")).mockResolvedValue(true) });
    mount(bridge);
    await screen.findByText("Xcode");
    fireEvent.change(screen.getByRole("textbox", { name: enApps.search }), { target: { value: "cur" } });
    expect(screen.queryByText("Xcode")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Cursor" }));
    expect(bridge.launch).not.toHaveBeenCalled(); // a single click only selects
    fireEvent.doubleClick(screen.getByRole("button", { name: "Cursor" }));
    await waitFor(() => expect(bridge.launch).toHaveBeenCalledWith("4"));
    expect(await screen.findByRole("alert")).toHaveTextContent("can’t be opened");
    fireEvent.keyDown(screen.getByRole("button", { name: "Cursor" }), { key: "Enter" });
    await waitFor(() => expect(bridge.launch).toHaveBeenCalledTimes(2));
    fireEvent.change(screen.getByRole("textbox", { name: enApps.search }), { target: { value: "zzz" } });
    expect(screen.getByText(enApps.empty)).toBeVisible();
  });

  it("offers Open and Show in folder on right-click", async () => {
    const bridge = makeBridge();
    mount(bridge);
    fireEvent.contextMenu(await screen.findByText("企业微信"));
    fireEvent.click(screen.getByRole("menuitem", { name: enApps.reveal }));
    await waitFor(() => expect(bridge.reveal).toHaveBeenCalledWith("2"));
    fireEvent.contextMenu(screen.getByText("Xcode"));
    fireEvent.click(screen.getByRole("menuitem", { name: enApps.menu_open }));
    await waitFor(() => expect(bridge.launch).toHaveBeenCalledWith("1"));
  });

  it("folds a category and remembers it, but shows matches while searching", async () => {
    useAppsViewStore.setState({ collapsed: [] });
    const view = mount(makeBridge());
    await screen.findByText("Xcode");
    fireEvent.click(screen.getByRole("button", { name: "Developer tools 2" }));
    expect(screen.queryByText("Xcode")).toBeNull();
    expect(useAppsViewStore.getState().collapsed).toEqual(["developer"]);
    fireEvent.change(screen.getByRole("textbox", { name: enApps.search }), { target: { value: "xc" } });
    expect(screen.getByText("Xcode")).toBeVisible();
    view.unmount();
    mount(makeBridge());
    await screen.findByText("Window management");
    expect(screen.getByRole("button", { name: "Developer tools 2" })).toHaveAttribute("aria-expanded", "false");
    useAppsViewStore.setState({ collapsed: [] });
  });

  it("shows yabai's keymappings from the right-click menu, filterable", async () => {
    const bridge = makeBridge();
    mount(bridge);
    fireEvent.contextMenu(await screen.findByText("yabai"));
    expect(screen.getAllByRole("menuitem").map((i) => i.textContent)).toEqual([enApps.menu_open_config, enApps.menu_intro, enApps.menu_keymap, enApps.reveal, enApps.menu_delete]);
    fireEvent.click(screen.getByRole("menuitem", { name: enApps.menu_keymap }));
    const dialog = await screen.findByRole("dialog");
    expect(bridge.keymap).toHaveBeenCalledWith("6");
    expect(within(dialog).getByRole("heading", { name: "yabai keymappings" })).toBeVisible();
    expect(await within(dialog).findByText("yabai -m window --focus west")).toBeVisible();
    expect(within(dialog).getByText("2 bindings from /Users/me/.config/skhd/skhdrc")).toBeVisible();
    expect(within(dialog).getByText("in cmux")).toBeVisible();
    fireEvent.change(within(dialog).getByRole("textbox", { name: enApps.keymap_search }), { target: { value: "cmux" } });
    expect(within(dialog).queryByText("yabai -m window --focus west")).toBeNull();
    fireEvent.change(within(dialog).getByRole("textbox", { name: enApps.keymap_search }), { target: { value: "nothing-like-this" } });
    expect(within(dialog).getByText(enApps.keymap_empty)).toBeVisible();
    fireEvent.click(within(dialog).getByRole("button", { name: "close dialog" }));
    expect(screen.queryByRole("dialog")).toBeNull();
    // A normal app has no keymap entry; a service opens its config instead.
    fireEvent.contextMenu(screen.getByText("Xcode"));
    expect(screen.getAllByRole("menuitem").map((i) => i.textContent)).toEqual([enApps.menu_open, enApps.menu_intro, enApps.reveal, enApps.menu_delete]);
  });

  it("shows an intro with description, source and homepage, or a web search when there is no description", async () => {
    const bridge = makeBridge();
    mount(bridge);
    fireEvent.contextMenu(await screen.findByText("yabai"));
    fireEvent.click(screen.getByRole("menuitem", { name: enApps.menu_intro }));
    const dialog = await screen.findByRole("dialog");
    expect(await within(dialog).findByText("A tiling window manager for macOS based on binary space partitioning.")).toBeVisible();
    expect(within(dialog).getByText("Homebrew formula (yabai)")).toBeVisible();
    expect(within(dialog).getByText("/Users/me/.yabairc")).toBeVisible();
    fireEvent.click(within(dialog).getByRole("button", { name: enApps.intro_homepage }));
    expect(external).toHaveBeenCalledWith("https://github.com/asmvik/yabai");
    expect(within(dialog).queryByRole("button", { name: enApps.intro_search_web })).toBeNull();
    fireEvent.click(within(dialog).getByRole("button", { name: "close dialog" }));

    fireEvent.contextMenu(screen.getByText("Odd"));
    fireEvent.click(screen.getByRole("menuitem", { name: enApps.menu_intro }));
    const second = await screen.findByRole("dialog");
    expect(await within(second).findByText(enApps.intro_no_description)).toBeVisible();
    expect(within(second).getByText("Installed manually")).toBeVisible();
    expect(within(second).getByText("© Odd Co.")).toBeVisible();
    fireEvent.click(within(second).getByRole("button", { name: enApps.intro_search_web }));
    expect(external).toHaveBeenLastCalledWith(expect.stringContaining("bing.com/search?q=Odd%20app"));
  });

  it("deletes an app from the list (remembered) and restores deleted apps", async () => {
    useAppsViewStore.setState({ hidden: [], collapsed: [] });
    const view = mount(makeBridge());
    fireEvent.contextMenu(await screen.findByText("Odd"));
    fireEvent.click(screen.getByRole("menuitem", { name: enApps.menu_delete }));
    expect(screen.queryByText("Odd")).toBeNull();
    expect(screen.queryByRole("region", { name: "Other" })).toBeNull();
    expect(screen.getByText("5 apps")).toBeVisible();
    expect(useAppsViewStore.getState().hidden).toEqual(["/Applications/Odd.app"]);
    view.unmount();
    mount(makeBridge());
    await screen.findByText("Xcode");
    expect(screen.queryByText("Odd")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Restore 1 deleted" }));
    expect(screen.getByText("Odd")).toBeVisible();
    expect(useAppsViewStore.getState().hidden).toEqual([]);
  });
});
