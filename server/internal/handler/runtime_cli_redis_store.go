package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Redis-backed implementations of RuntimeCLIListStore and RuntimeCLIRunStore.
//
// The wire layout matches runtime_models_redis_store.go and
// runtime_local_skills_redis_store.go, which solve the same multi-node
// dispatch problem: namespaced keys, a ZSET-backed pending queue, and an
// atomic claim through the shared claimPendingScript. InMemory stores are
// wrong here for the same reason they are wrong there — under a multi-node
// deploy the Web POST, the daemon heartbeat that claims the request, the
// daemon report, and the UI poll can land on four different nodes.
//
// Key layout:
//
//	mul:{runtime_pending}:cli_list:req:<id>          → JSON RuntimeCLIListRequest
//	mul:{runtime_pending}:cli_list:pending:<rt>      → ZSET { member = id, score = created_at UnixNano }
//	mul:{runtime_pending}:cli_run:req:<id>           → JSON c.runEnvelope
//	mul:{runtime_pending}:cli_run:pending:<rt>       → ZSET { member = id, score = created_at UnixNano }

const (
	cliListKeyPrefix      = "mul:" + runtimePendingRedisHashTag + ":cli_list:req:"
	cliListPendingPrefix  = "mul:" + runtimePendingRedisHashTag + ":cli_list:pending:"
	cliRunKeyPrefix       = "mul:" + runtimePendingRedisHashTag + ":cli_run:req:"
	cliRunPendingPrefix   = "mul:" + runtimePendingRedisHashTag + ":cli_run:pending:"
	cliRedisPopMaxRetries = 5
)

func cliListKey(id string) string               { return cliListKeyPrefix + id }
func cliListPendingKey(runtimeID string) string { return cliListPendingPrefix + runtimeID }
func cliRunKey(id string) string                { return cliRunKeyPrefix + id }
func cliRunPendingKey(runtimeID string) string  { return cliRunPendingPrefix + runtimeID }

// ---------------------------------------------------------------------------
// Registry listing
// ---------------------------------------------------------------------------

type RedisRuntimeCLIListStore struct {
	rdb redis.UniversalClient
}

func NewRedisRuntimeCLIListStore(rdb redis.UniversalClient) *RedisRuntimeCLIListStore {
	return &RedisRuntimeCLIListStore{rdb: rdb}
}

func (s *RedisRuntimeCLIListStore) Create(ctx context.Context, runtimeID string) (*RuntimeCLIListRequest, error) {
	now := time.Now()
	req := &RuntimeCLIListRequest{
		ID:        randomID(),
		RuntimeID: runtimeID,
		Status:    RuntimeCLIPending,
		CreatedAt: now,
		UpdatedAt: now,
	}
	data, err := json.Marshal(cliListEnvelope{Public: req, RunStartedAt: req.RunStartedAt})
	if err != nil {
		return nil, fmt.Errorf("marshal cli list request: %w", err)
	}

	requestKey := cliListKey(req.ID)
	pendingKey := cliListPendingKey(runtimeID)
	pipe := s.rdb.Pipeline()
	pipe.Set(ctx, requestKey, data, cliStoreRetention)
	pipe.ZAdd(ctx, pendingKey, redis.Z{Score: float64(now.UnixNano()), Member: req.ID})
	// Keep the pending zset alive past the per-record retention so stale
	// members can be lazily swept on PopPending.
	pipe.Expire(ctx, pendingKey, cliStoreRetention*2)
	if _, err := pipe.Exec(ctx); err != nil {
		_ = s.rdb.Del(ctx, requestKey).Err()
		_ = s.rdb.ZRem(ctx, pendingKey, req.ID).Err()
		return nil, fmt.Errorf("persist cli list request: %w", err)
	}
	return req, nil
}

func (s *RedisRuntimeCLIListStore) Get(ctx context.Context, id string) (*RuntimeCLIListRequest, error) {
	return s.load(ctx, id)
}

func (s *RedisRuntimeCLIListStore) load(ctx context.Context, id string) (*RuntimeCLIListRequest, error) {
	raw, err := s.rdb.Get(ctx, cliListKey(id)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get cli list request: %w", err)
	}
	var env cliListEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("decode cli list request: %w", err)
	}
	if env.Public == nil {
		return nil, fmt.Errorf("decode cli list request: missing payload")
	}
	env.Public.RunStartedAt = env.RunStartedAt
	if applyCLIListTimeout(env.Public, time.Now()) {
		if err := s.persist(ctx, env.Public); err != nil {
			return nil, err
		}
		s.rdb.ZRem(ctx, cliListPendingKey(env.Public.RuntimeID), env.Public.ID)
	}
	return env.Public, nil
}

func (s *RedisRuntimeCLIListStore) persist(ctx context.Context, req *RuntimeCLIListRequest) error {
	data, err := json.Marshal(cliListEnvelope{Public: req, RunStartedAt: req.RunStartedAt})
	if err != nil {
		return fmt.Errorf("marshal cli list request: %w", err)
	}
	if err := s.rdb.Set(ctx, cliListKey(req.ID), data, cliStoreRetention).Err(); err != nil {
		return fmt.Errorf("persist cli list request: %w", err)
	}
	return nil
}

// HasPending is a cheap read-only ZCARD probe gating the side-effecting Pop.
func (s *RedisRuntimeCLIListStore) HasPending(ctx context.Context, runtimeID string) (bool, error) {
	cnt, err := s.rdb.ZCard(ctx, cliListPendingKey(runtimeID)).Result()
	if err != nil {
		return false, fmt.Errorf("zcard cli list pending: %w", err)
	}
	return cnt > 0, nil
}

func (s *RedisRuntimeCLIListStore) PopPending(ctx context.Context, runtimeID string) (*RuntimeCLIListRequest, error) {
	pendingKey := cliListPendingKey(runtimeID)
	for attempt := 0; attempt < cliRedisPopMaxRetries; attempt++ {
		ids, err := s.rdb.ZRange(ctx, pendingKey, 0, 0).Result()
		if err != nil {
			return nil, fmt.Errorf("zrange cli list pending: %w", err)
		}
		if len(ids) == 0 {
			return nil, nil
		}
		id := ids[0]

		req, err := s.load(ctx, id)
		if err != nil {
			return nil, err
		}
		if req == nil {
			// Record expired but the zset still references it — drop and retry.
			s.rdb.ZRem(ctx, pendingKey, id)
			continue
		}
		if req.Status != RuntimeCLIPending {
			s.rdb.ZRem(ctx, pendingKey, id)
			continue
		}

		now := time.Now()
		req.Status = RuntimeCLIRunning
		req.RunStartedAt = &now
		req.UpdatedAt = now
		// RunStartedAt is json:"-" so it is never persisted by persist();
		// the claim below writes an envelope that re-promotes it.
		data, err := json.Marshal(cliListEnvelope{Public: req, RunStartedAt: req.RunStartedAt})
		if err != nil {
			return nil, fmt.Errorf("marshal cli list request: %w", err)
		}

		result, err := claimPendingScript.Run(ctx, s.rdb,
			[]string{pendingKey, cliListKey(id)},
			id, data, int(cliStoreRetention.Seconds()),
		).Int64()
		if err != nil {
			return nil, fmt.Errorf("claim cli list pending: %w", err)
		}
		if result == 0 {
			continue
		}
		return req, nil
	}
	return nil, nil
}

// cliListEnvelope re-promotes the json:"-" bookkeeping field across the Redis
// hop. Without it every reader would see RunStartedAt=nil and the running
// timeout would silently stop firing on multi-node deploys.
type cliListEnvelope struct {
	Public       *RuntimeCLIListRequest `json:"r"`
	RunStartedAt *time.Time             `json:"s,omitempty"`
}

func (s *RedisRuntimeCLIListStore) Complete(ctx context.Context, id string, clis []RuntimeCLISummary, registryPath string) error {
	req, err := s.load(ctx, id)
	if err != nil {
		return err
	}
	if req == nil {
		return nil
	}
	req.Status = RuntimeCLICompleted
	req.CLIs = clis
	req.RegistryPath = registryPath
	req.UpdatedAt = time.Now()
	return s.persist(ctx, req)
}

func (s *RedisRuntimeCLIListStore) Fail(ctx context.Context, id string, errMsg string) error {
	req, err := s.load(ctx, id)
	if err != nil {
		return err
	}
	if req == nil {
		return nil
	}
	req.Status = RuntimeCLIFailed
	req.Error = errMsg
	req.UpdatedAt = time.Now()
	return s.persist(ctx, req)
}

// ---------------------------------------------------------------------------
// Runs
// ---------------------------------------------------------------------------

type RedisRuntimeCLIRunStore struct {
	rdb redis.UniversalClient
}

func NewRedisRuntimeCLIRunStore(rdb redis.UniversalClient) *RedisRuntimeCLIRunStore {
	return &RedisRuntimeCLIRunStore{rdb: rdb}
}

func (s *RedisRuntimeCLIRunStore) Create(ctx context.Context, input CLIRunRequestInput) (*RuntimeCLIRunRequest, error) {
	// No cross-node in-progress check here: the pending zset drops a member as
	// soon as it is claimed, so a ZCARD probe cannot see a running request and
	// would only ever catch the unclaimed case the caller can already see. The
	// daemon's single-flight guard is the authoritative one under multi-node.
	now := time.Now()
	req := &RuntimeCLIRunRequest{
		ID:           randomID(),
		RuntimeID:    input.RuntimeID,
		WorkspaceID:  input.WorkspaceID,
		CLIKey:       input.CLIKey,
		Params:       input.Params,
		Status:       RuntimeCLIPending,
		CreatedAt:    now,
		UpdatedAt:    now,
		InitiatorID:  input.InitiatorUserID,
		RunningLimit: input.RunningTimeout,
	}
	data, err := json.Marshal(cliRunEnvelope{Public: req, RunStartedAt: nil, Initiator: req.InitiatorID, RunningLimit: req.RunningLimit})
	if err != nil {
		return nil, fmt.Errorf("marshal cli run request: %w", err)
	}

	requestKey := cliRunKey(req.ID)
	pendingKey := cliRunPendingKey(input.RuntimeID)
	pipe := s.rdb.Pipeline()
	pipe.Set(ctx, requestKey, data, cliStoreRetention)
	pipe.ZAdd(ctx, pendingKey, redis.Z{Score: float64(now.UnixNano()), Member: req.ID})
	pipe.Expire(ctx, pendingKey, cliStoreRetention*2)
	if _, err := pipe.Exec(ctx); err != nil {
		_ = s.rdb.Del(ctx, requestKey).Err()
		_ = s.rdb.ZRem(ctx, pendingKey, req.ID).Err()
		return nil, fmt.Errorf("persist cli run request: %w", err)
	}
	return req, nil
}

func (s *RedisRuntimeCLIRunStore) Get(ctx context.Context, id string) (*RuntimeCLIRunRequest, error) {
	return s.load(ctx, id)
}

type cliRunEnvelope struct {
	Public       *RuntimeCLIRunRequest `json:"r"`
	RunStartedAt *time.Time            `json:"s,omitempty"`
	// Initiator and RunningLimit are json:"-" on the public struct (they are
	// server bookkeeping, not panel data) but must survive the Redis hop —
	// InitiatorID feeds the audit row written at report time, and
	// RunningLimit is the only thing that makes per-entry timeouts work across
	// nodes.
	Initiator    string        `json:"i,omitempty"`
	RunningLimit time.Duration `json:"t,omitempty"`
}

func (s *RedisRuntimeCLIRunStore) load(ctx context.Context, id string) (*RuntimeCLIRunRequest, error) {
	raw, err := s.rdb.Get(ctx, cliRunKey(id)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get cli run request: %w", err)
	}
	var env cliRunEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("decode cli run request: %w", err)
	}
	if env.Public == nil {
		return nil, fmt.Errorf("decode cli run request: missing payload")
	}
	env.Public.RunStartedAt = env.RunStartedAt
	env.Public.InitiatorID = env.Initiator
	env.Public.RunningLimit = env.RunningLimit
	if applyCLIRunTimeout(env.Public, time.Now()) {
		if err := s.persist(ctx, env.Public); err != nil {
			return nil, err
		}
		s.rdb.ZRem(ctx, cliRunPendingKey(env.Public.RuntimeID), env.Public.ID)
	}
	return env.Public, nil
}

func (s *RedisRuntimeCLIRunStore) persist(ctx context.Context, req *RuntimeCLIRunRequest) error {
	data, err := json.Marshal(cliRunEnvelope{
		Public:       req,
		RunStartedAt: req.RunStartedAt,
		Initiator:    req.InitiatorID,
		RunningLimit: req.RunningLimit,
	})
	if err != nil {
		return fmt.Errorf("marshal cli run request: %w", err)
	}
	if err := s.rdb.Set(ctx, cliRunKey(req.ID), data, cliStoreRetention).Err(); err != nil {
		return fmt.Errorf("persist cli run request: %w", err)
	}
	return nil
}

func (s *RedisRuntimeCLIRunStore) HasPending(ctx context.Context, runtimeID string) (bool, error) {
	cnt, err := s.rdb.ZCard(ctx, cliRunPendingKey(runtimeID)).Result()
	if err != nil {
		return false, fmt.Errorf("zcard cli run pending: %w", err)
	}
	return cnt > 0, nil
}

func (s *RedisRuntimeCLIRunStore) PopPending(ctx context.Context, runtimeID string) (*RuntimeCLIRunRequest, error) {
	pendingKey := cliRunPendingKey(runtimeID)
	for attempt := 0; attempt < cliRedisPopMaxRetries; attempt++ {
		ids, err := s.rdb.ZRange(ctx, pendingKey, 0, 0).Result()
		if err != nil {
			return nil, fmt.Errorf("zrange cli run pending: %w", err)
		}
		if len(ids) == 0 {
			return nil, nil
		}
		id := ids[0]

		req, err := s.load(ctx, id)
		if err != nil {
			return nil, err
		}
		if req == nil {
			s.rdb.ZRem(ctx, pendingKey, id)
			continue
		}
		if req.Status != RuntimeCLIPending {
			s.rdb.ZRem(ctx, pendingKey, id)
			continue
		}

		now := time.Now()
		req.Status = RuntimeCLIRunning
		req.RunStartedAt = &now
		req.UpdatedAt = now
		data, err := json.Marshal(cliRunEnvelope{
			Public:       req,
			RunStartedAt: req.RunStartedAt,
			Initiator:    req.InitiatorID,
			RunningLimit: req.RunningLimit,
		})
		if err != nil {
			return nil, fmt.Errorf("marshal cli run request: %w", err)
		}

		result, err := claimPendingScript.Run(ctx, s.rdb,
			[]string{pendingKey, cliRunKey(id)},
			id, data, int(cliStoreRetention.Seconds()),
		).Int64()
		if err != nil {
			return nil, fmt.Errorf("claim cli run pending: %w", err)
		}
		if result == 0 {
			continue
		}
		return req, nil
	}
	return nil, nil
}

func (s *RedisRuntimeCLIRunStore) Complete(ctx context.Context, id string, result CLIRunResult) error {
	req, err := s.load(ctx, id)
	if err != nil {
		return err
	}
	if req == nil {
		return nil
	}
	req.Status = RuntimeCLICompleted
	req.Output = result.Output
	req.Truncated = result.Truncated
	req.ExitCode = result.ExitCode
	req.DurationMs = result.DurationMs
	req.OutputBytes = result.OutputBytes
	req.ResolvedArgv = result.ResolvedArgv
	req.UpdatedAt = time.Now()
	return s.persist(ctx, req)
}

func (s *RedisRuntimeCLIRunStore) Fail(ctx context.Context, id string, errMsg string) error {
	req, err := s.load(ctx, id)
	if err != nil {
		return err
	}
	if req == nil {
		return nil
	}
	req.Status = RuntimeCLIFailed
	req.Error = errMsg
	req.UpdatedAt = time.Now()
	return s.persist(ctx, req)
}
