package workflow

import (
	"strings"
	"testing"
)

// bugFixDefinition is the pilot workflow from plan section 11:
// Analyze -> Implement -> Validate -> Acceptance -> End, with rework targets.
// Used as the known-good baseline that the negative cases mutate.
func bugFixDefinition() *Definition {
	return &Definition{
		SchemaVersion: SchemaVersion,
		EntryNode:     "analyze",
		Nodes: []Node{
			{
				Key:              "analyze",
				Type:             NodeTypeAgent,
				Name:             "Analyze",
				Next:             []string{"implement"},
				Routing:          &Routing{Strategy: RoutingCapability, Capability: "bug_analysis"},
				SubmissionSchema: "analysis",
				OnFailure:        FailurePolicyBlock,
			},
			{
				Key:              "implement",
				Type:             NodeTypeAgent,
				Name:             "Implement",
				Next:             []string{"validate"},
				Routing:          &Routing{Strategy: RoutingCapability, Capability: "code_change"},
				SubmissionSchema: "code_change",
				OnFailure:        FailurePolicyRework,
				ReworkTargets:    []string{"analyze"},
			},
			{
				Key:              "validate",
				Type:             NodeTypeAgent,
				Name:             "Validate",
				Next:             []string{"acceptance"},
				Routing:          &Routing{Strategy: RoutingPreviousStep, FromNode: "implement"},
				SubmissionSchema: "test_report",
				OnFailure:        FailurePolicyRework,
				ReworkTargets:    []string{"implement"},
			},
			{
				Key:                "acceptance",
				Type:               NodeTypeAcceptance,
				Name:               "Human Acceptance",
				Next:               []string{"end"},
				AcceptanceCriteria: []string{"happy path verified", "edge case covered"},
				ReworkTargets:      []string{"analyze", "implement", "validate"},
			},
			{Key: "end", Type: NodeTypeEnd, Name: "Done"},
		},
		Limits: Limits{MaxAttemptsPerNode: 3, MaxReworkRounds: 3},
	}
}

func TestValidateAcceptsBugFixPilot(t *testing.T) {
	if err := Validate(bugFixDefinition(), DefaultWorkspacePolicy, DefaultSchemaRegistry); err != nil {
		t.Fatalf("expected the pilot Bug Fix definition to validate, got: %v", err)
	}
}

// intakeBugFixDefinition is the pilot graph with a declared intake node in front:
// intake -> analyze -> implement -> validate -> acceptance -> end.
//
// This is the fixture the input-node rejections mutate, and it has to be a
// SEPARATE baseline from bugFixDefinition rather than a mutation of it. Every
// input-node rule is about a node that must exist and must be the entry, so a
// fixture without one cannot express the failures: a table case that "removes the
// routing from the input node" against bugFixDefinition would be editing an agent
// node and would pass for the wrong reason. Keeping the entry-less baseline too is
// what proves backward compatibility - both must validate.
func intakeBugFixDefinition() *Definition {
	d := bugFixDefinition()
	d.EntryNode = "intake"
	d.Nodes = append([]Node{{
		Key:  "intake",
		Type: NodeTypeInput,
		Name: "Bug report",
		Next: []string{"analyze"},
		InputFields: []InputField{
			{Key: "title", Label: "Title", Type: InputFieldText, Required: true},
			{Key: "description", Label: "Bug description", Type: InputFieldTextarea, Required: true},
			{Key: "severity", Label: "Severity", Type: InputFieldSelect, Options: []string{"low", "high"}},
		},
	}}, d.Nodes...)
	return d
}

// TestValidateAcceptsIntakeGraph is the positive counterpart: a graph whose entry
// is a declared input node is legal, and so is the same graph without one.
func TestValidateAcceptsIntakeGraph(t *testing.T) {
	if err := Validate(intakeBugFixDefinition(), DefaultWorkspacePolicy, DefaultSchemaRegistry); err != nil {
		t.Fatalf("a graph entered through an input node must validate, got: %v", err)
	}
	// The no-input-node form must keep validating unchanged: published versions are
	// immutable, so every template that exists today has this shape forever.
	if err := Validate(bugFixDefinition(), DefaultWorkspacePolicy, DefaultSchemaRegistry); err != nil {
		t.Fatalf("a graph with NO input node must still validate, got: %v", err)
	}
}

// TestEntryInputNodeOnlyResolvesTheEntry pins the lookup the Run dialog and the
// engine both branch on. Reading "any input node" instead of "the entry node, if
// it is an input node" would make a mid-graph input node - which Validate rejects
// - look like a live declaration to callers that never validate.
func TestEntryInputNodeOnlyResolvesTheEntry(t *testing.T) {
	withIntake := intakeBugFixDefinition()
	entry, ok := withIntake.EntryInputNode()
	if !ok {
		t.Fatal("EntryInputNode must resolve the graph's declared intake node")
	}
	if entry.Key != "intake" || len(entry.InputFields) != 3 {
		t.Errorf("EntryInputNode = %q with %d fields, want intake with 3", entry.Key, len(entry.InputFields))
	}

	// The backward-compatible case: no input node at all.
	if _, ok := bugFixDefinition().EntryInputNode(); ok {
		t.Error("a graph with no input node must report none, so callers fall back to the freeform bag")
	}

	// An input node that is NOT the entry must not be mistaken for the declaration.
	misplaced := bugFixDefinition()
	misplaced.Nodes = append(misplaced.Nodes, Node{
		Key: "stray", Type: NodeTypeInput, Next: []string{"end"},
	})
	if _, ok := misplaced.EntryInputNode(); ok {
		t.Error("an input node that is not the entry must not resolve as the intake declaration")
	}
}

// TestValidateRejectsInputNodeMisuse covers the input-node contract. Separate
// from TestValidateRejects because these mutate the intake baseline, and a case
// here that ran against the entry-less fixture could not fail for its own reason.
func TestValidateRejectsInputNodeMisuse(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Definition)
		wantMsg string
	}{
		{
			name: "input node is not the entry node",
			mutate: func(d *Definition) {
				// The node stays wired in (analyze still follows it) so reachability
				// is clean and the ONLY problem is that entry moved off intake.
				d.EntryNode = "analyze"
			},
			wantMsg: "is not the entry_node",
		},
		{
			name: "two input nodes",
			mutate: func(d *Definition) {
				// Wired between intake and analyze so both are reachable and both have
				// exactly one edge: the only complaint may be the count.
				d.Nodes[0].Next = []string{"intake2"}
				d.Nodes = append(d.Nodes, Node{
					Key: "intake2", Type: NodeTypeInput, Next: []string{"analyze"},
				})
			},
			wantMsg: "input nodes",
		},
		{
			name: "input node with no outgoing edge",
			mutate: func(d *Definition) {
				d.Nodes[0].Next = nil
			},
			wantMsg: "must have exactly one outgoing edge",
		},
		{
			name: "input node with two outgoing edges",
			mutate: func(d *Definition) {
				d.Nodes[0].Next = []string{"analyze", "implement"}
			},
			wantMsg: "must have exactly one outgoing edge",
		},
		{
			name: "input node declares routing",
			mutate: func(d *Definition) {
				d.Nodes[0].Routing = &Routing{Strategy: RoutingCapability, Capability: "intake"}
			},
			wantMsg: "must not declare routing",
		},
		{
			name: "input node declares a submission schema",
			mutate: func(d *Definition) {
				// A KNOWN schema, so the failure is the misplacement and not the
				// unknown-schema rule.
				d.Nodes[0].SubmissionSchema = "analysis"
			},
			wantMsg: "must not declare a submission_schema",
		},
		{
			name: "input node declares rework targets",
			mutate: func(d *Definition) {
				d.Nodes[0].ReworkTargets = []string{"analyze"}
			},
			wantMsg: "must not declare rework_targets",
		},
		{
			name: "image input node has no selected image",
			mutate: func(d *Definition) {
				d.Nodes[0].InputMode = InputModeImage
			},
			wantMsg: "must select an image",
		},
		{
			name: "text input node carries a hidden image",
			mutate: func(d *Definition) {
				d.Nodes[0].ImageAttachmentID = "019ec09d-6222-722b-bdfa-427b105d80be"
			},
			wantMsg: "must not declare image_attachment_id",
		},
		{
			name: "acceptance reworks back to intake",
			mutate: func(d *Definition) {
				// The acceptance node is index 4 in the intake fixture.
				d.Nodes[4].ReworkTargets = append(d.Nodes[4].ReworkTargets, "intake")
			},
			wantMsg: "cannot re-prompt a human mid-run",
		},
		{
			name: "agent node declares input fields",
			mutate: func(d *Definition) {
				d.Nodes[1].InputFields = []InputField{{Key: "extra", Type: InputFieldText}}
			},
			wantMsg: "must not declare input_fields",
		},
		{
			name: "field with no key",
			mutate: func(d *Definition) {
				d.Nodes[0].InputFields[0].Key = ""
			},
			wantMsg: "declares a field with no key",
		},
		{
			name: "duplicate field key",
			mutate: func(d *Definition) {
				d.Nodes[0].InputFields = append(d.Nodes[0].InputFields,
					InputField{Key: "severity", Type: InputFieldText})
			},
			wantMsg: "duplicate field key",
		},
		{
			name: "unknown field type",
			mutate: func(d *Definition) {
				d.Nodes[0].InputFields[0].Type = "telepathy"
			},
			wantMsg: "has unknown type",
		},
		{
			name: "select with no options",
			mutate: func(d *Definition) {
				d.Nodes[0].InputFields[2].Options = nil
			},
			wantMsg: "select with no options",
		},
		{
			name: "select with a blank option",
			mutate: func(d *Definition) {
				d.Nodes[0].InputFields[2].Options = []string{"low", ""}
			},
			wantMsg: "blank option",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Prove the mutation is the ONLY problem: the baseline must be clean, or
			// a case could "pass" on an error it did not introduce.
			base := intakeBugFixDefinition()
			if err := Validate(base, DefaultWorkspacePolicy, DefaultSchemaRegistry); err != nil {
				t.Fatalf("the intake baseline must be valid before mutation, got: %v", err)
			}

			d := intakeBugFixDefinition()
			tt.mutate(d)
			err := Validate(d, DefaultWorkspacePolicy, DefaultSchemaRegistry)
			if err == nil {
				t.Fatalf("expected validation to reject %q, but it passed", tt.name)
			}
			all := err.Error()
			if ve, ok := err.(*ValidationErrors); ok {
				all = strings.Join(ve.Messages(), " | ")
			}
			if !strings.Contains(all, tt.wantMsg) {
				t.Fatalf("expected an error containing %q, got: %s", tt.wantMsg, all)
			}
		})
	}
}

// TestValidateAllowsAnInputNodeWithNoFields: an author who adds the node before
// its fields must still be able to save. The node alone already documents where
// work enters, and StartRun falls back to the freeform bag - so an empty
// declaration is a legal state, not a half-finished one.
func TestValidateAllowsAnInputNodeWithNoFields(t *testing.T) {
	d := intakeBugFixDefinition()
	d.Nodes[0].InputFields = nil
	if err := Validate(d, DefaultWorkspacePolicy, DefaultSchemaRegistry); err != nil {
		t.Fatalf("an input node with no declared fields must be legal, got: %v", err)
	}
}

// TestInputNodeSurvivesTheDefinitionRoundTrip: ParseDefinition rejects unknown
// fields, so `input_fields` must be a declared key on Node or a graph the editor
// saved would fail to parse on publish. And the values must actually come back -
// a field list that round-tripped to empty would leave the Run dialog with
// nothing to render and no error to explain why.
func TestInputNodeSurvivesTheDefinitionRoundTrip(t *testing.T) {
	definition := intakeBugFixDefinition()
	definition.Nodes[0].InputMode = InputModeImage
	definition.Nodes[0].ImageAttachmentID = "019ec09d-6222-722b-bdfa-427b105d80be"
	raw, err := MarshalDefinition(definition)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	back, err := ParseDefinition(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	entry, ok := back.EntryInputNode()
	if !ok {
		t.Fatal("the round-tripped graph lost its input node")
	}
	if len(entry.InputFields) != 3 {
		t.Fatalf("round trip kept %d fields, want 3", len(entry.InputFields))
	}
	if entry.ImageAttachmentID != "019ec09d-6222-722b-bdfa-427b105d80be" {
		t.Errorf("selected image did not survive: %q", entry.ImageAttachmentID)
	}
	sev := entry.InputFields[2]
	if sev.Key != "severity" || sev.EffectiveType() != InputFieldSelect || len(sev.Options) != 2 {
		t.Errorf("select field did not survive: %+v", sev)
	}
	if err := Validate(back, DefaultWorkspacePolicy, DefaultSchemaRegistry); err != nil {
		t.Fatalf("round-tripped intake graph must still validate: %v", err)
	}
}

// TestNoInputNodeGraphMarshalsWithoutTheNewKey is the backward-compatibility
// assertion the seeder depends on: adding InputFields to Node must not change the
// bytes of a definition that has none, or "is this built-in still pristine?"
// (decided by comparing published bytes to a shipped revision) would answer no
// for every existing template.
func TestNoInputNodeGraphMarshalsWithoutTheNewKey(t *testing.T) {
	raw, err := MarshalDefinition(bugFixDefinition())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "input_fields") {
		t.Fatalf("a graph with no input node must not emit input_fields; got: %s", raw)
	}
}

// TestValidateRejects covers the publish-time rejections enumerated in plan
// section 6. Each case mutates the known-good pilot graph in exactly one way, so
// a failure points at a single rule.
func TestValidateRejects(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Definition)
		wantMsg string
	}{
		{
			name:    "no nodes",
			mutate:  func(d *Definition) { d.Nodes = nil },
			wantMsg: "no nodes",
		},
		{
			name: "duplicate node key",
			mutate: func(d *Definition) {
				d.Nodes = append(d.Nodes, Node{Key: "analyze", Type: NodeTypeEnd})
			},
			wantMsg: "duplicate node key",
		},
		{
			name:    "empty node key",
			mutate:  func(d *Definition) { d.Nodes[0].Key = "" },
			wantMsg: "node key is empty",
		},
		{
			name:    "unknown node type",
			mutate:  func(d *Definition) { d.Nodes[0].Type = "wormhole" },
			wantMsg: "unknown type",
		},
		{
			name:    "entry node not declared",
			mutate:  func(d *Definition) { d.EntryNode = "nope" },
			wantMsg: "not a declared node",
		},
		{
			name:    "empty entry node",
			mutate:  func(d *Definition) { d.EntryNode = "" },
			wantMsg: "entry_node is empty",
		},
		{
			name:    "dangling edge",
			mutate:  func(d *Definition) { d.Nodes[0].Next = []string{"ghost"} },
			wantMsg: "points at undeclared node",
		},
		{
			name: "no end node",
			mutate: func(d *Definition) {
				// Drop the End node and retarget acceptance at itself's predecessor
				// so the only failure is the missing End.
				d.Nodes = d.Nodes[:4]
				d.Nodes[3].Next = nil
			},
			wantMsg: "no End node",
		},
		{
			name: "end node with outgoing edge",
			mutate: func(d *Definition) {
				d.Nodes[4].Next = []string{"analyze"}
			},
			wantMsg: "must not have outgoing edges",
		},
		{
			name: "non-rework cycle",
			mutate: func(d *Definition) {
				// validate -> analyze as a FORWARD edge (not a declared rework
				// target) closes an unbounded loop.
				d.Nodes[2].Next = []string{"analyze"}
			},
			wantMsg: "non-rework cycle",
		},
		{
			name: "rework target not declared",
			mutate: func(d *Definition) {
				d.Nodes[1].ReworkTargets = []string{"phantom"}
			},
			wantMsg: "undeclared rework target",
		},
		{
			name: "self rework target",
			mutate: func(d *Definition) {
				d.Nodes[1].ReworkTargets = []string{"implement"}
			},
			wantMsg: "itself as a rework target",
		},
		{
			name: "agent node without routing",
			mutate: func(d *Definition) {
				d.Nodes[0].Routing = nil
			},
			wantMsg: "declares no routing",
		},
		{
			name: "explicit routing without agent id",
			mutate: func(d *Definition) {
				d.Nodes[0].Routing = &Routing{Strategy: RoutingExplicit}
			},
			wantMsg: "sets no agent_id",
		},
		{
			name: "capability routing without capability",
			mutate: func(d *Definition) {
				d.Nodes[0].Routing = &Routing{Strategy: RoutingCapability}
			},
			wantMsg: "sets no capability",
		},
		{
			name: "previous_step routing from non-agent node",
			mutate: func(d *Definition) {
				d.Nodes[2].Routing = &Routing{Strategy: RoutingPreviousStep, FromNode: "acceptance"}
			},
			wantMsg: "has no Agent to reuse",
		},
		{
			name: "previous_step routing from undeclared node",
			mutate: func(d *Definition) {
				d.Nodes[2].Routing = &Routing{Strategy: RoutingPreviousStep, FromNode: "nobody"}
			},
			wantMsg: "routes from undeclared node",
		},
		{
			name: "unknown routing strategy",
			mutate: func(d *Definition) {
				d.Nodes[0].Routing = &Routing{Strategy: "vibes"}
			},
			wantMsg: "unknown routing strategy",
		},
		{
			name: "unknown submission schema",
			mutate: func(d *Definition) {
				d.Nodes[0].SubmissionSchema = "telepathy"
			},
			wantMsg: "unknown submission schema",
		},
		{
			name: "rework policy without targets",
			mutate: func(d *Definition) {
				d.Nodes[1].OnFailure = FailurePolicyRework
				d.Nodes[1].ReworkTargets = nil
			},
			wantMsg: "no rework_targets",
		},
		{
			name: "acceptance without rework targets",
			mutate: func(d *Definition) {
				d.Nodes[3].ReworkTargets = nil
			},
			wantMsg: "must declare at least one rework target",
		},
		{
			name: "agent node with two outgoing edges",
			mutate: func(d *Definition) {
				d.Nodes[0].Next = []string{"implement", "validate"}
			},
			wantMsg: "exactly one outgoing edge",
		},
		{
			name: "unreachable node",
			mutate: func(d *Definition) {
				d.Nodes = append(d.Nodes, Node{
					Key: "orphan", Type: NodeTypeAgent, Next: []string{"end"},
					Routing: &Routing{Strategy: RoutingCapability, Capability: "x"},
				})
			},
			wantMsg: "unreachable",
		},
		{
			name: "limits above workspace policy",
			mutate: func(d *Definition) {
				d.Limits.MaxFanOut = DefaultWorkspacePolicy.MaxFanOut + 1
			},
			wantMsg: "exceeds workspace policy ceiling",
		},
		{
			name: "negative limit",
			mutate: func(d *Definition) {
				d.Limits.MaxTotalSteps = -1
			},
			wantMsg: "is negative",
		},
		{
			name: "unsupported schema version",
			mutate: func(d *Definition) {
				d.SchemaVersion = SchemaVersion + 99
			},
			wantMsg: "unsupported schema_version",
		},
		{
			name: "join without sources",
			mutate: func(d *Definition) {
				d.Nodes[2].Next = []string{"join1"}
				d.Nodes = append(d.Nodes, Node{
					Key: "join1", Type: NodeTypeJoin, Next: []string{"acceptance"},
				})
			},
			wantMsg: "declares no join_sources",
		},
		{
			name: "condition with unknown verdict",
			mutate: func(d *Definition) {
				d.Nodes[2].Next = []string{"cond1"}
				d.Nodes = append(d.Nodes, Node{
					Key: "cond1", Type: NodeTypeCondition,
					Branches: []Branch{{WhenVerdict: "maybe", Target: "acceptance"}},
				})
			},
			wantMsg: "unknown verdict",
		},
		{
			name: "condition with two defaults",
			mutate: func(d *Definition) {
				d.Nodes[2].Next = []string{"cond1"}
				d.Nodes = append(d.Nodes, Node{
					Key: "cond1", Type: NodeTypeCondition,
					Branches: []Branch{{Target: "acceptance"}, {Target: "end"}},
				})
			},
			wantMsg: "more than one default branch",
		},
		{
			// The one input-node rule expressible against a graph whose entry is NOT
			// an input node: adding a mid-graph input node makes it an input node that
			// is not the entry. The rest of the contract needs an intake baseline and
			// lives in TestValidateRejectsInputNodeMisuse.
			name: "input node added mid-graph is not the entry",
			mutate: func(d *Definition) {
				d.Nodes[2].Next = []string{"gate"}
				d.Nodes = append(d.Nodes, Node{
					Key: "gate", Type: NodeTypeInput, Next: []string{"acceptance"},
				})
			},
			wantMsg: "is not the entry_node",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := bugFixDefinition()
			tt.mutate(d)
			err := Validate(d, DefaultWorkspacePolicy, DefaultSchemaRegistry)
			if err == nil {
				t.Fatalf("expected validation to reject %q, but it passed", tt.name)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				// The aggregate Error() only shows the first problem, so search all.
				var all string
				if ve, ok := err.(*ValidationErrors); ok {
					all = strings.Join(ve.Messages(), " | ")
				} else {
					all = err.Error()
				}
				if !strings.Contains(all, tt.wantMsg) {
					t.Fatalf("expected an error containing %q, got: %s", tt.wantMsg, all)
				}
			}
		})
	}
}

// TestValidateAllowsDeclaredReworkCycle is the counterpart to the non-rework
// cycle rejection: the pilot's rework edges intentionally form cycles
// (implement -> analyze, validate -> implement, acceptance -> all three) and
// must be accepted, because each is bounded by an attempt ceiling.
func TestValidateAllowsDeclaredReworkCycle(t *testing.T) {
	d := bugFixDefinition()
	if err := Validate(d, DefaultWorkspacePolicy, DefaultSchemaRegistry); err != nil {
		t.Fatalf("declared rework cycles must be legal, got: %v", err)
	}
	impl, _ := d.NodeByKey("implement")
	if !impl.AllowsReworkTo("analyze") {
		t.Error("implement should allow rework to analyze")
	}
	if impl.AllowsReworkTo("end") {
		t.Error("implement must not allow rework to an undeclared target")
	}
}

func TestValidateReportsEveryProblemAtOnce(t *testing.T) {
	d := bugFixDefinition()
	d.Nodes[0].Routing = nil            // problem 1
	d.Nodes[1].SubmissionSchema = "xyz" // problem 2
	err := Validate(d, DefaultWorkspacePolicy, DefaultSchemaRegistry)
	ve, ok := err.(*ValidationErrors)
	if !ok {
		t.Fatalf("expected *ValidationErrors, got %T", err)
	}
	if len(ve.Errors) < 2 {
		t.Fatalf("expected at least 2 problems reported together, got %d: %v", len(ve.Errors), ve.Messages())
	}
}

func TestEffectiveLimitsFillsDefaults(t *testing.T) {
	d := &Definition{}
	l := d.EffectiveLimits()
	if l.MaxAttemptsPerNode != DefaultLimits.MaxAttemptsPerNode {
		t.Errorf("MaxAttemptsPerNode = %d, want %d", l.MaxAttemptsPerNode, DefaultLimits.MaxAttemptsPerNode)
	}
	// Every bound must be finite: plan section 4 requires retry, rework,
	// fan-out, duration, and cost to be bounded, so there is no unlimited value.
	if l.MaxFanOut <= 0 || l.MaxDurationSeconds <= 0 || l.MaxCostCents <= 0 || l.MaxTotalSteps <= 0 || l.MaxReworkRounds <= 0 {
		t.Errorf("every effective limit must be positive and finite, got %+v", l)
	}
}

func TestEffectiveOnFailureDefaultsToBlock(t *testing.T) {
	n := &Node{Key: "x", Type: NodeTypeAgent}
	// Blocking is the safe default: it preserves the Run for diagnosis rather
	// than discarding it as failed.
	if got := n.EffectiveOnFailure(); got != FailurePolicyBlock {
		t.Errorf("default on_failure = %q, want %q", got, FailurePolicyBlock)
	}
}

func TestParseDefinitionRejectsUnknownFields(t *testing.T) {
	// A typo like "rework_target" (singular) would silently disable rework for
	// every Run pinned to the version, so it must fail at parse time.
	raw := []byte(`{"entry_node":"a","nodes":[{"key":"a","type":"end","rework_target":["b"]}]}`)
	if _, err := ParseDefinition(raw); err == nil {
		t.Fatal("expected ParseDefinition to reject an unknown field")
	}
}

func TestParseDefinitionRoundTrip(t *testing.T) {
	orig := bugFixDefinition()
	raw, err := MarshalDefinition(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	back, err := ParseDefinition(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := Validate(back, DefaultWorkspacePolicy, DefaultSchemaRegistry); err != nil {
		t.Fatalf("round-tripped definition must still validate: %v", err)
	}
	if back.EntryNode != orig.EntryNode || len(back.Nodes) != len(orig.Nodes) {
		t.Errorf("round trip changed the graph: entry %q vs %q, %d vs %d nodes",
			back.EntryNode, orig.EntryNode, len(back.Nodes), len(orig.Nodes))
	}
}
