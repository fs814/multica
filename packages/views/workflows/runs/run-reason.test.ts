import { describe, expect, it } from "vitest";
import { runElapsedSeconds } from "./run-reason";

/**
 * `runElapsedSeconds` is the one piece of arithmetic on this surface, and every
 * case below is a value that a naive `end - start` would render as a confident
 * wrong number in the duration column.
 */
describe("runElapsedSeconds", () => {
  it("measures a finished run between its own stamps", () => {
    expect(
      runElapsedSeconds("2026-06-01T10:00:00Z", "2026-06-01T10:05:00Z"),
    ).toBe(300);
  });

  it("measures a running run against now", () => {
    const now = Date.parse("2026-06-01T10:02:30Z");
    expect(runElapsedSeconds("2026-06-01T10:00:00Z", null, now)).toBe(150);
  });

  it("returns null for a run that never started", () => {
    // A pending or immediately-blocked run has no `started_at`. Zero would claim
    // it finished instantly, which is the opposite of what happened.
    expect(runElapsedSeconds(null, null)).toBeNull();
    expect(runElapsedSeconds(null, "2026-06-01T10:05:00Z")).toBeNull();
  });

  it("returns null rather than NaN for an unparseable stamp", () => {
    // NaN through a duration formatter becomes a plausible-looking string.
    expect(runElapsedSeconds("not-a-date", null)).toBeNull();
    expect(runElapsedSeconds("2026-06-01T10:00:00Z", "also-not")).toBeNull();
  });

  it("returns null when the two stamps disagree about order", () => {
    // Clock skew between the engine's writes. "-3s" is worse than admitting we
    // cannot measure it.
    expect(
      runElapsedSeconds("2026-06-01T10:05:00Z", "2026-06-01T10:00:00Z"),
    ).toBeNull();
  });
});
