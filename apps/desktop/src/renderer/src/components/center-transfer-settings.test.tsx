import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import { RESOURCES } from "@multica/views/locales";
import { CenterSettingsTab } from "./center-settings-tab";
import type { CenterRecoveryResult } from "../../../shared/center-recovery";

const source = "https://source.example";
const destination = "https://destination.example";
const center = { get: vi.fn(), save: vi.fn(), connect: vi.fn(), test: vi.fn(), saveTransfer: vi.fn(), transferData: vi.fn() };
const state = { saved: { version: 1 as const, url: source, profile: "desktop-services" }, activeUrl: source, transferUrl: destination };
beforeEach(() => {
  vi.resetAllMocks();
  center.get.mockResolvedValue(state);
  center.saveTransfer.mockImplementation(async (url: string) => ({ ...state, transferUrl: new URL(url.trim()).origin }));
  Object.defineProperty(window, "desktopAPI", { configurable: true, value: { center } });
});
async function show() {
  render(<I18nProvider locale="en" resources={RESOURCES}><CenterSettingsTab /></I18nProvider>);
  const section = screen.getByRole("region", { name: "Transfer to server" });
  await waitFor(() => expect(within(section).getByRole("textbox")).toBeEnabled());
  return within(section);
}
async function openTransfer() {
  const section = await show();
  const trigger = section.getByRole("button", { name: "Transfer data to new center server" });
  await waitFor(() => expect(trigger).toBeEnabled());
  fireEvent.click(trigger);
  const dialog = within(await screen.findByRole("dialog"));
  fireEvent.change(dialog.getByLabelText("Current center recovery token"), { target: { value: "s".repeat(64) } });
  fireEvent.change(dialog.getByLabelText("Destination center recovery token"), { target: { value: "t".repeat(64) } });
  return dialog;
}

describe("center transfer settings", () => {
  it("loads the saved destination, tests its draft, and saves it without changing the active server", async () => {
    const section = await show();
    const address = section.getByRole("textbox", { name: "Transfer to server address" });
    const transfer = section.getByRole("button", { name: "Transfer data to new center server" });
    expect(address).toHaveValue(destination); expect(transfer).toBeEnabled();
    fireEvent.change(address, { target: { value: "https://another.example/" } });
    expect(transfer).toBeDisabled();
    center.test.mockResolvedValue({ reachable: true });
    fireEvent.click(section.getByRole("button", { name: "Test server connection" }));
    await waitFor(() => expect(center.test).toHaveBeenCalledWith("https://another.example/"));
    await waitFor(() => expect(section.getByRole("button", { name: "Save" })).toBeEnabled());
    expect(transfer).toBeDisabled();
    fireEvent.click(section.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(address).toHaveValue("https://another.example"));
    expect(transfer).toBeEnabled();
    expect(center.saveTransfer).toHaveBeenCalledWith("https://another.example/");
    expect(center.save).not.toHaveBeenCalled(); expect(center.connect).not.toHaveBeenCalled();
    expect(screen.getByRole("textbox", { name: "Server address" })).toHaveValue(source);
  });
  it("keeps a failed destination save editable and does not transfer to the old saved address", async () => {
    const section = await show();
    center.saveTransfer.mockRejectedValue(new Error("Cannot save destination"));
    fireEvent.change(section.getByRole("textbox"), { target: { value: "https://unsaved.example" } });
    fireEvent.click(section.getByRole("button", { name: "Save" }));
    expect(await section.findByText("Cannot save destination")).toBeVisible();
    expect(section.getByRole("textbox")).toBeEnabled();
    expect(section.getByRole("button", { name: "Transfer data to new center server" })).toBeDisabled();
    expect(center.transferData).not.toHaveBeenCalled();
  });
  it("waits for transfer completion and keeps the current connection", async () => {
    let complete!: (result: CenterRecoveryResult) => void;
    center.transferData.mockImplementation(() => new Promise(resolve => { complete = resolve; }));
    const dialog = await openTransfer();
    fireEvent.click(dialog.getByRole("button", { name: "Transfer data to new center server" }));
    expect(center.transferData).toHaveBeenCalledWith({ targetUrl: destination, sourceRecoveryToken: "s".repeat(64), targetRecoveryToken: "t".repeat(64) });
    expect(dialog.getByRole("status")).toHaveTextContent("Transferring data");
    expect(dialog.getByRole("button", { name: "Cancel" })).toBeDisabled();
    expect(screen.queryByText(/Data transferred to/)).not.toBeInTheDocument();
    await act(async () => { complete({ cancelled: false }); });
    expect(await screen.findByText(/Data transferred to https:\/\/destination.example/)).toBeVisible();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(center.connect).not.toHaveBeenCalled(); expect(center.save).not.toHaveBeenCalled();
    expect(screen.getByRole("textbox", { name: "Server address" })).toHaveValue(source);
  });
  it("keeps tokens and the dialog available after failure, then allows retry", async () => {
    center.transferData.mockRejectedValueOnce(new Error("Destination recovery token is incorrect"));
    const dialog = await openTransfer();
    fireEvent.click(dialog.getByRole("button", { name: "Transfer data to new center server" }));
    expect(await dialog.findByText("Destination recovery token is incorrect")).toBeVisible();
    expect(dialog.getByLabelText("Destination center recovery token")).toHaveValue("t".repeat(64));
    center.transferData.mockResolvedValueOnce({ cancelled: false });
    fireEvent.click(dialog.getByRole("button", { name: "Transfer data to new center server" }));
    expect(await screen.findByText(/Data transferred to/)).toBeVisible();
  });
  it("does not report success when the native confirmation is cancelled", async () => {
    center.transferData.mockResolvedValue({ cancelled: true });
    const dialog = await openTransfer();
    fireEvent.click(dialog.getByRole("button", { name: "Transfer data to new center server" }));
    await waitFor(() => expect(dialog.getByRole("button", { name: "Cancel" })).toBeEnabled());
    expect(screen.queryByText(/Data transferred to/)).not.toBeInTheDocument();
    expect(center.connect).not.toHaveBeenCalled();
  });
  it.each([
    { ...state, transferUrl: source },
    { ...state, activeUrl: "" },
    { ...state, transferUrl: "" },
  ])("requires a source and a saved, different destination", async value => {
    center.get.mockResolvedValue(value);
    const section = await show();
    expect(section.getByRole("button", { name: "Transfer data to new center server" })).toBeDisabled();
  });
});
