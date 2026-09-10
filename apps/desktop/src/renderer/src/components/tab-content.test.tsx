import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryRouter } from "react-router-dom";
import { AppLink, NavigationProvider } from "@multica/views/navigation";
import { useTabStore, useActiveTabUrl } from "@/stores/tab-store";
import { __resetTabCoordinatorForTests, getAppRouter } from "@/platform/tab-coordinator";
import { TabContent } from "./tab-content";

vi.mock("@/routes", () => ({
  createAppRouter: () => createMemoryRouter([
    { path: "/", element: <div>root</div> },
    { path: "/:workspaceSlug/issues", element: <div>Issues page</div> },
    { path: "/:workspaceSlug/workflows", element: <div>Workflows page</div> },
    { path: "/:workspaceSlug/agents", element: <div>Agents page</div> },
  ]),
}));

function Shell({ client }: { client: QueryClient }) {
  const url = useActiveTabUrl() ?? "/";
  return (
    <QueryClientProvider client={client}>
      <NavigationProvider value={{
        pathname: url,
        searchParams: new URLSearchParams(),
        getShareableUrl: (path) => `http://localhost${path}`,
        push: (path) => useTabStore.getState().navigateActiveSession(path),
        replace: (path) => useTabStore.getState().navigateActiveSession(path, { replace: true }),
        back: () => useTabStore.getState().goBack(),
      }}>
        <nav>
          <AppLink href="/test/issues">Issues</AppLink>
          <AppLink href="/test/workflows">Workflows</AppLink>
          <AppLink href="/test/agents">Agents</AppLink>
        </nav>
        <TabContent />
      </NavigationProvider>
    </QueryClientProvider>
  );
}

beforeEach(() => {
  useTabStore.getState().reset();
  useTabStore.getState().switchWorkspace("test", "/test/issues");
});
afterEach(() => {
  cleanup();
  __resetTabCoordinatorForTests();
  vi.restoreAllMocks();
});

describe("desktop sidebar navigation", () => {
  it("updates page content on consecutive sidebar clicks", async () => {
    render(<Shell client={new QueryClient()} />);
    expect(await screen.findByText("Issues page")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("link", { name: "Workflows" }));
    expect(await screen.findByText("Workflows page")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("link", { name: "Agents" }));
    expect(await screen.findByText("Agents page")).toBeInTheDocument();
  });

  it("reconnects navigation after the coordinator is replaced while React state survives", async () => {
    const client = new QueryClient();
    const subscribe = useTabStore.subscribe;
    let disconnect = () => {};
    vi.spyOn(useTabStore, "subscribe").mockImplementation((listener) => {
      disconnect = subscribe(listener);
      return disconnect;
    });
    const view = render(<Shell client={client} />);
    expect(await screen.findByText("Issues page")).toBeInTheDocument();
    // Fast Refresh re-evaluates modules, but preserves the host's useState.
    act(() => {
      // A replaced module's subscription cannot drive the new module's router.
      disconnect();
      __resetTabCoordinatorForTests();
    });
    view.rerender(<Shell client={client} />);
    fireEvent.click(screen.getByRole("link", { name: "Workflows" }));
    expect(await screen.findByText("Workflows page")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("link", { name: "Agents" }));
    expect(await screen.findByText("Agents page")).toBeInTheDocument();
    act(() => useTabStore.getState().goBack());
    expect(await screen.findByText("Workflows page")).toBeInTheDocument();
  });

  it("detaches the retired router before binding a replacement", async () => {
    const client = new QueryClient();
    const subscribe = useTabStore.subscribe;
    let disconnect = vi.fn();
    vi.spyOn(useTabStore, "subscribe").mockImplementation((listener) => {
      disconnect = vi.fn(subscribe(listener));
      return disconnect;
    });
    const view = render(<Shell client={client} />);
    expect(await screen.findByText("Issues page")).toBeInTheDocument();
    const retired = getAppRouter();
    const dispose = vi.spyOn(retired, "dispose");
    const oldNavigate = vi.spyOn(retired, "navigate");
    act(() => {
      __resetTabCoordinatorForTests();
    });
    expect(dispose).toHaveBeenCalledOnce();
    expect(disconnect).toHaveBeenCalledOnce();
    view.rerender(<Shell client={client} />);
    fireEvent.click(screen.getByRole("link", { name: "Agents" }));
    expect(await screen.findByText("Agents page")).toBeInTheDocument();
    expect(oldNavigate).not.toHaveBeenCalled();
  });
});
