import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import { RESOURCES } from "@multica/views/locales";
import { CenterRecoveryActions } from "./center-recovery-actions";
const logout = vi.hoisted(() => vi.fn());
vi.mock("@multica/core/auth", () => ({ useAuthStore: { getState: () => ({ logout }) } }));
afterEach(() => { cleanup(); vi.clearAllMocks(); });
function setup() {
  const api = { exportData: vi.fn(), importData: vi.fn(), importStatus: vi.fn() };
  Object.defineProperty(window, "desktopAPI", { configurable: true, value: { center: api } });
  const message = vi.fn();
  render(<I18nProvider locale="en" resources={RESOURCES}><CenterRecoveryActions disabled={false} onBusyChange={vi.fn()} onMessage={message} /></I18nProvider>);
  return { api, message };
}
async function open(mode: "export" | "import") {
  fireEvent.click(screen.getByRole("button", { name: mode === "export" ? "Export center data" : "Import and use center data" }));
  const dialog = await screen.findByRole("dialog");
  fireEvent.change(within(dialog).getByLabelText("Backup password (at least 12 characters)"), { target: { value: "portable backup password" } });
  if (mode === "export") fireEvent.change(within(dialog).getByLabelText("Confirm backup password"), { target: { value: "portable backup password" } });
  return dialog;
}
describe("center recovery settings actions", () => {
  it("shows both buttons and keeps the export dialog open on failure", async () => {
    const { api } = setup(); expect(screen.getByRole("button", { name: "Import and use center data" })).toBeEnabled();
    api.exportData.mockRejectedValue(new Error("Center update required")); const dialog = await open("export");
    fireEvent.click(within(dialog).getByRole("button", { name: "Export center data" }));
    expect(await within(dialog).findByText("Center update required")).toBeVisible();
    expect(within(dialog).getByLabelText("Backup password (at least 12 characters)")).toHaveValue("portable backup password");
  });
  it("does not report success when native export is cancelled", async () => {
    const { api, message } = setup(); api.exportData.mockResolvedValue({ cancelled: true }); const dialog = await open("export");
    fireEvent.click(within(dialog).getByRole("button", { name: "Export center data" }));
    await waitFor(() => expect(api.exportData).toHaveBeenCalledOnce()); expect(message).not.toHaveBeenCalledWith("Center backup exported.");
  });
  it("signs out only after the imported center reports completion", async () => {
    const { api, message } = setup(); api.importData.mockResolvedValue({ cancelled: false, jobId: "a".repeat(32) });
    api.importStatus.mockResolvedValue({ state: "complete" }); const dialog = await open("import");
    fireEvent.click(within(dialog).getByRole("button", { name: "Import and use center data" }));
    await waitFor(() => expect(logout).toHaveBeenCalledOnce()); expect(message).toHaveBeenCalledWith("Imported center data is now active. Sign in again.");
  });
  it("keeps the current session when restore fails", async () => {
    const { api } = setup(); api.importData.mockResolvedValue({ cancelled: false, jobId: "a".repeat(32) }); api.importStatus.mockResolvedValue({ state: "failed", message: "Restore failed" });
    const dialog = await open("import");fireEvent.click(within(dialog).getByRole("button", { name: "Import and use center data" }));
    expect(await within(dialog).findByText("Restore failed")).toBeVisible(); expect(logout).not.toHaveBeenCalled();
  });
});
