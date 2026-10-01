package centersync

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

func runtimeRecord(workspace, daemon string) contentRecord {
	r := contentRecord{"agent_runtime", map[string]json.RawMessage{
		"id": rawValue(uuid.NewString()), "daemon_id": rawValue(daemon), "provider": rawValue("codex"),
		"runtime_mode": rawValue("local"), "profile_id": rawValue(nil), "owner_id": rawValue(nil),
		"name": rawValue("original machine"), "custom_name": rawValue(nil),
	}}
	r.Fields["id"] = rawValue(runtimeIdentity(workspace, r))
	return r
}

func TestRuntimeWireIdentity(t *testing.T) {
	workspace, daemon := uuid.NewString(), uuid.NewString()
	r := runtimeRecord(workspace, daemon)
	key := textValue(r.Fields["id"])
	if err := validateRuntimeRecord(workspace, r); err != nil {
		t.Fatal(err)
	}
	if key == runtimeIdentity(uuid.NewString(), r) {
		t.Fatal("identity escaped workspace")
	}
	localID := uuid.NewString()
	r.Fields["id"] = rawValue(localID)
	agent := contentRecord{"agent", map[string]json.RawMessage{"runtime_id": rawValue(localID)}}
	b := contentBundle{Workspace: workspace, Records: []contentRecord{r, agent}}
	if err := normalizeRuntimeIdentities(&b); err != nil {
		t.Fatal(err)
	}
	if textValue(r.Fields["id"]) != key || textValue(agent.Fields["runtime_id"]) != key {
		t.Fatal("binding not normalized")
	}
	for _, change := range []struct {
		field string
		value any
	}{
		{"id", uuid.NewString()}, {"daemon_id", ""}, {"provider", nil}, {"runtime_mode", "cloud"}, {"profile_id", "bad"}, {"owner_id", "bad"},
	} {
		t.Run(change.field, func(t *testing.T) {
			bad := runtimeRecord(workspace, daemon)
			bad.Fields[change.field] = rawValue(change.value)
			if validateRuntimeRecord(workspace, bad) == nil {
				t.Fatal("invalid identity accepted")
			}
		})
	}
}

func TestRuntimeBindingUpgradeAndConflicts(t *testing.T) {
	fields := func(id any) map[string]json.RawMessage { return map[string]json.RawMessage{"runtime_id": rawValue(id)} }
	a, b := uuid.NewString(), uuid.NewString()
	for _, tc := range []struct {
		name            string
		local, incoming any
		base            map[string]json.RawMessage
		want            any
		conflicts       int
	}{
		{"previous import", nil, a, map[string]json.RawMessage{}, a, 0},
		{"reverse previous import", a, nil, map[string]json.RawMessage{}, a, 0},
		{"independent bindings", a, b, nil, a, 1},
		{"explicit detach", a, nil, fields(a), nil, 0},
		{"concurrent rebind", b, nil, fields(a), b, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			merged, _, conflicts := mergeFields(fields(tc.local), fields(tc.incoming), tc.base, "agent", "agent")
			if !equalValue(merged["runtime_id"], rawValue(tc.want)) || len(conflicts) != tc.conflicts {
				t.Fatalf("merge=%v conflicts=%v", merged, conflicts)
			}
		})
	}
	for _, field := range []string{"owner_id", "provider", "daemon_id", "profile_id", "runtime_mode"} {
		base := map[string]json.RawMessage{field: rawValue(a)}
		merged, _, conflicts := mergeFields(base, map[string]json.RawMessage{field: rawValue(b)}, base, "agent_runtime", "runtime")
		if textValue(merged[field]) != a || len(conflicts) != 1 {
			t.Fatal("machine identity changed through sync")
		}
	}
}

func TestRuntimeReferencesAndModelConflicts(t *testing.T) {
	workspace, daemon := uuid.NewString(), uuid.NewString()
	runtime := runtimeRecord(workspace, daemon)
	agent := contentRecord{"agent", map[string]json.RawMessage{"id": rawValue(uuid.NewString()), "runtime_id": runtime.Fields["id"]}}
	if validateReferences(contentBundle{Records: []contentRecord{agent}}) == nil {
		t.Fatal("dangling runtime accepted")
	}
	if err := validateReferences(contentBundle{Records: []contentRecord{runtime, agent}}); err != nil {
		t.Fatal(err)
	}
	runtime.Fields["profile_id"] = rawValue(uuid.NewString())
	if validateReferences(contentBundle{Records: []contentRecord{runtime, agent}}) == nil {
		t.Fatal("missing custom profile accepted")
	}
	a, b := uuid.NewString(), uuid.NewString()
	local := map[string]json.RawMessage{"runtime_id": rawValue(a), "model": rawValue("local-model")}
	incoming := map[string]json.RawMessage{"runtime_id": rawValue(b), "model": rawValue("remote-model")}
	base := map[string]json.RawMessage{"runtime_id": rawValue(a), "model": rawValue("base-model")}
	merged, next, conflicts := mergeFields(local, incoming, base, "agent", "agent")
	if !equalValue(rawValue(merged), rawValue(local)) || !equalValue(rawValue(next), rawValue(base)) || len(conflicts) == 0 {
		t.Fatal("model conflict moved runtime or advanced baseline")
	}
}
