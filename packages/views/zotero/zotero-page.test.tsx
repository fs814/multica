import type { ReactElement, ReactNode } from "react";
import { cloneElement } from "react";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import type { LocalZoteroBridge } from "@multica/core/zotero";
import enZotero from "../locales/en/zotero.json";
import { ZoteroPage } from "./zotero-page";

vi.mock("../layout/page-header", () => ({ PageHeader: ({ children }: { children: ReactNode }) => <header>{children}</header>, PAGE_GUTTER: "" }));
// Render the collection picker inline (no portal / open state); cmdk itself is real.
vi.mock("@multica/ui/components/ui/popover", () => ({
  Popover: ({ children }: { children: ReactNode }) => <>{children}</>,
  PopoverTrigger: ({ children, render: el }: { children: ReactNode; render?: ReactElement }) => (el ? cloneElement(el, undefined, children) : <button>{children}</button>),
  PopoverContent: ({ children }: { children: ReactNode }) => <div>{children}</div>,
}));

// The right-click menu renders inline while open.
vi.mock("@multica/ui/components/ui/context-menu", () => ({
  ContextMenu: ({ open, children }: { open: boolean; children: ReactNode }) => (open ? <div role="menu">{children}</div> : null),
  ContextMenuContent: ({ children }: { children: ReactNode }) => <>{children}</>,
  ContextMenuItem: ({ children, onClick }: { children: ReactNode; onClick?: () => void }) => <button role="menuitem" onClick={onClick}>{children}</button>,
  ContextMenuSeparator: () => null,
}));

const file = (id: string, name: string, source: string, ext = "pdf", imported: unknown = null) => ({
  id, name, ext, size: 2048, modifiedAt: "2026-10-01T00:00:00Z", month: "2026-10", source, account: "", path: `/x/${name}`, imported,
});

function makeBridge(over: Partial<LocalZoteroBridge> = {}) {
  return {
    list: vi.fn().mockResolvedValue({
      roots: [{ source: "wecom", path: "/wecom" }, { source: "downloads", path: "/Downloads" }],
      files: [
        file("a", "paper.pdf", "wecom"),
        file("b", "deck.pptx", "downloads", "pptx"),
        file("c", "old.pdf", "wechat", "pdf", { key: "OLDKEY01", collection: "Papers", at: "now" }),
      ],
    }),
    status: vi.fn().mockResolvedValue({ state: "ready", url: "http://127.0.0.1:23119" }),
    authorize: vi.fn().mockResolvedValue({ remembered: true }),
    collections: vi.fn().mockResolvedValue([{ key: "AAAAAAAA", path: "Papers" }, { key: "BBBBBBBB", path: "Papers/LLM" }]),
    ensureCollection: vi.fn().mockResolvedValue({ key: "CCCCCCCC", path: "Inbox" }),
    importFile: vi.fn(async (id: string) => ({ id, status: "imported", key: `K${id}`, collection: "Papers/LLM", trashed: false })),
    reveal: vi.fn().mockResolvedValue(true),
    preview: vi.fn().mockResolvedValue(true),
    trash: vi.fn(async (id: string) => ({ id, trashed: true })),
    rename: vi.fn(async (id: string, name: string) => ({ ...file(`${id}2`, name, "wecom"), path: `/x/${name}` })),
    ...over,
  } satisfies LocalZoteroBridge;
}

function mount(bridge?: LocalZoteroBridge) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(<QueryClientProvider client={client}><I18nProvider locale="en" resources={{ en: { zotero: enZotero } }}><ZoteroPage bridge={bridge} /></I18nProvider></QueryClientProvider>);
}

describe("Zotero page", () => {
  it("explains that listing local files needs Desktop", () => {
    mount();
    expect(screen.getByText(enZotero.desktop_required)).toBeVisible();
  });

  it("lists files by source and kind, and shows what is already in Zotero", async () => {
    mount(makeBridge());
    expect(await screen.findByText("paper.pdf")).toBeVisible();
    expect(screen.getByText("In Papers")).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: /Downloads/ }));
    expect(screen.queryByText("paper.pdf")).toBeNull();
    expect(screen.getByText("deck.pptx")).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "All 3" }));
    fireEvent.click(screen.getByRole("button", { name: "Slides" }));
    expect(screen.getByText("deck.pptx")).toBeVisible();
    expect(screen.queryByText("old.pdf")).toBeNull();
  });

  it("filters collections by what is typed and transfers into the chosen one", async () => {
    const bridge = makeBridge();
    mount(bridge);
    await screen.findByText("paper.pdf");
    fireEvent.click(screen.getByRole("checkbox", { name: "Select paper.pdf" }));
    fireEvent.click(screen.getByRole("checkbox", { name: "Select deck.pptx" }));
    expect(screen.getByText("2 selected")).toBeVisible();
    const search = screen.getByRole("combobox", { name: enZotero.collection_search });
    fireEvent.change(search, { target: { value: "llm pap" } });
    expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual(["Papers/LLM", "Create “llm pap” and transfer"]);
    fireEvent.change(search, { target: { value: "papers/llm" } });
    expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual(["Papers/LLM"]);
    fireEvent.click(screen.getByRole("option", { name: "Papers/LLM" }));
    await waitFor(() => expect(bridge.importFile).toHaveBeenCalledTimes(2));
    expect(bridge.importFile).toHaveBeenCalledWith("a", "BBBBBBBB", false);
    expect(bridge.importFile).toHaveBeenCalledWith("b", "BBBBBBBB", false);
    expect(await screen.findByText("2 transferred, 0 already in Zotero, 0 failed.")).toBeVisible();
    expect(screen.getByText("0 selected")).toBeVisible();
    expect(search).toHaveValue("");
  });

  it("creates a typed collection, honours move-to-Trash and reports failures", async () => {
    const bridge = makeBridge({
      importFile: vi.fn(async (id: string) => (id === "a"
        ? { id, status: "imported", key: "K1", collection: "Inbox/2026", trashed: true }
        : { id, status: "failed", error: "disk gone" })),
    });
    mount(bridge);
    await screen.findByText("paper.pdf");
    fireEvent.click(screen.getByRole("checkbox", { name: "Select all shown files" }));
    fireEvent.click(screen.getByRole("checkbox", { name: enZotero.trash_originals }));
    fireEvent.change(screen.getByRole("combobox", { name: enZotero.collection_search }), { target: { value: " Inbox / 2026 " } });
    fireEvent.click(screen.getByRole("option", { name: "Create “Inbox/2026” and transfer" }));
    await waitFor(() => expect(bridge.ensureCollection).toHaveBeenCalledWith("Inbox/2026"));
    await waitFor(() => expect(bridge.importFile).toHaveBeenCalledTimes(3));
    expect(bridge.importFile).toHaveBeenCalledWith("a", "CCCCCCCC", true);
    const summary = await screen.findByText(/1 transferred, 0 already in Zotero, 2 failed\./);
    expect(within(summary.parentElement!).getByText("deck.pptx: disk gone")).toBeDefined();
  });

  it("previews a file on double-click of its name and reports a failed preview", async () => {
    const bridge = makeBridge({ preview: vi.fn().mockRejectedValueOnce(new Error("No application knows how to open it")).mockResolvedValue(true) });
    mount(bridge);
    fireEvent.doubleClick(await screen.findByText("deck.pptx"));
    await waitFor(() => expect(bridge.preview).toHaveBeenCalledWith("b"));
    expect(await screen.findByRole("alert")).toHaveTextContent("No application knows how to open it");
    expect(screen.getByText("paper.pdf")).toHaveAttribute("title", "/x/paper.pdf\nDouble-click to preview");
    fireEvent.click(screen.getByText("paper.pdf"));
    expect(bridge.preview).toHaveBeenCalledTimes(1);
  });

  it("moves one file to Trash after a second, confirming click", async () => {
    const bridge = makeBridge();
    mount(bridge);
    await screen.findByText("paper.pdf");
    fireEvent.click(screen.getByRole("button", { name: "Move paper.pdf to Trash" }));
    expect(bridge.trash).not.toHaveBeenCalled();
    const confirm = screen.getByRole("button", { name: "Move to Trash?" });
    fireEvent.blur(confirm); // leaving the button disarms it
    expect(screen.queryByRole("button", { name: "Move to Trash?" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Move paper.pdf to Trash" }));
    fireEvent.click(screen.getByRole("button", { name: "Move to Trash?" }));
    await waitFor(() => expect(bridge.trash).toHaveBeenCalledWith("a"));
    expect(await screen.findByText("1 moved to Trash.")).toBeVisible();
    await waitFor(() => expect(bridge.list).toHaveBeenCalledTimes(2));
  });

  it("moves the selected files to Trash and reports the ones that failed", async () => {
    const bridge = makeBridge({
      trash: vi.fn(async (id: string) => { if (id === "b") throw new Error("Operation not permitted"); return { id, trashed: true }; }),
    });
    mount(bridge);
    await screen.findByText("paper.pdf");
    expect(screen.getByRole("button", { name: "Move 0 to Trash" })).toBeDisabled();
    fireEvent.click(screen.getByRole("checkbox", { name: "Select paper.pdf" }));
    fireEvent.click(screen.getByRole("checkbox", { name: "Select deck.pptx" }));
    fireEvent.click(screen.getByRole("button", { name: "Move 2 to Trash" }));
    fireEvent.click(screen.getByRole("button", { name: "Move to Trash?" }));
    await waitFor(() => expect(bridge.trash).toHaveBeenCalledTimes(2));
    expect(await screen.findByText("1 moved to Trash.")).toBeVisible();
    expect(screen.getByText("deck.pptx: Operation not permitted")).toBeVisible();
    expect(screen.getByText("1 selected")).toBeVisible(); // the failed one stays selected
  });

  it("opens a right-click menu whose items preview, reveal and trash that file", async () => {
    const bridge = makeBridge();
    mount(bridge);
    fireEvent.contextMenu(await screen.findByText("deck.pptx"));
    expect(screen.getAllByRole("menuitem").map((i) => i.textContent)).toEqual(["Preview", "Rename…", "Show in folder", "Move to Trash"]);
    fireEvent.click(screen.getByRole("menuitem", { name: "Preview" }));
    await waitFor(() => expect(bridge.preview).toHaveBeenCalledWith("b"));
    expect(screen.queryByRole("menu")).toBeNull();
    fireEvent.contextMenu(screen.getByText("paper.pdf"));
    fireEvent.click(screen.getByRole("menuitem", { name: "Show in folder" }));
    expect(bridge.reveal).toHaveBeenCalledWith("a");
    fireEvent.contextMenu(screen.getByText("paper.pdf"));
    fireEvent.click(screen.getByRole("menuitem", { name: "Move to Trash" }));
    await waitFor(() => expect(bridge.trash).toHaveBeenCalledWith("a"));
  });

  it("renames inline from the menu: Enter saves, Esc cancels, and errors are shown", async () => {
    const bridge = makeBridge({
      rename: vi.fn()
        .mockRejectedValueOnce(new Error('A file named "x.pdf" already exists in that folder'))
        .mockImplementation(async (id: string, name: string) => ({ ...file(`${id}2`, name, "wecom") })),
    });
    mount(bridge);
    fireEvent.contextMenu(await screen.findByText("paper.pdf"));
    fireEvent.click(screen.getByRole("menuitem", { name: "Rename…" }));
    const input = screen.getByRole("textbox", { name: "New name for paper.pdf" });
    expect(input).toHaveValue("paper.pdf");
    fireEvent.keyDown(input, { key: "Escape" });
    expect(screen.queryByRole("textbox", { name: "New name for paper.pdf" })).toBeNull();
    expect(bridge.rename).not.toHaveBeenCalled();

    fireEvent.contextMenu(screen.getByText("paper.pdf"));
    fireEvent.click(screen.getByRole("menuitem", { name: "Rename…" }));
    fireEvent.change(screen.getByRole("textbox", { name: "New name for paper.pdf" }), { target: { value: "x.pdf" } });
    fireEvent.keyDown(screen.getByRole("textbox", { name: "New name for paper.pdf" }), { key: "Enter" });
    expect(await screen.findByRole("alert")).toHaveTextContent("already exists");

    fireEvent.contextMenu(screen.getByText("paper.pdf"));
    fireEvent.click(screen.getByRole("menuitem", { name: "Rename…" }));
    fireEvent.change(screen.getByRole("textbox", { name: "New name for paper.pdf" }), { target: { value: "  2026 paper.pdf " } });
    fireEvent.keyDown(screen.getByRole("textbox", { name: "New name for paper.pdf" }), { key: "Enter" });
    await waitFor(() => expect(bridge.rename).toHaveBeenLastCalledWith("a", "2026 paper.pdf"));
    await waitFor(() => expect(screen.queryByRole("textbox", { name: "New name for paper.pdf" })).toBeNull());
    await waitFor(() => expect(bridge.list).toHaveBeenCalledTimes(3));
  });

  it("offers Authorize when Zotero has no key yet and explains a disabled local API", async () => {
    const bridge = makeBridge({ status: vi.fn().mockResolvedValue({ state: "needs_key", url: "u" }) });
    mount(bridge);
    fireEvent.click(await screen.findByRole("button", { name: "Authorize" }));
    await waitFor(() => expect(bridge.authorize).toHaveBeenCalled());

    const disabled = makeBridge({ status: vi.fn().mockResolvedValue({ state: "disabled", url: "u" }) });
    const view = mount(disabled);
    expect(await view.findByText(enZotero.status_disabled)).toBeVisible();
  });
});
