package workflow

import "testing"

func TestValidationDiagnosticPointers(t *testing.T) {
	d := &Definition{Nodes: []Node{{Key: "first"}, {Key: "gate"}}, DataEdges: []DataEdge{{ID: "binding", Target: "gate"}}}
	v := &ValidationErrors{}
	v.add("nodes[1].input_ports[0]", "localized message with no node name")
	v.add("data_edges[0]", "another message")
	v.add("nodes[99]", "out of bounds")
	items := v.Diagnostics(d)
	if len(items) != 3 || items[0].NodeKey != "gate" || items[0].FieldPath != "nodes[1].input_ports[0]" || items[0].Code != ErrCodeInvalidDefinition {
		t.Fatalf("node diagnostic: %+v", items)
	}
	if items[1].NodeKey != "gate" || items[1].EdgeID != "binding" {
		t.Fatalf("edge diagnostic: %+v", items[1])
	}
	if items[2].NodeKey != "" {
		t.Fatal("invalid index must not invent a node")
	}
	for i, message := range v.Messages() {
		if message != items[i].Message {
			t.Fatal("legacy messages changed")
		}
	}
}

func TestGraphV2StructuredDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name              string
		mutate            func(*Definition)
		field, node, edge string
	}{
		{"missing required input", func(d *Definition) { d.DataEdges = d.DataEdges[1:] }, "nodes[3].input_ports[0]", "join", ""},
		{"incompatible binding", func(d *Definition) { d.Nodes[3].InputPorts[0].Type = "number" }, "data_edges[0]", "join", "data-a"},
		{"duplicate flow id", func(d *Definition) { d.Nodes[0].NextIDs[1] = "sa" }, "nodes[0].next_ids[1]", "start", "sa"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := graphV2Fixture()
			tc.mutate(d)
			err := Validate(d, DefaultWorkspacePolicy, DefaultSchemaRegistry)
			failures, ok := err.(*ValidationErrors)
			if !ok {
				t.Fatalf("expected diagnostics, got %v", err)
			}
			for _, item := range failures.Diagnostics(d) {
				if item.FieldPath == tc.field && item.NodeKey == tc.node && item.EdgeID == tc.edge && item.Code == ErrCodeInvalidDefinition {
					return
				}
			}
			t.Fatalf("missing pointer %s/%s/%s: %+v", tc.field, tc.node, tc.edge, failures.Diagnostics(d))
		})
	}
}
