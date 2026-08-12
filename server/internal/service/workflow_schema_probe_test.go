package service

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/workflow"
)

// TestSchemaProbeGatesRevisionsTheDatabaseCannotRun covers a deploy-order hazard
// that no other test could see.
//
// The binary and the schema ship separately, so a new build routinely serves
// traffic against the previous migration state. On such a database the seeder
// would publish the input-node graph as the workspace's CURRENT version - the
// graph passes workflow.Validate, and template/version rows carry no node_type at
// all, so nothing on the write path objects. The CHECK only fires later, when a
// Run inserts its first step: every run of a template that looks perfectly
// healthy 500s, and the seeder never revisits it because a published current
// version is exactly what it treats as finished.
//
// These cases pin the probe's decision rather than the seeding it guards, because
// the decision is the whole mechanism: a probe that always returned true would
// leave every seeding test green while restoring the bug.
func TestSchemaProbeGatesRevisionsTheDatabaseCannotRun(t *testing.T) {
	// The vocabulary each migration renders into the CHECK. These are the real
	// shapes: 235 shipped six kinds, 251 widened it to seven.
	const pre251 = `CHECK ((node_type = ANY (ARRAY['agent'::text, 'condition'::text, 'fan_out'::text, 'join'::text, 'acceptance'::text, 'end'::text])))`
	const post251 = `CHECK ((node_type = ANY (ARRAY['agent'::text, 'condition'::text, 'fan_out'::text, 'join'::text, 'acceptance'::text, 'end'::text, 'input'::text])))`

	inputGraph := &workflow.Definition{
		EntryNode: "intake",
		Nodes: []workflow.Node{
			{Key: "intake", Type: workflow.NodeTypeInput, Next: []string{"work"}},
			{Key: "work", Type: workflow.NodeTypeAgent, Next: []string{"done"}},
			{Key: "done", Type: workflow.NodeTypeEnd},
		},
	}
	legacyGraph := &workflow.Definition{
		EntryNode: "work",
		Nodes: []workflow.Node{
			{Key: "work", Type: workflow.NodeTypeAgent, Next: []string{"done"}},
			{Key: "done", Type: workflow.NodeTypeEnd},
		},
	}

	for _, tc := range []struct {
		name       string
		constraint string
		def        *workflow.Definition
		want       bool
	}{
		{"input graph on a pre-251 schema is refused", pre251, inputGraph, false},
		{"input graph on a post-251 schema is admitted", post251, inputGraph, true},
		// The legacy graph must keep seeding on BOTH schemas, or adding a node type
		// would strand every workspace that has not migrated yet.
		{"legacy graph on a pre-251 schema still seeds", pre251, legacyGraph, true},
		{"legacy graph on a post-251 schema still seeds", post251, legacyGraph, true},
		// Fail closed: an unrecognizable constraint means we cannot prove the graph
		// is runnable, so we decline rather than publish and hope.
		{"a constraint mentioning no node_type is refused", `CHECK ((attempt > 0))`, legacyGraph, false},
		{"a missing constraint is refused", "", legacyGraph, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probe := &fakeSchemaProbe{constraint: tc.constraint}
			got, err := workflowSchemaAdmitsNodeTypes(context.Background(), probe, tc.def)
			if err != nil {
				t.Fatalf("probe returned an error: %v", err)
			}
			if got != tc.want {
				t.Errorf("admitted = %v, want %v (constraint %q)", got, tc.want, tc.constraint)
			}
		})
	}

	// A nil probe means the caller has already established the schema is current
	// (migrations run to completion in tests and in `migrate up`), so it must not
	// silently block seeding.
	got, err := workflowSchemaAdmitsNodeTypes(context.Background(), nil, inputGraph)
	if err != nil || !got {
		t.Errorf("a nil probe must skip the check, got (%v, %v)", got, err)
	}
}

// fakeSchemaProbe answers the catalog query the way Postgres would for one
// constraint definition, so the test exercises the real candidate-matching logic
// rather than a mock of the answer.
type fakeSchemaProbe struct{ constraint string }

func (f *fakeSchemaProbe) QueryRow(_ context.Context, _ string, args ...any) pgx.Row {
	types, _ := args[0].([]string)
	admitted := true
	for _, t := range types {
		if f.constraint == "" ||
			!containsAll(f.constraint, "node_type", "'"+t+"'") {
			admitted = false
			break
		}
	}
	return fakeRow{admitted: admitted}
}

func containsAll(haystack string, needles ...string) bool {
	for _, n := range needles {
		if !stringsContains(haystack, n) {
			return false
		}
	}
	return true
}

func stringsContains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

type fakeRow struct{ admitted bool }

func (r fakeRow) Scan(dest ...any) error {
	if len(dest) > 0 {
		if p, ok := dest[0].(*bool); ok {
			*p = r.admitted
		}
	}
	return nil
}
