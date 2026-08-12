import { describe, expect, it } from "vitest";
import { linearizeGraph } from "./graph-order";

// The detail page renders the graph as an ordered list, so the ordering IS the
// visualization. These cases pin the properties that make that honest: the
// order follows edges rather than array position, backward rework edges can't
// make the walk diverge, and nothing gets silently dropped.

interface TestNode {
  key: string;
  next?: string[];
}

const keysOf = (nodes: TestNode[], entry: string | null) =>
  linearizeGraph(nodes, entry).map((entry) => entry.node.key);

describe("linearizeGraph", () => {
  it("follows next[] edges rather than array order", () => {
    // Bug Fix graph, deliberately shuffled in the array: an author reordering
    // nodes[] must not change what a reader sees.
    const nodes: TestNode[] = [
      { key: "validate", next: ["acceptance"] },
      { key: "end" },
      { key: "analyze", next: ["implement"] },
      { key: "acceptance", next: ["end"] },
      { key: "implement", next: ["validate"] },
    ];
    expect(keysOf(nodes, "analyze")).toEqual([
      "analyze",
      "implement",
      "validate",
      "acceptance",
      "end",
    ]);
  });

  it("marks every node the entry walk reaches as on-chain", () => {
    const ordered = linearizeGraph(
      [{ key: "a", next: ["b"] }, { key: "b" }],
      "a",
    );
    expect(ordered.map((e) => e.onChain)).toEqual([true, true]);
    expect(ordered.map((e) => e.index)).toEqual([1, 2]);
  });

  it("appends unreachable nodes flagged off-chain instead of hiding them", () => {
    // The server validator rejects orphans, but a graph published by a newer
    // or buggier server must still be inspectable by a human.
    const ordered = linearizeGraph(
      [{ key: "a", next: ["b"] }, { key: "b" }, { key: "orphan" }],
      "a",
    );
    expect(ordered.map((e) => [e.node.key, e.onChain])).toEqual([
      ["a", true],
      ["b", true],
      ["orphan", false],
    ]);
  });

  it("terminates on a cycle instead of looping forever", () => {
    // A condition branch pointing back at an earlier node is legal JSON.
    expect(keysOf([{ key: "a", next: ["b"] }, { key: "b", next: ["a"] }], "a")).toEqual(
      ["a", "b"],
    );
  });

  it("stops at a dangling edge but still lists the remaining nodes", () => {
    const ordered = linearizeGraph(
      [{ key: "a", next: ["missing"] }, { key: "later" }],
      "a",
    );
    expect(ordered.map((e) => [e.node.key, e.onChain])).toEqual([
      ["a", true],
      ["later", false],
    ]);
  });

  it("lists every node off-chain when the entry node is absent", () => {
    // An empty entry_node is what the core schema falls back to when the
    // definition is unreadable; the page must not render a blank graph.
    const ordered = linearizeGraph([{ key: "a" }, { key: "b" }], "");
    expect(ordered.map((e) => [e.node.key, e.onChain])).toEqual([
      ["a", false],
      ["b", false],
    ]);
  });

  it("follows only the first edge, so a condition node does not fork", () => {
    // Extra branch targets are surfaced on the node card; the reader needs one
    // authoritative reading order, and both targets still appear in the list.
    const ordered = linearizeGraph(
      [
        { key: "cond", next: ["pass", "fail"] },
        { key: "pass" },
        { key: "fail" },
      ],
      "cond",
    );
    expect(ordered.map((e) => [e.node.key, e.onChain])).toEqual([
      ["cond", true],
      ["pass", true],
      ["fail", false],
    ]);
  });

  it("renders a duplicated key once, keeping the first declaration", () => {
    // A duplicate key is invalid (edges address nodes by key, so the second
    // declaration is unaddressable). Emitting one card per key beats showing
    // two identical-looking steps a reader cannot tell apart.
    const ordered = linearizeGraph(
      [
        { key: "a", next: ["b"] },
        { key: "b", next: ["first"] },
        { key: "b", next: ["second"] },
        { key: "first" },
        { key: "second" },
      ],
      "a",
    );
    expect(ordered.map((e) => [e.node.key, e.onChain])).toEqual([
      ["a", true],
      ["b", true],
      ["first", true],
      ["second", false],
    ]);
    // The FIRST "b" is the one that ran the walk: its edge, not the shadowed
    // duplicate's, decided the third step.
    expect(ordered[1]?.node.next).toEqual(["first"]);
  });

  it("returns nothing for an empty graph", () => {
    expect(linearizeGraph([], "a")).toEqual([]);
  });
});
