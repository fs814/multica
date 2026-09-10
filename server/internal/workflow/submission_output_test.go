package workflow

import (
	"encoding/json"
	"testing"
)

func TestStructuredSubmissionStillValidatesVerdictsAndIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		valid        bool
	}{
		{"blocked clarification", `{"step_instance_id":"step-a","verdict":"blocked","rationale":"Which connection rules should change?"}`, true},
		{"explicit pass", `{"step_instance_id":"step-a","verdict":"pass","artifact":{"summary":"Verified the change"}}`, true},
		{"unrelated JSON", `{"title":"Task","description":"Done"}`, false},
		{"wrong step", `{"step_instance_id":"step-b","verdict":"blocked","rationale":"Need access"}`, false},
		{"unsupported verdict", `{"verdict":"success"}`, false},
		{"empty pass", `{"verdict":"pass"}`, false},
		{"plain question", "Which connection rules should change?", false},
		{"multiple objects", `{"verdict":"pass"} {"artifact":{"summary":"Done"}}`, false},
		{"marker text inside JSON remains evidence", `{"verdict":"blocked","rationale":"<<<MULTICA_SUBMISSION>>> missing closing marker"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, found := ExtractSubmission(tc.output)
			_, _, err := ParseSubmission([]byte(raw), "step-a")
			if (found && err == nil) != tc.valid {
				t.Fatalf("found=%v err=%v", found, err)
			}
		})
	}
	var schema map[string]any
	if err := json.Unmarshal(SubmissionOutputSchema("step-a"), &schema); err != nil {
		t.Fatal(err)
	}
	properties := schema["properties"].(map[string]any)
	if properties["step_instance_id"].(map[string]any)["enum"].([]any)[0] != "step-a" {
		t.Fatal("schema is not bound to the step")
	}
}
