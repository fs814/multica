// @vitest-environment node
import { describe, it, expect } from "vitest";
import { daemonStateDescription, daemonStatusAlive } from "./daemon-types";

describe("daemonStatusAlive", () => {
  it("treats a ready daemon as alive", () => {
    expect(daemonStatusAlive("running")).toBe(true);
  });

  it("treats a still-booting daemon as alive", () => {
    // /health binds before preflight and reports "starting" until ready; the
    // Desktop must not spawn a second daemon over it (the CLI rejects that as
    // "already running").
    expect(daemonStatusAlive("starting")).toBe(true);
  });

  it("treats stopped / unknown / missing as not alive", () => {
    expect(daemonStatusAlive("stopped")).toBe(false);
    expect(daemonStatusAlive("bogus")).toBe(false);
    expect(daemonStatusAlive("")).toBe(false);
    expect(daemonStatusAlive(undefined)).toBe(false);
  });
});

describe("daemonStateDescription", () => {
  it("makes an exhausted recovery budget actionable", () => {
    expect(daemonStateDescription("recovery_paused", 0)).toMatch(
      /start manually/i,
    );
  });
});
import { daemonAcceptsInitialCenter } from "./daemon-types";

describe("daemonAcceptsInitialCenter", () => {
  it("allows initial configuration only for a confirmed idle offline daemon", () => {
    const health = { status: "running", task_ready: false, active_task_count: 0, offline_reason: "unconfigured" };
    expect(daemonAcceptsInitialCenter(health)).toBe(true);
    expect(daemonAcceptsInitialCenter({ ...health, offline_reason: "unauthenticated" })).toBe(true);
    expect(daemonAcceptsInitialCenter({ ...health, offline_reason: "unreachable" })).toBe(true);
    for (const change of [{ task_ready: true }, { active_task_count: 1 }, { task_ready: undefined },
      { active_task_count: undefined }, { status: "stopped" }, { offline_reason: "connecting" }, { offline_reason: true }]) {
      expect(daemonAcceptsInitialCenter({ ...health, ...change })).toBe(false);
    }
  });
});
