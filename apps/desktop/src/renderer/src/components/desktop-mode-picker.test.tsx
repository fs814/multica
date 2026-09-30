import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import { RESOURCES } from "@multica/views/locales";
import { DesktopModePicker } from "./desktop-mode-picker";

vi.mock("@multica/views/platform", () => ({ DragStrip: () => null }));
vi.mock("@multica/core/auth", () => ({ useAuthStore: () => { throw new Error("Auth must not mount in the mode picker"); } }));
vi.mock("@multica/core/realtime", () => ({ useWS: () => { throw new Error("Center must not connect in the mode picker"); } }));

const saved = { version: 1 as const, url: "https://old-center.example", profile: "desktop-services" };
const center = {
  get: vi.fn(), save: vi.fn(), connect: vi.fn(), test: vi.fn(),
};

beforeEach(() => {
  vi.resetAllMocks();
  center.get.mockResolvedValue({ saved: null, activeUrl: "" });
  center.save.mockImplementation(async (url: string) => ({ saved: { ...saved, url }, activeUrl: "" }));
  center.connect.mockResolvedValue(undefined);
  Object.defineProperty(window, "desktopAPI", { configurable: true, value: { center } });
  Object.defineProperty(window, "daemonAPI", { configurable: true, value: {
    getStatus: vi.fn().mockResolvedValue({ state: "running" }),
    onStatusChange: () => () => {},
  } });
});

function showPicker(onLocal = vi.fn()) {
  render(<I18nProvider locale="en" resources={RESOURCES}><DesktopModePicker onLocal={onLocal} /></I18nProvider>);
}

describe("Desktop mode selection", () => {
  it("shows Center settings immediately without blocking offline local mode", async () => {
    const onLocal = vi.fn();
    showPicker(onLocal);
    expect(screen.getByRole("textbox", { name: "Server address" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Enter local mode" }));
    expect(onLocal).toHaveBeenCalledOnce();
    expect(center.save).not.toHaveBeenCalled();
    expect(center.connect).not.toHaveBeenCalled();
    await waitFor(() => expect(screen.getByRole("textbox", { name: "Server address" })).toBeEnabled());
    expect(screen.getByRole("button", { name: "Save and enter server mode" })).toBeDisabled();
  });

  it("allows changing a saved server and waits for saving before entering server mode", async () => {
    center.get.mockResolvedValue({ saved, activeUrl: saved.url });
    let finishSave!: (value: unknown) => void;
    center.save.mockImplementation(() => new Promise(resolve => { finishSave = resolve; }));
    showPicker();
    const address = screen.getByRole("textbox", { name: "Server address" });
    await waitFor(() => expect(address).toHaveValue(saved.url));
    fireEvent.change(address, { target: { value: "https://new-center.example" } });
    fireEvent.click(screen.getByRole("button", { name: "Save and enter server mode" }));
    expect(center.save).toHaveBeenCalledWith("https://new-center.example");
    expect(center.connect).not.toHaveBeenCalled();
    expect(address).toBeDisabled();
    finishSave({ saved: { ...saved, url: "https://new-center.example" }, activeUrl: saved.url });
    await waitFor(() => expect(center.connect).toHaveBeenCalledWith(true));
  });

  it("can configure the first server directly from the picker", async () => {
    showPicker();
    const address = screen.getByRole("textbox", { name: "Server address" });
    await waitFor(() => expect(address).toBeEnabled());
    fireEvent.change(address, { target: { value: "http://localhost:18080" } });
    fireEvent.click(screen.getByRole("button", { name: "Save and enter server mode" }));
    await waitFor(() => expect(center.connect).toHaveBeenCalledWith(true));
    expect(center.save).toHaveBeenCalledWith("http://localhost:18080");
  });

  it("keeps the address editable and does not connect when saving fails", async () => {
    center.get.mockResolvedValue({ saved, activeUrl: saved.url });
    center.save.mockRejectedValue(new Error("Invalid server address"));
    showPicker();
    const address = screen.getByRole("textbox", { name: "Server address" });
    await waitFor(() => expect(address).toBeEnabled());
    fireEvent.click(screen.getByRole("button", { name: "Save and enter server mode" }));
    expect(await screen.findByText("Invalid server address")).toBeInTheDocument();
    expect(center.connect).not.toHaveBeenCalled();
    expect(address).toHaveValue(saved.url);
    expect(address).toBeEnabled();
  });

  it("shows connection errors and allows retry without silently entering the old server", async () => {
    center.get.mockResolvedValue({ saved, activeUrl: saved.url });
    center.connect.mockRejectedValueOnce(new Error("Wait for running tasks to finish"));
    showPicker();
    await waitFor(() => expect(screen.getByRole("textbox", { name: "Server address" })).toBeEnabled());
    const enter = screen.getByRole("button", { name: "Save and enter server mode" });
    fireEvent.click(enter);
    expect(await screen.findByText("Wait for running tasks to finish")).toBeInTheDocument();
    expect(enter).toBeEnabled();
    fireEvent.click(enter);
    await waitFor(() => expect(center.connect).toHaveBeenCalledTimes(2));
  });
});
