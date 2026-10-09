import { fireEvent, render, screen } from "@testing-library/react";
import { expect, it, vi } from "vitest";
const openInNewTab = vi.hoisted(() => vi.fn());
vi.mock("@multica/views/navigation", () => ({ useNavigation: () => ({ openInNewTab }) }));
vi.mock("@multica/core/paths", () => ({ useWorkspacePaths: () => ({ webSite: (id: string) => `/test/web/${id}` }) }));
vi.mock("@multica/core/web-links", () => ({ useWebLinksStore: { getState: () => ({ links: [{ id: "jenkins", name: "Jenkins", url: "http://localhost:8080/" }] }) } }));
vi.mock("@multica/views/web-links", () => ({ WebPage: ({ openSite }: { openSite: (url: string) => void }) => <button onClick={() => openSite("http://localhost:8080/")}>Open site</button> }));
import { DesktopWebPage } from "./desktop-web-page";
it("opens the saved website as a foreground Multica tab", () => {
  render(<DesktopWebPage />);
  fireEvent.click(screen.getByRole("button", { name: "Open site" }));
  expect(openInNewTab).toHaveBeenCalledWith("/test/web/jenkins", "Jenkins", { activate: true });
});
