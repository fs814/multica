package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// DebugPolicy is independent of ordinary workspace execution limits. Values are
// pinned when a trial is admitted; changing these never mutates an existing run.
type DebugPolicy struct {
	Enabled              bool  `json:"enabled"`
	UserActiveRuns       int32 `json:"user_active_runs"`
	WorkspaceActiveRuns  int32 `json:"workspace_active_runs"`
	UserStartsPerHour    int32 `json:"user_starts_per_hour"`
	MaxDurationSeconds   int32 `json:"max_duration_seconds"`
	RetentionSeconds     int32 `json:"retention_seconds"`
	PayloadCapacityBytes int64 `json:"payload_capacity_bytes"`
}

func DefaultDebugPolicy() DebugPolicy { return DebugPolicy{false, 2, 5, 20, 1800, 2592000, 104857600} }
func DebugPolicyFromRow(p db.WorkflowDebugPolicy) DebugPolicy {
	return DebugPolicy{p.Enabled, p.UserActiveRuns, p.WorkspaceActiveRuns, p.UserStartsPerHour, p.MaxDurationSeconds, p.RetentionSeconds, p.PayloadCapacityBytes}
}
func (p DebugPolicy) Validate() error {
	for _, v := range []struct {
		name            string
		value, min, max int64
	}{
		{"user_active_runs", int64(p.UserActiveRuns), 1, 20}, {"workspace_active_runs", int64(p.WorkspaceActiveRuns), 1, 100},
		{"user_starts_per_hour", int64(p.UserStartsPerHour), 1, 1000}, {"max_duration_seconds", int64(p.MaxDurationSeconds), 60, 86400},
		{"retention_seconds", int64(p.RetentionSeconds), 86400, 7776000}, {"payload_capacity_bytes", p.PayloadCapacityBytes, 1048576, 1073741824},
	} {
		if v.value < v.min || v.value > v.max {
			return newEngineError("debug_invalid_policy", fmt.Sprintf("%s must be between %d and %d", v.name, v.min, v.max))
		}
	}
	if p.UserActiveRuns > p.WorkspaceActiveRuns {
		return newEngineError("debug_invalid_policy", "user_active_runs must not exceed workspace_active_runs")
	}
	return nil
}
func debugDuration(explicit int, p DebugPolicy, w WorkspacePolicy) (int, error) {
	ceiling := int(p.MaxDurationSeconds)
	if w.MaxDurationSeconds > 0 && w.MaxDurationSeconds < ceiling {
		ceiling = w.MaxDurationSeconds
	}
	if explicit < 0 || explicit > ceiling {
		return 0, &DefinitionError{Code: "debug_limit_conflict", Field: "limits.max_duration_seconds", Message: "explicit duration exceeds the trial or workspace limit"}
	}
	if explicit == 0 {
		return ceiling, nil
	}
	return explicit, nil
}
func debugMember(ctx context.Context, q *db.Queries, ws, user pgtype.UUID, edit bool) error {
	m, err := q.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{UserID: user, WorkspaceID: ws})
	if err != nil {
		return newEngineError(ErrCodeNotFound, "workspace not found")
	}
	if edit && m.Role != "owner" && m.Role != "admin" {
		return newEngineError("debug_forbidden", "owner or admin permission required")
	}
	return nil
}
func (e *Engine) UpdateDebugPolicy(ctx context.Context, ws, user pgtype.UUID, revision int64, p DebugPolicy) (db.WorkflowDebugPolicy, error) {
	var out db.WorkflowDebugPolicy
	if err := p.Validate(); err != nil {
		return out, err
	}
	err := e.runInTx(ctx, &txEffects{}, func(ctx context.Context, q *db.Queries) error {
		if err := debugMember(ctx, q, ws, user, true); err != nil {
			return err
		}
		if err := q.EnsureWorkflowDebugQuota(ctx, ws); err != nil {
			return err
		}
		if _, err := q.LockWorkflowDebugQuota(ctx, ws); err != nil {
			return err
		}
		if err := q.EnsureWorkflowDebugPolicy(ctx, ws); err != nil {
			return err
		}
		old, err := q.GetWorkflowDebugPolicy(ctx, ws)
		if err != nil {
			return err
		}
		if old.Revision != revision {
			return newEngineError("debug_policy_changed", "trial settings changed; confirm again")
		}
		if DebugPolicyFromRow(old) == p {
			out = old
			return nil
		}
		out, err = q.UpdateWorkflowDebugPolicy(ctx, db.UpdateWorkflowDebugPolicyParams{WorkspaceID: ws, ExpectedRevision: revision, Enabled: p.Enabled, UserActiveRuns: p.UserActiveRuns, WorkspaceActiveRuns: p.WorkspaceActiveRuns, UserStartsPerHour: p.UserStartsPerHour, MaxDurationSeconds: p.MaxDurationSeconds, RetentionSeconds: p.RetentionSeconds, PayloadCapacityBytes: p.PayloadCapacityBytes, UpdatedBy: user})
		return err
	})
	return out, err
}

// DebugQuotaError reports every exceeded dimension in stable product order.
type DebugQuotaDimension struct {
	Code  string `json:"code"`
	Usage int64  `json:"usage"`
	Limit int64  `json:"limit"`
}
type DebugQuotaError struct {
	Code              string                `json:"code"`
	Dimensions        []DebugQuotaDimension `json:"dimensions"`
	RequestedBytes    int64                 `json:"requested_bytes"`
	PolicyRevision    int64                 `json:"policy_revision"`
	RetryAfterSeconds int64                 `json:"retry_after_seconds,omitempty"`
}

func (e *DebugQuotaError) Error() string { return e.Code }
func checkDebugQuota(p db.WorkflowDebugPolicy, u db.GetWorkflowDebugUsageRow, used, requested int64) error {
	out := &DebugQuotaError{RequestedBytes: requested, PolicyRevision: p.Revision}
	for _, d := range []DebugQuotaDimension{{"debug_user_active_limit", u.UserActive, int64(p.UserActiveRuns)}, {"debug_workspace_active_limit", u.WorkspaceActive, int64(p.WorkspaceActiveRuns)}, {"debug_hourly_limit", u.UserHourly, int64(p.UserStartsPerHour)}, {"debug_storage_limit", used, p.PayloadCapacityBytes}} {
		exceeded := d.Usage >= d.Limit
		if d.Code == "debug_storage_limit" {
			exceeded = requested > d.Limit-d.Usage
		}
		if exceeded {
			out.Dimensions = append(out.Dimensions, d)
			if d.Code == "debug_hourly_limit" {
				out.RetryAfterSeconds = u.RetryAfterSeconds
			}
		}
	}
	if len(out.Dimensions) == 0 {
		return nil
	}
	out.Code = out.Dimensions[0].Code
	return out
}

// canonicalJSON counts UTF-8 JSON copies, not compressed database storage.
func canonicalJSON(raw []byte) ([]byte, error) {
	var v any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}
