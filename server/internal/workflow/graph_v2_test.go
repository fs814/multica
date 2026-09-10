package workflow

import (
	"reflect"
	"strings"
	"testing"
)

func graphV2Fixture() *Definition {
	return &Definition{SchemaVersion: 2, EntryNode: "start", Nodes: []Node{
		{Key: "start", Type: NodeTypeInput, Next: []string{"a", "b"}, NextIDs: []string{"sa", "sb"}},
		{Key: "a", Type: NodeTypeAgent, Next: []string{"join"}, NextIDs: []string{"aj"}, Routing: &Routing{Strategy: RoutingCapability, Capability: "work"}, OutputPorts: []Port{{ID: "summary", Type: "string"}}},
		{Key: "b", Type: NodeTypeAgent, Next: []string{"join"}, NextIDs: []string{"bj"}, Routing: &Routing{Strategy: RoutingCapability, Capability: "work"}, OutputPorts: []Port{{ID: "summary", Type: "string"}}},
		{Key: "join", Type: NodeTypeJoin, Next: []string{"end"}, NextIDs: []string{"je"}, InputPorts: []Port{{ID: "first", Type: "string", Required: true}, {ID: "second", Type: "string", Required: true}}},
		{Key: "end", Type: NodeTypeEnd},
	}, DataEdges: []DataEdge{{ID: "data-a", Source: "a", SourcePort: "summary", Target: "join", TargetPort: "first", Order: 0}, {ID: "data-b", Source: "b", SourcePort: "summary", Target: "join", TargetPort: "second", Order: 1}}}
}
func TestGraphV2ParallelJoinAndData(t *testing.T) {
	d := graphV2Fixture()
	if err := Validate(d, DefaultWorkspacePolicy, DefaultSchemaRegistry); err != nil {
		t.Fatal(err)
	}
	steps := map[string]GraphStep{"start": {Status: "passed", Attempt: 1}}
	ready := PlanGraphV2(d, steps)
	if len(ready) != 2 || ready[0].Node != "a" || ready[1].Node != "b" {
		t.Fatalf("fan out: %+v", ready)
	}
	steps["a"] = GraphStep{Status: "passed", Attempt: 1, Output: map[string]any{"summary": "A"}}
	steps["b"] = GraphStep{Status: "queued", Attempt: 1}
	if got := PlanGraphV2(d, steps); len(got) != 0 {
		t.Fatalf("join ran early: %+v", got)
	}
	steps["b"] = GraphStep{Status: "passed", Attempt: 1, Output: map[string]any{"summary": "B"}}
	got := PlanGraphV2(d, steps)
	if len(got) != 1 || !reflect.DeepEqual(got[0].Inputs, map[string]any{"first": "A", "second": "B"}) {
		t.Fatalf("join binding: %+v", got)
	}
	steps["join"] = GraphStep{Status: "passed", Attempt: 1}
	steps["end"] = GraphStep{Status: "passed", Attempt: 1}
	if len(PlanGraphV2(d, steps)) != 0 {
		t.Fatal("replayed completed graph scheduled work")
	}
}
func TestGraphV2FailureRetryAndMissingData(t *testing.T) {
	d := graphV2Fixture()
	d.Nodes[1].MaxAttempts = 2
	steps := map[string]GraphStep{"start": {Status: "passed", Attempt: 1}, "a": {Status: "failed", Attempt: 1}, "b": {Status: "queued", Attempt: 1}}
	got := PlanGraphV2(d, steps)
	if len(got) != 1 || got[0].Node != "a" || got[0].Attempt != 2 {
		t.Fatalf("retry: %+v", got)
	}
	steps["a"] = GraphStep{Status: "failed", Attempt: 2}
	steps["b"] = GraphStep{Status: "passed", Attempt: 1, Output: map[string]any{"summary": "B"}}
	got = PlanGraphV2(d, steps)
	if len(got) != 1 || got[0].Kind != "skip" || got[0].Reason != "dependency_failed" {
		t.Fatalf("failure propagation: %+v", got)
	}
	steps["a"] = GraphStep{Status: "passed", Attempt: 2, Output: map[string]any{}}
	got = PlanGraphV2(d, steps)
	if len(got) != 1 || got[0].Kind != "fail" || !strings.Contains(got[0].Reason, "required_input_missing") {
		t.Fatalf("missing input: %+v", got)
	}
}
func TestGraphV2ConditionalJoin(t *testing.T) {
	d := graphV2Fixture()
	d.Nodes[0].Type = NodeTypeCondition
	d.Nodes[0].Next = nil
	d.Nodes[0].NextIDs = nil
	d.Nodes[0].Branches = []Branch{{ID: "first", WhenVerdict: "pass", Target: "a"}, {ID: "second", WhenVerdict: "pass", Target: "b"}, {ID: "default", Target: "b"}}
	d.Nodes[3].InputPorts = nil
	d.DataEdges = nil
	if err := Validate(d, DefaultWorkspacePolicy, DefaultSchemaRegistry); err != nil {
		t.Fatal(err)
	}
	if got := selectGraphBranch(&d.Nodes[0], nil, "pass"); got != "a" {
		t.Fatal("first matching branch lost priority")
	}
	if got := selectGraphBranch(&d.Nodes[0], nil, "unknown"); got != "b" {
		t.Fatal("default branch not selected")
	}
	steps := map[string]GraphStep{"start": {Status: "passed", Attempt: 1, Output: map[string]any{"target": "a"}}, "a": {Status: "queued", Attempt: 1}}
	got := PlanGraphV2(d, steps)
	if len(got) != 1 || got[0].Node != "b" || got[0].Kind != "skip" {
		t.Fatalf("unselected branch: %+v", got)
	}
	steps["b"] = GraphStep{Status: "skipped", Attempt: 1, SkipReason: "branch_not_selected"}
	if len(PlanGraphV2(d, steps)) != 0 {
		t.Fatal("join did not wait for activated branch")
	}
	steps["a"] = GraphStep{Status: "passed", Attempt: 1}
	got = PlanGraphV2(d, steps)
	if len(got) != 1 || got[0].Node != "join" || got[0].Kind != "ready" {
		t.Fatalf("join: %+v", got)
	}
}
func TestGraphV2ValidationAndLegacy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Definition)
	}{
		{"type mismatch", func(d *Definition) { d.Nodes[3].InputPorts[0].Type = "number" }},
		{"missing port", func(d *Definition) { d.DataEdges[0].SourcePort = "missing" }},
		{"single input", func(d *Definition) { d.DataEdges[1].TargetPort = "first" }},
		{"duplicate id", func(d *Definition) { d.DataEdges[1].ID = d.DataEdges[0].ID }},
		{"cycle", func(d *Definition) { d.Nodes[3].Next = []string{"a"} }},
		{"rework", func(d *Definition) { d.Nodes[3].ReworkTargets = []string{"a"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := graphV2Fixture()
			tc.change(d)
			if Validate(d, DefaultWorkspacePolicy, DefaultSchemaRegistry) == nil {
				t.Fatal("accepted invalid graph")
			}
		})
	}
	d := graphV2Fixture()
	d.Nodes[0].Next = nil
	d.Nodes[0].NextIDs = nil
	if ValidateDraft(d, DefaultWorkspacePolicy, DefaultSchemaRegistry) != nil {
		t.Fatal("incomplete draft rejected")
	}
	if Validate(d, DefaultWorkspacePolicy, DefaultSchemaRegistry) == nil {
		t.Fatal("incomplete graph published")
	}
	legacy := &Definition{SchemaVersion: 1, EntryNode: "end", Nodes: []Node{{Key: "end", Type: NodeTypeEnd}}}
	if err := Validate(legacy, DefaultWorkspacePolicy, DefaultSchemaRegistry); err != nil {
		t.Fatal(err)
	}
}
func TestGraphV2CollectionOrderAndExclusiveRequiredSource(t *testing.T) {
	d := graphV2Fixture()
	d.Nodes[3].InputPorts = []Port{{ID: "items", Type: "string", Multiple: true, Required: true}}
	d.DataEdges[0].TargetPort = "items"
	d.DataEdges[1].TargetPort = "items"
	d.DataEdges[0].Order = 4
	if err := Validate(d, DefaultWorkspacePolicy, DefaultSchemaRegistry); err != nil {
		t.Fatal(err)
	}
	steps := map[string]GraphStep{"start": {Status: "passed", Attempt: 1}, "a": {Status: "passed", Attempt: 1, Output: map[string]any{"summary": "A"}}, "b": {Status: "passed", Attempt: 1, Output: map[string]any{"summary": "B"}}}
	got := PlanGraphV2(d, steps)
	if len(got) != 1 || !reflect.DeepEqual(got[0].Inputs["items"], []any{"B", "A"}) {
		t.Fatalf("unstable order: %+v", got)
	}
	d.Nodes[0].Type = NodeTypeCondition
	d.Nodes[0].Next = nil
	d.Nodes[0].NextIDs = nil
	d.Nodes[0].Branches = []Branch{{ID: "a", WhenVerdict: "pass", Target: "a"}, {ID: "b", Target: "b"}}
	if Validate(d, DefaultWorkspacePolicy, DefaultSchemaRegistry) == nil {
		t.Fatal("required source on exclusive branch accepted")
	}
}

func TestGraphV2DataDependencyWaitsForRetry(t *testing.T) {
	d := graphV2Fixture()
	d.Nodes[1].MaxAttempts = 2
	// b is independent in control flow but needs a's output.
	d.Nodes[2].InputPorts = []Port{{ID: "brief", Type: "string", Required: true}}
	d.DataEdges = append(d.DataEdges, DataEdge{ID: "a-b", Source: "a", SourcePort: "summary", Target: "b", TargetPort: "brief"})
	steps := map[string]GraphStep{"start": {Status: "passed", Attempt: 1}, "a": {Status: "failed", Attempt: 1}}
	got := PlanGraphV2(d, steps)
	if len(got) != 1 || got[0].Node != "a" || got[0].Attempt != 2 {
		t.Fatalf("dependent released before retry: %+v", got)
	}
	steps["a"] = GraphStep{Status: "passed", Attempt: 2, Output: map[string]any{"summary": "retried"}}
	got = PlanGraphV2(d, steps)
	if len(got) != 1 || got[0].Node != "b" || got[0].Inputs["brief"] != "retried" {
		t.Fatalf("retry output not bound: %+v", got)
	}
}

func TestGraphV2RejectsDowngradeAndUnboundedAttempts(t *testing.T) {
	d := graphV2Fixture()
	d.SchemaVersion = 1
	if Validate(d, DefaultWorkspacePolicy, DefaultSchemaRegistry) == nil {
		t.Fatal("v2 fields were silently ignored by v1")
	}
	d.SchemaVersion = 2
	d.Nodes[1].MaxAttempts = DefaultWorkspacePolicy.MaxAttemptsPerNode + 1
	if ValidateDraft(d, DefaultWorkspacePolicy, DefaultSchemaRegistry) == nil {
		t.Fatal("draft exceeded attempt policy")
	}
	if Validate(d, DefaultWorkspacePolicy, DefaultSchemaRegistry) == nil {
		t.Fatal("published above attempt policy")
	}
}
