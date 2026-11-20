import type { ReactNode } from "react";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import type { LocalContainersBridge } from "@multica/core/containers";
import enContainers from "../locales/en/containers.json";
import { ContainersPage } from "./containers-page";

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
  ContextMenuItem: ({ children, onClick, disabled }: { children: ReactNode; onClick?: () => void; disabled?: boolean }) =>
    <button role="menuitem" disabled={disabled} onClick={onClick}>{children}</button>,
}));

const container = (id: string, name: string, state: string, project: string | null = null) =>
  ({ id, name, image: `${name}:latest`, state, status: state === "running" ? "Up 2 minutes" : "Exited (0)", ports: state === "running" ? "0.0.0.0:8080->80/tcp" : "", createdAt: "2026-11-18T02:00:00.000Z", command: "", project, networks: [] });

function makeBridge(over: Partial<LocalContainersBridge> = {}) {
  return {
    status: vi.fn().mockResolvedValue({ engines: [{ kind: "docker", path: "/usr/bin/docker", version: "Docker version 28.1", running: true, error: null }], active: "docker" }),
    useEngine: vi.fn().mockResolvedValue(true),
    startEngine: vi.fn().mockResolvedValue("Machine started"),
    list: vi.fn(async (resource: string) => ({
      containers: [container("c1", "web", "running", "shop"), container("c2", "db", "exited", "shop"), container("c3", "scratch", "exited")],
      images: [{ id: "sha256:aaaaaaaaaaaaaaaa", repository: "alpine", tag: "3.20", ref: "alpine:3.20", size: "8.8MB", createdAt: "", containers: 1 }],
      volumes: [{ name: "pgdata", driver: "local", mountpoint: "/var/lib/docker/volumes/pgdata/_data", createdAt: "" }],
      networks: [{ id: "n1", name: "bridge", driver: "bridge", scope: "local", internal: false }, { id: "n2", name: "shop_default", driver: "bridge", scope: "local", internal: false }],
    } as Record<string, unknown>)[resource]),
    containerAction: vi.fn().mockResolvedValue(true),
    removeContainer: vi.fn().mockResolvedValue(true),
    relatedPlan: vi.fn(async (ids: string[]) => ({
      containers: ids.map((id) => ({ id, name: ({ c1: "web", c2: "db", c3: "scratch" } as Record<string, string>)[id] ?? id })),
      images: [{ id: "i1", name: "web:latest" }], volumes: ["web-data"], networks: [],
      kept: [{ kind: "network", name: "shop_default", usedBy: ["db"] }],
    })),
    removeWithRelated: vi.fn(async (ids: string[], o: { images: boolean; volumes: boolean }) => ({
      removed: { containers: ids, images: o.images ? ["web:latest"] : [], volumes: o.volumes ? ["web-data"] : [], networks: [] }, kept: [], failed: [],
    })),
    renameContainer: vi.fn().mockResolvedValue(true),
    logs: vi.fn().mockResolvedValue("hello from web\n"),
    inspect: vi.fn().mockResolvedValue('{ "Id": "c1" }'),
    exec: vi.fn().mockResolvedValue(true),
    attach: vi.fn().mockResolvedValue(true),
    run: vi.fn(async (o: { interactive: boolean }) => (o.interactive ? true : "f00dfeedcafe1234")),
    pull: vi.fn().mockResolvedValue("Pulled"),
    remove: vi.fn().mockResolvedValue(true),
    prune: vi.fn().mockResolvedValue("Total reclaimed space: 1.2GB"),
    ...over,
  } satisfies LocalContainersBridge;
}

function mount(bridge?: LocalContainersBridge) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(<QueryClientProvider client={client}><I18nProvider locale="en" resources={{ en: { containers: enContainers } }}><ContainersPage bridge={bridge} /></I18nProvider></QueryClientProvider>);
}

describe("Containers page", () => {
  it("explains that it needs Desktop", () => {
    mount();
    expect(screen.getByText(/Open Multica Desktop to manage the containers/)).toBeInTheDocument();
  });

  it("says when no engine is installed, and starts a stopped one", async () => {
    mount(makeBridge({ status: vi.fn().mockResolvedValue({ engines: [], active: null }) }));
    expect(await screen.findByRole("alert")).toHaveTextContent("No container engine found");

    const bridge = makeBridge({ status: vi.fn().mockResolvedValue({ engines: [{ kind: "podman", path: "/p", version: "podman version 6", running: false, error: "Cannot connect to Podman" }], active: "podman" }) });
    mount(bridge);
    expect(await screen.findByText("podman is not running: Cannot connect to Podman")).toBeInTheDocument();
    expect(bridge.list).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: /Start podman/ }));
    await waitFor(() => expect(bridge.startEngine).toHaveBeenCalledWith("podman"));
    expect(await screen.findByText("Machine started")).toBeInTheDocument();
  });

  it("selects rows with click, Shift-click and Ctrl-click, then stops and removes them in bulk", async () => {
    const bridge = makeBridge({
      containerAction: vi.fn(async (id: string) => { if (id === "c3") throw new Error("boom"); return true; }),
    });
    mount(bridge);
    // On-screen order: project "shop" (web running first, then db), then the ungrouped scratch.
    fireEvent.click(await screen.findByText("web"));
    expect(screen.getByText("1 selected")).toBeInTheDocument();
    fireEvent.click(screen.getByText("scratch"), { shiftKey: true });
    expect(screen.getByText("3 selected")).toBeInTheDocument();
    expect(screen.getByRole("row", { name: "Select db" })).toHaveAttribute("aria-selected", "true");
    fireEvent.click(screen.getByText("db"), { ctrlKey: true });
    expect(screen.getByText("2 selected")).toBeInTheDocument();
    expect(screen.getByRole("row", { name: "Select db" })).toHaveAttribute("aria-selected", "false");
    // A row's own buttons act on that row only and leave the selection alone.
    fireEvent.click(screen.getByRole("button", { name: "Logs db" }));
    fireEvent.click(screen.getByRole("button", { name: "close dialog" }));
    expect(screen.getByText("2 selected")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Stop" }));
    await waitFor(() => expect(bridge.containerAction).toHaveBeenCalledTimes(2));
    expect(bridge.containerAction).toHaveBeenCalledWith("c1", "stop");
    expect(bridge.containerAction).toHaveBeenCalledWith("c3", "stop");
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Stop: 1 of 2 done.");
    expect(alert).toHaveTextContent("scratch: boom");

    // Removing containers offers what only they use; volumes (data) must be opted into.
    fireEvent.click(screen.getByRole("button", { name: "Remove" }));
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText("Remove 2 container(s)")).toBeInTheDocument();
    await waitFor(() => expect(bridge.relatedPlan).toHaveBeenCalledWith(["c1", "c3"]));
    const volumes = await within(dialog).findByRole("checkbox", { name: /Also remove their volumes/ });
    expect(volumes).not.toBeChecked();
    fireEvent.click(volumes);
    fireEvent.click(within(dialog).getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(bridge.removeWithRelated).toHaveBeenCalledWith(["c1", "c3"], { images: true, volumes: true, networks: false, force: true }));
    expect(await screen.findByText("Removed 2 container(s), 1 image(s), 1 volume(s), 0 network(s).")).toBeInTheDocument();
    expect(bridge.removeContainer).not.toHaveBeenCalled();
    expect(screen.queryByText(/ selected$/)).toBeNull(); // done: selection cleared
  });

  it("selects a whole compose project from its header", async () => {
    mount(makeBridge());
    fireEvent.click(await screen.findByText("shop"));
    expect(screen.getByText("2 selected")).toBeInTheDocument();
    expect(screen.getByRole("row", { name: "Select web" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("row", { name: "Select scratch" })).toHaveAttribute("aria-selected", "false");
  });

  it("never removes a built-in network in a bulk remove, and clears the selection on tab change", async () => {
    const bridge = makeBridge();
    mount(bridge);
    await screen.findByText("web");
    fireEvent.click(screen.getByRole("tab", { name: "Networks" }));
    fireEvent.click(await screen.findByRole("row", { name: "Select bridge" }));
    fireEvent.click(screen.getByRole("row", { name: "Select shop_default" }), { shiftKey: true });
    expect(screen.getByText("2 selected")).toBeInTheDocument();
    fireEvent.click(within(screen.getByRole("toolbar", { name: "2 selected" })).getByRole("button", { name: "Remove" }));
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(bridge.remove).toHaveBeenCalledTimes(1));
    expect(bridge.remove).toHaveBeenCalledWith("networks", "n2", false);
    fireEvent.click(screen.getByRole("tab", { name: "Volumes" }));
    expect(screen.queryByText(/selected$/)).toBeNull();
  });

  it("lists containers by compose project, filters them, and starts / stops / opens a shell", async () => {
    const bridge = makeBridge();
    mount(bridge);
    const shop = await screen.findByRole("rowgroup", { name: "shop" });
    // Running first within the project.
    expect(within(shop).getAllByText(/^(web|db)$/).map((e) => e.textContent)).toEqual(["web", "db"]);
    expect(screen.getByRole("rowgroup", { name: "Not in a compose project" })).toHaveTextContent("scratch");
    expect(screen.getByText("3 of 3")).toBeInTheDocument();

    fireEvent.click(screen.getByLabelText("Running only"));
    expect(screen.getByText("1 of 3")).toBeInTheDocument();
    fireEvent.click(screen.getByLabelText("Running only"));
    fireEvent.change(screen.getByLabelText("Filter"), { target: { value: "scr" } });
    expect(screen.getByText("1 of 3")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Filter"), { target: { value: "" } });

    fireEvent.click(screen.getByRole("button", { name: "Stop web" }));
    await waitFor(() => expect(bridge.containerAction).toHaveBeenCalledWith("c1", "stop"));
    expect(await screen.findByText("Done: Stop web")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Start db" }));
    await waitFor(() => expect(bridge.containerAction).toHaveBeenCalledWith("c2", "start"));
    fireEvent.click(screen.getByRole("button", { name: "Open shell (sh) web" }));
    await waitFor(() => expect(bridge.exec).toHaveBeenCalledWith("c1", "sh"));
    expect(await screen.findByText("Opened in a terminal: web sh")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Start interactively (start -ai) scratch" }));
    await waitFor(() => expect(bridge.attach).toHaveBeenCalledWith("c3"));
  });

  it("offers every action in the row menu, confirming destructive ones", async () => {
    const bridge = makeBridge();
    mount(bridge);
    fireEvent.contextMenu(await screen.findByText("web"));
    const menu = screen.getByRole("menu");
    expect(within(menu).getAllByRole("menuitem").map((b) => b.textContent)).toEqual([
      "Stop", "Restart", "Pause", "Kill", "Open shell (sh)", "Open shell (bash)", "Logs", "Inspect", "Rename", "Copy ID", "Remove",
    ]);
    fireEvent.click(within(menu).getByRole("menuitem", { name: "Remove" }));
    const dialog = screen.getByRole("dialog");
    expect(dialog).toHaveTextContent("Remove 1 container(s)");
    expect(await within(dialog).findByText("web:latest")).toBeInTheDocument();
    expect(dialog).toHaveTextContent("shop_default — used by db"); // kept, with who still uses it
    expect(within(dialog).getByRole("checkbox", { name: /Also remove their networks/ })).toBeDisabled(); // nothing to free
    expect(within(dialog).getByLabelText("Force (even if running or in use)")).toBeChecked();
    fireEvent.click(within(dialog).getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(bridge.removeWithRelated).toHaveBeenCalledWith(["c1"], { images: true, volumes: false, networks: false, force: true }));

    fireEvent.contextMenu(screen.getByText("web"));
    fireEvent.click(screen.getByRole("menuitem", { name: "Rename" }));
    fireEvent.change(screen.getByLabelText("New name"), { target: { value: "web-2" } });
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Rename" }));
    await waitFor(() => expect(bridge.renameContainer).toHaveBeenCalledWith("c1", "web-2"));

    fireEvent.contextMenu(screen.getByText("web"));
    fireEvent.click(screen.getByRole("menuitem", { name: "Inspect" }));
    await waitFor(() => expect(screen.getByLabelText("Inspect")).toHaveTextContent('{ "Id": "c1" }'));
    expect(bridge.inspect).toHaveBeenCalledWith("containers", "c1");
  });

  it("shows logs with a line count and timestamps", async () => {
    const bridge = makeBridge();
    mount(bridge);
    fireEvent.click(await screen.findByRole("button", { name: "Logs web" }));
    await waitFor(() => expect(screen.getByLabelText("Logs")).toHaveTextContent("hello from web"));
    expect(bridge.logs).toHaveBeenLastCalledWith("c1", 500, false);
    fireEvent.change(screen.getByLabelText("Last lines"), { target: { value: "2000" } });
    fireEvent.click(screen.getByLabelText("Timestamps"));
    await waitFor(() => expect(bridge.logs).toHaveBeenLastCalledWith("c1", 2000, true));
  });

  it("runs an image interactively or in the background, pulls and prunes", async () => {
    const bridge = makeBridge();
    mount(bridge);
    fireEvent.click(await screen.findByRole("tab", { name: "Images" }));
    fireEvent.click(await screen.findByRole("button", { name: "Run interactively… alpine:3.20" }));
    let dialog = screen.getByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText(/^Command/), { target: { value: `sh -c "echo hi"` } });
    fireEvent.change(within(dialog).getByLabelText(/^Ports/), { target: { value: "8080:80, 53:53/udp" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Run" }));
    await waitFor(() => expect(bridge.run).toHaveBeenCalledWith({
      image: "alpine:3.20", interactive: true, remove: true, name: undefined, command: ["sh", "-c", "echo hi"],
      ports: ["8080:80", "53:53/udp"], env: [], volumes: [],
    }));
    expect(await screen.findByText("Opened in a terminal: alpine:3.20")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Run in the background… alpine:3.20" }));
    dialog = screen.getByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText(/^Container name/), { target: { value: "bg1" } });
    fireEvent.change(within(dialog).getByLabelText(/^Environment/), { target: { value: "A=1,2\nB=x" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Run" }));
    await waitFor(() => expect(bridge.run).toHaveBeenLastCalledWith(expect.objectContaining({ interactive: false, remove: false, name: "bg1", env: ["A=1,2", "B=x"] })));
    expect(await screen.findByText("Started container f00dfeedcafe")).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Image to pull, e.g. alpine:3.20"), { target: { value: "nginx:1.27" } });
    fireEvent.click(screen.getByRole("button", { name: "Pull" }));
    await waitFor(() => expect(bridge.pull).toHaveBeenCalledWith("nginx:1.27"));

    fireEvent.click(screen.getByRole("button", { name: "Prune" }));
    dialog = screen.getByRole("dialog");
    expect(dialog).toHaveTextContent("Remove dangling images?");
    fireEvent.click(within(dialog).getByLabelText(/Also unused images/));
    fireEvent.click(within(dialog).getByRole("button", { name: "Prune" }));
    await waitFor(() => expect(bridge.prune).toHaveBeenCalledWith("images", true));
    expect(await screen.findByText("Total reclaimed space: 1.2GB")).toBeInTheDocument();
  });

  it("manages volumes and networks, protecting built-in networks", async () => {
    const bridge = makeBridge();
    mount(bridge);
    fireEvent.click(await screen.findByRole("tab", { name: "Volumes" }));
    expect(await screen.findByText("pgdata")).toBeInTheDocument();
    fireEvent.click(screen.getAllByRole("button", { name: "Remove" })[0]!);
    expect(screen.getByRole("dialog")).toHaveTextContent("Remove volume pgdata? Its data is deleted.");
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(bridge.remove).toHaveBeenCalledWith("volumes", "pgdata", false));

    fireEvent.click(screen.getByRole("tab", { name: "Networks" }));
    expect(await screen.findByText("shop_default")).toBeInTheDocument();
    const [bridgeRemove, shopRemove] = screen.getAllByRole("button", { name: "Remove" });
    expect(bridgeRemove).toBeDisabled();
    expect(shopRemove).toBeEnabled();
  });

  it("reports an engine error", async () => {
    const bridge = makeBridge({ containerAction: vi.fn().mockRejectedValue(new Error("No such container: web")) });
    mount(bridge);
    fireEvent.click(await screen.findByRole("button", { name: "Stop web" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("No such container: web");
  });
});
