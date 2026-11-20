// @vitest-environment node
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { ContainerEngine, parseInspectedContainers, planRelated, type Exec } from "./container-engine";

// `docker container inspect` shapes (Podman's differ only where noted in the parser test).
const INSPECT: Record<string, object> = {
  w1: {
    Id: "w1", Name: "/web", Image: "sha256:img-nginx", Config: { Image: "nginx:1.27" },
    Mounts: [{ Type: "volume", Name: "webdata" }, { Type: "bind", Source: "/srv/html" }],
    NetworkSettings: { Networks: { shop_default: {} } }, HostConfig: { NetworkMode: "shop_default" },
  },
  d1: {
    Id: "d1", Name: "/db", Image: "sha256:img-pg", Config: { Image: "postgres:17" },
    Mounts: [{ Type: "volume", Name: "pgdata" }], NetworkSettings: { Networks: { shop_default: {} } }, HostConfig: { NetworkMode: "shop_default" },
  },
  w2: {
    Id: "w2", Name: "/web2", Image: "sha256:img-nginx", Config: { Image: "nginx:1.27" },
    Mounts: [], NetworkSettings: { Networks: { bridge: {} } }, HostConfig: { NetworkMode: "bridge" },
  },
};

let dir: string;
let calls: string[];
let failing: Set<string>;
/** ids `ps -a` lists that no longer exist when inspected (removed meanwhile). */
let vanished: string[];

const fakeExec: Exec = async (_file, args) => {
  const line = args.join(" ");
  calls.push(line);
  const ok = (stdout = "") => ({ code: 0, stdout, stderr: "", output: stdout });
  const fail = (stderr: string) => ({ code: 1, stdout: "", stderr, output: stderr });
  if (line === "--version") return ok("Docker version 28.1.0\n");
  if (line === "ps -q") return ok("");
  if (line === "ps -a -q --no-trunc") return ok([...Object.keys(INSPECT), ...vanished].join("\n"));
  if (args[0] === "container" && args[1] === "inspect") {
    const ids = args.slice(2);
    if (ids.some((id) => !INSPECT[id])) return fail(`Error: No such container: ${ids.find((id) => !INSPECT[id])}`);
    return ok(JSON.stringify(ids.map((id) => INSPECT[id])));
  }
  if (failing.has(line)) return fail("Error response from daemon: device or resource busy");
  return ok();
};

const engine = () => new ContainerEngine({ platform: "linux", home: dir, env: { PATH: "" }, searchDirs: [dir], exec: fakeExec });
/** Calls after engine discovery and the read-only inspection. */
const changes = () => calls.filter((c) => /^(rm|network rm|volume rm|rmi) /.test(c));

beforeEach(() => {
  dir = mkdtempSync(join(tmpdir(), "containers-related-"));
  writeFileSync(join(dir, "docker"), "");
  calls = [];
  failing = new Set();
  vanished = [];
});
afterEach(() => rmSync(dir, { recursive: true, force: true }));

describe("container inspect parsing", () => {
  it("reads image, named volumes and networks from Docker and Podman inspect output", () => {
    expect(parseInspectedContainers(JSON.stringify([INSPECT.w1]))).toEqual([
      { id: "w1", name: "web", imageId: "img-nginx", imageName: "nginx:1.27", volumes: ["webdata"], networks: ["shop_default"] },
    ]);
    // Podman: no leading slash, no sha256: prefix, image name in ImageName; a stopped container may have no Networks.
    expect(parseInspectedContainers(JSON.stringify([{
      Id: "p1", Name: "api", Image: "abc123", ImageName: "localhost/api:dev", Mounts: [{ Type: "volume", Name: "apidata" }],
      NetworkSettings: { Networks: {} }, HostConfig: { NetworkMode: "api_net" },
    }]))).toEqual([{ id: "p1", name: "api", imageId: "abc123", imageName: "localhost/api:dev", volumes: ["apidata"], networks: ["api_net"] }]);
    expect(parseInspectedContainers("not json")).toEqual([]);
  });
});

describe("planning what goes with the containers", () => {
  const [web, db, web2] = parseInspectedContainers(JSON.stringify([INSPECT.w1, INSPECT.d1, INSPECT.w2]));

  it("keeps what another container still uses, and says who uses it", () => {
    expect(planRelated([web!], [db!, web2!])).toEqual({
      containers: [{ id: "w1", name: "web" }],
      images: [],
      volumes: ["webdata"],
      networks: [],
      kept: [
        { kind: "image", name: "nginx:1.27", usedBy: ["web2"] },
        { kind: "network", name: "shop_default", usedBy: ["db"] },
      ],
    });
  });

  it("frees a whole compose project's network and volumes once all its containers go; never built-in networks", () => {
    const plan = planRelated([web!, db!, web2!], []);
    expect(plan.images).toEqual([{ id: "img-nginx", name: "nginx:1.27" }, { id: "img-pg", name: "postgres:17" }]);
    expect(plan.volumes).toEqual(["webdata", "pgdata"]);
    expect(plan.networks).toEqual(["shop_default"]); // not "bridge"
    expect(plan.kept).toEqual([]);
  });
});

describe("removing containers with what they use", () => {
  it("plans from every container on the engine, even if one vanished meanwhile", async () => {
    vanished = ["gone1"];
    const plan = await engine().relatedPlan(["w1", "d1"]);
    expect(plan.images).toEqual([{ id: "img-pg", name: "postgres:17" }]);
    expect(plan.volumes).toEqual(["webdata", "pgdata"]);
    expect(plan.networks).toEqual(["shop_default"]);
    expect(plan.kept).toEqual([{ kind: "image", name: "nginx:1.27", usedBy: ["web2"] }]);
    expect(changes()).toEqual([]); // planning never changes anything
  });

  it("removes containers first, then networks, volumes and images, by id", async () => {
    const result = await engine().removeWithRelated(["w1", "d1"], { images: true, volumes: true, networks: true, force: true });
    expect(changes()).toEqual([
      "rm -f -v w1", "rm -f -v d1", "network rm shop_default", "volume rm webdata", "volume rm pgdata", "rmi -f img-pg",
    ]);
    expect(result).toEqual({
      removed: { containers: ["web", "db"], images: ["postgres:17"], volumes: ["webdata", "pgdata"], networks: ["shop_default"] },
      kept: [{ kind: "image", name: "nginx:1.27", usedBy: ["web2"] }],
      failed: [],
    });
  });

  it("only takes what was chosen, and keeps what a container that failed to go still uses", async () => {
    failing.add("rm -f -v d1");
    const result = await engine().removeWithRelated(["w1", "d1"], { images: true, volumes: true, networks: true, force: true });
    // db is still there, so its network stays; web's own volume still goes.
    expect(changes()).toEqual(["rm -f -v w1", "rm -f -v d1", "volume rm webdata"]);
    expect(result.removed).toEqual({ containers: ["web"], images: [], volumes: ["webdata"], networks: [] });
    expect(result.failed).toEqual([{ kind: "container", name: "db", error: "device or resource busy" }]);
    expect(result.kept).toContainEqual({ kind: "network", name: "shop_default", usedBy: ["db"] });

    calls = [];
    await engine().removeWithRelated(["w1"], { images: false, volumes: false, networks: true, force: false });
    expect(changes()).toEqual(["rm w1"]); // no -v: volumes were not chosen; shop_default is still db's
  });
});
