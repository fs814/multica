package workflow

import (
	"encoding/json"
	"fmt"
	"strings"
)

// The Agent Task brief for a workflow Step.
//
// This type is the fix for the central defect in the first cut of the engine:
// dispatchAgentStep created an agent_task_queue row carrying only ids
// (agent, runtime, issue, priority, accountable user) and NO work description.
// A daemon claiming that task had nothing to render into a prompt, so the agent
// was asked, in effect, to do nothing - and whatever prose it replied with then
// failed the submission contract and blocked the Step. A Step that dispatches
// without a brief is strictly worse than a Step that never dispatched.
//
// The delivery mechanism deliberately mirrors service.QuickCreateContext: a
// `type` discriminator plus payload written into agent_task_queue.context JSONB,
// read back on the daemon claim path (handler/daemon.go) and surfaced on the
// claim response. That pair is the established precedent for "a task whose whole
// job description lives in context", and reusing it means no new transport, no
// new daemon protocol, and no new failure mode.
//
// It lives in package workflow rather than package service because the engine
// writes it and package service imports package workflow (service/builtin_workflows.go),
// so the reverse import would be a cycle. handler/daemon.go already imports
// both.

// TaskContextType marks an agent_task_queue row as a workflow Step's task. The
// daemon claim path switches on it exactly like QuickCreateContextType.
const TaskContextType = "workflow_step"

// TaskContext is the JSON payload stored on a workflow Step's task context.
//
// Every field exists because the agent cannot do the work without it:
//
//   - Instruction / NodeName: what this Step is for. Without the instruction the
//     agent has no task at all.
//   - RunTitle / RunDescription / RunFields: the Run input the human supplied.
//     The graph's instruction is generic ("analyze the defect"); this is the
//     actual defect. RunFields are the extra values an input node declared.
//   - AcceptanceCriteria: what a reviewer will check, when the graph declares it.
//   - UpstreamSummary / UpstreamArtifact: the previous Step's deliverable, so
//     `implement` acts on `analyze`'s findings instead of re-deriving them.
//   - ReworkReason / ReworkFromNode: why this attempt exists, on a rework. An
//     agent re-running a node without being told what was rejected will
//     reproduce the rejected work.
//   - StepInstanceID: the Step this submission answers. ParseSubmission compares
//     it, which is what stops an agent submitting against a different attempt.
//   - SubmissionContract: the literal instructions for emitting the delimited
//     submission block. See SubmissionContractInstructions for why this is not
//     optional.
type TaskContext struct {
	Type string `json:"type"`

	// RunID / StepInstanceID / NodeKey identify the workflow position. The agent
	// echoes StepInstanceID in its submission.
	RunID          string `json:"run_id"`
	StepInstanceID string `json:"step_instance_id"`
	NodeKey        string `json:"node_key"`
	NodeName       string `json:"node_name,omitempty"`
	Attempt        int32  `json:"attempt,omitempty"`

	// WorkspaceID mirrors QuickCreateContext.WorkspaceID: the claim path uses it
	// as the authority for MULTICA_WORKSPACE_ID on a task with no issue link, and
	// the workspace-isolation check compares it against the runtime's workspace.
	WorkspaceID string `json:"workspace_id"`

	TemplateKey  string `json:"template_key,omitempty"`
	TemplateName string `json:"template_name,omitempty"`

	Instruction string `json:"instruction"`

	// RunTitle / RunDescription are the freeform Run input every template has
	// always carried. They remain separate from RunFields rather than being folded
	// into it because a published version is immutable: a template with no input
	// node has only these, forever, and RenderPrompt gives them dedicated
	// formatting (title as a heading, description as prose).
	RunTitle       string `json:"run_title,omitempty"`
	RunDescription string `json:"run_description,omitempty"`

	// RunFields are the values of the entry input node's DECLARED fields, beyond
	// title/description. Empty — and, thanks to omitempty, absent from the JSON —
	// for a template with no input node, which is what keeps an existing brief
	// byte-identical to what it was before typed inputs existed.
	RunFields       []RunInputValue     `json:"run_fields,omitempty"`
	ImageAttachment *ImageAttachmentRef `json:"image_attachment,omitempty"`

	AcceptanceCriteria []string `json:"acceptance_criteria,omitempty"`

	// UpstreamNodeKey / UpstreamVerdict / UpstreamSummary / UpstreamReferences
	// carry the immediately-preceding Step's submission. The full artifact JSON
	// is NOT copied: summary + references is what a downstream agent acts on,
	// and an unbounded artifact blob would put the previous step's entire
	// payload into every subsequent prompt.
	UpstreamNodeKey    string   `json:"upstream_node_key,omitempty"`
	UpstreamVerdict    string   `json:"upstream_verdict,omitempty"`
	UpstreamSummary    string   `json:"upstream_summary,omitempty"`
	UpstreamReferences []string `json:"upstream_references,omitempty"`

	ReworkFromNode string `json:"rework_from_node,omitempty"`
	ReworkReason   string `json:"rework_reason,omitempty"`
	ReworkDetail   string `json:"rework_detail,omitempty"`

	// SubmissionSchema is the artifact contract the node declares, echoed so the
	// prompt can name the expected artifact type.
	SubmissionSchema string `json:"submission_schema,omitempty"`

	// SubmissionContract is the verbatim instruction block telling the agent how
	// to emit its verdict. Stored on the row rather than composed by the daemon
	// so an older daemon that only knows how to print a prompt still delivers
	// the contract, and so the contract a given task was given is auditable
	// after the fact.
	SubmissionContract string `json:"submission_contract"`
}

// maxTaskContextFieldBytes bounds each freeform string copied into a task
// context. The Run description is user input and the upstream summary is agent
// output; neither is length-checked at its source, and agent_task_queue.context
// is read on every claim. Generous enough for a real bug report, small enough
// that a pasted log cannot make the row expensive.
const maxTaskContextFieldBytes = 8 * 1024

// RunInput is the Run input bag, resolved for one Definition.
//
// Title and Description are the freeform fields the Run dialog has always
// collected and remain first-class: every template published before the input
// node existed carries only those, and a published version is immutable, so the
// pair can never stop being the fallback shape.
//
// Fields carries the values of an input node's DECLARED fields. It is
// `json:"-"` on purpose. The stored bag is flat — `{"title": …, "severity": …}`
// — and the handler marshals a RunInput to produce it, so emitting a nested
// `fields` array would write a second copy of values that already live at the
// top level, and the two copies would then be free to disagree. Fields is a read
// model: populated by ParseRunInputFor from the flat bag plus the declaration,
// never serialized back into it.
type RunInput struct {
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`

	// Fields are the declared values in declaration order, so the prompt reads
	// in the order the author laid the form out rather than in map order.
	Fields []RunInputValue `json:"-"`
}

// RunInputValue is one declared field's submitted value, paired with the label
// the graph gave it. The label travels with the value because the agent's prompt
// has to name it: a bare string under a raw key like `repro_steps` is a value the
// agent has to guess the meaning of.
type RunInputValue struct {
	Key   string `json:"key"`
	Label string `json:"label,omitempty"`
	Value string `json:"value"`
}

// ParseRunInput reads the Run's input JSONB. A malformed or absent bag yields a
// zero RunInput rather than an error: the Run's input is advisory context for
// the prompt, and refusing to dispatch a Step because the optional description
// did not parse would turn a cosmetic problem into a blocked Run.
//
// It resolves title/description only. Use ParseRunInputFor when the pinned
// definition is available and its declared fields should be read too.
func ParseRunInput(raw []byte) RunInput {
	return ParseRunInputFor(raw, nil)
}

// ParseRunInputFor reads the Run's input JSONB against an input node's
// declaration.
//
// `entry` is the graph's entry input node, or nil for a template that has none —
// which is every template published before input nodes existed, and the reason
// this takes a nilable node rather than requiring one. With nil it is exactly
// ParseRunInput.
//
// Only DECLARED keys are read. The bag is deliberately open (the handler also
// writes things like project_id, and a newer client may add more), but an
// undeclared key is not something the author asked the human for, so putting it
// in an agent's prompt would inject data the graph never described.
//
// Tolerant in the same direction as ParseRunInput: a value that is not a string
// is skipped rather than failing the parse, because a cosmetic type mismatch
// must not block a dispatch. StartRun's ValidateRunInput is where a REQUIRED
// field's absence becomes a typed rejection — that check runs once, before the
// Run exists, so this read path stays a pure best-effort projection.
func ParseRunInputFor(raw []byte, entry *Node) RunInput {
	var in RunInput
	if len(raw) == 0 {
		return in
	}
	// Decode into a generic bag as well as the typed struct: the declared keys are
	// only known at runtime, so they cannot be struct fields.
	bag := map[string]any{}
	if err := json.Unmarshal(raw, &bag); err != nil {
		return RunInput{}
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return RunInput{}
	}
	if entry == nil {
		return in
	}
	for i := range entry.InputFields {
		f := &entry.InputFields[i]
		// title/description already have first-class slots, and RenderPrompt gives
		// them dedicated formatting (the title as a heading, the description as
		// prose). Listing them again as labelled fields would print the human's bug
		// report twice in one prompt — which reads as two different reports.
		if f.Key == "title" || f.Key == "description" {
			continue
		}
		s, ok := bag[f.Key].(string)
		if !ok || s == "" {
			continue
		}
		in.Fields = append(in.Fields, RunInputValue{
			Key:   f.Key,
			Label: f.DisplayLabel(),
			Value: s,
		})
	}
	return in
}

// ValidateRunInput checks a submitted run input against an input node's declared
// fields, BEFORE the Run exists.
//
// Why this is a hard rejection rather than a tolerated gap: a required field is
// the author stating that the first agent cannot do the work without this value.
// Starting the Run anyway produces a step whose prompt is missing the thing it
// was told it would have, so the agent either guesses (confident wrong work) or
// reports blocked (a burnt attempt and a human interruption) — both strictly
// worse outcomes than telling the submitter, synchronously, which field is
// missing.
//
// The error code is ErrCodeInvalidSubmission, and the choice is deliberate:
//   - It is a payload failing its DECLARED contract, which is precisely what
//     that code already means for an agent's submission against a
//     submission_schema. The handler maps it to 422 — the right status for "the
//     request was understood and refused on its contents".
//   - Not ErrCodeInvalidDefinition, which also maps to 422 but tells the client
//     the pinned GRAPH is unrunnable. The graph is fine here; the caller's values
//     are not, and a client acting on that code could reasonably disable a
//     perfectly good template.
//   - Not ErrCodeInvariantViolation, which is for server-side impossibilities and
//     maps to 500. A human leaving a field blank is not an outage.
//
// `entry` nil (a template with no input node) means there is nothing declared, so
// nothing to reject: the freeform path keeps behaving exactly as it did.
func ValidateRunInput(raw []byte, entry *Node) error {
	if entry == nil || len(entry.InputFields) == 0 {
		return nil
	}
	bag := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &bag); err != nil {
			// Unlike the read path, an unparseable bag IS fatal here: this node
			// declares fields, so "we could not read the input" cannot be reported
			// as "the required fields were satisfied".
			return newEngineError(ErrCodeInvalidSubmission,
				"run input is not a JSON object, so its declared fields cannot be checked")
		}
	}
	var problems []string
	for i := range entry.InputFields {
		f := &entry.InputFields[i]
		value, present := bag[f.Key]
		s, isString := value.(string)
		blank := !present || !isString || strings.TrimSpace(s) == ""

		if f.Required && blank {
			// Absent and whitespace-only are the same failure on purpose: a
			// description of " " satisfies a presence check and tells an agent
			// nothing.
			problems = append(problems, fmt.Sprintf("%q is required", f.DisplayLabel()))
			continue
		}
		if blank {
			continue
		}
		// A select value outside the declared options is as unusable as a missing
		// one: the author enumerated the values downstream steps are written
		// against, so an off-list value is a value nothing knows how to act on.
		if f.EffectiveType() == InputFieldSelect && !containsString(f.Options, s) {
			problems = append(problems, fmt.Sprintf("%q must be one of %s", f.DisplayLabel(), strings.Join(f.Options, ", ")))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	// All problems in one message, matching Validate's collect-then-report habit:
	// a submitter fixing a form one field per round trip gives up.
	return newEngineError(ErrCodeInvalidSubmission,
		"run input does not satisfy the workflow's declared fields: "+strings.Join(problems, "; "))
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// ParseTaskContext returns the workflow brief when the task context carries the
// workflow discriminator. The bool is false for every other task kind, so the
// daemon claim path can short-circuit exactly like parseQuickCreateContext.
func ParseTaskContext(raw []byte) (TaskContext, bool) {
	if len(raw) == 0 {
		return TaskContext{}, false
	}
	var tc TaskContext
	if err := json.Unmarshal(raw, &tc); err != nil {
		return TaskContext{}, false
	}
	if tc.Type != TaskContextType {
		return TaskContext{}, false
	}
	return tc, true
}

// SubmissionContractInstructions is what the agent must be told, verbatim, or
// the workflow cannot work.
//
// The engine accepts a Step's result ONLY as a parseable Submission: prose never
// implies a pass, and an unparseable payload blocks the Step with
// submission_contract_invalid (see SubmitResult). An agent that was never told
// about the delimited block will answer in prose - correct work, rejected
// result - and EVERY step of EVERY run will block. That failure mode is the
// entire reason the contract, and this text, exist.
//
// Composed from the same submissionOpen/submissionClose constants
// ExtractDelimitedSubmission searches for, so the instructions and the parser
// cannot drift apart.
func SubmissionContractInstructions(stepInstanceID string) string {
	var b strings.Builder
	b.WriteString("When you have finished this step you MUST end your final message with a submission block.\n")
	b.WriteString("This is not optional formatting: the workflow engine reads ONLY this block. Prose is never\n")
	b.WriteString("interpreted as success - a step whose output has no valid block is blocked as\n")
	b.WriteString("`submission_contract_invalid` and a human has to intervene, even if your work was correct.\n\n")
	b.WriteString("Emit it exactly like this, with the markers on their own lines:\n\n")
	b.WriteString(submissionOpen)
	b.WriteString("\n")
	b.WriteString("{\n")
	b.WriteString(fmt.Sprintf("  %q: %d,\n", "schema_version", SchemaVersion))
	b.WriteString(fmt.Sprintf("  %q: %q,\n", "step_instance_id", stepInstanceID))
	b.WriteString(fmt.Sprintf("  %q: \"pass | fail | blocked\",\n", "verdict"))
	b.WriteString(fmt.Sprintf("  %q: {\n", "artifact"))
	b.WriteString("    \"type\": \"the artifact kind you produced\",\n")
	b.WriteString("    \"summary\": \"what you produced, in enough detail for the next step to act on it\",\n")
	b.WriteString("    \"references\": [\"file paths, PR urls, test names - optional\"]\n")
	b.WriteString("  },\n")
	b.WriteString(fmt.Sprintf("  %q: \"why this verdict - required for fail and blocked\",\n", "rationale"))
	b.WriteString(fmt.Sprintf("  %q: 0.0,\n", "confidence"))
	b.WriteString(fmt.Sprintf("  %q: \"the underlying cause - optional, use it on fail/blocked\"\n", "root_cause"))
	b.WriteString("}\n")
	b.WriteString(submissionClose)
	b.WriteString("\n\nRules the engine enforces:\n")
	b.WriteString("  - verdict must be exactly one of pass, fail, blocked.\n")
	b.WriteString("  - `pass` REQUIRES a non-empty artifact.summary. A pass with no summary is rejected,\n")
	b.WriteString("    because acceptance has nothing to review.\n")
	b.WriteString("  - `fail` and `blocked` REQUIRE rationale or root_cause, so the rework attempt can be\n")
	b.WriteString("    told what went wrong.\n")
	b.WriteString("  - Use `blocked`, not `fail`, when you could not proceed (missing access, ambiguous\n")
	b.WriteString("    requirement, environment broken). `fail` means you did the work and it did not pass.\n")
	b.WriteString("    A human unblocks a `blocked` step; a `fail` may be sent back for rework.\n")
	b.WriteString(fmt.Sprintf("  - step_instance_id must be %q. Any other value is rejected as cross-talk.\n", stepInstanceID))
	b.WriteString("  - confidence, when present, must be between 0.0 and 1.0. Omit it if you have no basis\n")
	b.WriteString("    for a number - an absent confidence is not the same as zero confidence.\n")
	b.WriteString("  - Do not report success you did not verify. A false `pass` is the most expensive\n")
	b.WriteString("    outcome in the system: downstream steps and the human reviewer both trust it.\n")
	return b.String()
}

// truncateContextField bounds one freeform field copied into a task context,
// marking any truncation so a reader never mistakes a clipped value for the
// whole story.
func truncateContextField(s string) string {
	if len(s) <= maxTaskContextFieldBytes {
		return s
	}
	const marker = "\n...[truncated]"
	return s[:maxTaskContextFieldBytes-len(marker)] + marker
}

// truncateRunFields bounds each declared field's value with the same ceiling as
// every other freeform string on a brief.
//
// Declared values are user input on a path with no per-field length check of its
// own (the handler bounds title and description; a declared field's value has no
// such rule yet), and agent_task_queue.context is read on every claim. Per-field
// rather than per-brief so one pasted log clips itself instead of eating the
// budget of every other field. Returns nil for an empty input so the `omitempty`
// tag on TaskContext.RunFields still elides the key — a `[]` in the JSON would
// make a legacy brief differ from what it used to be.
func truncateRunFields(fields []RunInputValue) []RunInputValue {
	if len(fields) == 0 {
		return nil
	}
	out := make([]RunInputValue, 0, len(fields))
	for _, f := range fields {
		f.Value = truncateContextField(f.Value)
		f.Label = truncateContextField(f.Label)
		out = append(out, f)
	}
	return out
}

// RenderPrompt composes the agent's brief for this Step as a single markdown
// document.
//
// Rendering server-side, and shipping the result as one string, is the same
// choice QuickCreatePrompt made and it is deliberate: the daemon is a separately
// versioned binary, so any structure it has to assemble is structure an older
// daemon will assemble wrongly or not at all. A single prompt field means a
// daemon that knows only how to print a prompt still delivers the complete brief
// — including the submission contract, without which every Step blocks. The
// structured fields travel alongside for daemons and UIs that want them.
//
// Section order is by decreasing generality, so the agent reads the goal before
// the mechanics: what the workflow is, what this step is, what the human asked
// for, what the previous step produced, why this attempt exists, then how to
// report. The contract goes LAST because it is the instruction that must still be
// in view when the agent writes its final message.
func (tc TaskContext) RenderPrompt() string {
	var b strings.Builder

	b.WriteString("# Workflow step: ")
	if tc.NodeName != "" {
		b.WriteString(tc.NodeName)
	} else {
		b.WriteString(tc.NodeKey)
	}
	b.WriteString("\n\n")

	b.WriteString("You are executing one step of a multi-agent workflow. Other agents ran the steps\n")
	b.WriteString("before yours and will run the steps after. Do THIS step's work and report it in the\n")
	b.WriteString("required format — do not attempt the whole workflow, and do not do a later step's job.\n\n")

	if tc.TemplateName != "" {
		b.WriteString("Workflow: ")
		b.WriteString(tc.TemplateName)
		b.WriteString("\n")
	}
	b.WriteString("Step: ")
	b.WriteString(tc.NodeKey)
	if tc.Attempt > 1 {
		// Surfaced because attempt > 1 means an earlier attempt at this same node
		// was rejected. An agent that does not know it is retrying will reproduce
		// the rejected work.
		b.WriteString(fmt.Sprintf(" (attempt %d)", tc.Attempt))
	}
	b.WriteString("\n\n")

	b.WriteString("## Your task\n\n")
	if strings.TrimSpace(tc.Instruction) != "" {
		b.WriteString(tc.Instruction)
	} else {
		// Should be unreachable: Validate requires an instruction on an Agent
		// node. Say so explicitly rather than emitting an empty section, so the
		// agent reports blocked instead of inventing a task.
		b.WriteString("(This step has no instruction. Report a `blocked` verdict naming this as the reason " +
			"rather than guessing what was intended.)")
	}
	b.WriteString("\n\n")

	// The guard adds RunFields to the old title/description condition, and the
	// field loop writes nothing for an empty slice, so a brief with no declared
	// fields renders byte for byte what it rendered before input nodes existed.
	// That is a hard requirement, not a nicety: published versions are immutable,
	// so most Runs will forever be title/description-only, and existing tests
	// assert on this exact text.
	if tc.RunTitle != "" || tc.RunDescription != "" || len(tc.RunFields) > 0 || tc.ImageAttachment != nil {
		b.WriteString("## What this run is about\n\n")
		b.WriteString("This is what the person who started the workflow asked for. Your step's instruction\n")
		b.WriteString("above is generic; this is the specific work.\n\n")
		if tc.RunTitle != "" {
			b.WriteString("**")
			b.WriteString(tc.RunTitle)
			b.WriteString("**\n\n")
		}
		if tc.RunDescription != "" {
			b.WriteString(tc.RunDescription)
			b.WriteString("\n\n")
		}
		if tc.ImageAttachment != nil {
			b.WriteString("Image input: `")
			b.WriteString(tc.ImageAttachment.Filename)
			b.WriteString("` (`")
			b.WriteString(tc.ImageAttachment.ContentType)
			b.WriteString("). Download it locally before analysis:\n\n")
			b.WriteString("`multica attachment download ")
			b.WriteString(tc.ImageAttachment.ID)
			b.WriteString("`\n\n")
		}
		// Declared fields come last in this section and are labelled, so the agent
		// reads the free prose first and then the structured values the workflow
		// author specifically asked for. Rendered as a bolded label plus the value
		// rather than as a table: a value may itself be multi-line (a textarea), and
		// a broken table is harder to read than a plain list.
		for _, f := range tc.RunFields {
			b.WriteString("**")
			b.WriteString(f.Label)
			b.WriteString(":** ")
			b.WriteString(f.Value)
			b.WriteString("\n\n")
		}
	}

	if tc.UpstreamSummary != "" || tc.UpstreamNodeKey != "" {
		b.WriteString("## Result of the previous step\n\n")
		if tc.UpstreamNodeKey != "" {
			b.WriteString("Step `")
			b.WriteString(tc.UpstreamNodeKey)
			b.WriteString("`")
			if tc.UpstreamVerdict != "" {
				b.WriteString(" returned `")
				b.WriteString(tc.UpstreamVerdict)
				b.WriteString("`")
			}
			b.WriteString(".\n\n")
		}
		if tc.UpstreamSummary != "" {
			b.WriteString(tc.UpstreamSummary)
			b.WriteString("\n\n")
		}
		if len(tc.UpstreamReferences) > 0 {
			b.WriteString("References from that step:\n")
			for _, ref := range tc.UpstreamReferences {
				b.WriteString("  - ")
				b.WriteString(ref)
				b.WriteString("\n")
			}
			b.WriteString("\n")
		}
		b.WriteString("Build on this. Do not re-derive it from scratch unless you find it is wrong — and if\n")
		b.WriteString("you do find it wrong, say so in your rationale rather than silently working around it.\n\n")
	}

	if tc.ReworkReason != "" || tc.ReworkFromNode != "" {
		b.WriteString("## Why you are running this step again\n\n")
		if tc.ReworkFromNode != "" {
			b.WriteString("Step `")
			b.WriteString(tc.ReworkFromNode)
			b.WriteString("` sent this work back.\n\n")
		}
		if tc.ReworkReason != "" {
			b.WriteString("Reason: ")
			b.WriteString(tc.ReworkReason)
			b.WriteString("\n\n")
		}
		if tc.ReworkDetail != "" {
			b.WriteString(tc.ReworkDetail)
			b.WriteString("\n\n")
		}
		b.WriteString("Address this specifically. Repeating the previous attempt will be rejected again.\n\n")
	}

	if len(tc.AcceptanceCriteria) > 0 {
		b.WriteString("## What will be checked\n\n")
		b.WriteString("A human reviewer will judge this run against these criteria:\n\n")
		for _, c := range tc.AcceptanceCriteria {
			b.WriteString("  - ")
			b.WriteString(c)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	b.WriteString("## How to report your result\n\n")
	if tc.SubmissionSchema != "" {
		b.WriteString("Expected artifact type for this step: `")
		b.WriteString(tc.SubmissionSchema)
		b.WriteString("`\n\n")
	}
	b.WriteString(tc.SubmissionContract)
	b.WriteString("\n")

	return b.String()
}
