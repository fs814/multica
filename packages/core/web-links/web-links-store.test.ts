// @vitest-environment node
import { beforeEach, expect, it, vi } from "vitest";
const values = vi.hoisted(() => new Map<string, string>());
vi.mock("../platform/storage", () => ({ defaultStorage: {
  getItem: (key: string) => values.get(key) ?? null,
  setItem: (key: string, value: string) => values.set(key, value),
  removeItem: (key: string) => values.delete(key),
} }));
import { setCurrentWorkspace } from "../platform/workspace-storage";
import { useWebLinksStore } from "./web-links-store";

beforeEach(async () => {
  values.clear();
  setCurrentWorkspace("web-test", "a");
  await Promise.resolve();
  useWebLinksStore.setState({ links: [] });
});

it("persists edits and deletion and isolates links across workspace switches", async () => {
  useWebLinksStore.getState().save({ id: "site", name: "Jenkins", url: "http://localhost:8080" });
  await useWebLinksStore.persist.rehydrate();
  expect(useWebLinksStore.getState().links[0]?.url).toBe("http://localhost:8080/");
  setCurrentWorkspace("other", "b");
  await Promise.resolve();
  expect(useWebLinksStore.getState().links).toEqual([]);
  setCurrentWorkspace("web-test", "a");
  await Promise.resolve();
  expect(useWebLinksStore.getState().links[0]?.name).toBe("Jenkins");
  useWebLinksStore.getState().save({ id: "site", name: "CI", url: "https://ci.example.com" });
  await useWebLinksStore.persist.rehydrate();
  expect(useWebLinksStore.getState().links).toEqual([{ id: "site", name: "CI", url: "https://ci.example.com/" }]);
  useWebLinksStore.getState().remove("site");
  await useWebLinksStore.persist.rehydrate();
  expect(useWebLinksStore.getState().links).toEqual([]);
});
