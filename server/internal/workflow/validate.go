package workflow

import (
	"fmt"
	"strings"
)

// Validate checks a definition against every publishing rule in plan section 6:
// missing/duplicate nodes, dangling edges, no reachable End, outgoing End edges,
// non-rework cycles, unbounded rework, unmatched Join, unresolved routing,
// unknown Submission schemas, invalid rework targets, and limits above
// workspace policy.
//
// It also owns the input-node contract: at most one input node, it must be the
// entry node, exactly one outgoing edge, no routing / submission_schema /
// rework_targets, and field declarations the Run dialog can actually render.
// Those rules are collected here rather than spread across the engine because
// they are all statically decidable, and a graph that reaches a Run with a
// malformed intake declaration produces a Run nobody can start.
//
// It collects all problems rather than failing on the first, and it is a pure
// function of the definition plus the policy ceiling — no database, no clock —
// so the template editor can validate a draft without persisting it.
//
// The load-bearing checks are reachability and acyclicity. A graph with no
// reachable End could never complete (a Run completes only through End), and an
// undeclared cycle would let a Run loop forever inside its duration budget. Both
// are cheap to prove here and expensive to diagnose in production.
func Validate(d *Definition, policy WorkspacePolicy, schemas SchemaRegistry) error {
	v := &ValidationErrors{}

	if d.SchemaVersion != 0 && d.SchemaVersion != SchemaVersion {
		v.add("schema_version", fmt.Sprintf("unsupported schema_version %d (this server understands %d)", d.SchemaVersion, SchemaVersion))
		// A version mismatch makes every other check unreliable: node semantics
		// may differ. Stop here rather than emit a cascade of misleading errors.
		return v
	}

	if len(d.Nodes) == 0 {
		v.add("nodes", "definition has no nodes")
		return v
	}

	// Node keys must be unique and non-empty: everything else addresses nodes by
	// key, and a duplicate would make "which node did this Step run?" ambiguous
	// for the whole lifetime of every Run pinned to this version.
	byKey := make(map[string]*Node, len(d.Nodes))
	for i := range d.Nodes {
		n := &d.Nodes[i]
		field := fmt.Sprintf("nodes[%d]", i)
		if n.Key == "" {
			v.add(field+".key", "node key is empty")
			continue
		}
		if _, dup := byKey[n.Key]; dup {
			v.add(field+".key", fmt.Sprintf("duplicate node key %q", n.Key))
			continue
		}
		if !validNodeTypes[n.Type] {
			v.add(field+".type", fmt.Sprintf("node %q has unknown type %q", n.Key, n.Type))
			continue
		}
		byKey[n.Key] = n
	}
	if v.HasErrors() {
		// Later checks dereference byKey; with malformed keys/types their output
		// would be noise.
		return v
	}

	if d.EntryNode == "" {
		v.add("entry_node", "entry_node is empty")
	} else if _, ok := byKey[d.EntryNode]; !ok {
		v.add("entry_node", fmt.Sprintf("entry_node %q is not a declared node", d.EntryNode))
	}

	cycleCheck := &ValidationErrors{}
	validateAcyclic(cycleCheck, d, byKey)
	forwardGraphIsAcyclic := !cycleCheck.HasErrors()

	endCount := 0
	inputCount := 0
	for i := range d.Nodes {
		n := &d.Nodes[i]
		field := fmt.Sprintf("nodes[%d]", i)

		// Dangling edges. Checked for every node type because an edge to a
		// deleted node is the most common authoring mistake.
		for j, next := range n.Next {
			if next == "" {
				v.add(fmt.Sprintf("%s.next[%d]", field, j), fmt.Sprintf("node %q has an empty edge target", n.Key))
				continue
			}
			if _, ok := byKey[next]; !ok {
				v.add(fmt.Sprintf("%s.next[%d]", field, j), fmt.Sprintf("node %q points at undeclared node %q", n.Key, next))
			}
		}

		// Rework targets must exist and must not be the node itself: a self-
		// rework would spin the same attempt without making progress.
		for j, target := range n.ReworkTargets {
			rf := fmt.Sprintf("%s.rework_targets[%d]", field, j)
			t, ok := byKey[target]
			if !ok {
				v.add(rf, fmt.Sprintf("node %q lists undeclared rework target %q", n.Key, target))
				continue
			}
			if target == n.Key {
				v.add(rf, fmt.Sprintf("node %q lists itself as a rework target", n.Key))
			}
			// Nothing may be sent back to intake. The human already supplied the
			// input, and the engine's rework path activates a node WITHOUT asking a
			// human anything — so a rework edge to an input node would re-run
			// passthrough on the values already recorded and hand the same brief
			// back to the same agent, burning an attempt with nothing changed. When
			// what is actually needed is a human deciding something mid-run, that is
			// what the acceptance gate is for.
			if t.Type == NodeTypeInput {
				v.add(rf, fmt.Sprintf("node %q lists input node %q as a rework target; nothing can be sent back to intake because the engine cannot re-prompt a human mid-run — use an acceptance node for that", n.Key, target))
			}
			if forwardGraphIsAcyclic && target != n.Key && t.Type != NodeTypeInput && (!forwardReachable(byKey, d.EntryNode, target) || !forwardReachable(byKey, target, n.Key)) {
				v.add(rf, fmt.Sprintf("node %q lists rework target %q that is not a reachable upstream node on a forward path from entry_node %q to %q", n.Key, target, d.EntryNode, n.Key))
			}
		}

		if n.MaxAttempts < 0 {
			v.add(field+".max_attempts", fmt.Sprintf("node %q has negative max_attempts", n.Key))
		}
		// Unbounded rework: declaring targets without an attempt ceiling (here or
		// in Limits) would let a fail/rework loop run forever.
		if len(n.ReworkTargets) > 0 && n.MaxAttempts == 0 && d.Limits.MaxAttemptsPerNode == 0 && d.Limits.MaxReworkRounds == 0 {
			// DefaultLimits still bounds this, so it is a warning-shaped error only
			// when the author explicitly zeroed the graph limits. Both are zero
			// here, meaning "unset", so defaults apply and this is safe.
			_ = n
		}

		switch n.Type {
		case NodeTypeInput:
			inputCount++
			// An input node is where work enters, so it must BE the entry: an input
			// node reachable only mid-graph would be a form the engine walks past
			// without asking anyone to fill it in, and the Run dialog — which reads
			// the entry node's declaration — would never show it.
			if d.EntryNode != "" && d.EntryNode != n.Key {
				v.add(field, fmt.Sprintf("input node %q is not the entry_node (%q); an input node is where work enters the graph, so it can only be the entry", n.Key, d.EntryNode))
			}
			// Exactly one successor, like every other single-successor kind: intake
			// hands the run to one first step. Zero would stall the Run at intake
			// with nowhere to go; more than one would be an undeclared fan-out,
			// and the engine's passthrough advances to Next[0] only.
			if len(n.Next) != 1 {
				v.add(field+".next", fmt.Sprintf("Input node %q must have exactly one outgoing edge, got %d", n.Key, len(n.Next)))
			}
			// No routing: an input node is a human's contribution, not an agent's.
			// The DB says the same thing structurally (task_id IS NULL OR
			// node_type = 'agent'), so routing here could never be honoured — it
			// would be a declaration the author expected to do something.
			if n.Routing != nil {
				v.add(field+".routing", fmt.Sprintf("Input node %q must not declare routing; intake is filled in by a human and never dispatches an Agent Task", n.Key))
			}
			// No submission schema: nothing submits at an input node. The step
			// passes through at activation, so a schema would name a contract no
			// submission will ever be checked against.
			if n.SubmissionSchema != "" {
				v.add(field+".submission_schema", fmt.Sprintf("Input node %q must not declare a submission_schema; an input step passes through at activation and never produces a submission", n.Key))
			}
			// No rework FROM intake either. The node cannot fail — there is no work
			// to fail — so a failure policy with targets describes an unreachable
			// branch.
			if len(n.ReworkTargets) > 0 {
				v.add(field+".rework_targets", fmt.Sprintf("Input node %q must not declare rework_targets; it cannot fail, so a rework edge out of it is unreachable", n.Key))
			}
			validateInputFields(v, n, field)

		case NodeTypeEnd:
			endCount++
			// "Publishing rejects ... outgoing End edges": End is terminal, and an
			// edge out of it would imply work continues after the Run completed.
			if len(n.Next) > 0 {
				v.add(field+".next", fmt.Sprintf("End node %q must not have outgoing edges", n.Key))
			}
			if n.Routing != nil {
				v.add(field+".routing", fmt.Sprintf("End node %q must not declare routing", n.Key))
			}

		case NodeTypeAgent:
			if len(n.Next) != 1 {
				v.add(field+".next", fmt.Sprintf("Agent node %q must have exactly one outgoing edge, got %d", n.Key, len(n.Next)))
			}
			validateRouting(v, n, byKey, field)
			if n.SubmissionSchema != "" && schemas != nil && !schemas.Has(n.SubmissionSchema) {
				v.add(field+".submission_schema", fmt.Sprintf("Agent node %q references unknown submission schema %q", n.Key, n.SubmissionSchema))
			}
			// A rework failure policy with nowhere to send the work is
			// contradictory and would leave the engine with no legal move.
			if n.EffectiveOnFailure() == FailurePolicyRework && len(n.ReworkTargets) == 0 {
				v.add(field+".rework_targets", fmt.Sprintf("Agent node %q has on_failure=rework but no rework_targets", n.Key))
			}

		case NodeTypeAcceptance:
			if len(n.Next) != 1 {
				v.add(field+".next", fmt.Sprintf("Acceptance node %q must have exactly one outgoing edge, got %d", n.Key, len(n.Next)))
			}
			if n.Routing != nil {
				v.add(field+".routing", fmt.Sprintf("Acceptance node %q must not declare routing", n.Key))
			}
			// A reviewer who rejects must have somewhere to send the work;
			// otherwise rejection is indistinguishable from failure.
			if len(n.ReworkTargets) == 0 {
				v.add(field+".rework_targets", fmt.Sprintf("Acceptance node %q must declare at least one rework target so a rejection can route somewhere", n.Key))
			}

		case NodeTypeCondition:
			if len(n.Branches) == 0 {
				v.add(field+".branches", fmt.Sprintf("Condition node %q has no branches", n.Key))
			}
			seenDefault := false
			for j, b := range n.Branches {
				bf := fmt.Sprintf("%s.branches[%d]", field, j)
				if b.Target == "" {
					v.add(bf+".target", fmt.Sprintf("Condition node %q has a branch with no target", n.Key))
				} else if _, ok := byKey[b.Target]; !ok {
					v.add(bf+".target", fmt.Sprintf("Condition node %q branches to undeclared node %q", n.Key, b.Target))
				}
				switch b.WhenVerdict {
				case "":
					if seenDefault {
						v.add(bf+".when_verdict", fmt.Sprintf("Condition node %q has more than one default branch", n.Key))
					}
					seenDefault = true
				case string(VerdictPass), string(VerdictFail), string(VerdictBlocked):
					// ok
				default:
					v.add(bf+".when_verdict", fmt.Sprintf("Condition node %q branches on unknown verdict %q", n.Key, b.WhenVerdict))
				}
			}

		case NodeTypeFanOut:
			if len(n.Next) != 1 {
				v.add(field+".next", fmt.Sprintf("FanOut node %q must have exactly one outgoing edge (the node to expand), got %d", n.Key, len(n.Next)))
			}
			if n.FanOutMax < 0 {
				v.add(field+".fan_out_max", fmt.Sprintf("FanOut node %q has negative fan_out_max", n.Key))
			}

		case NodeTypeJoin:
			if len(n.Next) != 1 {
				v.add(field+".next", fmt.Sprintf("Join node %q must have exactly one outgoing edge, got %d", n.Key, len(n.Next)))
			}
			// "Publishing rejects ... unmatched Join": a Join that waits on
			// nothing would either pass instantly or hang forever.
			if len(n.JoinSources) == 0 {
				v.add(field+".join_sources", fmt.Sprintf("Join node %q declares no join_sources", n.Key))
			}
			for j, src := range n.JoinSources {
				sf := fmt.Sprintf("%s.join_sources[%d]", field, j)
				if _, ok := byKey[src]; !ok {
					v.add(sf, fmt.Sprintf("Join node %q waits on undeclared node %q", n.Key, src))
				} else if src == n.Key {
					v.add(sf, fmt.Sprintf("Join node %q waits on itself", n.Key))
				}
			}
			switch n.JoinPolicy {
			case "", JoinPolicyFailFast, JoinPolicyContinue, JoinPolicyPause, JoinPolicyRework:
				// ok
			default:
				v.add(field+".join_policy", fmt.Sprintf("Join node %q has unknown join_policy %q", n.Key, n.JoinPolicy))
			}
			if n.JoinPolicy == JoinPolicyRework && len(n.ReworkTargets) == 0 {
				v.add(field+".rework_targets", fmt.Sprintf("Join node %q has join_policy=rework but no rework_targets", n.Key))
			}
		}

		// Field declarations only mean something on an input node: the Run dialog
		// reads them off the ENTRY node and nowhere else. Declaring them elsewhere
		// is the "misplaced field" case this file exists to catch — the author
		// expected a form and would get silence.
		if n.Type != NodeTypeInput && len(n.InputFields) > 0 {
			v.add(field+".input_fields", fmt.Sprintf("node %q is a %s node and must not declare input_fields; only an input node's fields are collected", n.Key, n.Type))
		}
		if n.Type != NodeTypeInput && n.InputMode != "" {
			v.add(field+".input_mode", fmt.Sprintf("node %q is a %s node and must not declare input_mode; only an input node collects input", n.Key, n.Type))
		}
		imageAttachmentID := strings.TrimSpace(n.ImageAttachmentID)
		if n.Type != NodeTypeInput && imageAttachmentID != "" {
			v.add(field+".image_attachment_id", fmt.Sprintf("node %q is a %s node and must not declare image_attachment_id; only an image input node owns an image", n.Key, n.Type))
		}
		if n.Type == NodeTypeInput && !validInputModes[n.EffectiveInputMode()] {
			v.add(field+".input_mode", fmt.Sprintf("input node %q has unknown input_mode %q (want text or image)", n.Key, n.InputMode))
		}
		if n.Type == NodeTypeInput && n.EffectiveInputMode() == InputModeImage && imageAttachmentID == "" {
			v.add(field+".image_attachment_id", fmt.Sprintf("image input node %q must select an image", n.Key))
		}
		if n.Type == NodeTypeInput && n.EffectiveInputMode() != InputModeImage && imageAttachmentID != "" {
			v.add(field+".image_attachment_id", fmt.Sprintf("text input node %q must not declare image_attachment_id", n.Key))
		}

		switch n.EffectiveOnFailure() {
		case FailurePolicyFail, FailurePolicyBlock, FailurePolicyRework:
			// ok
		default:
			v.add(field+".on_failure", fmt.Sprintf("node %q has unknown on_failure %q", n.Key, n.OnFailure))
		}
	}

	if endCount == 0 {
		v.add("nodes", "definition has no End node, so a Run could never complete")
	}
	// At most one input node. Two would mean two places work enters, but a Run has
	// exactly one input bag and exactly one entry node, so the second could only be
	// unreachable-as-intake: the Run dialog would collect one node's fields and the
	// engine would pass the other through with nothing filled in.
	if inputCount > 1 {
		v.add("nodes", fmt.Sprintf("definition declares %d input nodes; a Run has one input, collected at the entry node, so at most one input node is meaningful", inputCount))
	}

	validateLimits(v, d, policy)

	// Reachability and acyclicity need a well-formed edge set; running them on a
	// graph with dangling edges produces misleading results.
	if !v.HasErrors() {
		validateReachability(v, d, byKey)
		validateAcyclic(v, d, byKey)
	}

	if v.HasErrors() {
		return v
	}
	return nil
}

// validateInputFields checks an input node's declared fields.
//
// Every rule here protects the same property: a declared field must be one the
// Run dialog can render and a human can satisfy. A field that fails any of them
// is worse than a missing field, because the graph advertises an input the run
// can never actually receive — and StartRun's required-field check would then
// reject every run of a template that looks correct on the canvas.
func validateInputFields(v *ValidationErrors, n *Node, field string) {
	seen := make(map[string]bool, len(n.InputFields))
	for j := range n.InputFields {
		f := &n.InputFields[j]
		ff := fmt.Sprintf("%s.input_fields[%d]", field, j)

		if f.Key == "" {
			// The key is the JSON key the value is stored under in
			// workflow_run.input, so an empty one has nowhere to put the value.
			v.add(ff+".key", fmt.Sprintf("input node %q declares a field with no key; the key is where the submitted value is stored", n.Key))
			continue
		}
		if seen[f.Key] {
			// Two fields with one key: the dialog would send one value and the
			// second declaration would silently lose its input — including, if it
			// were the required one, its required check.
			v.add(ff+".key", fmt.Sprintf("input node %q declares duplicate field key %q; the second field's value would overwrite the first", n.Key, f.Key))
			continue
		}
		seen[f.Key] = true

		if !validInputFieldTypes[f.EffectiveType()] {
			v.add(ff+".type", fmt.Sprintf("input node %q field %q has unknown type %q (want text, textarea, or select)", n.Key, f.Key, f.Type))
			continue
		}
		// A select with no options is a dropdown with nothing in it: the dialog
		// cannot render a choice, so a required select would make the run
		// unstartable and an optional one would be dead UI.
		if f.EffectiveType() == InputFieldSelect && len(f.Options) == 0 {
			v.add(ff+".options", fmt.Sprintf("input node %q field %q is a select with no options, which the Run dialog cannot render", n.Key, f.Key))
		}
		for k, opt := range f.Options {
			if opt == "" {
				// A blank option is indistinguishable from "nothing selected", so a
				// required select could be satisfied by a value that reads as absent.
				v.add(fmt.Sprintf("%s.options[%d]", ff, k), fmt.Sprintf("input node %q field %q has a blank option, which cannot be told apart from no selection", n.Key, f.Key))
			}
		}
	}
}

func validateRouting(v *ValidationErrors, n *Node, byKey map[string]*Node, field string) {
	if n.Routing == nil {
		v.add(field+".routing", fmt.Sprintf("Agent node %q declares no routing", n.Key))
		return
	}
	r := n.Routing
	switch r.Strategy {
	case RoutingExplicit:
		if r.AgentID == "" {
			v.add(field+".routing.agent_id", fmt.Sprintf("Agent node %q uses explicit routing but sets no agent_id", n.Key))
		}
	case RoutingPreviousStep:
		if r.FromNode == "" {
			v.add(field+".routing.from_node", fmt.Sprintf("Agent node %q uses previous_step routing but sets no from_node", n.Key))
			return
		}
		from, ok := byKey[r.FromNode]
		if !ok {
			v.add(field+".routing.from_node", fmt.Sprintf("Agent node %q routes from undeclared node %q", n.Key, r.FromNode))
			return
		}
		// Only an Agent node has an Agent to inherit.
		if from.Type != NodeTypeAgent {
			v.add(field+".routing.from_node", fmt.Sprintf("Agent node %q routes from %q, which is a %s node and has no Agent to reuse", n.Key, r.FromNode, from.Type))
		}
		if r.FromNode == n.Key {
			v.add(field+".routing.from_node", fmt.Sprintf("Agent node %q routes from itself", n.Key))
		}
	case RoutingCapability:
		if r.Capability == "" {
			v.add(field+".routing.capability", fmt.Sprintf("Agent node %q uses capability routing but sets no capability", n.Key))
		}
	case "":
		v.add(field+".routing.strategy", fmt.Sprintf("Agent node %q declares no routing strategy", n.Key))
	default:
		v.add(field+".routing.strategy", fmt.Sprintf("Agent node %q has unknown routing strategy %q", n.Key, r.Strategy))
	}
}

func validateLimits(v *ValidationErrors, d *Definition, policy WorkspacePolicy) {
	l := d.Limits
	// Negative is always wrong; zero means "inherit the default".
	for _, c := range []struct {
		name  string
		value int
	}{
		{"max_attempts_per_node", l.MaxAttemptsPerNode},
		{"max_rework_rounds", l.MaxReworkRounds},
		{"max_fan_out", l.MaxFanOut},
		{"max_duration_seconds", l.MaxDurationSeconds},
		{"max_total_steps", l.MaxTotalSteps},
		{"max_cost_cents", l.MaxCostCents},
	} {
		if c.value < 0 {
			v.add("limits."+c.name, fmt.Sprintf("limits.%s is negative", c.name))
		}
	}

	// "limits above workspace policy" are rejected at publish time, so a
	// template cannot opt itself out of the workspace's cost ceiling.
	eff := d.EffectiveLimits()
	for _, c := range []struct {
		name    string
		value   int
		ceiling int
	}{
		{"max_attempts_per_node", eff.MaxAttemptsPerNode, policy.MaxAttemptsPerNode},
		{"max_rework_rounds", eff.MaxReworkRounds, policy.MaxReworkRounds},
		{"max_fan_out", eff.MaxFanOut, policy.MaxFanOut},
		{"max_duration_seconds", eff.MaxDurationSeconds, policy.MaxDurationSeconds},
		{"max_total_steps", eff.MaxTotalSteps, policy.MaxTotalSteps},
		{"max_cost_cents", eff.MaxCostCents, policy.MaxCostCents},
	} {
		if c.ceiling > 0 && c.value > c.ceiling {
			v.add("limits."+c.name, fmt.Sprintf("limits.%s=%d exceeds workspace policy ceiling %d", c.name, c.value, c.ceiling))
		}
	}
}

// validateReachability proves an End node is reachable from the entry, and warns
// about nodes that are not reachable at all. Rework edges are included: a node
// only reachable via rework is legitimate.
func validateReachability(v *ValidationErrors, d *Definition, byKey map[string]*Node) {
	if d.EntryNode == "" {
		return
	}
	seen := make(map[string]bool, len(d.Nodes))
	stack := []string{d.EntryNode}
	for len(stack) > 0 {
		key := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[key] {
			continue
		}
		seen[key] = true
		n, ok := byKey[key]
		if !ok {
			continue
		}
		for _, next := range outgoing(n) {
			if !seen[next] {
				stack = append(stack, next)
			}
		}
	}

	reachableEnd := false
	for key := range seen {
		if n, ok := byKey[key]; ok && n.Type == NodeTypeEnd {
			reachableEnd = true
			break
		}
	}
	if !reachableEnd {
		v.add("nodes", fmt.Sprintf("no End node is reachable from entry_node %q, so a Run could never complete", d.EntryNode))
	}

	// Report unreachable nodes in declaration order for a stable message.
	for i := range d.Nodes {
		if !seen[d.Nodes[i].Key] {
			v.add(fmt.Sprintf("nodes[%d]", i), fmt.Sprintf("node %q is unreachable from entry_node %q", d.Nodes[i].Key, d.EntryNode))
		}
	}
}

// outgoing returns every edge out of a node, including branches, join
// continuation, and declared rework edges.
func outgoing(n *Node) []string {
	out := make([]string, 0, len(n.Next)+len(n.Branches)+len(n.ReworkTargets))
	out = append(out, n.Next...)
	for _, b := range n.Branches {
		if b.Target != "" {
			out = append(out, b.Target)
		}
	}
	out = append(out, n.ReworkTargets...)
	return out
}

// forwardOutgoing excludes rework edges: an upstream target must be on the
// ordinary execution path that produced the result being rejected.
func forwardOutgoing(n *Node) []string {
	out := make([]string, 0, len(n.Next)+len(n.Branches))
	out = append(out, n.Next...)
	for _, b := range n.Branches {
		if b.Target != "" {
			out = append(out, b.Target)
		}
	}
	return out
}

func forwardReachable(byKey map[string]*Node, from, target string) bool {
	seen := make(map[string]bool, len(byKey))
	stack := []string{from}
	for len(stack) > 0 {
		key := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[key] {
			continue
		}
		seen[key] = true
		if key == target {
			return true
		}
		if node, ok := byKey[key]; ok {
			stack = append(stack, forwardOutgoing(node)...)
		}
	}
	return false
}

// validateAcyclic rejects cycles that are not made exclusively of declared
// rework edges. "Arbitrary cycles" are deferred (plan section 2); only explicit
// bounded rework may loop, because only rework carries an attempt ceiling that
// guarantees termination.
func validateAcyclic(v *ValidationErrors, d *Definition, byKey map[string]*Node) {
	const (
		white = 0 // unvisited
		grey  = 1 // on the current DFS path
		black = 2 // fully explored
	)
	color := make(map[string]int, len(d.Nodes))

	// Iterative DFS with an explicit edge cursor per frame, so a deep graph
	// cannot blow the goroutine stack.
	type frame struct {
		key   string
		edges []string
		i     int
	}

	for i := range d.Nodes {
		root := d.Nodes[i].Key
		if color[root] != white {
			continue
		}
		n := byKey[root]
		stack := []frame{{key: root, edges: forwardOutgoing(n)}}
		color[root] = grey

		for len(stack) > 0 {
			top := &stack[len(stack)-1]
			if top.i >= len(top.edges) {
				color[top.key] = black
				stack = stack[:len(stack)-1]
				continue
			}
			next := top.edges[top.i]
			top.i++

			switch color[next] {
			case grey:
				// Back edge on a non-rework path: a cycle the engine cannot bound.
				v.add("nodes", fmt.Sprintf("non-rework cycle detected: %q -> %q (only declared rework_targets may form a cycle)", top.key, next))
				// Keep scanning siblings; one report per back edge is enough.
			case white:
				child, ok := byKey[next]
				if !ok {
					continue
				}
				color[next] = grey
				stack = append(stack, frame{key: next, edges: forwardOutgoing(child)})
			}
		}
	}
}
