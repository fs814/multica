package worksync

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RecoveryBundle is a point-in-time export, not a credential. Its digest must
// be pinned by the independent recovery authority after authenticating the
// source machine. A self-computed checksum alone proves no provenance.
type RecoveryBundle struct {
	State  ReplicaState `json:"state"`
	Digest string       `json:"digest"`
}

func (b *RecoveryBundle) Seal() { b.Digest = digest(b.State) }
func (r *Replica) ExportRecovery() (RecoveryBundle, error) {
	s, err := r.State()
	if err != nil {
		return RecoveryBundle{}, err
	}
	if !s.Initialized {
		return RecoveryBundle{}, ErrCursor
	}
	b := RecoveryBundle{State: s}
	b.Seal()
	return b, nil
}

// RecoveryPlan is fixed by an operator, never by the first uploading daemon.
// Every expected source must be supplied. Missing machines cannot silently be
// dropped from a retry. Boundary is the reviewed durable recovery point, not
// an assertion that all writes acknowledged by the lost Center survived.
type RecoveryPlan struct {
	ID       string            `json:"id"`
	Source   Scope             `json:"source"`
	Target   Scope             `json:"target"`
	Owner    string            `json:"owner"`
	Sources  map[string]string `json:"sources"` // node -> independently pinned bundle digest
	Boundary int64             `json:"boundary"`
}
type RecoveryReport struct {
	Plan        RecoveryPlan     `json:"plan"`
	Records     []Record         `json:"records"`
	Bundles     []RecoveryBundle `json:"bundles"` // includes original conflicts and pending intent; never replayed
	Pending     int              `json:"pending"`
	Conflicts   int              `json:"conflicts"`
	Limitations []string         `json:"limitations"`
	Digest      string           `json:"digest"`
}

func (r RecoveryReport) checksum() string { r.Digest = ""; return digest(r) }

// PlanRecovery validates complete snapshots on one explicitly trusted lineage.
// Receipt records ahead of the best snapshot may complement one another only
// if their union covers every next sequence. Incomplete or divergent histories
// fail closed instead of treating a higher object version as a full backup.
func PlanRecovery(p RecoveryPlan, bundles []RecoveryBundle) (RecoveryReport, error) {
	fail := func(reason string) (RecoveryReport, error) {
		return RecoveryReport{}, fmt.Errorf("%w: %s", ErrOperation, reason)
	}
	if p.Source.Validate() != nil || p.Target.Validate() != nil || p.Source.Workspace != p.Target.Workspace || p.Source.Group != p.Target.Group || p.Source.Epoch == p.Target.Epoch {
		return RecoveryReport{}, ErrScope
	}
	for _, x := range []string{p.ID, p.Owner} {
		u, e := uuid.Parse(x)
		if e != nil || u == uuid.Nil || u.String() != x {
			return fail("invalid recovery identity")
		}
	}
	if len(p.Sources) < 2 || len(p.Sources) > 32 || len(bundles) != len(p.Sources) || p.Boundary < 0 {
		return fail("all approved replicas required")
	}
	encoded, err := json.Marshal(bundles)
	if err != nil || len(encoded) > MaxWireBytes {
		return RecoveryReport{}, ErrLimit
	}
	bundles = append([]RecoveryBundle(nil), bundles...)
	sort.Slice(bundles, func(i, j int) bool { return bundles[i].State.Principal.Node < bundles[j].State.Principal.Node })
	seen := map[string]bool{}
	best := -1
	var cursor int64 = -1
	versions := map[int64]Record{}
	report := RecoveryReport{Plan: p, Bundles: bundles, Limitations: []string{
		"Only the Issues/Projects/Agents allowlisted projection is recovered; this is not a complete Work backup.",
		"Unreplicated Center commits are unknowable; the reviewed boundary is not RPO=0.",
		"Pending operations and conflicts are retained for manual review and are never replayed into the new epoch.",
		"ACLs, credentials, runtimes, agent execution configuration, attachments and historical effects are not restored.",
	}}
	for i, b := range bundles {
		s := b.State
		node := s.Principal.Node
		if seen[node] || p.Sources[node] == "" || p.Sources[node] != b.Digest || b.Digest != digest(s) {
			return fail("unapproved source or digest mismatch")
		}
		seen[node] = true
		if s.Schema != Schema || s.Scope != p.Source || s.Revoked || len(s.Quarantined) != 0 || !s.Initialized || s.Principal.Actor != p.Owner || s.Principal.Account != p.Owner || s.Cursor < 0 || s.Sequence < 0 || s.Records == nil || s.Acknowledged == nil {
			return fail("invalid or unauthorized replica")
		}
		if _, err := uuid.Parse(s.Incarnation); err != nil {
			return fail("invalid incarnation")
		}
		if len(s.Records) > MaxSnapshot {
			return RecoveryReport{}, ErrLimit
		}
		if s.Cursor > cursor {
			best = i
			cursor = s.Cursor
		}
		for _, records := range []map[string]Record{s.Records, s.Acknowledged} {
			for key, r := range records {
				if r.Validate() != nil || key != r.Key() || (!r.Deleted && len(r.Fields) != len(exportFields[r.Kind])) {
					return fail("invalid record")
				}
				if old, ok := versions[r.Version]; ok && digest(old) != digest(r) {
					return fail("divergent confirmed history")
				}
				versions[r.Version] = r
			}
		}
		for _, r := range s.Records {
			if r.Version > s.Cursor {
				return fail("record exceeds snapshot boundary")
			}
		}
		var last int64
		for _, op := range s.Outbox {
			if op.Validate(s.Principal, s.Scope) != nil || op.Incarnation != s.Incarnation || op.Sequence <= last || op.Sequence > s.Sequence {
				return fail("invalid pending operation")
			}
			last = op.Sequence
		}
		for _, review := range s.Review {
			op, receipt := review.Operation, review.Receipt
			if op.Validate(s.Principal, s.Scope) != nil || op.Incarnation != s.Incarnation || op.Sequence > s.Sequence || receipt.Operation != op.ID || (receipt.Status != "conflict" && receipt.Status != "rejected") {
				return fail("invalid conflict")
			}
			// A conflict/rejection can carry a confirmed Center record newer
			// than the last pull. In particular a delete receipt may be the
			// only surviving tombstone if the process crashes before pulling.
			rec := receipt.Record
			if rec.ID != "" || rec.Kind != "" || rec.Version != 0 || rec.Deleted || len(rec.Fields) != 0 {
				if rec.Validate() != nil || rec.Key() != op.Kind+"/"+op.Entity || (!rec.Deleted && len(rec.Fields) != len(exportFields[rec.Kind])) {
					return fail("invalid confirmed conflict record")
				}
				if old, ok := versions[rec.Version]; ok && digest(old) != digest(rec) {
					return fail("divergent confirmed conflict history")
				}
				versions[rec.Version] = rec
			}
			for _, conflict := range receipt.Conflicts {
				if !contains(writableFields[op.Kind], conflict.Field) {
					return fail("invalid conflict field")
				}
				for _, value := range []json.RawMessage{conflict.Base, conflict.Local, conflict.Center} {
					if !json.Valid(value) || len(value) > 1<<20 {
						return fail("invalid conflict value")
					}
				}
			}
		}
		report.Pending += len(s.Outbox)
		report.Conflicts += len(s.Review)
	}
	records := map[string]Record{}
	for key, r := range bundles[best].State.Records {
		records[key] = r
	}
	for _, b := range bundles {
		if b.State.Cursor == cursor && len(b.State.Records) != len(records) {
			return fail("incomplete equal-boundary snapshot")
		}
		for key, older := range b.State.Records {
			latest, ok := records[key]
			if !ok || latest.Version < older.Version || (b.State.Cursor == cursor && digest(latest) != digest(older)) {
				return fail("incomplete snapshot or divergent history")
			}
		}
	}
	for version, rec := range versions {
		if version <= cursor {
			latest, ok := records[rec.Key()]
			if !ok || latest.Version < version {
				return fail("snapshot omits confirmed record")
			}
		}
	}
	if p.Boundary-cursor > int64(len(versions)) {
		return fail("missing confirmed range")
	}
	for seq := cursor + 1; seq <= p.Boundary; seq++ {
		r, ok := versions[seq]
		if !ok {
			return fail("missing confirmed sequence")
		}
		records[r.Key()] = r
	}
	if cursor > p.Boundary {
		return fail("reviewed boundary is stale")
	}
	for version := range versions {
		if version > p.Boundary {
			return fail("unreviewed confirmed sequence")
		}
	}
	if len(records) > MaxSnapshot {
		return RecoveryReport{}, ErrLimit
	}
	keys := make([]string, 0, len(records))
	for key := range records {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		report.Records = append(report.Records, records[key])
	}
	// References outside this bounded projection require a preprovisioned identity
	// or catalog. Entity references must be closed before staging can succeed.
	for _, r := range report.Records {
		if r.Deleted || r.Kind != "issue" {
			continue
		}
		for field, kind := range map[string]string{"project_id": "project", "parent_issue_id": "issue"} {
			var ref *string
			if json.Unmarshal(r.Fields[field], &ref) != nil {
				return fail("invalid relationship")
			}
			if ref != nil {
				target, ok := records[kind+"/"+*ref]
				if !ok || target.Deleted {
					return fail("missing relationship dependency")
				}
			}
		}
	}
	report.Digest = report.checksum()
	return report, nil
}

// Recovery is an operator service, deliberately absent from public sync routes.
// Authorize must use an independent recovery identity and recheck expiration,
// scope, owner and pinned plan at every call. VerifyFence must verify deployment
// isolation, not infer it from an epoch or a failed HTTP probe. Nil hooks deny.
// No production identity/fencing adapter is supplied by this bounded stage.
type Recovery struct {
	Enabled     bool
	Pool        *pgxpool.Pool
	Authorize   func(context.Context, pgx.Tx, Principal, RecoveryPlan, string) error
	VerifyFence func(context.Context, RecoveryPlan) error
}

func (r *Recovery) begin(ctx context.Context, p Principal, plan RecoveryPlan, action string) (pgx.Tx, error) {
	if !r.Enabled {
		return nil, ErrDisabled
	}
	if r.Authorize == nil {
		return nil, ErrDenied
	}
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if err = r.Authorize(ctx, tx, p, plan, action); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}
func (r *Recovery) Stage(ctx context.Context, p Principal, plan RecoveryPlan, bundles []RecoveryBundle) (RecoveryReport, error) {
	tx, err := r.begin(ctx, p, plan, "stage")
	if err != nil {
		return RecoveryReport{}, err
	}
	defer tx.Rollback(ctx)
	// Serialize staging with workspace deletion; deleted workspaces cannot
	// regain retained recovery data through a delayed upload.
	var workspace string
	if err = tx.QueryRow(ctx, `SELECT id::text FROM workspace WHERE id=$1 FOR KEY SHARE`, plan.Target.Workspace).Scan(&workspace); err != nil {
		return RecoveryReport{}, err
	}
	report, err := PlanRecovery(plan, bundles)
	if err != nil {
		return report, err
	}
	data, _ := json.Marshal(report)
	_, err = tx.Exec(ctx, `INSERT INTO work_sync_recovery(id,workspace_id,report_hash,report) VALUES($1,$2,$3,$4) ON CONFLICT(id) DO NOTHING`, plan.ID, plan.Target.Workspace, report.Digest, data)
	if err != nil {
		return RecoveryReport{}, err
	}
	var hash string
	if err = tx.QueryRow(ctx, `SELECT report_hash FROM work_sync_recovery WHERE id=$1`, plan.ID).Scan(&hash); err != nil {
		return RecoveryReport{}, err
	}
	if hash != report.Digest {
		return RecoveryReport{}, ErrOperation
	}
	return report, tx.Commit(ctx)
}

// Activate atomically installs a new epoch into an empty, independently
// provisioned workspace. Restored agents have no runtime and private permissions.
// Recovery stores preserve source versions; the new journal gets fresh versions.
func (r *Recovery) Activate(ctx context.Context, p Principal, plan RecoveryPlan, approvedDigest string) error {
	tx, err := r.begin(ctx, p, plan, "activate")
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Use workspace -> session -> business order, matching staging/deletion.
	var owner string
	if err = tx.QueryRow(ctx, `SELECT m.user_id::text FROM workspace w JOIN member m ON m.workspace_id=w.id WHERE w.id=$1 AND m.user_id=$2 AND m.role='owner' FOR UPDATE OF w,m`, plan.Target.Workspace, plan.Owner).Scan(&owner); err != nil {
		return ErrDenied
	}
	var data []byte
	var hash string
	var active bool
	if err = tx.QueryRow(ctx, `SELECT report,report_hash,activated FROM work_sync_recovery WHERE id=$1 FOR UPDATE`, plan.ID).Scan(&data, &hash, &active); err != nil {
		return err
	}
	var report RecoveryReport
	if json.Unmarshal(data, &report) != nil || report.checksum() != hash || hash != approvedDigest || digest(report.Plan) != digest(plan) {
		return ErrOperation
	}
	if r.VerifyFence == nil {
		return ErrDenied
	}
	if err = r.VerifyFence(ctx, plan); err != nil {
		return err
	}
	if active {
		return tx.Commit(ctx)
	}
	// Lock bootstrap identity and business tables before checking emptiness. No
	// ordinary writer can race the empty check or mutate the imported projection.
	if _, err = tx.Exec(ctx, `LOCK TABLE issue,project,agent,agent_task_queue,agent_runtime,daemon_token,autopilot,issue_wakeup,workflow_run IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return err
	}
	// Reusing business UUIDs must not reconnect old tasks or automation.
	// These tables share the write barrier until the activation commits.
	agents, issues := []string{}, []string{}
	for _, rec := range report.Records {
		switch rec.Kind {
		case "agent":
			agents = append(agents, rec.ID)
		case "issue":
			issues = append(issues, rec.ID)
		}
	}
	var occupied bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(
      SELECT 1 FROM issue WHERE workspace_id=$1
      UNION ALL SELECT 1 FROM project WHERE workspace_id=$1
      UNION ALL SELECT 1 FROM agent WHERE workspace_id=$1
      UNION ALL SELECT 1 FROM work_sync_scope WHERE workspace_id=$1
      UNION ALL SELECT 1 FROM work_sync_change WHERE workspace_id=$1
      UNION ALL SELECT 1 FROM work_sync_receipt WHERE workspace_id=$1
      UNION ALL SELECT 1 FROM work_sync_grant WHERE workspace_id=$1
      UNION ALL SELECT 1 FROM agent_runtime WHERE workspace_id=$1
      UNION ALL SELECT 1 FROM daemon_token WHERE workspace_id=$1
      UNION ALL SELECT 1 FROM autopilot WHERE workspace_id=$1
      UNION ALL SELECT 1 FROM issue_wakeup WHERE workspace_id=$1
      UNION ALL SELECT 1 FROM workflow_run WHERE workspace_id=$1
      UNION ALL SELECT 1 FROM agent_task_queue WHERE agent_id=ANY($2::uuid[]) OR issue_id=ANY($3::uuid[])
    )`, plan.Target.Workspace, agents, issues).Scan(&occupied); err != nil {
		return err
	}
	if occupied {
		return fmt.Errorf("%w: target is not empty", ErrScope)
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('multica.work_sync_apply','on',true)`); err != nil {
		return err
	}
	for _, kind := range []string{"project", "agent", "issue"} {
		for _, rec := range report.Records {
			if rec.Kind != kind || rec.Deleted {
				continue
			}
			if err = restoreRecord(ctx, tx, plan, rec); err != nil {
				return err
			}
		}
	}
	// Existing installations retain the historical parent foreign key. Insert
	// every Issue first, then restore parent links, independent of UUID order.
	for _, rec := range report.Records {
		if rec.Kind != "issue" || rec.Deleted {
			continue
		}
		fields, _ := json.Marshal(rec.Fields)
		if _, err = tx.Exec(ctx, `UPDATE issue SET parent_issue_id=($2::jsonb->>'parent_issue_id')::uuid WHERE id=$1 AND workspace_id=$3`, rec.ID, fields, plan.Target.Workspace); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE workspace SET issue_counter=GREATEST(issue_counter,COALESCE((SELECT max(number) FROM issue WHERE workspace_id=$1),0)) WHERE id=$1`, plan.Target.Workspace); err != nil {
		return err
	}
	// Verify stored projection after all ordinary database triggers have run.
	for _, rec := range report.Records {
		if rec.Deleted {
			continue
		}
		var matches bool
		fields, _ := json.Marshal(rec.Fields)
		if err = tx.QueryRow(ctx, `SELECT work_sync_fields($1,to_jsonb(t))=$2::jsonb FROM `+rec.Kind+` t WHERE id=$3 AND workspace_id=$4`, rec.Kind, fields, rec.ID, plan.Target.Workspace).Scan(&matches); err != nil {
			return err
		}
		if !matches {
			return fmt.Errorf("%w: restored projection differs", ErrOperation)
		}
	}
	// Flush deferred capture while no scope exists, before installing the journal.
	if _, err = tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO work_sync_scope(workspace_id,group_id,epoch,sequence) VALUES($1,$2,$3,$4)`, plan.Target.Workspace, plan.Target.Group, plan.Target.Epoch, len(report.Records)); err != nil {
		return err
	}
	for i, rec := range report.Records {
		fields, _ := json.Marshal(rec.Fields)
		if _, err = tx.Exec(ctx, `INSERT INTO work_sync_change(workspace_id,sequence,kind,entity_id,deleted,fields) VALUES($1,$2,$3,$4,$5,$6)`, plan.Target.Workspace, i+1, rec.Kind, rec.ID, rec.Deleted, fields); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE work_sync_recovery SET activated=true WHERE id=$1`, plan.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func restoreRecord(ctx context.Context, tx pgx.Tx, p RecoveryPlan, r Record) error {
	// Typed SQL populates only the fixed projection; unknown fields were rejected
	// at staging. Database constraints validate catalog values and uniqueness.
	data, _ := json.Marshal(r.Fields)
	var query string
	switch r.Kind {
	case "project":
		query = `INSERT INTO project(id,workspace_id,title,description,priority,status,icon) SELECT $1,$2,x.title,x.description,x.priority,x.status,x.icon FROM jsonb_populate_record(NULL::project,$3) x`
	case "agent":
		query = `INSERT INTO agent(id,workspace_id,name,description,avatar_url,archived_at,runtime_mode,runtime_config,owner_id,visibility,permission_mode) SELECT $1,$2,x.name,x.description,x.avatar_url,x.archived_at,'local','{}',$4,'private','private' FROM jsonb_populate_record(NULL::agent,$3) x`
	case "issue":
		var status string
		if json.Unmarshal(r.Fields["status"], &status) != nil || !contains([]string{"backlog", "todo", "in_progress", "in_review", "done", "blocked", "cancelled"}, status) {
			return fmt.Errorf("%w: custom status catalog is not included in this recovery projection", ErrOperation)
		}
		var assignee, assigneeType *string
		if json.Unmarshal(r.Fields["assignee_id"], &assignee) != nil || json.Unmarshal(r.Fields["assignee_type"], &assigneeType) != nil {
			return ErrOperation
		}
		if (assignee == nil) != (assigneeType == nil) {
			return ErrOperation
		}
		if assignee != nil {
			var ok bool
			switch *assigneeType {
			case "member":
				err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM member WHERE workspace_id=$1 AND user_id=$2)`, p.Target.Workspace, *assignee).Scan(&ok)
				if err != nil {
					return err
				}
			case "agent":
				err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent WHERE workspace_id=$1 AND id=$2 AND kind='user')`, p.Target.Workspace, *assignee).Scan(&ok)
				if err != nil {
					return err
				}
			default:
				return ErrOperation
			}
			if !ok {
				return fmt.Errorf("%w: missing assignee identity", ErrOperation)
			}
		}
		query = `INSERT INTO issue(id,workspace_id,title,description,priority,status,number,project_id,parent_issue_id,assignee_type,assignee_id,creator_type,creator_id) SELECT $1,$2,x.title,x.description,x.priority,x.status,x.number,x.project_id,NULL,x.assignee_type,x.assignee_id,'member',$4 FROM jsonb_populate_record(NULL::issue,$3) x`
	default:
		return ErrOperation
	}
	args := []any{r.ID, p.Target.Workspace, data}
	if r.Kind != "project" {
		args = append(args, p.Owner)
	}
	_, err := tx.Exec(ctx, query, args...)
	return err
}
