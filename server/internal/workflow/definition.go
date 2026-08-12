// Package workflow implements the multi-agent workflow control plane that sits
// above Issue and Agent Task.
//
// The layering decision (plan section 1) is that this package does NOT replace
// the Agent Task scheduler. That scheduler already owns runtime selection,
// queueing, daemon dispatch, task lifecycle, failure classification, recovery,
// usage accounting, and realtime updates. This package adds only the
// coordination that was missing: materializing an immutable template version
// into a durable Run, activating ready Steps, converting Agent output into a
// structured Submission and Verdict, and choosing what happens next.
//
// This file defines the graph contract. A Definition is stored as JSONB on
// workflow_template_version and is immutable once published, so a Run can pin
// the exact graph it started with and stay replayable even after the template
// is edited.
package workflow

import (
	"bytes"
	"encoding/json"
)

// SchemaVersion is the definition format this server understands. It is stored
// on each version row so the engine can refuse a graph written by a newer
// server rather than silently misinterpreting unknown node semantics.
const SchemaVersion = 1

// NodeType enumerates the node kinds of the first release. Condition, FanOut,
// and Join are part of the contract now — the validator accepts and checks them
// so published graphs stay forward-compatible — but only Agent, Acceptance,
// Input, and End have executors in U2. Fan-out/Join execution lands in U7.
type NodeType string

const (
	NodeTypeAgent      NodeType = "agent"
	NodeTypeCondition  NodeType = "condition"
	NodeTypeFanOut     NodeType = "fan_out"
	NodeTypeJoin       NodeType = "join"
	NodeTypeAcceptance NodeType = "acceptance"
	NodeTypeEnd        NodeType = "end"
	// NodeTypeInput is where work ENTERS the graph: the node a human fills in
	// before the first agent runs. It exists because the alternative was
	// invisible — the Run dialog collected a title and a description that no node
	// declared, so an author reading a template could not see that the workflow
	// took an input at all. An input node makes the graph self-documenting
	// ([INPUT|intake] -> [ISSUE|analyze]) and gives the Run dialog a declaration
	// to render instead of hardcoded fields.
	//
	// It never dispatches work. See the explicit passthrough in
	// Engine.activateNode, and the DB's workflow_step_instance_task_only_on_agent
	// constraint, which makes "an intake step has no Agent Task" a row-level fact
	// rather than a convention.
	NodeTypeInput NodeType = "input"
)

// validNodeTypes mirrors the CHECK constraint on
// workflow_step_instance.node_type — currently the seven-value form installed by
// migration 251_workflow_step_input_node_type. Keeping the sets aligned means a
// definition that validates here can always be persisted as step rows; adding a
// kind here without a migration would produce a graph that publishes and then
// fails its first INSERT.
var validNodeTypes = map[NodeType]bool{
	NodeTypeAgent:      true,
	NodeTypeCondition:  true,
	NodeTypeFanOut:     true,
	NodeTypeJoin:       true,
	NodeTypeAcceptance: true,
	NodeTypeEnd:        true,
	NodeTypeInput:      true,
}

// InputFieldType is the shape of one declared intake field. Deliberately three
// kinds and no more: this is the seam typed run inputs grow from, not the
// finished feature, and every kind added here is a kind the Run dialog must be
// able to render and the engine must be able to validate.
type InputFieldType string

// InputMode describes the primary payload collected by an input node. Empty defaults to text for backwards compatibility.
type InputMode string

const (
	InputModeText  InputMode = "text"
	InputModeImage InputMode = "image"
)

func (n *Node) EffectiveInputMode() InputMode {
	if n.InputMode == "" {
		return InputModeText
	}
	return n.InputMode
}

var validInputModes = map[InputMode]bool{InputModeText: true, InputModeImage: true}

const (
	// InputFieldText is a single-line value.
	InputFieldText InputFieldType = "text"
	// InputFieldTextarea is multi-line prose (a defect report, a spec).
	InputFieldTextarea InputFieldType = "textarea"
	// InputFieldSelect is a choice from Options. A select with no options is
	// rejected by Validate: it is a field the dialog cannot render and the human
	// cannot satisfy.
	InputFieldSelect InputFieldType = "select"
)

// validInputFieldTypes bounds InputField.Type. An unknown type would reach the
// Run dialog as a field it does not know how to draw, which is a field the human
// silently cannot fill — so it is a publish-time rejection, not a UI fallback.
var validInputFieldTypes = map[InputFieldType]bool{
	InputFieldText:     true,
	InputFieldTextarea: true,
	InputFieldSelect:   true,
}

// InputField is one field an input node declares.
//
// The declaration lives on the graph rather than in the dialog because the graph
// is the immutable, versioned artifact: a Run pinned to version 3 must be
// interpretable by the version-3 field list forever, even after the template is
// edited. A dialog-side list would be whatever the client shipped last.
type InputField struct {
	// Key is the JSON key this field's value is stored under in
	// workflow_run.input. It is also what RenderPrompt falls back to when Label
	// is empty, so it must be non-empty and unique within the node.
	Key string `json:"key"`
	// Label is what the human sees, and what the agent's prompt names the value.
	// Optional: an unlabelled field renders under its Key rather than being
	// dropped, because a nameless value in a prompt is worse than an ugly one.
	Label string `json:"label,omitempty"`
	// Type is one of validInputFieldTypes. Empty means InputFieldText, so a
	// hand-written declaration that omits it still works.
	Type InputFieldType `json:"type,omitempty"`
	// Required makes an absent-or-blank value a typed StartRun rejection rather
	// than a Run that starts and then confuses its first agent.
	Required bool `json:"required,omitempty"`
	// Options are the permitted values for InputFieldSelect. Ignored for the
	// other kinds; Validate rejects a select without them.
	Options []string `json:"options,omitempty"`
	// Placeholder is dialog-only hint text. Never sent to an agent: it is an
	// example, and an example in a prompt reads as data.
	Placeholder string `json:"placeholder,omitempty"`
}

// EffectiveType resolves a field's kind, defaulting to text.
func (f *InputField) EffectiveType() InputFieldType {
	if f.Type == "" {
		return InputFieldText
	}
	return f.Type
}

// DisplayLabel is what a human or an agent should see this field called.
func (f *InputField) DisplayLabel() string {
	if f.Label != "" {
		return f.Label
	}
	return f.Key
}

// RoutingStrategy is how an Agent node picks its Agent. The engine tries them
// in the order given by plan section 8: explicit Agent, prior-Step selection,
// business capability match, then configured fallback.
type RoutingStrategy string

const (
	// RoutingExplicit pins one Agent by ID.
	RoutingExplicit RoutingStrategy = "explicit"
	// RoutingPreviousStep reuses whichever Agent ran a named earlier node, so a
	// fix lands with the Agent that wrote the code.
	RoutingPreviousStep RoutingStrategy = "previous_step"
	// RoutingCapability matches on a business capability label rather than a
	// technical provider capability.
	RoutingCapability RoutingStrategy = "capability"
)

// FailurePolicy decides what a node's failure does to the Run.
type FailurePolicy string

const (
	// FailurePolicyFail ends the Run as failed.
	FailurePolicyFail FailurePolicy = "fail"
	// FailurePolicyBlock parks the Run for human intervention, preserving the
	// distinction between "we could not proceed" and "this work failed".
	FailurePolicyBlock FailurePolicy = "block"
	// FailurePolicyRework sends work back to a bounded upstream target.
	FailurePolicyRework FailurePolicy = "rework"
)

// JoinPolicy is how an AND Join reacts to a failed sibling (plan section 8).
type JoinPolicy string

const (
	JoinPolicyFailFast JoinPolicy = "fail_fast"
	JoinPolicyContinue JoinPolicy = "continue"
	JoinPolicyPause    JoinPolicy = "pause"
	JoinPolicyRework   JoinPolicy = "rework"
)

// Definition is the whole graph: entry node, nodes, edges, and the hard limits
// that bound a Run. Edges live on the nodes themselves (Next) rather than in a
// separate list, which makes a dangling edge a local, checkable property.
type Definition struct {
	SchemaVersion int    `json:"schema_version"`
	EntryNode     string `json:"entry_node"`
	Nodes         []Node `json:"nodes"`
	Limits        Limits `json:"limits"`
}

// Node is one vertex. Only the fields meaningful for its Type are read; the
// validator rejects combinations that would be silently ignored at runtime,
// because a misplaced field almost always means the author expected behavior
// they will not get.
type Node struct {
	Key         string   `json:"key"`
	Type        NodeType `json:"type"`
	Name        string   `json:"name,omitempty"`
	Instruction string   `json:"instruction,omitempty"`

	// Next holds the outgoing edges. Agent/Acceptance/Join/FanOut nodes have
	// exactly one successor in the first release; Condition nodes branch.
	Next []string `json:"next,omitempty"`

	// Routing is required on Agent nodes.
	Routing *Routing `json:"routing,omitempty"`

	// SubmissionSchema names the expected artifact contract for an Agent node,
	// resolved against the schema registry at submission time (U3).
	SubmissionSchema string `json:"submission_schema,omitempty"`

	// AcceptanceCriteria is shown to the reviewer on an Acceptance node.
	AcceptanceCriteria []string `json:"acceptance_criteria,omitempty"`

	// OnFailure defaults to FailurePolicyBlock when empty: blocking is the safe
	// default because it preserves the Run for diagnosis instead of discarding
	// it (plan section 4, blocked is first-class).
	OnFailure FailurePolicy `json:"on_failure,omitempty"`

	// ReworkTargets bounds where this node may send work back to. An empty list
	// means rework is not permitted from here, which is what makes the graph's
	// cycles enumerable rather than arbitrary.
	ReworkTargets []string `json:"rework_targets,omitempty"`

	// MaxAttempts bounds attempts at this node including rework. Zero inherits
	// Limits.MaxAttemptsPerNode.
	MaxAttempts int `json:"max_attempts,omitempty"`

	// Condition branches, evaluated in order; the first match wins. Condition
	// nodes only.
	Branches []Branch `json:"branches,omitempty"`

	// JoinPolicy applies to Join nodes; defaults to JoinPolicyFailFast.
	JoinPolicy JoinPolicy `json:"join_policy,omitempty"`

	// JoinSources lists the fan_out/agent nodes a Join waits on. Join only.
	JoinSources []string `json:"join_sources,omitempty"`

	// FanOutMax bounds children of a fan_out node. Zero inherits
	// Limits.MaxFanOut.
	FanOutMax int `json:"fan_out_max,omitempty"`

	// InputMode is versioned with the graph. Image mode stores only an attachment reference in the Run, never image bytes or temporary URLs.
	InputMode InputMode `json:"input_mode,omitempty"`

	// ImageAttachmentID is the durable image selected while authoring an
	// image-mode input node. The pinned definition, not the Run request, chooses
	// the image so every runner executes exactly what the canvas shows.
	ImageAttachmentID string `json:"image_attachment_id,omitempty"`

	// InputFields declares what the Run dialog collects, on an input node only.
	//
	// `omitempty` is load-bearing for backward compatibility: a template with no
	// input node marshals to exactly the bytes it did before this field existed,
	// so a previously-published definition re-marshalled by MarshalDefinition is
	// still byte-identical — which is what lets the seeder decide whether a
	// built-in is pristine by comparing bytes.
	//
	// An EMPTY list on an input node is legal and means "this workflow takes no
	// typed input": the node still documents where work enters, and StartRun
	// falls back to the freeform title/description bag. That is deliberately not
	// an error, because an author adding the node before its fields should not be
	// blocked from saving a draft.
	InputFields []InputField `json:"input_fields,omitempty"`
}

// Branch is one Condition edge. Expr is intentionally not a general expression
// language in the first release: it selects on the upstream verdict, which is
// the only routing input the Submission contract guarantees.
type Branch struct {
	// WhenVerdict matches "pass", "fail", or "blocked"; empty means default.
	WhenVerdict string `json:"when_verdict,omitempty"`
	Target      string `json:"target"`
}

// Routing describes how to choose the Agent for an Agent node.
type Routing struct {
	Strategy RoutingStrategy `json:"strategy"`
	// AgentID is required for RoutingExplicit.
	AgentID string `json:"agent_id,omitempty"`
	// FromNode is required for RoutingPreviousStep.
	FromNode string `json:"from_node,omitempty"`
	// Capability is required for RoutingCapability.
	Capability string `json:"capability,omitempty"`
	// FallbackAgentID is optional for every strategy and is the last resort in
	// the routing order.
	FallbackAgentID string `json:"fallback_agent_id,omitempty"`
}

// Limits are the hard bounds a Run snapshots at start. They are snapshotted
// rather than read live so tightening workspace policy cannot retroactively
// make an in-flight Run illegal (plan section 5, policy column).
type Limits struct {
	MaxAttemptsPerNode int `json:"max_attempts_per_node,omitempty"`
	MaxReworkRounds    int `json:"max_rework_rounds,omitempty"`
	MaxFanOut          int `json:"max_fan_out,omitempty"`
	MaxDurationSeconds int `json:"max_duration_seconds,omitempty"`
	MaxTotalSteps      int `json:"max_total_steps,omitempty"`
	MaxCostCents       int `json:"max_cost_cents,omitempty"`
}

// DefaultLimits are applied where a Definition leaves a bound unset. Every
// value is finite: "retry, rework, fan-out, duration, token, and cost are
// bounded" (plan section 4), so there is no unlimited sentinel.
var DefaultLimits = Limits{
	MaxAttemptsPerNode: 3,
	MaxReworkRounds:    3,
	MaxFanOut:          10,
	MaxDurationSeconds: 24 * 60 * 60,
	MaxTotalSteps:      100,
	MaxCostCents:       10000,
}

// WorkspacePolicy is the ceiling a workspace imposes on any definition it
// publishes. Publishing rejects limits above these (plan section 6).
type WorkspacePolicy struct {
	MaxAttemptsPerNode int
	MaxReworkRounds    int
	MaxFanOut          int
	MaxDurationSeconds int
	MaxTotalSteps      int
	MaxCostCents       int
}

// DefaultWorkspacePolicy is the ceiling used until per-workspace policy is
// configurable. Chosen well above DefaultLimits so ordinary templates are
// unaffected, while still bounding a runaway definition.
var DefaultWorkspacePolicy = WorkspacePolicy{
	MaxAttemptsPerNode: 10,
	MaxReworkRounds:    10,
	MaxFanOut:          50,
	MaxDurationSeconds: 7 * 24 * 60 * 60,
	MaxTotalSteps:      1000,
	MaxCostCents:       1000000,
}

// EffectiveLimits fills unset bounds from DefaultLimits.
func (d *Definition) EffectiveLimits() Limits {
	l := d.Limits
	if l.MaxAttemptsPerNode <= 0 {
		l.MaxAttemptsPerNode = DefaultLimits.MaxAttemptsPerNode
	}
	if l.MaxReworkRounds <= 0 {
		l.MaxReworkRounds = DefaultLimits.MaxReworkRounds
	}
	if l.MaxFanOut <= 0 {
		l.MaxFanOut = DefaultLimits.MaxFanOut
	}
	if l.MaxDurationSeconds <= 0 {
		l.MaxDurationSeconds = DefaultLimits.MaxDurationSeconds
	}
	if l.MaxTotalSteps <= 0 {
		l.MaxTotalSteps = DefaultLimits.MaxTotalSteps
	}
	if l.MaxCostCents <= 0 {
		l.MaxCostCents = DefaultLimits.MaxCostCents
	}
	return l
}

// NodeByKey returns the node with the given key.
func (d *Definition) NodeByKey(key string) (*Node, bool) {
	for i := range d.Nodes {
		if d.Nodes[i].Key == key {
			return &d.Nodes[i], true
		}
	}
	return nil, false
}

// EntryInputNode returns the graph's declared intake node, or nil when the graph
// has none.
//
// It reads the ENTRY node and checks its type, rather than scanning for any
// input node, because Validate guarantees an input node is the entry node. That
// makes the lookup a single hop and, more importantly, makes the answer the same
// one the engine will act on: an input node somewhere else in the graph would be
// an unvalidatable definition, and treating it as the intake declaration here
// would let the Run dialog collect fields the engine never reaches.
//
// The (nil, false) case is the whole backward-compatibility story: every
// template published before the input node existed answers false, and every
// caller must then fall back to the freeform title/description bag rather than
// requiring a declaration that cannot exist in an immutable published version.
func (d *Definition) EntryInputNode() (*Node, bool) {
	if d.EntryNode == "" {
		return nil, false
	}
	n, ok := d.NodeByKey(d.EntryNode)
	if !ok || n.Type != NodeTypeInput {
		return nil, false
	}
	return n, true
}

// EffectiveOnFailure resolves a node's failure policy, defaulting to block so
// an unexplained failure parks the Run instead of discarding it.
func (n *Node) EffectiveOnFailure() FailurePolicy {
	if n.OnFailure == "" {
		return FailurePolicyBlock
	}
	return n.OnFailure
}

// EffectiveMaxAttempts resolves a node's attempt bound against the graph limits.
func (n *Node) EffectiveMaxAttempts(l Limits) int {
	if n.MaxAttempts > 0 {
		return n.MaxAttempts
	}
	return l.MaxAttemptsPerNode
}

// AllowsReworkTo reports whether this node may send work back to target. Rework
// edges are the only cycles the graph permits, and they must be declared.
func (n *Node) AllowsReworkTo(target string) bool {
	for _, t := range n.ReworkTargets {
		if t == target {
			return true
		}
	}
	return false
}

// ParseDefinition decodes a stored definition. It rejects unknown fields so a
// typo like "rework_target" silently disabling rework is caught at publish time
// rather than discovered when a rejection fails to route.
func ParseDefinition(raw []byte) (*Definition, error) {
	var d Definition
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return nil, &DefinitionError{Code: ErrCodeInvalidDefinition, Message: "definition is not valid JSON: " + err.Error()}
	}
	return &d, nil
}

// MarshalDefinition encodes a definition for storage.
func MarshalDefinition(d *Definition) ([]byte, error) {
	return json.Marshal(d)
}
