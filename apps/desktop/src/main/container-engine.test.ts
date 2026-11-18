// @vitest-environment node
import { chmodSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  ContainerEngine, cliError, listArgs, normalizeContainers, normalizeImages, parseJsonRecords, runArgs,
  validRef, validRunOptions, validShell,
} from "./container-engine";

// Real output shapes: Docker `--format '{{json .}}'` (one object per line) and Podman `--format json` (an array).
const DOCKER_PS = [
  { ID: "a1b2c3", Names: "web-1", Image: "nginx:1.27", State: "running", Status: "Up 3 minutes", Ports: "0.0.0.0:8080->80/tcp",
    CreatedAt: "2026-11-18 10:00:00 +0800 CST", Command: "\"/docker-entrypoint.sh nginx -g 'daemon off;'\"",
    Labels: "com.docker.compose.project=shop,com.docker.compose.service=web", Networks: "shop_default" },
  { ID: "d4e5f6", Names: "old", Image: "alpine", State: "", Status: "Exited (0) 2 hours ago", Ports: "", CreatedAt: "2026-11-17", Command: "sh", Labels: "", Networks: "bridge" },
].map((x) => JSON.stringify(x)).join("\n");
const PODMAN_PS = JSON.stringify([
  { Id: "p1", Names: ["db"], Image: "docker.io/library/postgres:17", State: "exited", Status: "Exited (1) 5 seconds ago",
    Ports: [{ host_ip: "", container_port: 5432, host_port: 5433, range: 1, protocol: "tcp" }],
    Created: 1763460000, Command: ["postgres"], Labels: { "io.podman.compose.project": "api" }, Networks: ["podman"] },
]);
const PODMAN_IMAGES = JSON.stringify([
  { Id: "sha256abc", RepoTags: ["docker.io/library/alpine:3.20", "localhost:5000/team/alpine:edge"], Size: 8_830_000, Created: 1763000000, Containers: 2 },
  { Id: "sha256none", RepoTags: [], Size: 1200, Created: 1763000000, Containers: 0 },
]);

let dir: string;
let log: string;

function fakeCli(name: string, cases: Record<string, { out?: string; err?: string; code?: number }>) {
  const branches = Object.entries(cases).map(([prefix, r], i) => {
    const out = join(dir, `${name}-${i}.out`);
    writeFileSync(out, r.out ?? "");
    const err = join(dir, `${name}-${i}.err`);
    writeFileSync(err, r.err ?? "");
    return `  "${prefix}"*) /bin/cat '${out}'; /bin/cat '${err}' >&2; exit ${r.code ?? 0};;`;
  });
  const path = join(dir, name);
  writeFileSync(path, `#!/bin/sh\necho "${name} $*" >> '${log}'\ncase "$*" in\n${branches.join("\n")}\n  *) exit 0;;\nesac\n`);
  chmodSync(path, 0o755);
}

const calls = () => readFileSync(log, "utf8").trim().split("\n");
const engine = () => new ContainerEngine({ platform: "linux", home: dir, env: { PATH: "" }, searchDirs: [dir] });

beforeEach(() => {
  dir = mkdtempSync(join(tmpdir(), "containers-"));
  log = join(dir, "calls.log");
  writeFileSync(log, "");
  fakeCli("docker", {
    "--version": { out: "Docker version 28.1.0, build abc\n" },
    "ps -q": { out: "a1b2c3\n" },
    "ps -a": { out: DOCKER_PS },
    "image inspect": { out: '[{"Id":"sha256:1","RepoTags":["nginx:1.27"]}]' },
    "logs": { out: "line 1\n", err: "warn 2\n" },
    "start missing": { err: "Error response from daemon: No such container: missing\n", code: 1 },
    "run -d": { out: "Unable to find image 'alpine' locally\nf00dfeed\n" },
  });
  fakeCli("podman", {
    "--version": { out: "podman version 6.0.0-dev\n" },
    "ps -q": { err: "Cannot connect to Podman. Please verify your connection\nError: unable to connect\n", code: 125 },
  });
  // An uninstalled Docker.app leaves a dangling link; it must not count as nerdctl/docker.
  symlinkSync(join(dir, "gone/nerdctl"), join(dir, "nerdctl"));
});
afterEach(() => rmSync(dir, { recursive: true, force: true }));

describe("container engine discovery", () => {
  it("finds executables, skips dangling links, and picks the running engine", async () => {
    const e = engine();
    expect(e.find().map((x) => x.kind)).toEqual(["docker", "podman"]);
    const status = await e.status();
    expect(status.active).toBe("docker");
    expect(status.engines).toEqual([
      { kind: "docker", path: join(dir, "docker"), version: "Docker version 28.1.0, build abc", running: true, error: null },
      { kind: "podman", path: join(dir, "podman"), version: "podman version 6.0.0-dev", running: false, error: "Cannot connect to Podman. Please verify your connection" },
    ]);
    e.use("podman");
    expect((await e.status()).active).toBe("podman");
    expect(() => e.use("nerdctl")).toThrow("not installed");
  });
});

describe("container engine CLI", () => {
  it("lists and normalizes Docker containers", async () => {
    const rows = await engine().list("containers");
    expect(rows).toEqual([
      { id: "a1b2c3", name: "web-1", image: "nginx:1.27", state: "running", status: "Up 3 minutes", ports: "0.0.0.0:8080->80/tcp",
        createdAt: "2026-11-18 10:00:00 +0800 CST", command: "/docker-entrypoint.sh nginx -g 'daemon off;'", project: "shop", networks: ["shop_default"] },
      // No State column (old Docker): derived from Status.
      { id: "d4e5f6", name: "old", image: "alpine", state: "exited", status: "Exited (0) 2 hours ago", ports: "", createdAt: "2026-11-17", command: "sh", project: null, networks: ["bridge"] },
    ]);
    expect(calls()).toContain("docker ps -a --no-trunc --format {{json .}}");
  });

  it("normalizes Podman containers and images", () => {
    expect(normalizeContainers("podman", parseJsonRecords(PODMAN_PS))).toEqual([{
      id: "p1", name: "db", image: "docker.io/library/postgres:17", state: "exited", status: "Exited (1) 5 seconds ago",
      ports: "0.0.0.0:5433->5432/tcp", createdAt: new Date(1763460000 * 1000).toISOString(), command: "postgres", project: "api", networks: ["podman"],
    }]);
    const images = normalizeImages("podman", parseJsonRecords(PODMAN_IMAGES));
    expect(images.map((i) => [i.repository, i.tag, i.ref, i.size, i.containers])).toEqual([
      ["docker.io/library/alpine", "3.20", "docker.io/library/alpine:3.20", "8.8MB", 2],
      ["localhost:5000/team/alpine", "edge", "localhost:5000/team/alpine:edge", "8.8MB", 2],
      ["<none>", "<none>", "sha256none", "1.2kB", 0],
    ]);
    expect(normalizeImages("docker", [{ ID: "sha256:x", Repository: "<none>", Tag: "<none>", Size: "5MB", Containers: "N/A" }])[0])
      .toMatchObject({ ref: "sha256:x", containers: null });
    expect(listArgs("podman", "images")).toEqual(["images", "--format", "json"]);
  });

  it("runs actions with exact argv and surfaces the daemon's error", async () => {
    const e = engine();
    await e.containerAction("a1b2c3", "stop");
    await e.removeContainer("a1b2c3", true);
    await e.rename("a1b2c3", "web-2");
    expect(await e.logs("a1b2c3", 200, true)).toBe("line 1\nwarn 2\n");
    expect(await e.inspect("images", "nginx:1.27")).toBe('[\n  {\n    "Id": "sha256:1",\n    "RepoTags": [\n      "nginx:1.27"\n    ]\n  }\n]');
    expect(await e.runDetached({ image: "alpine", interactive: true, remove: false, name: "t1", ports: ["8080:80"], env: ["A=1"], volumes: ["/tmp:/data:ro"], command: ["sleep", "60"] })).toBe("f00dfeed");
    await e.remove("images", "nginx:1.27", false);
    await e.prune("images", true);
    await expect(e.containerAction("missing", "start")).rejects.toThrow(/^No such container: missing$/);
    // Without the discovery probes (`--version`, `ps -q` for every engine).
    expect(calls().filter((c) => !c.endsWith(" ps -q") && !c.includes("--version"))).toEqual([
      "docker stop a1b2c3", "docker rm -f a1b2c3", "docker rename a1b2c3 web-2", "docker logs --tail 200 --timestamps a1b2c3",
      "docker image inspect nginx:1.27", "docker run -d --name t1 -p 8080:80 -e A=1 -v /tmp:/data:ro alpine sleep 60",
      "docker rmi nginx:1.27", "docker image prune -f -a", "docker start missing",
    ]);
  });
});

describe("validation", () => {
  it("rejects option injection and malformed run options", () => {
    expect(validRef("docker.io/library/alpine:3.20@sha256:abc")).toBe("docker.io/library/alpine:3.20@sha256:abc");
    for (const bad of ["-rf", "--privileged", "a b", "", 5, "x;rm"]) expect(() => validRef(bad)).toThrow("Invalid");
    expect(() => validShell("bash -c id")).toThrow("Invalid shell");
    expect(() => validRunOptions({ image: "alpine", ports: ["80:80;rm"] }, "linux")).toThrow("Invalid port mapping");
    expect(() => validRunOptions({ image: "alpine", env: ["1A=x"] }, "linux")).toThrow("Invalid environment variable");
    expect(() => validRunOptions({ image: "alpine", name: "-x" }, "linux")).toThrow("Invalid container name");
    expect(() => validRunOptions({ image: "alpine", interactive: true, command: ["echo", "a&b"] }, "win32")).toThrow("Unsupported character");
    expect(validRunOptions({ image: "alpine", interactive: true, command: ["echo", "a&b"] }, "linux").command).toEqual(["echo", "a&b"]);
    expect(runArgs(validRunOptions({ image: "ubuntu", interactive: true, remove: true, command: ["bash"] }, "darwin"))).toEqual(["run", "-it", "--rm", "ubuntu", "bash"]);
  });

  it("strips the daemon prefix from CLI errors", () => {
    expect(cliError({ code: 1, stdout: "", stderr: "\nError response from daemon: conflict: unable to remove\nmore", output: "" }, "x").message).toBe("conflict: unable to remove");
    expect(cliError({ code: 1, stdout: "", stderr: "", output: "" }, "rm failed").message).toBe("rm failed");
  });
});
