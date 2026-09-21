package handler

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// Tests for RedisRuntimeCLIListStore / RedisRuntimeCLIRunStore, shaped after
// runtime_update_redis_store_test.go and runtime_local_skills_redis_store_test.go.
//
// These stores are the multi-node path: the Web POST that enqueues a CLI run,
// the daemon heartbeat that claims it, the daemon report, and the UI poll can
// all land on different API nodes, so nothing may live in process memory. The
// three things these tests pin are the three ways that contract can break
// silently:
//
//  1. The public structs hide InitiatorID / RunningLimit / RunStartedAt behind
//     `json:"-"`. They survive only because cliRunEnvelope / cliListEnvelope
//     re-promote them. Lose that and the audit row loses its initiator, per-entry
//     timeouts stop firing, and the running bound never trips — all without a
//     single error.
//  2. The claim (ZREM pending + persist running) must be one atomic unit via the
//     shared claimPendingScript, or two nodes both dispatch the same run.
//  3. Timeouts are applied inside load(), not by a background sweeper, so a
//     record that went stale on another node must still surface as terminal.

// TestCLIRunEnvelope_WireTagsCarryJSONDashFields pins the envelope's JSON key
// names. The public struct's `json:"-"` fields travel under these short tags;
// renaming one silently drops the field for every request already sitting in
// Redis at deploy time (the old writer's key no longer matches the new reader's).
func TestCLIRunEnvelope_WireTagsCarryJSONDashFields(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	req := &RuntimeCLIRunRequest{
		ID:           "run-wire",
		RuntimeID:    "rt-wire",
		CLIKey:       "zhihu",
		Status:       RuntimeCLIRunning,
		InitiatorID:  "user-1",
		RunningLimit: 90 * time.Second,
		RunStartedAt: &now,
	}

	data, err := json.Marshal(cliRunEnvelope{
		Public:       req,
		RunStartedAt: req.RunStartedAt,
		Initiator:    req.InitiatorID,
		RunningLimit: req.RunningLimit,
	})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal raw envelope: %v", err)
	}
	for _, key := range []string{"r", "s", "i", "t"} {
		if _, ok := raw[key]; !ok {
			t.Fatalf("envelope wire key %q missing from %s", key, data)
		}
	}
	// The public payload must NOT carry them under their own names — if it did,
	// the envelope would be redundant and a future reader might delete it.
	var public map[string]json.RawMessage
	if err := json.Unmarshal(raw["r"], &public); err != nil {
		t.Fatalf("unmarshal public payload: %v", err)
	}
	for _, key := range []string{"initiator_id", "running_limit", "run_started_at"} {
		if _, ok := public[key]; ok {
			t.Fatalf("public payload unexpectedly serialises %q: %s", key, raw["r"])
		}
	}
}

// ---------------------------------------------------------------------------
// Registry listing
// ---------------------------------------------------------------------------

func TestRedisCLIListStore_CreateGetCompleteAcrossInstances(t *testing.T) {
	rdb := newRedisTestClient(t)
	ctx := context.Background()

	nodeA := NewRedisRuntimeCLIListStore(rdb)
	nodeB := NewRedisRuntimeCLIListStore(rdb)
	nodeC := NewRedisRuntimeCLIListStore(rdb)
	nodeD := NewRedisRuntimeCLIListStore(rdb)

	req, err := nodeA.Create(ctx, "runtime-list")
	if err != nil {
		t.Fatalf("node A create: %v", err)
	}
	if req.Status != RuntimeCLIPending {
		t.Fatalf("initial status = %s, want %s", req.Status, RuntimeCLIPending)
	}

	// A different node must see the request the first node enqueued.
	got, err := nodeB.Get(ctx, req.ID)
	if err != nil {
		t.Fatalf("node B get: %v", err)
	}
	if got == nil || got.ID != req.ID || got.RuntimeID != "runtime-list" {
		t.Fatalf("node B round trip mismatch: %+v", got)
	}
	if got.Status != RuntimeCLIPending {
		t.Fatalf("node B status = %s, want %s", got.Status, RuntimeCLIPending)
	}

	clis := []RuntimeCLISummary{{
		Key:            "zhihu",
		Label:          "知乎 CLI",
		Params:         []RuntimeCLIParamDescriptor{{Name: "command", Type: "enum", Required: true, Values: []string{"status", "hot"}}},
		TimeoutSeconds: 60,
		MaxOutputBytes: 65536,
		Available:      true,
	}}
	if err := nodeC.Complete(ctx, req.ID, clis, "/home/u/.multica/clis.json"); err != nil {
		t.Fatalf("node C complete: %v", err)
	}

	final, err := nodeD.Get(ctx, req.ID)
	if err != nil {
		t.Fatalf("node D get: %v", err)
	}
	if final.Status != RuntimeCLICompleted {
		t.Fatalf("terminal status = %s, want %s", final.Status, RuntimeCLICompleted)
	}
	if final.RegistryPath != "/home/u/.multica/clis.json" {
		t.Fatalf("registry path lost: %q", final.RegistryPath)
	}
	if len(final.CLIs) != 1 || final.CLIs[0].Key != "zhihu" || !final.CLIs[0].Available {
		t.Fatalf("registry summaries lost: %+v", final.CLIs)
	}
	if len(final.CLIs[0].Params) != 1 || final.CLIs[0].Params[0].Name != "command" {
		t.Fatalf("param descriptors lost: %+v", final.CLIs[0].Params)
	}
	if !final.UpdatedAt.After(final.CreatedAt) {
		t.Fatalf("updated_at %s not after created_at %s", final.UpdatedAt, final.CreatedAt)
	}
}

func TestRedisCLIListStore_FailAcrossInstances(t *testing.T) {
	rdb := newRedisTestClient(t)
	ctx := context.Background()

	nodeA := NewRedisRuntimeCLIListStore(rdb)
	nodeB := NewRedisRuntimeCLIListStore(rdb)

	req, err := nodeA.Create(ctx, "runtime-list-fail")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := nodeB.PopPending(ctx, "runtime-list-fail"); err != nil {
		t.Fatalf("pop: %v", err)
	}
	if err := nodeB.Fail(ctx, req.ID, "registry file unreadable"); err != nil {
		t.Fatalf("fail: %v", err)
	}

	got, err := nodeA.Get(ctx, req.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != RuntimeCLIFailed || got.Error != "registry file unreadable" {
		t.Fatalf("failed request mismatch: %+v", got)
	}
}

func TestRedisCLIListStore_PopPendingAcrossInstances(t *testing.T) {
	rdb := newRedisTestClient(t)
	ctx := context.Background()

	nodeA := NewRedisRuntimeCLIListStore(rdb)
	nodeB := NewRedisRuntimeCLIListStore(rdb)

	req, err := nodeA.Create(ctx, "runtime-list-pop")
	if err != nil {
		t.Fatalf("node A create: %v", err)
	}

	popped, err := nodeB.PopPending(ctx, "runtime-list-pop")
	if err != nil {
		t.Fatalf("node B pop: %v", err)
	}
	if popped == nil {
		t.Fatal("node B did not see node A's pending list request")
	}
	if popped.ID != req.ID || popped.Status != RuntimeCLIRunning {
		t.Fatalf("popped mismatch: %+v", popped)
	}
	if popped.RunStartedAt == nil {
		t.Fatal("RunStartedAt not set after pop")
	}

	again, err := nodeB.PopPending(ctx, "runtime-list-pop")
	if err != nil {
		t.Fatalf("node B second pop: %v", err)
	}
	if again != nil {
		t.Fatalf("expected no more pending, got %+v", again)
	}
}

// TestRedisCLIListStore_RunStartedAtSurvivesRedisHop is the list-store half of
// the json:"-" contract. RunStartedAt is the only input to the running bound in
// applyCLIListTimeout, and it is stripped from the public payload — so a reader
// on another node sees nil unless the envelope re-promotes it. A nil here does
// not error; it just means a daemon that claimed the request and then died holds
// the request in "running" forever.
func TestRedisCLIListStore_RunStartedAtSurvivesRedisHop(t *testing.T) {
	rdb := newRedisTestClient(t)
	ctx := context.Background()

	nodeA := NewRedisRuntimeCLIListStore(rdb)
	nodeB := NewRedisRuntimeCLIListStore(rdb)
	nodeC := NewRedisRuntimeCLIListStore(rdb)

	req, err := nodeA.Create(ctx, "runtime-list-hop")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	popped, err := nodeB.PopPending(ctx, "runtime-list-hop")
	if err != nil {
		t.Fatalf("pop: %v", err)
	}
	if popped == nil || popped.RunStartedAt == nil {
		t.Fatalf("pop did not set RunStartedAt: %+v", popped)
	}

	// Re-read from Redis through a third node: this is a pure decode, so a nil
	// here means the field never made it to the wire.
	reloaded, err := nodeC.Get(ctx, req.ID)
	if err != nil {
		t.Fatalf("node C get: %v", err)
	}
	if reloaded.Status != RuntimeCLIRunning {
		t.Fatalf("status = %s, want %s", reloaded.Status, RuntimeCLIRunning)
	}
	if reloaded.RunStartedAt == nil {
		t.Fatal("RunStartedAt lost across the Redis hop — running timeout will never fire")
	}
	if !reloaded.RunStartedAt.Equal(*popped.RunStartedAt) {
		t.Fatalf("RunStartedAt = %s, want %s", reloaded.RunStartedAt, *popped.RunStartedAt)
	}
}

// TestRedisCLIListStore_PopPendingAtomicClaim pins the reason the claim goes
// through the shared claimPendingScript instead of two round trips. A
// non-atomic claim (persist "running", forget the ZREM) looks perfectly healthy
// from the happy path — the request is dispatched and the record reads
// "running" — while the id stays queued, so the next heartbeat hands the same
// request to a second daemon. The half that a non-atomic claim forgets is the
// queue, so the queue is what this test reads.
func TestRedisCLIListStore_PopPendingAtomicClaim(t *testing.T) {
	rdb := newRedisTestClient(t)
	ctx := context.Background()
	store := NewRedisRuntimeCLIListStore(rdb)

	req, err := store.Create(ctx, "runtime-list-atomic")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	popped, err := store.PopPending(ctx, "runtime-list-atomic")
	if err != nil {
		t.Fatalf("pop: %v", err)
	}
	if popped == nil || popped.ID != req.ID {
		t.Fatalf("pop returned wrong request: %+v", popped)
	}

	got, err := store.Get(ctx, req.ID)
	if err != nil {
		t.Fatalf("get after pop: %v", err)
	}
	if got.Status != RuntimeCLIRunning {
		t.Fatalf("record status = %s, want %s", got.Status, RuntimeCLIRunning)
	}

	ids, err := rdb.ZRange(ctx, cliListPendingKey("runtime-list-atomic"), 0, -1).Result()
	if err != nil {
		t.Fatalf("zrange: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("claimed id still queued (%v) — the claim was not atomic", ids)
	}
}

func TestRedisCLIListStore_PopPendingConcurrent(t *testing.T) {
	rdb := newRedisTestClient(t)
	ctx := context.Background()
	store := NewRedisRuntimeCLIListStore(rdb)

	req, err := store.Create(ctx, "runtime-list-race")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	const n = 8
	var wg sync.WaitGroup
	results := make(chan *RuntimeCLIListRequest, n)
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			popped, err := store.PopPending(ctx, "runtime-list-race")
			if err != nil {
				errs <- err
				return
			}
			results <- popped
		}()
	}
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent pop error: %v", err)
	}

	winners := 0
	for popped := range results {
		if popped != nil {
			winners++
			if popped.ID != req.ID {
				t.Fatalf("winner popped wrong id: %s", popped.ID)
			}
			if popped.RunStartedAt == nil {
				t.Fatal("winner has no RunStartedAt")
			}
		}
	}
	if winners != 1 {
		t.Fatalf("expected exactly one winner, got %d", winners)
	}
}

func TestRedisCLIListStore_PendingTimeoutLandsInLoad(t *testing.T) {
	rdb := newRedisTestClient(t)
	ctx := context.Background()

	writer := NewRedisRuntimeCLIListStore(rdb)
	reader := NewRedisRuntimeCLIListStore(rdb)

	req, err := writer.Create(ctx, "runtime-list-pending-timeout")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Rewind CreatedAt past the pending threshold instead of sleeping 30s.
	req.CreatedAt = time.Now().Add(-cliListPendingTimeout - time.Second)
	if err := writer.persist(ctx, req); err != nil {
		t.Fatalf("persist rewound: %v", err)
	}

	got, err := reader.Get(ctx, req.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != RuntimeCLITimeout {
		t.Fatalf("status = %s, want %s", got.Status, RuntimeCLITimeout)
	}
	if got.Error == "" {
		t.Fatal("timed-out request has no error message")
	}

	// The timeout must have been persisted, not just returned to this caller.
	reread, err := NewRedisRuntimeCLIListStore(rdb).Get(ctx, req.ID)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if reread.Status != RuntimeCLITimeout {
		t.Fatalf("timeout was not persisted: status = %s", reread.Status)
	}

	// And a timed-out request must not be dispatched to a daemon.
	popped, err := reader.PopPending(ctx, "runtime-list-pending-timeout")
	if err != nil {
		t.Fatalf("pop after timeout: %v", err)
	}
	if popped != nil {
		t.Fatalf("expected no pending after timeout, got %+v", popped)
	}
}

func TestRedisCLIListStore_RunningTimeoutLandsInLoad(t *testing.T) {
	rdb := newRedisTestClient(t)
	ctx := context.Background()

	nodeA := NewRedisRuntimeCLIListStore(rdb)
	nodeB := NewRedisRuntimeCLIListStore(rdb)

	if _, err := nodeA.Create(ctx, "runtime-list-running-timeout"); err != nil {
		t.Fatalf("create: %v", err)
	}
	popped, err := nodeA.PopPending(ctx, "runtime-list-running-timeout")
	if err != nil {
		t.Fatalf("pop: %v", err)
	}
	if popped == nil {
		t.Fatal("nothing popped")
	}

	// Age the running start past the bound and hand it back to Redis. This is
	// what a daemon that claimed the request and then died leaves behind.
	aged := time.Now().Add(-(cliListRunningTimeout + time.Second))
	popped.RunStartedAt = &aged
	if err := nodeA.persist(ctx, popped); err != nil {
		t.Fatalf("persist aged: %v", err)
	}

	got, err := nodeB.Get(ctx, popped.ID)
	if err != nil {
		t.Fatalf("node B get: %v", err)
	}
	if got.Status != RuntimeCLITimeout {
		t.Fatalf("status = %s, want %s (running bound did not trip)", got.Status, RuntimeCLITimeout)
	}

	// load() drops the pending member when it times a record out, so the stale
	// id cannot be re-dispatched.
	ids, err := rdb.ZRange(ctx, cliListPendingKey("runtime-list-running-timeout"), 0, -1).Result()
	if err != nil {
		t.Fatalf("zrange: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("expected pending zset to be swept after timeout, got %v", ids)
	}
}

// TestRedisCLIListStore_SkipsExpiredRecordInPendingQueue covers the lazy sweep:
// the record key can expire on its own TTL while the pending zset (which lives
// twice as long) still references it. PopPending must drop the dead member and
// move on rather than returning a nil request or erroring.
func TestRedisCLIListStore_SkipsExpiredRecordInPendingQueue(t *testing.T) {
	rdb := newRedisTestClient(t)
	ctx := context.Background()
	store := NewRedisRuntimeCLIListStore(rdb)

	req, err := store.Create(ctx, "runtime-list-expired")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := rdb.Del(ctx, cliListKey(req.ID)).Err(); err != nil {
		t.Fatalf("delete record: %v", err)
	}

	popped, err := store.PopPending(ctx, "runtime-list-expired")
	if err != nil {
		t.Fatalf("pop with expired record: %v", err)
	}
	if popped != nil {
		t.Fatalf("expected nil for an expired record, got %+v", popped)
	}
	ids, err := rdb.ZRange(ctx, cliListPendingKey("runtime-list-expired"), 0, -1).Result()
	if err != nil {
		t.Fatalf("zrange: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("expired member was not swept: %v", ids)
	}
}

func TestRedisCLIListStore_PerRuntimeIsolation(t *testing.T) {
	rdb := newRedisTestClient(t)
	ctx := context.Background()
	store := NewRedisRuntimeCLIListStore(rdb)

	if _, err := store.Create(ctx, "runtime-list-A"); err != nil {
		t.Fatalf("create A: %v", err)
	}
	reqB, err := store.Create(ctx, "runtime-list-B")
	if err != nil {
		t.Fatalf("create B: %v", err)
	}

	popped, err := store.PopPending(ctx, "runtime-list-B")
	if err != nil {
		t.Fatalf("pop B: %v", err)
	}
	if popped == nil || popped.ID != reqB.ID {
		t.Fatalf("pop returned wrong request: %+v", popped)
	}

	ids, err := rdb.ZRange(ctx, cliListPendingKey("runtime-list-A"), 0, -1).Result()
	if err != nil {
		t.Fatalf("zrange A: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("expected 1 pending for A after pop(B), got %d: %v", len(ids), ids)
	}
}

func TestRedisCLIListStore_HasPendingReflectsQueue(t *testing.T) {
	rdb := newRedisTestClient(t)
	ctx := context.Background()
	store := NewRedisRuntimeCLIListStore(rdb)

	pending, err := store.HasPending(ctx, "runtime-list-haspending")
	if err != nil {
		t.Fatalf("has pending (empty): %v", err)
	}
	if pending {
		t.Fatal("empty queue reported pending")
	}

	if _, err := store.Create(ctx, "runtime-list-haspending"); err != nil {
		t.Fatalf("create: %v", err)
	}
	pending, err = store.HasPending(ctx, "runtime-list-haspending")
	if err != nil {
		t.Fatalf("has pending (queued): %v", err)
	}
	if !pending {
		t.Fatal("queued request not reported pending")
	}

	if _, err := store.PopPending(ctx, "runtime-list-haspending"); err != nil {
		t.Fatalf("pop: %v", err)
	}
	pending, err = store.HasPending(ctx, "runtime-list-haspending")
	if err != nil {
		t.Fatalf("has pending (claimed): %v", err)
	}
	if pending {
		t.Fatal("claimed request still reported pending")
	}
}

func TestRedisCLIListStore_CreateWithoutMultiPermission(t *testing.T) {
	rdb := newRedisTestClientWithoutMulti(t)
	ctx := context.Background()
	store := NewRedisRuntimeCLIListStore(rdb)

	req, err := store.Create(ctx, "runtime-list-no-multi")
	if err != nil {
		t.Fatalf("create without MULTI permission: %v", err)
	}
	got, err := store.Get(ctx, req.ID)
	if err != nil {
		t.Fatalf("get created request: %v", err)
	}
	if got == nil || got.ID != req.ID {
		t.Fatalf("created request was not persisted: %+v", got)
	}
	pending, err := store.HasPending(ctx, "runtime-list-no-multi")
	if err != nil {
		t.Fatalf("check pending request: %v", err)
	}
	if !pending {
		t.Fatal("created request was not queued")
	}
}

// ---------------------------------------------------------------------------
// Runs
// ---------------------------------------------------------------------------

func TestRedisCLIRunStore_CreateGetCompleteAcrossInstances(t *testing.T) {
	rdb := newRedisTestClient(t)
	ctx := context.Background()

	nodeA := NewRedisRuntimeCLIRunStore(rdb)
	nodeB := NewRedisRuntimeCLIRunStore(rdb)
	nodeC := NewRedisRuntimeCLIRunStore(rdb)
	nodeD := NewRedisRuntimeCLIRunStore(rdb)

	const limit = 90 * time.Second
	req, err := nodeA.Create(ctx, CLIRunRequestInput{
		RuntimeID:       "runtime-run",
		WorkspaceID:     "ws-1",
		CLIKey:          "zhihu",
		Params:          map[string]string{"command": "status"},
		InitiatorUserID: "user-42",
		RunningTimeout:  limit,
	})
	if err != nil {
		t.Fatalf("node A create: %v", err)
	}
	if req.Status != RuntimeCLIPending {
		t.Fatalf("initial status = %s, want %s", req.Status, RuntimeCLIPending)
	}

	// InitiatorID and RunningLimit are json:"-" on RuntimeCLIRunRequest — they
	// reach the other node only through cliRunEnvelope.
	got, err := nodeB.Get(ctx, req.ID)
	if err != nil {
		t.Fatalf("node B get: %v", err)
	}
	if got == nil || got.ID != req.ID {
		t.Fatalf("node B round trip mismatch: %+v", got)
	}
	if got.InitiatorID != "user-42" {
		t.Fatalf("InitiatorID = %q, want %q (audit row would lose its initiator)", got.InitiatorID, "user-42")
	}
	if got.RunningLimit != limit {
		t.Fatalf("RunningLimit = %s, want %s", got.RunningLimit, limit)
	}
	if got.CLIKey != "zhihu" || got.Params["command"] != "status" || got.WorkspaceID != "ws-1" {
		t.Fatalf("request fields lost: %+v", got)
	}

	exitCode := 0
	if err := nodeC.Complete(ctx, req.ID, CLIRunResult{
		Output:       `{"ok":true}`,
		Truncated:    true,
		ExitCode:     &exitCode,
		DurationMs:   1234,
		OutputBytes:  4001,
		ResolvedArgv: []string{"zhihu-cli.exe", "status"},
	}); err != nil {
		t.Fatalf("node C complete: %v", err)
	}

	final, err := nodeD.Get(ctx, req.ID)
	if err != nil {
		t.Fatalf("node D get: %v", err)
	}
	if final.Status != RuntimeCLICompleted {
		t.Fatalf("terminal status = %s, want %s", final.Status, RuntimeCLICompleted)
	}
	if final.Output != `{"ok":true}` || !final.Truncated || final.OutputBytes != 4001 {
		t.Fatalf("result payload lost: %+v", final)
	}
	if final.ExitCode == nil || *final.ExitCode != 0 {
		t.Fatalf("exit code lost: %v", final.ExitCode)
	}
	if final.DurationMs != 1234 {
		t.Fatalf("duration lost: %d", final.DurationMs)
	}
	if len(final.ResolvedArgv) != 2 || final.ResolvedArgv[0] != "zhihu-cli.exe" {
		t.Fatalf("resolved argv lost: %v", final.ResolvedArgv)
	}
	// persist() rewrites the envelope, so completing a run must not strip the
	// bookkeeping fields the report path still needs.
	if final.InitiatorID != "user-42" || final.RunningLimit != limit {
		t.Fatalf("bookkeeping fields lost on complete: initiator=%q limit=%s", final.InitiatorID, final.RunningLimit)
	}
}

func TestRedisCLIRunStore_FailAcrossInstances(t *testing.T) {
	rdb := newRedisTestClient(t)
	ctx := context.Background()

	nodeA := NewRedisRuntimeCLIRunStore(rdb)
	nodeB := NewRedisRuntimeCLIRunStore(rdb)
	nodeC := NewRedisRuntimeCLIRunStore(rdb)

	req, err := nodeA.Create(ctx, CLIRunRequestInput{
		RuntimeID:       "runtime-run-fail",
		CLIKey:          "zhihu",
		InitiatorUserID: "user-1",
		RunningTimeout:  time.Minute,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := nodeB.PopPending(ctx, "runtime-run-fail"); err != nil {
		t.Fatalf("pop: %v", err)
	}
	if err := nodeC.Fail(ctx, req.ID, "executable hash mismatch"); err != nil {
		t.Fatalf("fail: %v", err)
	}

	got, err := nodeA.Get(ctx, req.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != RuntimeCLIFailed || got.Error != "executable hash mismatch" {
		t.Fatalf("failed request mismatch: %+v", got)
	}
	if got.InitiatorID != "user-1" {
		t.Fatalf("InitiatorID lost on fail: %q", got.InitiatorID)
	}
}

// TestRedisCLIRunStore_PopPendingPreservesInitiatorAndLimitAcrossNodes covers
// the claim path specifically: the record handed to the daemon is rebuilt from
// Redis (load) and then persisted by the claim script, so both hops must carry
// the json:"-" fields. InitiatorID is what ReportCLIRunResult writes into
// activity_log.details.initiator; RunningLimit is what makes a per-entry timeout
// survive the claim.
func TestRedisCLIRunStore_PopPendingPreservesInitiatorAndLimitAcrossNodes(t *testing.T) {
	rdb := newRedisTestClient(t)
	ctx := context.Background()

	nodeA := NewRedisRuntimeCLIRunStore(rdb)
	nodeB := NewRedisRuntimeCLIRunStore(rdb)
	nodeC := NewRedisRuntimeCLIRunStore(rdb)

	const limit = 45 * time.Second
	req, err := nodeA.Create(ctx, CLIRunRequestInput{
		RuntimeID:       "runtime-run-pop",
		CLIKey:          "zhihu",
		Params:          map[string]string{"command": "search", "query": "golang"},
		InitiatorUserID: "user-7",
		RunningTimeout:  limit,
	})
	if err != nil {
		t.Fatalf("node A create: %v", err)
	}

	popped, err := nodeB.PopPending(ctx, "runtime-run-pop")
	if err != nil {
		t.Fatalf("node B pop: %v", err)
	}
	if popped == nil {
		t.Fatal("node B did not see node A's pending run")
	}
	if popped.ID != req.ID || popped.Status != RuntimeCLIRunning {
		t.Fatalf("popped mismatch: %+v", popped)
	}
	if popped.RunStartedAt == nil {
		t.Fatal("RunStartedAt not set after pop")
	}
	if popped.InitiatorID != "user-7" {
		t.Fatalf("InitiatorID = %q after pop, want %q", popped.InitiatorID, "user-7")
	}
	if popped.RunningLimit != limit {
		t.Fatalf("RunningLimit = %s after pop, want %s", popped.RunningLimit, limit)
	}
	if popped.Params["query"] != "golang" {
		t.Fatalf("params lost after pop: %v", popped.Params)
	}

	// The claimed record was written by the Lua script; a third node reading it
	// back must still see the same three fields.
	reloaded, err := nodeC.Get(ctx, req.ID)
	if err != nil {
		t.Fatalf("node C get: %v", err)
	}
	if reloaded.InitiatorID != "user-7" || reloaded.RunningLimit != limit {
		t.Fatalf("claimed record lost bookkeeping: initiator=%q limit=%s", reloaded.InitiatorID, reloaded.RunningLimit)
	}
	if reloaded.RunStartedAt == nil {
		t.Fatal("claimed record lost RunStartedAt — running timeout will never fire")
	}
	if !reloaded.RunStartedAt.Equal(*popped.RunStartedAt) {
		t.Fatalf("RunStartedAt = %s, want %s", reloaded.RunStartedAt, *popped.RunStartedAt)
	}

	again, err := nodeB.PopPending(ctx, "runtime-run-pop")
	if err != nil {
		t.Fatalf("node B second pop: %v", err)
	}
	if again != nil {
		t.Fatalf("expected no more pending, got %+v", again)
	}
}

// TestRedisCLIRunStore_PopPendingAtomicClaim is the run-store half of the
// atomicity invariant. Here the stake is worse than a duplicate discovery
// request: a re-dispatched run executes the CLI a second time, so a search or
// any other side-effecting command fires twice while the panel shows one run.
func TestRedisCLIRunStore_PopPendingAtomicClaim(t *testing.T) {
	rdb := newRedisTestClient(t)
	ctx := context.Background()
	store := NewRedisRuntimeCLIRunStore(rdb)

	req, err := store.Create(ctx, CLIRunRequestInput{
		RuntimeID:       "runtime-run-atomic",
		CLIKey:          "zhihu",
		InitiatorUserID: "user-1",
		RunningTimeout:  time.Minute,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	popped, err := store.PopPending(ctx, "runtime-run-atomic")
	if err != nil {
		t.Fatalf("pop: %v", err)
	}
	if popped == nil || popped.ID != req.ID {
		t.Fatalf("pop returned wrong request: %+v", popped)
	}

	got, err := store.Get(ctx, req.ID)
	if err != nil {
		t.Fatalf("get after pop: %v", err)
	}
	if got.Status != RuntimeCLIRunning {
		t.Fatalf("record status = %s, want %s", got.Status, RuntimeCLIRunning)
	}

	ids, err := rdb.ZRange(ctx, cliRunPendingKey("runtime-run-atomic"), 0, -1).Result()
	if err != nil {
		t.Fatalf("zrange: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("claimed id still queued (%v) — the claim was not atomic", ids)
	}
}

func TestRedisCLIRunStore_PopPendingConcurrent(t *testing.T) {
	rdb := newRedisTestClient(t)
	ctx := context.Background()
	store := NewRedisRuntimeCLIRunStore(rdb)

	req, err := store.Create(ctx, CLIRunRequestInput{
		RuntimeID:       "runtime-run-race",
		CLIKey:          "zhihu",
		InitiatorUserID: "user-1",
		RunningTimeout:  time.Minute,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	const n = 8
	var wg sync.WaitGroup
	results := make(chan *RuntimeCLIRunRequest, n)
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			popped, err := store.PopPending(ctx, "runtime-run-race")
			if err != nil {
				errs <- err
				return
			}
			results <- popped
		}()
	}
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent pop error: %v", err)
	}

	winners := 0
	for popped := range results {
		if popped != nil {
			winners++
			if popped.ID != req.ID {
				t.Fatalf("winner popped wrong id: %s", popped.ID)
			}
			if popped.InitiatorID != "user-1" {
				t.Fatalf("winner lost InitiatorID: %q", popped.InitiatorID)
			}
		}
	}
	if winners != 1 {
		t.Fatalf("expected exactly one winner, got %d", winners)
	}
}

func TestRedisCLIRunStore_PendingTimeoutLandsInLoad(t *testing.T) {
	rdb := newRedisTestClient(t)
	ctx := context.Background()

	writer := NewRedisRuntimeCLIRunStore(rdb)
	reader := NewRedisRuntimeCLIRunStore(rdb)

	req, err := writer.Create(ctx, CLIRunRequestInput{
		RuntimeID:       "runtime-run-pending-timeout",
		CLIKey:          "zhihu",
		InitiatorUserID: "user-1",
		RunningTimeout:  time.Minute,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	req.CreatedAt = time.Now().Add(-cliRunPendingTimeout - time.Second)
	if err := writer.persist(ctx, req); err != nil {
		t.Fatalf("persist rewound: %v", err)
	}

	got, err := reader.Get(ctx, req.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != RuntimeCLITimeout {
		t.Fatalf("status = %s, want %s", got.Status, RuntimeCLITimeout)
	}
	if got.Error == "" {
		t.Fatal("timed-out run has no error message")
	}

	popped, err := reader.PopPending(ctx, "runtime-run-pending-timeout")
	if err != nil {
		t.Fatalf("pop after timeout: %v", err)
	}
	if popped != nil {
		t.Fatalf("expected no pending after timeout, got %+v", popped)
	}
}

// TestRedisCLIRunStore_RunningTimeoutUsesDeclaredLimitFromRedis is the test
// that would fail if RunningLimit stopped crossing the Redis hop. The control
// half is deliberate: a run with no declared limit and the same elapsed time
// must still be running, because the fallback bound (default + grace) is far
// longer than the declared one used here. That difference is only observable if
// the limit itself made it back out of Redis — if it defaulted to 0 on read,
// both halves would behave identically.
func TestRedisCLIRunStore_RunningTimeoutUsesDeclaredLimitFromRedis(t *testing.T) {
	rdb := newRedisTestClient(t)
	ctx := context.Background()

	const declared = 5 * time.Second
	if declared >= time.Duration(cliRunDefaultTimeoutS)*time.Second {
		t.Fatalf("test premise broken: declared limit %s must be well under the default bound", declared)
	}

	for _, tc := range []struct {
		name  string
		limit time.Duration
		want  RuntimeCLIRequestStatus
	}{
		{name: "declared limit trips", limit: declared, want: RuntimeCLITimeout},
		{name: "no declared limit falls back", limit: 0, want: RuntimeCLIRunning},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtimeID := "runtime-run-limit-" + tc.name
			writer := NewRedisRuntimeCLIRunStore(rdb)
			reader := NewRedisRuntimeCLIRunStore(rdb)

			if _, err := writer.Create(ctx, CLIRunRequestInput{
				RuntimeID:       runtimeID,
				CLIKey:          "zhihu",
				InitiatorUserID: "user-1",
				RunningTimeout:  tc.limit,
			}); err != nil {
				t.Fatalf("create: %v", err)
			}
			popped, err := writer.PopPending(ctx, runtimeID)
			if err != nil {
				t.Fatalf("pop: %v", err)
			}
			if popped == nil {
				t.Fatal("nothing popped")
			}
			// Same elapsed time for both halves — only the limit differs.
			aged := time.Now().Add(-(declared + time.Second))
			popped.RunStartedAt = &aged
			if err := writer.persist(ctx, popped); err != nil {
				t.Fatalf("persist aged: %v", err)
			}

			got, err := reader.Get(ctx, popped.ID)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if got.Status != tc.want {
				t.Fatalf("status = %s, want %s", got.Status, tc.want)
			}
			if got.RunningLimit != tc.limit {
				t.Fatalf("RunningLimit = %s, want %s (declared limit lost on the Redis hop)", got.RunningLimit, tc.limit)
			}
		})
	}
}

func TestRedisCLIRunStore_CreateWithoutMultiPermission(t *testing.T) {
	rdb := newRedisTestClientWithoutMulti(t)
	ctx := context.Background()
	store := NewRedisRuntimeCLIRunStore(rdb)

	req, err := store.Create(ctx, CLIRunRequestInput{
		RuntimeID:       "runtime-run-no-multi",
		CLIKey:          "zhihu",
		InitiatorUserID: "user-1",
		RunningTimeout:  time.Minute,
	})
	if err != nil {
		t.Fatalf("create without MULTI permission: %v", err)
	}
	got, err := store.Get(ctx, req.ID)
	if err != nil {
		t.Fatalf("get created request: %v", err)
	}
	if got == nil || got.ID != req.ID || got.InitiatorID != "user-1" {
		t.Fatalf("created request was not persisted correctly: %+v", got)
	}
	pending, err := store.HasPending(ctx, "runtime-run-no-multi")
	if err != nil {
		t.Fatalf("check pending request: %v", err)
	}
	if !pending {
		t.Fatal("created request was not queued")
	}
}

// Compile-time assertions: the Redis stores MUST satisfy the interfaces so
// NewRouter's assignment stays type-safe.
var (
	_ RuntimeCLIListStore = (*RedisRuntimeCLIListStore)(nil)
	_ RuntimeCLIRunStore  = (*RedisRuntimeCLIRunStore)(nil)
	_ RuntimeCLIListStore = (*InMemoryRuntimeCLIListStore)(nil)
	_ RuntimeCLIRunStore  = (*InMemoryRuntimeCLIRunStore)(nil)
)
