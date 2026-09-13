/**
 * Client-side pre-flight for a workflow graph.
 *
 * **The server is authoritative.** `Validate` in
 * server/internal/workflow/validate.go is the rule that decides whether a graph
 * may be published, and it runs against the definition the client actually sent.
 * This file is a mirror, and a mirror can drift. It exists for two reasons that
 * do not require it to be authoritative:
 *
 *  - 校验 must answer instantly, on an unsaved draft, without a round trip. An
 *    author fixing a graph one error per network hop gives up.
 *  - 保存 can warn before spending a request that is going to come back 422.
 *
 * Because it is not authoritative, it fails in the safe direction: when a rule
 * here is unsure, it stays quiet and lets the server speak. It never throws -
 * a crash in a *validator* would take down the editor over the exact graph the
 * author most needs to fix - and it returns messages worded to match the
 * server's so the two never appear to disagree about the same problem.
 *
 * Message text is intentionally not routed through `useT`: these strings mirror
 * the server's own untranslated validation messages verbatim, and translating
 * only the client half would make an author fixing an error see two different
 * descriptions of it depending on which side reported it first. The button
 * labels and section headings around them are translated as usual.
 */

import type {
  WorkflowDefinition,
  WorkflowNode,
  WorkflowDiagnostic,
} from "@multica/core/workflows";
import { isWorkflowNodeType } from "./types";
import { legalReworkTargetNodes } from "./rework-targets";

/** The only definition format any current server understands. */
const SCHEMA_VERSION = 1;
import { diagnoseGraphV2 } from "@multica/core/workflows";

/** Mirrors `DefaultSchemaRegistry` in the workflow package. */
const KNOWN_SUBMISSION_SCHEMAS = new Set([
  "analysis",
  "code_change",
  "test_report",
  "review_report",
  "generic",
]);

/** Mirrors `validVerdicts` in server/internal/workflow/submission.go. */
const VALID_VERDICTS = new Set(["pass", "fail", "blocked"]);

/** Mirrors the `JoinPolicy` constants; `""` means the server default. */
const VALID_JOIN_POLICIES = new Set([
  "",
  "fail_fast",
  "continue",
  "pause",
  "rework",
]);

/** Mirrors the `FailurePolicy` constants; `""` means the server default (block). */
const VALID_FAILURE_POLICIES = new Set(["", "fail", "block", "rework"]);

/** `EffectiveOnFailure()`: an empty policy is `block`. */
function effectiveOnFailure(node: WorkflowNode): string {
  return node.on_failure === "" ? "block" : node.on_failure;
}

/** Node kinds that must have exactly one successor. */
const SINGLE_SUCCESSOR_TYPES: Record<string, string> = {
  input: "Input",
  agent: "Agent",
  acceptance: "Acceptance",
  join: "Join",
  fan_out: "FanOut",
};

/** Mirrors `validInputFieldTypes`; `""` means the server default (text). */
const VALID_INPUT_FIELD_TYPES = new Set(["", "text", "textarea", "select"]);

/**
 * Returns one human-readable message per problem, or an empty array for a graph
 * this build believes the server will accept.
 *
 * Problems are collected rather than thrown one at a time, and the phase
 * ordering mirrors the server's: a graph with malformed keys or dangling edges
 * short-circuits before reachability and acyclicity run, because those two
 * checks dereference the edge set and would otherwise emit noise that buries the
 * real error.
 */
export function clientValidateGraph(def: WorkflowDefinition): string[] {
  return clientDiagnoseGraph(def).map((item) => item.message);
}

export function clientDiagnoseGraph(
  def: WorkflowDefinition,
): WorkflowDiagnostic[] {
  const diagnostics: WorkflowDiagnostic[] = [];
  const messages = collectGraphProblems(def, diagnostics);
  // Global early returns have no node location, intentionally.
  return messages.map(
    (message, index) =>
      diagnostics[index] ?? {
        code: "workflow_invalid_definition",
        message,
        fieldPath: "definition",
      },
  );
}

type ProblemSink = {
  readonly length: number;
  push(...messages: string[]): number;
};

function collectGraphProblems(
  def: WorkflowDefinition,
  diagnostics: WorkflowDiagnostic[],
): string[] {
  let nodeKey: string | undefined;
  const messages: string[] = [];
  const problems: ProblemSink = {
    get length() {
      return messages.length;
    },
    push(...incoming) {
      for (const message of incoming)
        diagnostics.push({
          code: "workflow_invalid_definition",
          message,
          fieldPath:
            nodeKey === undefined
              ? "definition"
              : `nodes[${def.nodes.findIndex((n) => n.key === nodeKey)}]`,
          nodeKey,
        });
      return messages.push(...incoming);
    },
  };
  if (def.schema_version === 2) {
    const detailed = diagnoseGraphV2(def);
    diagnostics.push(...detailed);
    messages.push(...detailed.map((item) => item.message));
  }

  if (
    def.schema_version !== 0 &&
    def.schema_version !== SCHEMA_VERSION &&
    def.schema_version !== 2
  ) {
    // A version mismatch makes every other check unreliable - node semantics may
    // differ - so stop rather than emit a cascade of misleading errors.
    return [
      `unsupported schema_version ${def.schema_version} (this client understands ${SCHEMA_VERSION})`,
    ];
  }

  if (def.nodes.length === 0) return ["definition has no nodes"];

  const byKey = new Map<string, WorkflowNode>();
  for (const node of def.nodes) {
    nodeKey = node.key;
    if (node.key === "") {
      problems.push("node key is empty");
      continue;
    }
    if (byKey.has(node.key)) {
      problems.push(`duplicate node key "${node.key}"`);
      continue;
    }
    if (!isWorkflowNodeType(node.type)) {
      // Unknown-but-newer node kinds are reported rather than tolerated *here*
      // on purpose: the read-only viewer degrades gracefully (see
      // packages/core/workflows/schemas.ts), but the editor is about to write
      // this graph back, and a save that silently reshapes a node kind this
      // build does not understand is worse than a warning.
      problems.push(`node "${node.key}" has unknown type "${node.type}"`);
      continue;
    }
    byKey.set(node.key, node);
  }
  // Every later check dereferences byKey; with malformed keys or types their
  // output would be noise.
  if (problems.length > 0) return messages;

  nodeKey = undefined;
  if (def.entry_node === "") {
    problems.push("entry_node is empty");
  } else if (!byKey.has(def.entry_node)) {
    problems.push(`entry_node "${def.entry_node}" is not a declared node`);
  }

  const forwardEdgesAreDeclared = def.nodes.every(
    (node) =>
      node.next.every((target) => target !== "" && byKey.has(target)) &&
      node.branches.every(
        (branch) => branch.target !== "" && byKey.has(branch.target),
      ),
  );
  const cycleProbe: string[] = [];
  if (forwardEdgesAreDeclared) checkAcyclic(cycleProbe, def, byKey);
  const canCheckReworkUpstream =
    forwardEdgesAreDeclared && cycleProbe.length === 0;

  let endCount = 0;
  let inputCount = 0;
  for (const node of def.nodes) {
    nodeKey = node.key;
    checkEdges(problems, node, byKey);
    checkReworkTargets(problems, def, node, byKey, canCheckReworkUpstream);
    const imageAttachmentId = (node.image_attachment_id ?? "").trim();

    if (node.type !== "input" && imageAttachmentId !== "") {
      problems.push(
        `node "${node.key}" is a ${node.type} node and must not declare image_attachment_id; only an image input node owns an image`,
      );
    }

    if (node.max_attempts < 0) {
      problems.push(`node "${node.key}" has negative max_attempts`);
    }

    const single = SINGLE_SUCCESSOR_TYPES[node.type];
    if (
      single !== undefined &&
      node.next.length !== 1 &&
      (def.schema_version !== 2 || node.next.length === 0)
    ) {
      problems.push(
        node.type === "fan_out"
          ? `FanOut node "${node.key}" must have exactly one outgoing edge (the node to expand), got ${node.next.length}`
          : `${single} node "${node.key}" must have exactly one outgoing edge, got ${node.next.length}`,
      );
    }

    switch (node.type) {
      case "input":
        inputCount++;
        // An input node is where work enters, so it must BE the entry: an input
        // node reachable only mid-graph would be a form the engine walks past
        // without asking anyone to fill it in, and the Run dialog - which reads
        // the entry node's declaration - would never show it.
        if (def.entry_node !== "" && def.entry_node !== node.key) {
          problems.push(
            `input node "${node.key}" is not the entry_node ("${def.entry_node}"); an input node is where work enters the graph, so it can only be the entry`,
          );
        }
        // Intake is filled in by a human and never dispatches an Agent Task; the
        // DB says the same structurally (task_id IS NULL OR node_type='agent').
        if (node.routing) {
          problems.push(
            `Input node "${node.key}" must not declare routing; intake is filled in by a human and never dispatches an Agent Task`,
          );
        }
        // An input step passes through at activation, so it never submits and a
        // schema would name a contract nothing is checked against.
        if (node.submission_schema !== "") {
          problems.push(
            `Input node "${node.key}" must not declare a submission_schema; an input step passes through at activation and never produces a submission`,
          );
        }
        // It cannot fail - there is no work to fail - so rework out of it is an
        // unreachable branch.
        if (node.rework_targets.length > 0) {
          problems.push(
            `Input node "${node.key}" must not declare rework_targets; it cannot fail, so a rework edge out of it is unreachable`,
          );
        }
        if (node.input_mode === "image" && imageAttachmentId === "") {
          problems.push(`image input node "${node.key}" must select an image`);
        }
        if (node.input_mode !== "image" && imageAttachmentId !== "") {
          problems.push(
            `text input node "${node.key}" must not declare image_attachment_id`,
          );
        }
        checkInputFields(problems, node);
        break;

      case "end":
        endCount++;
        // End is terminal: an edge out of it would imply work continues after
        // the Run completed.
        if (node.next.length > 0) {
          problems.push(`End node "${node.key}" must not have outgoing edges`);
        }
        if (node.routing) {
          problems.push(`End node "${node.key}" must not declare routing`);
        }
        break;

      case "agent":
        checkRouting(problems, node, byKey);
        if (
          node.submission_schema !== "" &&
          !KNOWN_SUBMISSION_SCHEMAS.has(node.submission_schema)
        ) {
          problems.push(
            `Agent node "${node.key}" references unknown submission schema "${node.submission_schema}"`,
          );
        }
        // A rework policy with nowhere to send the work is contradictory: it
        // would leave the engine with no legal move.
        if (
          effectiveOnFailure(node) === "rework" &&
          node.rework_targets.length === 0
        ) {
          problems.push(
            `Agent node "${node.key}" has on_failure=rework but no rework_targets`,
          );
        }
        break;

      case "acceptance":
        if (node.routing) {
          problems.push(
            `Acceptance node "${node.key}" must not declare routing`,
          );
        }
        // A reviewer who rejects must have somewhere to send the work; otherwise
        // rejection is indistinguishable from failure.
        if (node.rework_targets.length === 0 && def.schema_version !== 2) {
          problems.push(
            `Acceptance node "${node.key}" must declare at least one rework target so a rejection can route somewhere`,
          );
        }
        break;

      case "condition":
        checkBranches(problems, node, byKey, def.schema_version === 2);
        break;

      case "fan_out":
        if (node.fan_out_max < 0) {
          problems.push(`FanOut node "${node.key}" has negative fan_out_max`);
        }
        break;

      case "join":
        checkJoin(problems, node, byKey, def.schema_version === 2);
        break;
    }

    // Field declarations only mean something on an input node: the Run dialog
    // reads them off the ENTRY node and nowhere else. Declaring them elsewhere is
    // the "misplaced field" case - the author expected a form and would get
    // silence.
    if (node.type !== "input" && node.input_fields.length > 0) {
      problems.push(
        `node "${node.key}" is a ${node.type} node and must not declare input_fields; only an input node's fields are collected`,
      );
    }

    if (!VALID_FAILURE_POLICIES.has(node.on_failure)) {
      problems.push(
        `node "${node.key}" has unknown on_failure "${node.on_failure}"`,
      );
    }
  }

  nodeKey = undefined;
  if (endCount === 0) {
    problems.push("definition has no End node, so a Run could never complete");
  }
  // At most one input node. Two would mean two places work enters, but a Run has
  // exactly one input bag and exactly one entry node, so the second could only be
  // unreachable-as-intake.
  if (inputCount > 1) {
    problems.push(
      `definition declares ${inputCount} input nodes; a Run has one input, collected at the entry node, so at most one input node is meaningful`,
    );
  }

  checkLimits(problems, def);

  // Reachability and acyclicity need a well-formed edge set; running them on a
  // graph with dangling edges produces misleading results.
  if (problems.length === 0) {
    checkReachability(problems, def, byKey);
    checkAcyclic(problems, def, byKey);
  }

  return messages;
}

/**
 * Dangling forward edges. Checked for every node type because an edge to a
 * deleted node is the most common authoring mistake.
 */
function checkEdges(
  problems: ProblemSink,
  node: WorkflowNode,
  byKey: ReadonlyMap<string, WorkflowNode>,
): void {
  for (const next of node.next) {
    if (next === "") {
      problems.push(`node "${node.key}" has an empty edge target`);
      continue;
    }
    if (!byKey.has(next)) {
      problems.push(`node "${node.key}" points at undeclared node "${next}"`);
    }
  }
}

/**
 * Rework targets must exist, must not be the node itself (a self-rework would
 * spin the same attempt without making progress), and must not be an input node.
 */
function checkReworkTargets(
  problems: ProblemSink,
  definition: WorkflowDefinition,
  node: WorkflowNode,
  byKey: ReadonlyMap<string, WorkflowNode>,
  checkReachableUpstream: boolean,
): void {
  const legalTargets = new Set(
    legalReworkTargetNodes(definition, node.key).map((target) => target.key),
  );
  for (const target of node.rework_targets) {
    const targetNode = byKey.get(target);
    if (!targetNode) {
      problems.push(
        `node "${node.key}" lists undeclared rework target "${target}"`,
      );
      continue;
    }
    if (target === node.key) {
      problems.push(`node "${node.key}" lists itself as a rework target`);
    }
    // Nothing may be sent back to intake. The human already supplied the input,
    // and the engine's rework path activates a node WITHOUT asking a human
    // anything - so a rework edge to an input node would re-run passthrough on
    // the values already recorded and hand the same brief back to the same agent.
    // When a human really must decide something mid-run, that is the acceptance
    // gate's job.
    if (targetNode.type === "input") {
      problems.push(
        `node "${node.key}" lists input node "${target}" as a rework target; nothing can be sent back to intake because the engine cannot re-prompt a human mid-run — use an acceptance node for that`,
      );
    }
    if (
      checkReachableUpstream &&
      target !== node.key &&
      targetNode.type !== "input" &&
      !legalTargets.has(target)
    ) {
      problems.push(
        `node "${node.key}" lists rework target "${target}" that is not a reachable upstream node on a forward path from entry_node "${definition.entry_node}" to "${node.key}"`,
      );
    }
  }
}

/**
 * An input node's declared fields, mirroring `validateInputFields`.
 *
 * Every rule protects one property: a declared field must be one the Run dialog
 * can render and a human can satisfy. A field that fails any of them is worse
 * than a missing field, because the graph advertises an input the run can never
 * receive - and the server's required-field check would then reject every run of
 * a template that looks correct on the canvas.
 */
function checkInputFields(problems: ProblemSink, node: WorkflowNode): void {
  const seen = new Set<string>();
  for (const field of node.input_fields) {
    if (field.key === "") {
      problems.push(
        `input node "${node.key}" declares a field with no key; the key is where the submitted value is stored`,
      );
      continue;
    }
    if (seen.has(field.key)) {
      problems.push(
        `input node "${node.key}" declares duplicate field key "${field.key}"; the second field's value would overwrite the first`,
      );
      continue;
    }
    seen.add(field.key);

    if (!VALID_INPUT_FIELD_TYPES.has(field.type)) {
      problems.push(
        `input node "${node.key}" field "${field.key}" has unknown type "${field.type}" (want text, textarea, or select)`,
      );
      continue;
    }
    // A select with no options is a dropdown with nothing in it: the dialog
    // cannot render a choice, so a required select would make the run
    // unstartable and an optional one would be dead UI.
    if (field.type === "select" && field.options.length === 0) {
      problems.push(
        `input node "${node.key}" field "${field.key}" is a select with no options, which the Run dialog cannot render`,
      );
    }
    for (const option of field.options) {
      // A blank option cannot be told apart from "nothing selected", so a
      // required select could be satisfied by a value that reads as absent.
      if (option === "") {
        problems.push(
          `input node "${node.key}" field "${field.key}" has a blank option, which cannot be told apart from no selection`,
        );
      }
    }
  }
}

/** Routing completeness, mirroring `validateRouting`. */
function checkRouting(
  problems: ProblemSink,
  node: WorkflowNode,
  byKey: ReadonlyMap<string, WorkflowNode>,
): void {
  const routing = node.routing;
  if (!routing) {
    problems.push(`Agent node "${node.key}" declares no routing`);
    return;
  }
  switch (routing.strategy) {
    case "explicit":
      if (routing.agent_id === "") {
        problems.push(
          `Agent node "${node.key}" uses explicit routing but sets no agent_id`,
        );
      }
      return;
    case "previous_step": {
      if (routing.from_node === "") {
        problems.push(
          `Agent node "${node.key}" uses previous_step routing but sets no from_node`,
        );
        return;
      }
      const from = byKey.get(routing.from_node);
      if (!from) {
        problems.push(
          `Agent node "${node.key}" routes from undeclared node "${routing.from_node}"`,
        );
        return;
      }
      // Only an Agent node has an Agent to inherit.
      if (from.type !== "agent") {
        problems.push(
          `Agent node "${node.key}" routes from "${routing.from_node}", which is a ${from.type} node and has no Agent to reuse`,
        );
      }
      if (routing.from_node === node.key) {
        problems.push(`Agent node "${node.key}" routes from itself`);
      }
      return;
    }
    case "capability":
      if (routing.capability === "") {
        problems.push(
          `Agent node "${node.key}" uses capability routing but sets no capability`,
        );
      }
      return;
    case "":
      problems.push(`Agent node "${node.key}" declares no routing strategy`);
      return;
    default:
      problems.push(
        `Agent node "${node.key}" has unknown routing strategy "${routing.strategy}"`,
      );
  }
}

/** Condition branch rules: at least one branch, at most one default, known verdicts. */
function checkBranches(
  problems: ProblemSink,
  node: WorkflowNode,
  byKey: ReadonlyMap<string, WorkflowNode>,
  graphV2 = false,
): void {
  if (node.branches.length === 0) {
    problems.push(`Condition node "${node.key}" has no branches`);
  }
  let seenDefault = false;
  for (const branch of node.branches) {
    if (branch.target === "") {
      problems.push(`Condition node "${node.key}" has a branch with no target`);
    } else if (!byKey.has(branch.target)) {
      problems.push(
        `Condition node "${node.key}" branches to undeclared node "${branch.target}"`,
      );
    }
    if (graphV2 && branch.predicate) continue;
    if (branch.when_verdict === "") {
      // An empty verdict is the fallthrough. Two of them would make the engine's
      // choice depend on array order, which is not a decision an author made.
      if (seenDefault) {
        problems.push(
          `Condition node "${node.key}" has more than one default branch`,
        );
      }
      seenDefault = true;
    } else if (!VALID_VERDICTS.has(branch.when_verdict)) {
      problems.push(
        `Condition node "${node.key}" branches on unknown verdict "${branch.when_verdict}"`,
      );
    }
  }
}

/** Join rules: a Join that waits on nothing passes instantly or hangs forever. */
function checkJoin(
  problems: ProblemSink,
  node: WorkflowNode,
  byKey: ReadonlyMap<string, WorkflowNode>,
  graphV2 = false,
): void {
  if (node.join_sources.length === 0 && !graphV2) {
    problems.push(`Join node "${node.key}" declares no join_sources`);
  }
  for (const source of node.join_sources) {
    if (!byKey.has(source)) {
      problems.push(
        `Join node "${node.key}" waits on undeclared node "${source}"`,
      );
    } else if (source === node.key) {
      problems.push(`Join node "${node.key}" waits on itself`);
    }
  }
  if (!VALID_JOIN_POLICIES.has(node.join_policy)) {
    problems.push(
      `Join node "${node.key}" has unknown join_policy "${node.join_policy}"`,
    );
  }
  if (node.join_policy === "rework" && node.rework_targets.length === 0) {
    problems.push(
      `Join node "${node.key}" has join_policy=rework but no rework_targets`,
    );
  }
}

/**
 * Only the sign of each limit is checked here. The workspace policy ceiling is
 * deliberately NOT mirrored: the ceiling lives on the server, is per-workspace,
 * and this client has no way to know it. Guessing one would reject graphs the
 * server would accept - a false negative from a non-authoritative validator is
 * far more expensive than a missed warning.
 */
function checkLimits(problems: ProblemSink, def: WorkflowDefinition): void {
  const limits: Array<[string, number]> = [
    ["max_attempts_per_node", def.limits.max_attempts_per_node],
    ["max_rework_rounds", def.limits.max_rework_rounds],
    ["max_fan_out", def.limits.max_fan_out],
    ["max_duration_seconds", def.limits.max_duration_seconds],
    ["max_total_steps", def.limits.max_total_steps],
    ["max_cost_cents", def.limits.max_cost_cents],
  ];
  for (const [name, value] of limits) {
    // Zero means "inherit the server default"; only negative is wrong.
    if (value < 0) problems.push(`limits.${name} is negative`);
  }
}

/**
 * Every edge out of a node, rework included. A node reachable *only* via rework
 * is legitimate, so reachability must consider those edges even though
 * acyclicity must not.
 */
function outgoing(node: WorkflowNode): string[] {
  const out = [...node.next];
  for (const branch of node.branches) {
    if (branch.target !== "") out.push(branch.target);
  }
  out.push(...node.rework_targets);
  return out;
}

/**
 * An End node must be reachable from the entry (a Run completes only through
 * End), and every declared node must be reachable from somewhere (an orphan is
 * either a typo or dead weight in an immutable published version).
 */
function checkReachability(
  problems: ProblemSink,
  def: WorkflowDefinition,
  byKey: ReadonlyMap<string, WorkflowNode>,
): void {
  if (def.entry_node === "") return;

  const seen = new Set<string>();
  const stack = [def.entry_node];
  while (stack.length > 0) {
    const key = stack.pop()!;
    if (seen.has(key)) continue;
    seen.add(key);
    const node = byKey.get(key);
    if (!node) continue;
    for (const next of outgoing(node)) {
      if (!seen.has(next)) stack.push(next);
    }
  }

  let reachableEnd = false;
  for (const key of seen) {
    if (byKey.get(key)?.type === "end") {
      reachableEnd = true;
      break;
    }
  }
  if (!reachableEnd) {
    problems.push(
      `no End node is reachable from entry_node "${def.entry_node}", so a Run could never complete`,
    );
  }

  // Declaration order, for a message list that is stable across runs.
  for (const node of def.nodes) {
    if (!seen.has(node.key)) {
      problems.push(
        `node "${node.key}" is unreachable from entry_node "${def.entry_node}"`,
      );
    }
  }
}

/**
 * Rejects cycles not made exclusively of declared rework edges.
 *
 * Only explicit bounded rework may loop, because only rework carries an attempt
 * ceiling that guarantees termination. An undeclared cycle would let a Run spin
 * inside its duration budget with nothing to stop it.
 *
 * Iterative DFS with an explicit per-frame edge cursor, mirroring the server's,
 * so a deep graph cannot blow the JS stack.
 */
function checkAcyclic(
  problems: ProblemSink,
  def: WorkflowDefinition,
  byKey: ReadonlyMap<string, WorkflowNode>,
): void {
  const WHITE = 0;
  const GREY = 1; // on the current DFS path
  const BLACK = 2;
  const color = new Map<string, number>();

  // Deliberately excludes rework_targets: those are the permitted cycles.
  const forward = (node: WorkflowNode): string[] => {
    const out = [...node.next];
    for (const branch of node.branches) {
      if (branch.target !== "") out.push(branch.target);
    }
    return out;
  };

  for (const root of def.nodes) {
    if ((color.get(root.key) ?? WHITE) !== WHITE) continue;
    const rootNode = byKey.get(root.key);
    if (!rootNode) continue;

    const stack: Array<{ key: string; edges: string[]; i: number }> = [
      { key: root.key, edges: forward(rootNode), i: 0 },
    ];
    color.set(root.key, GREY);

    while (stack.length > 0) {
      const top = stack[stack.length - 1]!;
      if (top.i >= top.edges.length) {
        color.set(top.key, BLACK);
        stack.pop();
        continue;
      }
      const next = top.edges[top.i]!;
      top.i++;

      const nextColor = color.get(next) ?? WHITE;
      if (nextColor === GREY) {
        // Back edge on a non-rework path: a cycle the engine cannot bound. Keep
        // scanning siblings - one report per back edge is enough.
        problems.push(
          `non-rework cycle detected: "${top.key}" -> "${next}" (only declared rework_targets may form a cycle)`,
        );
      } else if (nextColor === WHITE) {
        const child = byKey.get(next);
        if (!child) continue;
        color.set(next, GREY);
        stack.push({ key: next, edges: forward(child), i: 0 });
      }
    }
  }
}
