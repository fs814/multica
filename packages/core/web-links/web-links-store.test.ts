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
import { DEFAULT_WEB_LINKS } from "./default-links";

const own = () => useWebLinksStore.getState().links.filter((link) => !link.id.startsWith("default:"));

beforeEach(async () => {
  values.clear();
  setCurrentWorkspace("web-test", "a");
  await Promise.resolve();
  useWebLinksStore.setState({ links: [], seeded: [] });
});

it("persists edits and deletion and isolates links across workspace switches", async () => {
  useWebLinksStore.getState().save({ id: "site", name: "Jenkins", url: "http://localhost:8080" });
  await useWebLinksStore.persist.rehydrate();
  expect(own()[0]?.url).toBe("http://localhost:8080/");
  setCurrentWorkspace("other", "b");
  await Promise.resolve();
  expect(own()).toEqual([]);
  setCurrentWorkspace("web-test", "a");
  await Promise.resolve();
  expect(own()[0]?.name).toBe("Jenkins");
  useWebLinksStore.getState().save({ id: "site", name: "CI", url: "https://ci.example.com" });
  await useWebLinksStore.persist.rehydrate();
  expect(own()).toEqual([{ id: "site", name: "CI", url: "https://ci.example.com/" }]);
  useWebLinksStore.getState().remove("site");
  await useWebLinksStore.persist.rehydrate();
  expect(own()).toEqual([]);
});

it("offers the default services once per workspace and keeps deletions and edits", async () => {
  setCurrentWorkspace("fresh", "c");
  await Promise.resolve();
  expect(useWebLinksStore.getState().links.map((link) => link.id)).toEqual(DEFAULT_WEB_LINKS.map((link) => link.id));
  useWebLinksStore.getState().remove("default:ntfy");
  useWebLinksStore.getState().save({ id: "default:jenkins", name: "CI", url: "http://ci.local:8080" });
  await useWebLinksStore.persist.rehydrate();
  const links = useWebLinksStore.getState().links;
  expect(links.some((link) => link.id === "default:ntfy")).toBe(false);
  expect(links.find((link) => link.id === "default:jenkins")).toEqual({ id: "default:jenkins", name: "CI", url: "http://ci.local:8080/" });
  expect(links).toHaveLength(DEFAULT_WEB_LINKS.length - 1);
});

it("does not duplicate a default whose URL the workspace already saved", async () => {
  values.clear();
  setCurrentWorkspace("has-memos", "d");
  await Promise.resolve();
  useWebLinksStore.setState({ links: [{ id: "mine", name: "My memos", url: "http://9.134.118.150:5230/" }], seeded: [] });
  await useWebLinksStore.persist.rehydrate();
  const memos = useWebLinksStore.getState().links.filter((link) => link.url === "http://9.134.118.150:5230/");
  expect(memos.map((link) => link.id)).toEqual(["mine"]);
});

it("files defaults under their category once, even for workspaces seeded before categories existed", async () => {
  values.clear();
  setCurrentWorkspace("old", "e");
  await Promise.resolve();
  // A workspace seeded by the previous version: default links without categories.
  useWebLinksStore.setState({ links: [{ id: "default:memos", name: "Memos", url: "http://9.134.118.150:5230/" }], seeded: ["default:memos"] });
  await useWebLinksStore.persist.rehydrate();
  const memos = () => useWebLinksStore.getState().links.find((link) => link.id === "default:memos")!;
  expect(memos().category).toBe("Self-hosted");
  expect(useWebLinksStore.getState().links.find((link) => link.id === "default:sipeed")?.category).toBe("Embedded");
  // Clearing it sticks.
  useWebLinksStore.getState().save({ id: "default:memos", name: "Memos", url: "http://9.134.118.150:5230/", category: "  " });
  await useWebLinksStore.persist.rehydrate();
  expect(memos().category).toBeUndefined();
});

it("files the user's own copy of a default (e.g. a hand-added Jenkins) under the default's category", async () => {
  values.clear();
  setCurrentWorkspace("own-jenkins", "f");
  await Promise.resolve();
  useWebLinksStore.setState({ links: [
    { id: "a7625f6d", name: "jenkins", url: "http://jenkins.lan:8080/" },
    { id: "b1", name: "My memos", url: "http://9.134.118.150:5230/" },
    { id: "c1", name: "Notes", url: "https://notes.example/" },
  ], seeded: ["default:jenkins", "default:memos", "categories:v1"] });
  await useWebLinksStore.persist.rehydrate();
  const byId = (id: string) => useWebLinksStore.getState().links.find((link) => link.id === id);
  expect(byId("a7625f6d")?.category).toBe("Self-hosted");
  expect(byId("b1")?.category).toBe("Self-hosted");
  expect(byId("c1")?.category).toBeUndefined();
});

it("moves DM and FEETECH from Robot to Humanoid robot once, keeping a category the user chose", async () => {
  values.clear();
  setCurrentWorkspace("moves", "g");
  await Promise.resolve();
  useWebLinksStore.setState({ links: [
    { id: "default:dmbot", name: "DM Robot (达妙)", url: "https://dmbot.cn/", category: "Robot" },
    { id: "default:feetech", name: "FEETECH (飞特)", url: "https://www.feetech.cn/", category: "Servos" },
  ], seeded: ["default:dmbot", "default:feetech", "categories:v2"] });
  await useWebLinksStore.persist.rehydrate();
  const byId = (id: string) => useWebLinksStore.getState().links.find((link) => link.id === id);
  expect(byId("default:dmbot")?.category).toBe("Humanoid robot");
  expect(byId("default:feetech")?.category).toBe("Servos");
  expect(byId("default:agilex-piper")?.category).toBe("Humanoid robot");
  // Moving it back by hand sticks.
  useWebLinksStore.getState().save({ id: "default:dmbot", name: "DM", url: "https://dmbot.cn/", category: "Robot" });
  await useWebLinksStore.persist.rehydrate();
  expect(byId("default:dmbot")?.category).toBe("Robot");
});
