import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { DesktopModePicker } from "./desktop-mode-picker";

vi.mock("@multica/views/platform", () => ({ DragStrip: () => null }));
vi.mock("./center-settings-tab", () => ({
  LocalDaemonConnection: () => <span>Local daemon connected</span>,
  CenterSettingsTab: () => <span>Center settings form</span>,
}));

describe("Desktop mode selection", () => {
  it("enters local mode without any Center configuration", () => {
    const onLocal = vi.fn();
    const onCenter = vi.fn();
    render(<DesktopModePicker centerConfigured={false} onLocal={onLocal} onCenter={onCenter} />);
    expect(screen.getByText("Local daemon connected")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Enter local mode" }));
    expect(onLocal).toHaveBeenCalledOnce();
    expect(onCenter).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Set up Center" }));
    expect(screen.getByText("Center settings form")).toBeInTheDocument();
  });

  it("opens the regular workspace only when Center is configured", () => {
    const onCenter = vi.fn();
    render(<DesktopModePicker centerConfigured onLocal={vi.fn()} onCenter={onCenter} />);
    fireEvent.click(screen.getByRole("button", { name: "Open Multica workspace" }));
    expect(onCenter).toHaveBeenCalledOnce();
  });
});
