import { describe, expect, it } from "vitest";
import { createPathWithParams } from "./squad-param";

describe("createPathWithParams", () => {
  it("keeps the runtime and Knot agent seeds across the Builder route hop", () => {
    expect(
      createPathWithParams("/agents/new/ai/session-1", {
        squad: "squad-1",
        runtime: "runtime-1",
        knotAgent: "7a5d51d0b14f449683fdb839c5e3e448",
      }),
    ).toBe(
      "/agents/new/ai/session-1?squad=squad-1&runtime=runtime-1&knot_agent=7a5d51d0b14f449683fdb839c5e3e448",
    );
  });

  it("omits empty optional seeds", () => {
    expect(
      createPathWithParams("/agents/new/ai/session-1", {
        squad: null,
        runtime: null,
        knotAgent: "",
      }),
    ).toBe("/agents/new/ai/session-1");
  });
});
