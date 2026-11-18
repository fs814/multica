import { describe, expect, it } from "vitest";
import {
  groupContainers, matchesQuery, parseEngineStatus, parseResourceList, shortId, splitCommand, splitEntries, type ContainerInfo,
} from "./models";

const c = (name: string, state: ContainerInfo["state"], project: string | null = null): ContainerInfo =>
  ({ id: name, name, image: "img", state, status: "", ports: "", createdAt: "", command: "", project, networks: [] });

describe("containers models", () => {
  it("parses the bridge payloads, tolerating unknown states", () => {
    expect(parseEngineStatus({ engines: [{ kind: "podman", path: "/p", running: false }], active: "podman" }))
      .toEqual({ engines: [{ kind: "podman", path: "/p", version: null, running: false, error: null }], active: "podman" });
    expect(parseResourceList("containers", [{ id: "1", state: "hibernating" }])[0]).toMatchObject({ id: "1", state: "unknown", name: "", networks: [] });
    expect(parseResourceList("images", "garbage")).toEqual([]);
  });

  it("groups by compose project with running containers first", () => {
    const groups = groupContainers([c("b", "exited"), c("a", "running", "shop"), c("z", "running"), c("db", "exited", "shop"), c("api", "paused", "api")]);
    expect(groups.map((g) => [g.project, g.containers.map((x) => x.name)])).toEqual([
      ["api", ["api"]], ["shop", ["a", "db"]], [null, ["z", "b"]],
    ]);
  });

  it("splits commands like a shell and entries by line or comma", () => {
    expect(splitCommand(`sh -c "echo 'hi there'" ''`)).toEqual(["sh", "-c", "echo 'hi there'", ""]);
    expect(splitCommand("a\\ b  c")).toEqual(["a b", "c"]);
    expect(() => splitCommand('echo "open')).toThrow("Unclosed quote");
    expect(splitEntries("8080:80, 443:443\n\n53:53/udp")).toEqual(["8080:80", "443:443", "53:53/udp"]);
    expect(splitEntries("A=1,2\nB=x", false)).toEqual(["A=1,2", "B=x"]);
  });

  it("matches queries on any field and shortens ids", () => {
    expect(matchesQuery(c("web", "running", "shop"), "SHOP")).toBe(true);
    expect(matchesQuery({ networks: ["frontend"] }, "front")).toBe(true);
    expect(matchesQuery(c("web", "running"), "db")).toBe(false);
    expect(shortId("sha256:0123456789abcdef")).toBe("0123456789ab");
  });
});
