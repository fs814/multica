package worksync

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/google/uuid"
)

const maxCheckpoint = 32 << 20

type ReplicaConfig struct {
	Enabled   bool
	Root      string
	Scope     Scope
	Principal Principal
}

type Review struct {
	Operation Operation `json:"operation"`
	Receipt   Receipt   `json:"receipt"`
}
type ReplicaState struct {
	Schema      int               `json:"schema"`
	Scope       Scope             `json:"scope"`
	Principal   Principal         `json:"principal"`
	Incarnation string            `json:"incarnation"`
	Sequence    int64             `json:"sequence"`
	Initialized bool              `json:"initialized"`
	Cursor      int64             `json:"cursor"`
	Records     map[string]Record `json:"records"`
	// Applied receipts can arrive ahead of the pull cursor. Keep their bases
	// separately so a new local edit never falls back to an older confirmation.
	Acknowledged map[string]Record `json:"acknowledged"`
	Outbox       []Operation       `json:"outbox"`
	Review       []Review          `json:"review"`
}

// Replica is a bounded, single-writer transactional checkpoint store. All
// confirmed records, cursor, local sequence, outbox and review records commit
// in one fsynced atomic replacement. No external database driver is needed for
// the initial small-replica foundation. A 32 MiB cap fails closed; a paginated
// embedded database is needed before larger workspaces can be enabled.
type Replica struct {
	mu     sync.Mutex
	config ReplicaConfig
	dir    string
	lock   *os.File
	write  func([]byte) error
}

func OpenReplica(config ReplicaConfig) (*Replica, error) {
	if !config.Enabled {
		return nil, ErrDisabled
	}
	if err := config.Scope.Validate(); err != nil {
		return nil, err
	}
	if config.Root == "" || config.Principal.Account == "" || config.Principal.Actor == "" || config.Principal.Node == "" {
		return nil, ErrScope
	}
	// Account and actor are part of the namespace, not credentials or URLs.
	dir := filepath.Join(config.Root, "work-replicas", "v1", digest(struct {
		Scope     Scope
		Principal Principal
	}{config.Scope, config.Principal}))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lock, err := lockReplica(filepath.Join(dir, "lock"))
	if err != nil {
		return nil, err
	}
	r := &Replica{config: config, dir: dir, lock: lock}
	r.write = r.writeCheckpoint
	_, err = r.load()
	if errors.Is(err, os.ErrNotExist) {
		s := ReplicaState{Schema: Schema, Scope: config.Scope, Principal: config.Principal, Incarnation: uuid.NewString(), Records: map[string]Record{}, Acknowledged: map[string]Record{}}
		err = r.save(s)
	}
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	return r, nil
}

func (r *Replica) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lock == nil {
		return nil
	}
	err := r.lock.Close()
	r.lock = nil
	return err
}

func (r *Replica) load() (ReplicaState, error) {
	if r.lock == nil {
		return ReplicaState{}, os.ErrClosed
	}
	f, err := os.Open(filepath.Join(r.dir, "checkpoint.json"))
	if err != nil {
		return ReplicaState{}, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxCheckpoint+1))
	if err != nil {
		return ReplicaState{}, err
	}
	if len(b) > maxCheckpoint {
		return ReplicaState{}, fmt.Errorf("replica exceeds checkpoint limit")
	}
	var envelope struct {
		State  ReplicaState `json:"state"`
		Digest string       `json:"digest"`
	}
	if err = json.Unmarshal(b, &envelope); err != nil {
		return ReplicaState{}, err
	}
	s := envelope.State
	if envelope.Digest != digest(s) || s.Schema != Schema || s.Scope != r.config.Scope || s.Principal != r.config.Principal {
		return ReplicaState{}, ErrScope
	}
	if s.Cursor < 0 || s.Sequence < 0 || s.Records == nil || s.Acknowledged == nil {
		return ReplicaState{}, ErrCursor
	}
	if _, err = uuid.Parse(s.Incarnation); err != nil {
		return ReplicaState{}, err
	}
	for key, record := range s.Records {
		if record.Validate() != nil || record.Key() != key || record.Version > s.Cursor {
			return ReplicaState{}, ErrCursor
		}
	}
	for key, record := range s.Acknowledged {
		if record.Validate() != nil || record.Key() != key {
			return ReplicaState{}, ErrOperation
		}
	}
	var seq int64
	for _, op := range s.Outbox {
		if op.Validate(s.Principal, s.Scope) != nil || op.Incarnation != s.Incarnation || op.Sequence <= seq || op.Sequence > s.Sequence {
			return ReplicaState{}, ErrOperation
		}
		seq = op.Sequence
	}
	return s, nil
}

func (r *Replica) save(s ReplicaState) error {
	b, err := json.Marshal(struct {
		State  ReplicaState `json:"state"`
		Digest string       `json:"digest"`
	}{s, digest(s)})
	if err != nil {
		return err
	}
	if len(b) > maxCheckpoint {
		return fmt.Errorf("replica exceeds checkpoint limit")
	}
	return r.write(b)
}

func (r *Replica) writeCheckpoint(b []byte) error {
	f, err := os.CreateTemp(r.dir, ".checkpoint-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	defer f.Close()
	if _, err = f.Write(b); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return replaceCheckpoint(name, filepath.Join(r.dir, "checkpoint.json"))
}

func (r *Replica) State() (ReplicaState, error) { r.mu.Lock(); defer r.mu.Unlock(); return r.load() }

// Apply advances the cursor only in the same durable commit as the records.
// A repeated batch is harmless; gaps, rollback snapshots and history changes
// are rejected. A snapshot replaces only confirmed state, never local intent.
func (r *Replica) Apply(batch Batch) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := batch.Validate(r.config.Scope); err != nil {
		return err
	}
	s, err := r.load()
	if err != nil {
		return err
	}
	if !batch.Snapshot && s.Initialized && batch.Cursor <= s.Cursor {
		return nil
	}
	if batch.Snapshot {
		if batch.Cursor < s.Cursor {
			return ErrCursor
		}
		incoming := map[string]Record{}
		for _, record := range batch.Records {
			incoming[record.Key()] = record
		}
		for key, old := range s.Records {
			next, ok := incoming[key]
			if !ok || next.Version < old.Version || (next.Version == old.Version && digest(next) != digest(old)) {
				return ErrCursor
			}
		}
		s.Records = map[string]Record{}
	} else if !s.Initialized || batch.From != s.Cursor {
		return ErrCursor
	}
	for _, record := range batch.Records {
		s.Records[record.Key()] = record
		if ack, ok := s.Acknowledged[record.Key()]; ok && record.Version >= ack.Version {
			delete(s.Acknowledged, record.Key())
		}
	}
	s.Cursor = batch.Cursor
	s.Initialized = true
	return r.save(s)
}

func (r *Replica) Queue(kind, entity string, patch Fields) (Operation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, err := r.load()
	if err != nil {
		return Operation{}, err
	}
	base, ok := s.Records[kind+"/"+entity]
	if ack, exists := s.Acknowledged[kind+"/"+entity]; exists && ack.Version > base.Version {
		base = ack
		ok = true
	}
	if !s.Initialized || !ok || base.Deleted {
		return Operation{}, ErrOperation
	}
	op := Operation{ID: uuid.NewString(), Node: s.Principal.Node, Incarnation: s.Incarnation, Sequence: s.Sequence + 1, Actor: s.Principal.Actor, Scope: s.Scope, Kind: kind, Entity: entity, Base: base.Version, Patch: patch}
	for _, pending := range s.Outbox {
		if pending.Kind == kind && pending.Entity == entity {
			op.DependsOn = pending.ID
		}
	}
	if err = op.Validate(s.Principal, s.Scope); err != nil {
		return Operation{}, err
	}
	s.Sequence = op.Sequence
	s.Outbox = append(s.Outbox, op)
	if err = r.save(s); err != nil {
		return Operation{}, err
	}
	return op, nil
}

// Acknowledge removes only the oldest pending operation, after preserving any
// rejected/conflicting edit together with its base/local/center evidence.
func (r *Replica) Acknowledge(receipt Receipt) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, err := r.load()
	if err != nil {
		return err
	}
	if len(s.Outbox) == 0 || s.Outbox[0].ID != receipt.Operation {
		return ErrOperation
	}
	if !contains([]string{"applied", "conflict", "rejected"}, receipt.Status) {
		return ErrOperation
	}
	op := s.Outbox[0]
	if receipt.Status == "applied" && (receipt.Record.Validate() != nil || receipt.Record.Key() != op.Kind+"/"+op.Entity || receipt.Record.Version < op.Base || receipt.Record.Deleted) {
		return ErrOperation
	}
	if receipt.Status != "applied" {
		s.Review = append(s.Review, Review{op, receipt})
	} else if receipt.Record.Version > s.Cursor {
		s.Acknowledged[receipt.Record.Key()] = receipt.Record
	}
	s.Outbox = s.Outbox[1:]
	return r.save(s)
}
