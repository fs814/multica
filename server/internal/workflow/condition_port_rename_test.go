package workflow

import (
	"encoding/json"
	"os"
	"testing"
)

// The TS authoring suite checks these exact after graphs against renameWorkflowPort.
// Use the production planner and selector; preserve executeGraphControl's implicit
// verdict lookup/default so an editor rename cannot silently change its branch.
func TestConditionPortRenamePreservesBranch(t *testing.T) {
	raw, err := os.ReadFile("testdata/condition-port-rename.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name          string
		Before, After json.RawMessage
		Targets       map[string]string
	}
	if err = json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		for _, version := range []struct {
			name string
			raw  json.RawMessage
		}{{"before", tc.Before}, {"after", tc.After}} {
			for value, expected := range tc.Targets {
				t.Run(tc.Name+"/"+version.name+"/"+value, func(t *testing.T) {
					def, err := ParseDefinition(version.raw)
					if err != nil {
						t.Fatal(err)
					}
					if err = Validate(def, DefaultWorkspacePolicy, DefaultSchemaRegistry); err != nil {
						t.Fatal(err)
					}
					decisions := PlanGraphV2(def, map[string]GraphStep{"source": {Status: "passed", Attempt: 1, Output: map[string]any{"title": value}}})
					if len(decisions) != 1 || decisions[0].Node != "gate" || decisions[0].Kind != "ready" {
						t.Fatalf("unexpected plan: %+v", decisions)
					}
					verdict := "pass"
					if v, ok := decisions[0].Inputs["verdict"].(string); ok {
						verdict = v
					}
					node, _ := def.NodeByKey("gate")
					if got := selectGraphBranch(node, decisions[0].Inputs, verdict); got != expected {
						t.Fatalf("branch changed: got %s want %s; bound inputs=%v", got, expected, decisions[0].Inputs)
					}
				})
			}
		}
	}
}
