// @vitest-environment jsdom
import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import userEvent from "@testing-library/user-event";
import enClis from "../../locales/en/clis.json";
import { ClisPage } from "./clis-page";

const state = vi.hoisted(() => ({ daemonId: null as string | null }));
const machines = [
  { id: "remote", owner_id: "owner", daemon_id: "remote-daemon", status: "online", name: "Remote", machine_name: "Remote" },
  { id: "local", owner_id: "owner", daemon_id: "local-daemon", status: "online", name: "Mac Studio", machine_name: "Mac Studio" },
];
vi.mock("@multica/core/auth", () => {
  const value = { user: { id: "owner" } };
  return { useAuthStore: Object.assign((select: (v: typeof value) => unknown) => select(value), { getState: () => value }) };
});
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "workspace" }));
vi.mock("../../platform/use-local-daemon-status", () => ({ useLocalDaemonStatus: () => ({ ...state, running: true, deviceName: "Mac Studio" }) }));
vi.mock("@tanstack/react-query", async (original) => ({
  ...await original<typeof import("@tanstack/react-query")>(),
  useQuery: ({ queryKey }: { queryKey: string[] }) => queryKey[1] === "clis"
    ? { data: { entries: queryKey[2] === "local" ? [{ key: "gh-trending", label: "GitHub Trending" }] : [{ key: "zhihu", label: "Zhihu CLI" }], registryPath: "/test/clis.json" }, refetch: vi.fn() }
    : { data: machines },
}));
vi.mock("./cli-run-section", () => ({ CLIRunSection: ({ entry }: { entry: { label: string } }) => <div>{entry.label}</div> }));
function Page() {
  return <I18nProvider locale="en" resources={{ en: { clis: enClis } }}><ClisPage /></I18nProvider>;
}
beforeEach(() => { state.daemonId = null; });
describe("CLI machine selection", () => {
  it("switches the automatic default to this desktop when its status arrives", () => {
    const view = render(<Page />);
    expect(screen.getByText("Zhihu CLI")).toBeVisible();
    state.daemonId = "local-daemon";
    view.rerender(<Page />);
    expect(screen.getByText("GitHub Trending")).toBeVisible();
    expect(screen.queryByText("Zhihu CLI")).toBeNull();
  });
  it("preserves an explicit machine choice after desktop status changes", async () => {
    state.daemonId = "local-daemon";
    const view = render(<Page />);
    const user = userEvent.setup();
    await user.click(screen.getByRole("combobox"));
    await user.click(await screen.findByRole("option", { name: /Remote/ }));
    expect(await screen.findByText("Zhihu CLI")).toBeVisible();
    state.daemonId = null;
    view.rerender(<Page />);
    state.daemonId = "local-daemon";
    view.rerender(<Page />);
    expect(screen.getByText("Zhihu CLI")).toBeVisible();
  });
});
