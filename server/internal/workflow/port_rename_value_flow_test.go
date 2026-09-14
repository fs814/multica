package workflow

import (
	"encoding/json"
	"os"
	"testing"
)

// The TypeScript authoring suite verifies its rename result against the same
// before/after fixtures. This checks actual values with the Go validator/planner.
func TestPortRenamePreservesProducedValues(t *testing.T) {
	raw, err := os.ReadFile("testdata/port-rename-value-flow.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name         string                               `json:"name"`
		Before       json.RawMessage                      `json:"before"`
		After        json.RawMessage                      `json:"after"`
		SourceOutput map[string]any                       `json:"source_output"`
		Expected     []struct{ Node, Port, Value string } `json:"expected"`
	}
	if err = json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		for _, version := range []struct {
			name string
			raw  json.RawMessage
		}{{"before", tc.Before}, {"after", tc.After}} {
			t.Run(tc.Name+"/"+version.name, func(t *testing.T) {
				def, err := ParseDefinition(version.raw)
				if err != nil {
					t.Fatal(err)
				}
				if err = Validate(def, DefaultWorkspacePolicy, DefaultSchemaRegistry); err != nil {
					t.Fatal(err)
				}
				steps := map[string]GraphStep{"source": {Status: "passed", Attempt: 1, Output: tc.SourceOutput}}
				for _, expected := range tc.Expected {
					decisions := PlanGraphV2(def, steps)
					if len(decisions) != 1 || decisions[0].Kind != "ready" || decisions[0].Node != expected.Node || decisions[0].Inputs[expected.Port] != expected.Value {
						t.Fatalf("required value did not survive authoring: %+v", decisions)
					}
					steps[expected.Node] = GraphStep{Status: "passed", Attempt: 1, Output: decisions[0].Inputs}
				}
			})
		}
	}
}
