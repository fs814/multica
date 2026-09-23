import { fireEvent, render, screen } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import App from "./App";

vi.mock("@multica/core/auth", () => ({
  useAuthStore: () => { throw new Error("Auth store not initialised — call registerAuthStore() first"); },
}));
vi.mock("@multica/core/platform", () => ({ CoreProvider: () => { throw new Error("Center mounted while offline"); }, setCurrentWorkspace: vi.fn() }));
vi.mock("@multica/core/i18n", () => ({ pickLocale: () => "en" }));
vi.mock("@multica/core/i18n/react", () => ({ I18nProvider: ({ children }: { children: React.ReactNode }) => children }));
vi.mock("@multica/ui/components/common/theme-provider", () => ({ ThemeProvider: ({ children }: { children: React.ReactNode }) => children }));
vi.mock("@multica/ui/components/ui/sonner", () => ({ Toaster: () => null }));
vi.mock("./components/desktop-mode-picker", () => ({ DesktopModePicker: ({ onLocal }: { onLocal: () => void }) => <button onClick={onLocal}>Enter local mode</button> }));
vi.mock("./components/local-daemon-mode", () => ({ LocalDaemonMode: () => <div>Local workspace</div> }));
vi.mock("./components/update-notification", () => ({ UpdateNotification: () => null }));
vi.mock("./hooks/use-open-settings-shortcut", () => ({ useOpenSettingsShortcut: () => {} }));
vi.mock("./hooks/use-tab-selection-shortcut", () => ({ useTabSelectionShortcut: () => {} }));
vi.mock("./platform/i18n-adapter", () => ({ createDesktopLocaleAdapter: () => ({ getUserChoice: () => null }) }));
vi.mock("./freeze-flush", () => ({ flushFreezeBreadcrumb: vi.fn() }));

it("renders the offline mode picker before any Center auth store exists", () => {
  Object.defineProperty(window, "desktopAPI", { configurable: true, value: {
    appInfo: { version: "test", os: "macos" }, systemLocale: "en",
    runtimeConfig: { ok: false, error: { code: "center_unconfigured", message: "Center not configured" } },
    windowContext: { kind: "main" },
    onCloseActiveTab: () => () => {}, onSystemLocaleChanged: () => () => {},
  } });
  Object.defineProperty(window, "daemonAPI", { configurable: true, value: { setTargetApiUrl: vi.fn() } });
  render(<App />);
  fireEvent.click(screen.getByRole("button", { name: "Enter local mode" }));
  expect(screen.getByText("Local workspace")).toBeInTheDocument();
});
