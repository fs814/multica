// Package worksync implements the opt-in Work replication foundation. It has
// no task dispatch or notifications. Recovery activation requires a separate
// explicitly authorized operator service with a verified external fence.
package worksync

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

const Schema = 1
const MaxBatch = 256
const MaxSnapshot = 10000

var (
	ErrDisabled        = errors.New("work replication disabled")
	ErrScope           = errors.New("replication identity or history mismatch")
	ErrDenied          = errors.New("replication access denied")
	ErrUnauthenticated = errors.New("replication credential invalid or expired")
	ErrLimit           = errors.New("replication capacity limit exceeded")
	ErrCursor          = errors.New("invalid replication cursor")
	ErrOperation       = errors.New("invalid replication operation")
)

// Scope is an immutable history identity; a newer epoch is never auto-trusted.
type Scope struct {
	Workspace string `json:"workspace"`
	Group     string `json:"group"`
	Epoch     string `json:"epoch"`
}

func (s Scope) Validate() error {
	for _, id := range []string{s.Workspace, s.Group, s.Epoch} {
		if u, err := uuid.Parse(id); err != nil || u == uuid.Nil || u.String() != id {
			return ErrScope
		}
	}
	return nil
}

// Fields are deliberately a small, non-secret projection. Null is distinct
// from an omitted patch key. Execution/ACL/relationship fields are read-only.
type Fields map[string]json.RawMessage

var exportFields = map[string][]string{
	"issue":   {"title", "description", "priority", "status", "number", "project_id", "parent_issue_id", "assignee_type", "assignee_id"},
	"project": {"title", "description", "priority", "status", "icon"},
	"agent":   {"name", "description", "avatar_url", "archived_at"},
}
var writableFields = map[string][]string{
	"issue":   {"title", "description", "priority"},
	"project": {"title", "description", "priority", "icon"},
	"agent":   {"description", "avatar_url"},
}

func contains(list []string, key string) bool {
	for _, k := range list {
		if key == k {
			return true
		}
	}
	return false
}

func validateFields(kind string, fields Fields, patch bool) error {
	allowed, ok := exportFields[kind]
	if patch {
		allowed = writableFields[kind]
	}
	if !ok || fields == nil {
		return ErrOperation
	}
	for k, v := range fields {
		if !contains(allowed, k) || !json.Valid(v) || len(v) > 1<<20 {
			return ErrOperation
		}
		if patch {
			var value *string
			if err := json.Unmarshal(v, &value); err != nil {
				return ErrOperation
			}
			if (k == "title" || k == "priority") && (value == nil || *value == "") {
				return ErrOperation
			}
			if value != nil && strings.ContainsRune(*value, '\x00') {
				return ErrOperation
			}
			if k == "title" && strings.TrimSpace(*value) == "" {
				return ErrOperation
			}
			if kind == "agent" && k == "description" && (value == nil || utf8.RuneCountInString(*value) > 255) {
				return ErrOperation
			}
			if k == "priority" && !contains([]string{"none", "low", "medium", "high", "urgent"}, *value) {
				return ErrOperation
			}
		}
	}
	return nil
}

type Record struct {
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Version int64  `json:"version"`
	Deleted bool   `json:"deleted"`
	Fields  Fields `json:"fields"`
}

func (r Record) Key() string { return r.Kind + "/" + r.ID }
func (r Record) Validate() error {
	if _, err := uuid.Parse(r.ID); err != nil || r.Version <= 0 {
		return ErrOperation
	}
	if r.Deleted && len(r.Fields) != 0 {
		return ErrOperation
	}
	return validateFields(r.Kind, r.Fields, false)
}

type Batch struct {
	Schema   int      `json:"schema"`
	Scope    Scope    `json:"scope"`
	From     int64    `json:"from"`
	Cursor   int64    `json:"cursor"`
	Snapshot bool     `json:"snapshot"`
	Records  []Record `json:"records"`
	Digest   string   `json:"digest"`
}

func digest(value any) string {
	b, _ := json.Marshal(value)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
func (b Batch) checksum() string { b.Digest = ""; return digest(b) }
func (b *Batch) Seal()           { b.Digest = b.checksum() }
func (b Batch) Validate(scope Scope) error {
	if b.Schema != Schema || b.Scope != scope || scope.Validate() != nil {
		return ErrScope
	}
	if b.From < 0 || b.Cursor < b.From || b.Digest != b.checksum() {
		return ErrCursor
	}
	limit := MaxBatch
	if b.Snapshot {
		limit = MaxSnapshot
		if b.From != 0 {
			return ErrCursor
		}
	}
	if len(b.Records) > limit {
		return ErrCursor
	}
	seen := map[string]bool{}
	previous := b.From
	for _, r := range b.Records {
		if err := r.Validate(); err != nil {
			return err
		}
		if r.Version > b.Cursor {
			return ErrCursor
		}
		if b.Snapshot {
			if seen[r.Key()] {
				return ErrCursor
			}
			seen[r.Key()] = true
		} else {
			if r.Version != previous+1 {
				return ErrCursor
			}
			previous = r.Version
		}
	}
	if !b.Snapshot && previous != b.Cursor {
		return ErrCursor
	}
	return nil
}

// Principal must come from authenticated transport, never from an operation.
// Authorization is rechecked for each request, including duplicate pushes.
type Principal struct{ Account, Actor, Node string }
type Operation struct {
	ID          string `json:"id"`
	Node        string `json:"node"`
	Incarnation string `json:"incarnation"`
	Sequence    int64  `json:"sequence"`
	Actor       string `json:"actor"`
	Scope       Scope  `json:"scope"`
	Kind        string `json:"kind"`
	Entity      string `json:"entity"`
	Base        int64  `json:"base"`
	DependsOn   string `json:"depends_on,omitempty"`
	Patch       Fields `json:"patch"`
}

func (o Operation) Validate(p Principal, scope Scope) error {
	if o.Scope != scope || scope.Validate() != nil {
		return ErrScope
	}
	if o.Node != p.Node || o.Actor != p.Actor || p.Account == "" || p.Node == "" || p.Actor == "" {
		return ErrDenied
	}
	for _, id := range []string{o.ID, o.Entity, o.Incarnation} {
		if _, err := uuid.Parse(id); err != nil {
			return ErrOperation
		}
	}
	if o.DependsOn != "" {
		if _, err := uuid.Parse(o.DependsOn); err != nil || o.DependsOn == o.ID {
			return ErrOperation
		}
	}
	if o.Sequence < 1 || o.Base < 1 || len(o.Patch) == 0 {
		return ErrOperation
	}
	return validateFields(o.Kind, o.Patch, true)
}

type Conflict struct {
	Field  string          `json:"field"`
	Base   json.RawMessage `json:"base"`
	Local  json.RawMessage `json:"local"`
	Center json.RawMessage `json:"center"`
}
type Receipt struct {
	Operation string     `json:"operation"`
	Status    string     `json:"status"` // applied, conflict, rejected
	Reason    string     `json:"reason,omitempty"`
	Record    Record     `json:"record"`
	Conflicts []Conflict `json:"conflicts,omitempty"`
	// LocalFields contains only edits accepted from this dependency chain,
	// never unrelated center fields returned in Record. It lets later queued
	// edits advance their own field bases without adopting unseen remote edits.
	LocalFields map[string]AcceptedField `json:"local_fields,omitempty"`
}

type AcceptedField struct {
	Version int64           `json:"version"`
	Value   json.RawMessage `json:"value"`
}

func equal(a, b json.RawMessage) bool {
	if bytes.Equal(a, b) {
		return true
	}
	var x, y any
	da, db := json.NewDecoder(bytes.NewReader(a)), json.NewDecoder(bytes.NewReader(b))
	da.UseNumber()
	db.UseNumber()
	return da.Decode(&x) == nil && db.Decode(&y) == nil && reflect.DeepEqual(x, y)
}

// Merge is all-or-nothing for an operation. Conflicts retain every proposed
// field in the durable operation; no wall clock participates in arbitration.
func Merge(base, current Record, patch Fields) (Fields, []Conflict, error) {
	if base.Key() != current.Key() || base.Version > current.Version || base.Deleted {
		return nil, nil, ErrOperation
	}
	if err := validateFields(current.Kind, patch, true); err != nil {
		return nil, nil, err
	}
	if current.Deleted {
		return nil, nil, fmt.Errorf("entity deleted")
	}
	merged := Fields{}
	for k, v := range current.Fields {
		merged[k] = append(json.RawMessage(nil), v...)
	}
	keys := make([]string, 0, len(patch))
	for k := range patch {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var conflicts []Conflict
	for _, k := range keys {
		v := patch[k]
		if equal(base.Fields[k], current.Fields[k]) || equal(v, current.Fields[k]) {
			merged[k] = v
		} else {
			conflicts = append(conflicts, Conflict{k, base.Fields[k], v, current.Fields[k]})
		}
	}
	return merged, conflicts, nil
}
