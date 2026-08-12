package workflow

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Verdict is the structured judgement an Agent returns for a Step. It exists
// because free-form LLM prose is not a handoff contract: without a verdict field
// the engine would have to infer success from natural language, and "I wasn't
// able to reproduce the bug, but the code looks fine to me" is not a pass.
//
// Three values, not two. Blocked is first-class (plan section 4) so "I could not
// proceed" never collapses into "this failed", which is the difference between a
// Run a human can unblock and a Run that looks broken.
type Verdict string

const (
	VerdictPass    Verdict = "pass"
	VerdictFail    Verdict = "fail"
	VerdictBlocked Verdict = "blocked"
)

// validVerdicts mirrors the CHECK constraint on workflow_submission.verdict.
var validVerdicts = map[Verdict]bool{
	VerdictPass:    true,
	VerdictFail:    true,
	VerdictBlocked: true,
}

// Artifact is the business deliverable a Step produced.
type Artifact struct {
	Type       string   `json:"type"`
	Summary    string   `json:"summary"`
	References []string `json:"references,omitempty"`
}

// Submission is the canonical payload from plan section 6. It is what an Agent
// returns through the authenticated MCP/CLI command bound to its claimed Task,
// or — as a measured compatibility path — as a delimited final JSON block.
type Submission struct {
	SchemaVersion  int      `json:"schema_version"`
	StepInstanceID string   `json:"step_instance_id"`
	Verdict        Verdict  `json:"verdict"`
	Artifact       Artifact `json:"artifact"`
	Rationale      string   `json:"rationale"`
	// Confidence is a pointer so "absent" is distinguishable from 0.0: an Agent
	// reporting zero confidence is meaningful, and defaulting it to zero would
	// misrepresent silence as no-confidence.
	Confidence *float64 `json:"confidence,omitempty"`
	RootCause  *string  `json:"root_cause,omitempty"`
}

// maxRawResultBytes bounds the verbatim agent output retained on a submission.
// Large enough to hold a real explanation, small enough that a runaway log dump
// cannot bloat the row (the artifact column has its own 128KB CHECK).
const maxRawResultBytes = 64 * 1024

// SchemaRegistry resolves the submission schema names an Agent node may
// reference. Publishing rejects unknown names (plan section 6) so a typo cannot
// silently disable artifact validation for every Run of that version.
type SchemaRegistry interface {
	Has(name string) bool
}

// MapSchemaRegistry is the built-in registry. The artifact types listed here are
// the ones the Bug Fix pilot needs; U3 extends this with per-type field
// validation.
type MapSchemaRegistry map[string]bool

// Has implements SchemaRegistry.
func (m MapSchemaRegistry) Has(name string) bool { return m[name] }

// DefaultSchemaRegistry covers the pilot's artifact types.
var DefaultSchemaRegistry = MapSchemaRegistry{
	"analysis":      true,
	"code_change":   true,
	"test_report":   true,
	"review_report": true,
	"generic":       true,
}

// ParseSubmission decodes and validates an Agent's structured output.
//
// Parsing failure is not a pass and not an unknown failure: the caller blocks
// the Step with submission_contract_invalid and stores both the raw output and
// these messages, so a human can see exactly what the Agent said and why it was
// rejected. The returned []string is the validation_errors payload.
func ParseSubmission(raw []byte, expectStepID string) (*Submission, []string, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, []string{"submission payload is empty"}, newEngineError(ErrCodeInvalidSubmission, "empty submission payload")
	}

	var s Submission
	dec := json.NewDecoder(strings.NewReader(trimmed))
	// Unknown fields are tolerated here, unlike in a Definition: an Agent adding
	// an extra key is harmless, whereas rejecting the whole submission over it
	// would turn a cosmetic difference into a blocked Run.
	if err := dec.Decode(&s); err != nil {
		return nil, []string{"submission is not valid JSON: " + err.Error()}, newEngineError(ErrCodeInvalidSubmission, "submission is not valid JSON")
	}

	var problems []string

	if s.SchemaVersion != 0 && s.SchemaVersion != SchemaVersion {
		problems = append(problems, fmt.Sprintf("unsupported schema_version %d (expected %d)", s.SchemaVersion, SchemaVersion))
	}

	if s.Verdict == "" {
		problems = append(problems, "verdict is missing")
	} else if !validVerdicts[s.Verdict] {
		problems = append(problems, fmt.Sprintf("verdict %q is not one of pass, fail, blocked", s.Verdict))
	}

	// The submission must name the Step it answers. Without this an Agent could
	// submit against a different (or already-completed) Step attempt, which is
	// exactly the cross-talk the task binding is meant to prevent.
	if expectStepID != "" && s.StepInstanceID != "" && !strings.EqualFold(s.StepInstanceID, expectStepID) {
		problems = append(problems, fmt.Sprintf("step_instance_id %q does not match the claimed step %q", s.StepInstanceID, expectStepID))
	}

	if s.Confidence != nil && (*s.Confidence < 0 || *s.Confidence > 1) {
		problems = append(problems, fmt.Sprintf("confidence %v is outside [0,1]", *s.Confidence))
	}

	// A pass has to say what it produced; otherwise "pass" carries no reviewable
	// evidence and acceptance has nothing to show.
	if s.Verdict == VerdictPass && strings.TrimSpace(s.Artifact.Summary) == "" {
		problems = append(problems, "a pass verdict requires artifact.summary")
	}

	// A non-pass has to say why, so a rework attempt can be given the reason.
	if (s.Verdict == VerdictFail || s.Verdict == VerdictBlocked) &&
		strings.TrimSpace(s.Rationale) == "" && (s.RootCause == nil || strings.TrimSpace(*s.RootCause) == "") {
		problems = append(problems, fmt.Sprintf("a %s verdict requires rationale or root_cause", s.Verdict))
	}

	if len(problems) > 0 {
		return nil, problems, newEngineError(ErrCodeInvalidSubmission, "submission failed contract validation")
	}

	if s.SchemaVersion == 0 {
		s.SchemaVersion = SchemaVersion
	}
	return &s, nil, nil
}

// submissionDelimiter marks the structured payload in an Agent's final message.
// This is the compatibility path for runtimes that cannot yet call the MCP
// submit tool; the preferred path is the authenticated tool call bound to the
// claimed Task (plan section 6).
const (
	submissionOpen  = "<<<MULTICA_SUBMISSION>>>"
	submissionClose = "<<<END_MULTICA_SUBMISSION>>>"
)

// ExtractDelimitedSubmission pulls the delimited JSON block out of an Agent's
// free-form output. It takes the LAST occurrence: an Agent that explains the
// format mid-answer and then submits should not have its explanation parsed as
// the real payload.
//
// Returns ok=false when no block is present, which the caller treats as
// submission_contract_invalid rather than inferring a verdict from the prose.
func ExtractDelimitedSubmission(output string) (string, bool) {
	start := strings.LastIndex(output, submissionOpen)
	if start < 0 {
		return "", false
	}
	rest := output[start+len(submissionOpen):]
	end := strings.Index(rest, submissionClose)
	if end < 0 {
		return "", false
	}
	payload := strings.TrimSpace(rest[:end])
	if payload == "" {
		return "", false
	}
	return payload, true
}

// TruncateRawResult bounds retained agent output, marking any truncation so a
// reader never mistakes a clipped payload for the whole story.
func TruncateRawResult(s string) string {
	if len(s) <= maxRawResultBytes {
		return s
	}
	const marker = "\n...[truncated]"
	return s[:maxRawResultBytes-len(marker)] + marker
}
