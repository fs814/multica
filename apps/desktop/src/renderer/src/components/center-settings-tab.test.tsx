import { act, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import { RESOURCES } from "@multica/views/locales";
import type { DaemonStatus } from "../../../shared/daemon-types";
import { CenterConnectionPanel } from "./center-settings-tab";

vi.mock("@multica/core/realtime", () => ({ useWS: () => ({ connected: false }) }));
vi.mock("@multica/core/auth", () => ({
  useAuthStore: Object.assign((selector: (state: { user: null }) => unknown) => selector({ user: null }), {
    getState: () => ({ user: null }),
  }),
}));

describe("local daemon connection before Center login", () => {
  it("shows local reachability separately and does not overwrite a newer status event", async () => {
    let emit: (status: DaemonStatus) => void = () => {};
    let resolveInitial: (status: DaemonStatus) => void = () => {};
    Object.defineProperty(window, "daemonAPI", { configurable: true, value: {
      getStatus: () => new Promise<DaemonStatus>(resolve => { resolveInitial = resolve; }),
      onStatusChange: (handler: typeof emit) => { emit = handler; return () => {}; },
    } });
    Object.defineProperty(window, "desktopAPI", { configurable: true, value: {
      runtimeConfig: { ok: true, config: { apiUrl: "http://localhost:8080" } },
    } });
    render(<I18nProvider locale="en" resources={RESOURCES}><CenterConnectionPanel /></I18nProvider>);
    await act(async () => { emit({ state: "running", centerConnected: false, profile: "desktop-services" }); });
    expect(screen.getByTestId("local-daemon-connection")).toHaveTextContent("Desktop → local daemon: Connected");
    expect(screen.getByTestId("daemon-center-connection")).toHaveTextContent("Daemon → Center: Not connected");
    await act(async () => { resolveInitial({ state: "stopped" }); });
    expect(screen.getByTestId("local-daemon-connection")).toHaveTextContent("Connected");
    await act(async () => { emit({ state: "stopped" }); });
    expect(screen.getByTestId("local-daemon-connection")).toHaveTextContent("Stopped");
  });
});
