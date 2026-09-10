package workflow

import (
	"encoding/json"
	"fmt"
	"strings"
)

// SubmissionOutputSchema is the same contract as ParseSubmission, constrained
// at generation time for runtimes that support structured final output.
func SubmissionOutputSchema(stepID string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{
 "type":"object","additionalProperties":false,
 "required":["schema_version","step_instance_id","verdict","artifact","rationale"],
 "properties":{
  "schema_version":{"type":"integer","enum":[1]},
  "step_instance_id":{"type":"string","enum":[%q]},
  "verdict":{"type":"string","enum":["pass","fail","blocked"]},
  "artifact":{"type":"object","additionalProperties":false,
    "required":["type","summary","references"],
    "properties":{"type":{"type":"string"},"summary":{"type":"string"},
      "references":{"type":"array","items":{"type":"string"}}}},
  "rationale":{"type":"string"}
 }
}`, stepID))
}

// ExtractSubmission also accepts a complete JSON object from a runtime using
// structured output. All candidates
// still pass through ParseSubmission; prose never implies a verdict.
func ExtractSubmission(output string) (string, bool) {
	raw := strings.TrimSpace(output)
	// A JSON string may itself quote delimiter examples; do not extract inside it.
	if strings.HasPrefix(raw, "{") && json.Valid([]byte(raw)) {
		return raw, true
	}
	return ExtractDelimitedSubmission(output)
}
