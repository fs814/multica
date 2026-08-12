package workflow

import (
	"strings"
	"testing"
)

const testStepID = "66666666-6666-6666-6666-666666666666"

func TestParseSubmissionAcceptsCanonicalPayload(t *testing.T) {
	// The exact payload shape from plan section 6.
	raw := []byte(`{
	  "schema_version": 1,
	  "step_instance_id": "66666666-6666-6666-6666-666666666666",
	  "verdict": "pass",
	  "artifact": { "type": "code_change", "summary": "Fixed the off-by-one in pagination", "references": ["PR #123"] },
	  "rationale": "Why the result satisfies the step",
	  "confidence": 0.92,
	  "root_cause": null
	}`)
	s, problems, err := ParseSubmission(raw, testStepID)
	if err != nil {
		t.Fatalf("expected the canonical payload to parse, got %v (%v)", err, problems)
	}
	if s.Verdict != VerdictPass {
		t.Errorf("verdict = %q, want pass", s.Verdict)
	}
	if s.Artifact.Type != "code_change" || len(s.Artifact.References) != 1 {
		t.Errorf("artifact not parsed: %+v", s.Artifact)
	}
	if s.Confidence == nil || *s.Confidence != 0.92 {
		t.Errorf("confidence = %v, want 0.92", s.Confidence)
	}
}

// TestParseSubmissionRejectsNaturalLanguage is the core anti-false-success test:
// "natural language never implies pass" (plan section 6). Prose that sounds
// successful must not parse into a passing submission.
func TestParseSubmissionRejectsNaturalLanguage(t *testing.T) {
	for _, prose := range []string{
		"Done! I fixed the bug and all tests pass.",
		"Looks good to me, shipping it.",
		"完成",
		"OK",
	} {
		_, problems, err := ParseSubmission([]byte(prose), testStepID)
		if err == nil {
			t.Errorf("prose %q must not parse as a submission", prose)
		}
		if len(problems) == 0 {
			t.Errorf("prose %q should report why it was rejected", prose)
		}
	}
}

func TestParseSubmissionRejects(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantMsg string
	}{
		{
			name:    "empty payload",
			raw:     "   ",
			wantMsg: "empty",
		},
		{
			name:    "not json",
			raw:     "this is not json at all",
			wantMsg: "not valid JSON",
		},
		{
			name:    "missing verdict",
			raw:     `{"artifact":{"summary":"did stuff"},"rationale":"because"}`,
			wantMsg: "verdict is missing",
		},
		{
			name:    "unknown verdict",
			raw:     `{"verdict":"probably","rationale":"hmm"}`,
			wantMsg: "not one of pass, fail, blocked",
		},
		{
			name:    "pass without artifact summary",
			raw:     `{"verdict":"pass","rationale":"trust me"}`,
			wantMsg: "requires artifact.summary",
		},
		{
			name:    "fail without reason",
			raw:     `{"verdict":"fail","artifact":{"summary":"x"}}`,
			wantMsg: "requires rationale or root_cause",
		},
		{
			name:    "blocked without reason",
			raw:     `{"verdict":"blocked","artifact":{"summary":"x"}}`,
			wantMsg: "requires rationale or root_cause",
		},
		{
			name:    "confidence out of range",
			raw:     `{"verdict":"pass","artifact":{"summary":"x"},"confidence":42}`,
			wantMsg: "outside [0,1]",
		},
		{
			name:    "wrong step binding",
			raw:     `{"verdict":"pass","step_instance_id":"99999999-9999-9999-9999-999999999999","artifact":{"summary":"x"}}`,
			wantMsg: "does not match the claimed step",
		},
		{
			name:    "unsupported schema version",
			raw:     `{"schema_version":99,"verdict":"pass","artifact":{"summary":"x"}}`,
			wantMsg: "unsupported schema_version",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, problems, err := ParseSubmission([]byte(tt.raw), testStepID)
			if err == nil {
				t.Fatalf("expected rejection for %q", tt.name)
			}
			joined := strings.Join(problems, " | ")
			if !strings.Contains(joined, tt.wantMsg) && !strings.Contains(err.Error(), tt.wantMsg) {
				t.Fatalf("expected a problem containing %q, got: %s (err %v)", tt.wantMsg, joined, err)
			}
		})
	}
}

// A fail/blocked verdict is a VALID submission — the Step did its job and
// reported a negative result. Only contract violations are invalid.
func TestParseSubmissionAcceptsNegativeVerdicts(t *testing.T) {
	raw := []byte(`{"verdict":"fail","artifact":{"type":"test_report","summary":"3 tests failed"},"rationale":"assertion mismatch in pagination test","root_cause":"off-by-one persists"}`)
	s, problems, err := ParseSubmission(raw, testStepID)
	if err != nil {
		t.Fatalf("a fail verdict is a valid submission, got %v (%v)", err, problems)
	}
	if s.Verdict != VerdictFail {
		t.Errorf("verdict = %q, want fail", s.Verdict)
	}
	if s.RootCause == nil || *s.RootCause == "" {
		t.Error("root_cause should be preserved so a rework attempt can use it")
	}
}

// Confidence 0.0 must survive as "zero confidence", not be lost as "absent" —
// that is why the field is a pointer.
func TestParseSubmissionDistinguishesZeroConfidenceFromAbsent(t *testing.T) {
	withZero := []byte(`{"verdict":"pass","artifact":{"summary":"x"},"confidence":0}`)
	s, _, err := ParseSubmission(withZero, testStepID)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if s.Confidence == nil {
		t.Fatal("explicit confidence 0 must not be treated as absent")
	}
	if *s.Confidence != 0 {
		t.Errorf("confidence = %v, want 0", *s.Confidence)
	}

	absent := []byte(`{"verdict":"pass","artifact":{"summary":"x"}}`)
	s2, _, err := ParseSubmission(absent, testStepID)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if s2.Confidence != nil {
		t.Errorf("omitted confidence should stay nil, got %v", *s2.Confidence)
	}
}

// Extra keys are tolerated: an Agent adding a field is harmless, whereas
// rejecting the whole submission over it would block a Run for a cosmetic
// difference. (Contrast with ParseDefinition, which is strict.)
func TestParseSubmissionToleratesExtraFields(t *testing.T) {
	raw := []byte(`{"verdict":"pass","artifact":{"summary":"x"},"tokens_used":1234,"model":"claude"}`)
	if _, problems, err := ParseSubmission(raw, testStepID); err != nil {
		t.Fatalf("extra fields must be tolerated, got %v (%v)", err, problems)
	}
}

func TestParseSubmissionSkipsBindingCheckWhenUnbound(t *testing.T) {
	// Passing an empty expectStepID (e.g. reconciler-side parsing) must not
	// invent a mismatch.
	raw := []byte(`{"verdict":"pass","step_instance_id":"anything","artifact":{"summary":"x"}}`)
	if _, _, err := ParseSubmission(raw, ""); err != nil {
		t.Fatalf("unbound parse should not check the step id: %v", err)
	}
}

func TestExtractDelimitedSubmission(t *testing.T) {
	payload := `{"verdict":"pass","artifact":{"summary":"ok"}}`
	output := "Here is my analysis.\nLots of prose.\n" +
		submissionOpen + "\n" + payload + "\n" + submissionClose +
		"\nThanks!"
	got, ok := ExtractDelimitedSubmission(output)
	if !ok {
		t.Fatal("expected to find the delimited block")
	}
	if got != payload {
		t.Errorf("payload = %q, want %q", got, payload)
	}
}

// An Agent that explains the format and then submits must have the REAL
// submission parsed, not its explanation. Hence LastIndex.
func TestExtractDelimitedSubmissionTakesLastBlock(t *testing.T) {
	first := `{"verdict":"fail","rationale":"example of the format"}`
	real := `{"verdict":"pass","artifact":{"summary":"actually done"}}`
	output := "For example you would write:\n" +
		submissionOpen + "\n" + first + "\n" + submissionClose +
		"\n\nNow my real answer:\n" +
		submissionOpen + "\n" + real + "\n" + submissionClose
	got, ok := ExtractDelimitedSubmission(output)
	if !ok {
		t.Fatal("expected to find a block")
	}
	if got != real {
		t.Errorf("expected the LAST block %q, got %q", real, got)
	}
}

func TestExtractDelimitedSubmissionMissing(t *testing.T) {
	for _, output := range []string{
		"I finished the work.",
		submissionOpen + " unterminated",
		submissionOpen + "\n\n" + submissionClose, // empty payload
		"",
	} {
		if _, ok := ExtractDelimitedSubmission(output); ok {
			t.Errorf("must not find a usable block in %q", output)
		}
	}
}

func TestTruncateRawResult(t *testing.T) {
	short := "small output"
	if got := TruncateRawResult(short); got != short {
		t.Errorf("short output must pass through unchanged, got %q", got)
	}
	long := strings.Repeat("x", maxRawResultBytes*2)
	got := TruncateRawResult(long)
	if len(got) > maxRawResultBytes {
		t.Errorf("truncated length %d exceeds bound %d", len(got), maxRawResultBytes)
	}
	// Truncation must be visible, so a reader never mistakes a clipped payload
	// for the whole story.
	if !strings.Contains(got, "truncated") {
		t.Error("truncated output must be marked as truncated")
	}
}

func TestSchemaRegistryCoversPilotArtifacts(t *testing.T) {
	for _, name := range []string{"analysis", "code_change", "test_report", "review_report"} {
		if !DefaultSchemaRegistry.Has(name) {
			t.Errorf("pilot artifact schema %q should be registered", name)
		}
	}
	if DefaultSchemaRegistry.Has("telepathy") {
		t.Error("unregistered schema must not resolve")
	}
}
