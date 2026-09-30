// @vitest-environment node
import { describe, expect, it } from "vitest";
import { parseCenterImportStatus } from "./center-recovery-schema";
describe("center recovery response boundary", () => {
  it("rejects incomplete and malformed responses", () => {
    for (const value of [null, {}, { job_id: "wrong", state: "complete", center_id: "source" }, { job_id: "a".repeat(32), state: 1, center_id: "source" }]) expect(parseCenterImportStatus(value)).toBeNull();
  });
  it("defaults optional messages and preserves unknown states for callers to reject", () => {
    expect(parseCenterImportStatus({ job_id: "a".repeat(32), state: "future-state", center_id: "source" })).toEqual({ jobId: "a".repeat(32), state: "future-state", centerId: "source", message: "" });
  });
});
