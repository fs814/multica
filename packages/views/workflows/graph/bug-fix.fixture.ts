/**
 * The real built-in Bug Fix graph, copied verbatim from
 * server/internal/service/builtin_workflows/bug_fix.json.
 *
 * Inlined rather than imported from the server tree on purpose: the round-trip
 * test is about the *client's* translation layer, and reading across the
 * language boundary would make a frontend test fail when a Go file moved. It is
 * also the honest input - the raw JSON is deliberately sparse (no `name` on
 * routing, no `next` on the End node, no `branches` anywhere), so parsing it
 * through the wire schema is what a real detail page does and is what the
 * round trip must survive.
 *
 * Keep in sync with the seed if the seed changes; the point of the test is that
 * this shape - rework cycles, three routing strategies, a node with no routing
 * at all - survives an open/save with nothing added and nothing lost.
 */

import {
  WorkflowDefinitionSchema,
  type WorkflowDefinition,
} from "@multica/core/workflows";

const BUG_FIX_RAW = {
  schema_version: 1,
  entry_node: "analyze",
  nodes: [
    {
      key: "analyze",
      type: "agent",
      name: "Analyze",
      instruction:
        "Reproduce the reported defect, identify the root cause, and name the files and functions that must change. Do not write the fix yet: the implement node routes on this analysis, so a guess here becomes a wasted implementation attempt.",
      next: ["implement"],
      routing: {
        strategy: "capability",
        capability: "bug_analysis",
      },
      submission_schema: "analysis",
      on_failure: "block",
    },
    {
      key: "implement",
      type: "agent",
      name: "Implement",
      instruction:
        "Apply the minimal change that fixes the root cause identified by the analysis, and add or extend a test that fails before the change and passes after it.",
      next: ["validate"],
      routing: {
        strategy: "capability",
        capability: "code_change",
      },
      submission_schema: "code_change",
      on_failure: "rework",
      rework_targets: ["analyze"],
    },
    {
      key: "validate",
      type: "agent",
      name: "Validate",
      instruction:
        "Run the project's build and test suite against the change and report exactly which checks ran and how they concluded. Report a failure as a failure: a false pass here is worse than no validation, because acceptance trusts this report.",
      next: ["acceptance"],
      routing: {
        strategy: "previous_step",
        from_node: "implement",
      },
      submission_schema: "test_report",
      on_failure: "rework",
      rework_targets: ["implement"],
    },
    {
      key: "acceptance",
      type: "acceptance",
      name: "Acceptance",
      instruction:
        "Confirm the reported defect is actually gone and that the change is bounded to the root cause. Reject to whichever upstream node owns the gap.",
      next: ["end"],
      acceptance_criteria: ["happy path verified", "edge case covered"],
      rework_targets: ["analyze", "implement", "validate"],
    },
    {
      key: "end",
      type: "end",
      name: "Done",
    },
  ],
  limits: {
    max_attempts_per_node: 3,
    max_rework_rounds: 3,
  },
};

/**
 * The Bug Fix definition as the client sees it: parsed through the wire schema,
 * so absent fields carry their defaults exactly as they would on a real page.
 *
 * A function rather than a constant so no test can mutate the fixture out from
 * under another - the round-trip assertion compares against this value.
 */
export function bugFixDefinition(): WorkflowDefinition {
  return WorkflowDefinitionSchema.parse(
    // Structured clone so the parse cannot alias the module-level literal.
    JSON.parse(JSON.stringify(BUG_FIX_RAW)),
  );
}
