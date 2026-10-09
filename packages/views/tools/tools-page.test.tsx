import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import type { LocalToolsBridge } from "@multica/core/tools";
import enTools from "../locales/en/tools.json";
import { ToolsPage } from "./tools-page";

vi.mock("../layout/page-header", () => ({ PageHeader: ({ children }: { children: React.ReactNode }) => <header>{children}</header>, PAGE_GUTTER: "" }));

function mount(bridge?: LocalToolsBridge) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(<QueryClientProvider client={client}><I18nProvider locale="en" resources={{ en: { tools: enTools } }}><ToolsPage bridge={bridge} /></I18nProvider></QueryClientProvider>);
}

describe("Tools page", () => {
  it("explains local execution availability on web", () => {
    mount();
    expect(screen.getByText(enTools.desktop_required)).toBeVisible();
    expect(screen.queryByRole("button", { name: "Run" })).toBeNull();
  });
  it("browses folders, runs a selected script and restores running state on remount", async () => {
    const running = { id: "run-1", path: "game/run.sh", status: "running", startedAt: "now", pid: 42, output: "Building…" };
    let runs: unknown[] = [];
    const bridge = {
      catalog: vi.fn().mockResolvedValue({ root: "/Settings/macbuild", platform: "darwin", scripts: [{ path: "game/run.sh", name: "run.sh" }] }),
      runs: vi.fn(async () => runs),
      run: vi.fn(async () => { runs = [running]; return running; }),
      output: vi.fn().mockResolvedValue(running),
      stop: vi.fn().mockResolvedValue(running),
    };
    const view = mount(bridge);
    expect(await screen.findByText("/Settings/macbuild")).toBeVisible();
    fireEvent.click(screen.getByText("game"));
    fireEvent.click(screen.getByRole("button", { name: "run.sh" }));
    fireEvent.click(screen.getByRole("button", { name: "Run" }));
    await waitFor(() => expect(bridge.run).toHaveBeenCalledWith("game/run.sh"));
    expect(await screen.findByRole("button", { name: "Running" })).toBeDisabled();
    expect(await screen.findByText("Building…")).toBeVisible();
    view.unmount();
    mount(bridge);
    await screen.findByText("1 running");
    fireEvent.click(screen.getByText("game"));
    fireEvent.click(screen.getByRole("button", { name: "run.sh Running" }));
    fireEvent.click(screen.getByRole("button", { name: "Stop" }));
    await waitFor(() => expect(bridge.stop).toHaveBeenCalledWith("run-1"));
    expect(bridge.run).toHaveBeenCalledTimes(1);
  });
  it("reports discovery errors and permits retry", async () => {
    const bridge: LocalToolsBridge = { catalog: vi.fn().mockRejectedValue(new Error("Settings directory not found")), runs: vi.fn().mockResolvedValue([]), run: vi.fn(), output: vi.fn(), stop: vi.fn() };
    mount(bridge);
    expect(await screen.findByRole("alert")).toHaveTextContent("Settings directory not found");
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    await waitFor(() => expect(bridge.catalog).toHaveBeenCalledTimes(2));
  });
});
