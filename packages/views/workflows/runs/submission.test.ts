import { describe, expect, it } from "vitest";
import {
  artifactHasExtraKeys,
  artifactReferences,
  artifactSummary,
  artifactType,
} from "./submission";

/**
 * The artifact is an open bag by design - its shape is a per-node contract, not
 * a per-engine one. Every case here is a value a direct property read would have
 * rendered as `[object Object]` or as a dead link, on the one line a reviewer
 * uses to decide whether to accept.
 */
describe("artifactSummary", () => {
  it("reads a string summary", () => {
    expect(artifactSummary({ summary: "  Null deref  " })).toBe("Null deref");
  });

  it("refuses to coerce a structured summary into prose", () => {
    // `String({text: "x"})` is "[object Object]", which a reader cannot tell
    // apart from a real summary. No summary is the honest answer.
    expect(artifactSummary({ summary: { text: "x" } })).toBe("");
    expect(artifactSummary({ summary: 42 })).toBe("");
    expect(artifactSummary({})).toBe("");
  });
});

describe("artifactType", () => {
  it("reads the discriminator when it is a string", () => {
    expect(artifactType({ type: "analysis" })).toBe("analysis");
  });

  it("degrades a non-string type to empty", () => {
    expect(artifactType({ type: ["analysis"] })).toBe("");
  });
});

describe("artifactReferences", () => {
  it("keeps string references", () => {
    expect(artifactReferences({ references: ["a.go:1", "b.go:2"] })).toEqual([
      "a.go:1",
      "b.go:2",
    ]);
  });

  it("drops non-string entries instead of stringifying them", () => {
    // A reference is meant to be followable; a rendered `[object Object]` is a
    // dead end dressed up as a target.
    expect(
      artifactReferences({ references: ["a.go:1", { path: "b.go" }, 3] }),
    ).toEqual(["a.go:1"]);
  });

  it("returns an empty list when references is absent or not an array", () => {
    expect(artifactReferences({})).toEqual([]);
    expect(artifactReferences({ references: "a.go:1" })).toEqual([]);
  });
});

describe("artifactHasExtraKeys", () => {
  it("is false when the artifact carries only keys the trace already renders", () => {
    // Otherwise every step would grow a redundant JSON block.
    expect(
      artifactHasExtraKeys({ type: "a", summary: "b", references: [] }),
    ).toBe(false);
    expect(artifactHasExtraKeys({})).toBe(false);
  });

  it("is true when a node contract added its own keys", () => {
    // This is exactly the case an operator diagnosing a new node type needs to
    // see, so the raw block is worth the space.
    expect(artifactHasExtraKeys({ summary: "b", diff_stats: { files: 3 } })).toBe(
      true,
    );
  });
});
