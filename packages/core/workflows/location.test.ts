// @vitest-environment node
import { describe, expect, it } from "vitest";
import { workflowListOffset, workflowReturnPath } from "./location";

describe("workflow URL state", () => {
  it("normalizes malformed and unaligned pages", () => {
    expect(
      [null, "", "-1", "x", "Infinity", "3.5"].map(workflowListOffset),
    ).toEqual([0, 0, 0, 0, 0, 0]);
    expect(workflowListOffset("61")).toBe(60);
  });
  it("preserves filters only for allowed workspace lists", () => {
    const fallback = "/acme/workflow-runs";
    const allowed = [fallback, "/acme/workflows/t"];
    expect(
      workflowReturnPath(
        "/acme/workflows/t?section=runs&status=failed&run_offset=30",
        fallback,
        allowed,
      ),
    ).toContain("run_offset=30");
    for (const path of [
      "https://example.com",
      "//example.com",
      "/other/workflow-runs",
      "/acme/issues",
    ]) {
      expect(workflowReturnPath(path, fallback, allowed)).toBe(fallback);
    }
  });
});
