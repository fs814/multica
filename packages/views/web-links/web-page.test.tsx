import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import { useWebLinksStore } from "@multica/core/web-links";
import en from "../locales/en/web-links.json";
import { WebPage } from "./web-page";

vi.mock("../layout/page-header", () => ({ PageHeader: ({ children }: { children: React.ReactNode }) => <header>{children}</header>, PAGE_GUTTER: "" }));
const external = vi.hoisted(() => vi.fn());
vi.mock("../platform/open-external", () => ({ openExternal: external }));
function mount(openSite?: (url: string) => Promise<void>) {
  return render(<I18nProvider locale="en" resources={{ en: { "web-links": en } }}><WebPage openSite={openSite} /></I18nProvider>);
}
beforeEach(() => { useWebLinksStore.setState({ links: [] }); external.mockReset(); });

it("adds, opens, edits and removes a saved site", async () => {
  const openSite = vi.fn().mockResolvedValue(undefined);
  mount(openSite);
  fireEvent.click(screen.getByRole("button", { name: en.add }));
  fireEvent.change(screen.getByLabelText(en.name), { target: { value: "Jenkins" } });
  fireEvent.change(screen.getByLabelText(en.url), { target: { value: "http://localhost:8080" } });
  fireEvent.click(screen.getByRole("button", { name: en.save }));
  expect(screen.getByRole("heading", { name: "Jenkins" })).toBeVisible();
  fireEvent.click(screen.getByRole("button", { name: en.open }));
  await waitFor(() => expect(openSite).toHaveBeenCalledWith("http://localhost:8080/"));
  fireEvent.click(screen.getByRole("button", { name: en.open_browser }));
  expect(external).toHaveBeenCalledWith("http://localhost:8080/");
  fireEvent.click(screen.getByRole("button", { name: "Edit Jenkins" }));
  fireEvent.change(screen.getByLabelText(en.name), { target: { value: "Builds" } });
  fireEvent.click(screen.getByRole("button", { name: en.save }));
  expect(screen.getByRole("heading", { name: "Builds" })).toBeVisible();
  fireEvent.click(screen.getByRole("button", { name: "Remove Builds" }));
  expect(screen.getByText(en.empty)).toBeVisible();
});

it("reports a native page load failure and leaves browser opening available", async () => {
  useWebLinksStore.getState().save({ id: "1", name: "CI", url: "https://ci.example.com" });
  mount(vi.fn().mockRejectedValue(new Error("offline")));
  fireEvent.click(screen.getByRole("button", { name: en.open }));
  expect(await screen.findByRole("alert")).toHaveTextContent(en.open_error);
  expect(screen.getByRole("button", { name: en.open_browser })).toBeEnabled();
});

it("opens sites in the browser on web", () => {
  useWebLinksStore.getState().save({ id: "1", name: "CI", url: "https://ci.example.com" });
  mount();
  fireEvent.click(screen.getByRole("button", { name: en.open_browser }));
  expect(external).toHaveBeenCalledWith("https://ci.example.com/");
});
