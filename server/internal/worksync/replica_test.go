package worksync

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/google/uuid"
)

func fields(values map[string]any) Fields {
	b, _ := json.Marshal(values)
	var f Fields
	_ = json.Unmarshal(b, &f)
	return f
}
func scope() Scope { return Scope{uuid.NewString(), uuid.NewString(), uuid.NewString()} }
func config(t *testing.T) ReplicaConfig {
	return ReplicaConfig{true, t.TempDir(), scope(), Principal{"account", "actor", "node"}}
}
func open(t *testing.T, c ReplicaConfig) *Replica {
	t.Helper()
	r, e := OpenReplica(c)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}
func snapshot(s Scope, records ...Record) Batch {
	var seq int64
	for _, r := range records {
		if r.Version > seq {
			seq = r.Version
		}
	}
	b := Batch{Schema: Schema, Scope: s, Snapshot: true, Cursor: seq, Records: records}
	b.Seal()
	return b
}
func issue() Record {
	return Record{"issue", uuid.NewString(), 1, false, fields(map[string]any{"title": "original", "description": nil, "priority": "none"})}
}

func TestMergeThreeWay(t *testing.T) {
	base := issue()
	current := base
	current.Fields = fields(map[string]any{"title": "center", "description": nil, "priority": "none"})
	current.Version = 4
	merged, conflicts, err := Merge(base, current, fields(map[string]any{"priority": "high"}))
	if err != nil || len(conflicts) != 0 || string(merged["title"]) != `"center"` || string(merged["priority"]) != `"high"` {
		t.Fatalf("merge: %v %v %v", merged, conflicts, err)
	}
	_, conflicts, err = Merge(base, current, fields(map[string]any{"title": "local"}))
	if err != nil || len(conflicts) != 1 || string(conflicts[0].Base) != `"original"` || string(conflicts[0].Local) != `"local"` || string(conflicts[0].Center) != `"center"` {
		t.Fatalf("lost conflict evidence: %+v %v", conflicts, err)
	}
	_, conflicts, err = Merge(base, current, fields(map[string]any{"title": "center"}))
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("equal edit conflicts: %v %v", conflicts, err)
	}
	current.Deleted = true
	if _, _, err = Merge(base, current, fields(map[string]any{"title": "resurrect"})); err == nil {
		t.Fatal("deleted record resurrected")
	}
}

func TestReplicaRestartAtomicFailureAndDependencies(t *testing.T) {
	c := config(t)
	r := open(t, c)
	record := issue()
	b := snapshot(c.Scope, record)
	if err := r.Apply(b); err != nil {
		t.Fatal(err)
	}
	op, err := r.Queue("issue", record.ID, fields(map[string]any{"title": "first"}))
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.Queue("issue", record.ID, fields(map[string]any{"title": "second"}))
	if err != nil || second.DependsOn != op.ID {
		t.Fatalf("dependency lost: %+v %v", second, err)
	}
	before, _ := r.State()
	r.write = func([]byte) error { return errors.New("disk full") }
	if _, err = r.Queue("issue", record.ID, fields(map[string]any{"title": "not saved"})); err == nil {
		t.Fatal("disk failure acknowledged")
	}
	if err = r.Acknowledge(Receipt{Operation: op.ID, Status: "applied", Record: record}); err == nil {
		t.Fatal("lost outbox on failed save")
	}
	after, _ := r.State()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed commit changed state")
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	r = open(t, c)
	after, _ = r.State()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("restart lost confirmed records or local intent")
	}
	if err = r.Acknowledge(Receipt{Operation: op.ID, Status: "conflict", Record: record}); err != nil {
		t.Fatal(err)
	}
	s, _ := r.State()
	if len(s.Review) != 1 || s.Review[0].Operation.ID != op.ID || len(s.Outbox) != 1 {
		t.Fatalf("review lost: %+v", s)
	}
	if err = r.Apply(b); err != nil {
		t.Fatal(err)
	}
	s, _ = r.State()
	if len(s.Review) != 1 || len(s.Outbox) != 1 {
		t.Fatal("snapshot cleared local intent")
	}
}

func TestReplicaRejectsCorruptionHistoryGapsAndCrossAccount(t *testing.T) {
	c := config(t)
	r := open(t, c)
	record := issue()
	b := snapshot(c.Scope, record)
	if err := r.Apply(b); err != nil {
		t.Fatal(err)
	}
	other := b
	other.Scope.Epoch = uuid.NewString()
	other.Seal()
	if err := r.Apply(other); !errors.Is(err, ErrScope) {
		t.Fatalf("accepted another history: %v", err)
	}
	bad := b
	bad.Digest = "damaged"
	if err := r.Apply(bad); err == nil {
		t.Fatal("accepted bad digest")
	}
	record.Version = 3
	gap := Batch{Schema: Schema, Scope: c.Scope, From: 1, Cursor: 3, Records: []Record{record}}
	gap.Seal()
	if err := r.Apply(gap); !errors.Is(err, ErrCursor) {
		t.Fatalf("accepted log gap: %v", err)
	}
	if _, err := OpenReplica(c); err == nil {
		t.Fatal("second writer obtained lock")
	}
	otherConfig := c
	otherConfig.Principal.Account = "different-account"
	second := open(t, otherConfig)
	s, _ := second.State()
	if s.Initialized || len(s.Records) != 0 || second.dir == r.dir {
		t.Fatal("cross account reuse")
	}
	if err := os.WriteFile(filepath.Join(r.dir, "checkpoint.json"), []byte(`{"broken":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	if _, err := OpenReplica(c); err == nil {
		t.Fatal("silently replaced corrupted replica")
	}
}

func TestReplicaCursorAndTombstoneCommitTogether(t *testing.T) {
	c := config(t)
	r := open(t, c)
	record := issue()
	if err := r.Apply(snapshot(c.Scope, record)); err != nil {
		t.Fatal(err)
	}
	record.Version = 2
	record.Deleted = true
	record.Fields = Fields{}
	b := Batch{Schema: Schema, Scope: c.Scope, From: 1, Cursor: 2, Records: []Record{record}}
	b.Seal()
	r.write = func([]byte) error { return errors.New("I/O failure") }
	if err := r.Apply(b); err == nil {
		t.Fatal("accepted failed checkpoint")
	}
	s, _ := r.State()
	if s.Cursor != 1 || s.Records[record.Key()].Deleted {
		t.Fatal("partial checkpoint")
	}
	r.write = r.writeCheckpoint
	if err := r.Apply(b); err != nil {
		t.Fatal(err)
	}
	if err := r.Apply(b); err != nil {
		t.Fatal(err)
	}
	s, _ = r.State()
	if s.Cursor != 2 || !s.Records[record.Key()].Deleted {
		t.Fatal("lost tombstone")
	}
	if _, err := r.Queue(record.Kind, record.ID, fields(map[string]any{"title": "revive"})); err == nil {
		t.Fatal("queued resurrection")
	}
}

func TestDisabledAndFieldAllowlist(t *testing.T) {
	c := config(t)
	c.Enabled = false
	if _, err := OpenReplica(c); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(c.Root, "work-replicas")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("disabled feature wrote disk")
	}
	for _, f := range []string{"custom_env", "mcp_config", "runtime_id", "instructions", "permission_mode", "name"} {
		if err := validateFields("agent", fields(map[string]any{f: "forbidden"}), true); err == nil {
			t.Fatalf("writable agent field: %s", f)
		}
	}
	for _, patch := range []Fields{fields(map[string]any{"title": nil}), fields(map[string]any{"priority": "bogus"}), fields(map[string]any{"status": "in_progress"}), fields(map[string]any{"priority": 42})} {
		if err := validateFields("issue", patch, true); err == nil {
			t.Fatalf("accepted invalid patch: %v", patch)
		}
	}
}

func TestQueueUsesAppliedReceiptBeforePull(t *testing.T) {
	c := config(t)
	r := open(t, c)
	original := issue()
	if err := r.Apply(snapshot(c.Scope, original)); err != nil {
		t.Fatal(err)
	}
	op, err := r.Queue(original.Kind, original.ID, fields(map[string]any{"title": "first"}))
	if err != nil {
		t.Fatal(err)
	}
	applied := original
	applied.Version = 2
	applied.Fields = fields(map[string]any{"title": "first", "description": nil, "priority": "none"})
	if err = r.Acknowledge(Receipt{Operation: op.ID, Status: "applied", Record: applied}); err != nil {
		t.Fatal(err)
	}
	next, err := r.Queue(original.Kind, original.ID, fields(map[string]any{"title": "second"}))
	if err != nil {
		t.Fatal(err)
	}
	if next.Base != 2 {
		t.Fatal("new edit used stale base after acknowledged write")
	}
	state, _ := r.State()
	if state.Cursor != 1 || state.Records[original.Key()].Version != 1 {
		t.Fatal("receipt skipped pull cursor")
	}
	missing := snapshot(c.Scope)
	missing.Cursor = 2
	missing.Seal()
	if err = r.Apply(missing); err == nil {
		t.Fatal("snapshot absence treated as deletion")
	}
	batch := Batch{Schema: Schema, Scope: c.Scope, From: 1, Cursor: 2, Records: []Record{applied}}
	batch.Seal()
	if err = r.Apply(batch); err != nil {
		t.Fatal(err)
	}
	state, _ = r.State()
	if len(state.Acknowledged) != 0 || state.Cursor != 2 {
		t.Fatal("receipt base not reconciled")
	}
}
