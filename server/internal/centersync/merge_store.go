package centersync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	ws "github.com/multica-ai/multica/server/internal/worksync"
)

type mergeInput struct {
	Workspace string         `json:"workspace"`
	Peer      string         `json:"peer"`
	Bundle    *contentBundle `json:"bundle,omitempty"`
}

func quoted(s string) string { return pgx.Identifier{s}.Sanitize() }
func projection(t contentTable) string {
	var columns []string
	for _, col := range strings.Split(t.columns, ",") {
		if t.name == "attachment" && col == "content_sha256" {
			continue // Derived from local bytes, never from a database or peer URL.
		}
		columns = append(columns, "'"+col+"',t."+quoted(col))
	}
	return "jsonb_build_object(" + strings.Join(columns, ",") + ")"
}
func (h *Handler) owns(ctx context.Context, tx pgx.Tx, workspace string, allowMissing bool) error {
	var exists, owner, fenced, deleted bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM workspace WHERE id=$1), EXISTS(SELECT 1 FROM member WHERE workspace_id=$1 AND user_id=$2 AND role='owner'), EXISTS(SELECT 1 FROM work_sync_recovery_fence WHERE workspace_id=$1 AND proof IS NOT NULL), EXISTS(SELECT 1 FROM center_content_merge WHERE workspace_id=$1 AND record_key='workspace:' || $1::uuid::text AND baseline='{}'::jsonb)`, workspace, h.config.Owner).Scan(&exists, &owner, &fenced, &deleted)
	if err != nil {
		return err
	}
	if deleted || fenced || (exists && !owner) || (!exists && !allowMissing) {
		return ws.ErrDenied
	}
	return nil
}

// User metadata is selected only for members and content authors of this
// workspace. No password, provider identity, session, token or settings columns.
func (h *Handler) snapshot(ctx context.Context, tx pgx.Tx, workspace string, files bool) (contentBundle, error) {
	b := contentBundle{Version: mergeVersion, Workspace: workspace, Records: []contentRecord{}, Users: []contentUser{}, Files: []contentFile{}}
	users := map[string]contentUser{}
	fileBytes := 0
	contentBytes := 0
	addUser := func(id, role string) (string, error) {
		if u, ok := users[id]; ok {
			if role != "" {
				u.Role = role
				users[id] = u
			}
			return u.ID, nil
		}
		var u contentUser
		if err := tx.QueryRow(ctx, `SELECT email,name FROM "user" WHERE id=$1`, id).Scan(&u.Email, &u.Name); err != nil {
			return "", err
		}
		u.ID = canonicalUser(u.Email)
		u.Role = role
		users[id] = u
		if len(users) > mergeLimit {
			return "", ws.ErrLimit
		}
		return u.ID, nil
	}
	var err error
	b.Owner, err = addUser(h.config.Owner, "")
	if err != nil {
		return b, err
	}
	rows, err := tx.Query(ctx, `SELECT user_id::text,role FROM member WHERE workspace_id=$1 ORDER BY user_id`, workspace)
	if err != nil {
		return b, err
	}
	type membership struct{ id, role string }
	var members []membership
	for rows.Next() {
		var m membership
		if err = rows.Scan(&m.id, &m.role); err != nil {
			rows.Close()
			return b, err
		}
		members = append(members, m)
		if len(members) > mergeLimit {
			rows.Close()
			return b, ws.ErrLimit
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return b, err
	}
	for _, m := range members {
		if _, err = addUser(m.id, m.role); err != nil {
			return b, err
		}
	}
	for _, table := range contentTables {
		rows, err := tx.Query(ctx, "SELECT "+projection(table)+" FROM "+quoted(table.name)+" t WHERE "+table.scope+" ORDER BY "+strings.Join(strings.Split(table.keys, ","), ",")+" LIMIT 10001", workspace)
		if err != nil {
			return b, err
		}
		var records []contentRecord
		for rows.Next() {
			r := contentRecord{Table: table.name}
			if err = rows.Scan(&r.Fields); err != nil {
				rows.Close()
				return b, err
			}
			records = append(records, r)
			if len(b.Records)+len(records) > mergeLimit {
				rows.Close()
				return b, ws.ErrLimit
			}
			for _, value := range r.Fields {
				contentBytes += len(value)
				if len(value) > 1<<20 || contentBytes > 8<<20 {
					rows.Close()
					return b, ws.ErrLimit
				}
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return b, err
		}
		for _, r := range records {
			for field := range r.Fields {
				if isUserField(r, field) && textValue(r.Fields[field]) != "" {
					id, e := addUser(textValue(r.Fields[field]), "")
					if e != nil {
						return b, e
					}
					r.Fields[field] = rawValue(id)
				}
			}
			b.Records = append(b.Records, r)
			if len(b.Records) > mergeLimit {
				return b, ws.ErrLimit
			}
			if table.name == "attachment" {
				var address string
				if err = tx.QueryRow(ctx, `SELECT url FROM attachment WHERE id=$1 AND workspace_id=$2`, textValue(r.Fields["id"]), workspace).Scan(&address); err != nil {
					return b, err
				}
				data, e := readAttachment(address)
				if e != nil {
					return b, e
				}
				r.Fields["content_sha256"] = rawValue(contentHash(data))
				if files {
					b.Files = append(b.Files, contentFile{textValue(r.Fields["id"]), data})
				}
				fileBytes += len(data)
				if fileBytes > 16<<20 {
					return b, ws.ErrLimit
				}
			}
		}
	}
	for _, u := range users {
		b.Users = append(b.Users, u)
	}
	sort.Slice(b.Users, func(i, j int) bool { return b.Users[i].ID < b.Users[j].ID })
	return b, nil
}

func isUserField(r contentRecord, field string) bool {
	if r.Table == "chat_session" && field == "creator_id" {
		return true
	}
	for _, prefix := range []string{"assignee", "creator", "author", "resolved_by", "uploader", "member"} {
		if field == prefix+"_id" {
			return textValue(r.Fields[prefix+"_type"]) == "member"
		}
	}
	return false
}

func uploadRoot() (*os.Root, error) {
	if os.Getenv("S3_BUCKET") != "" {
		return nil, errors.New("workspace merge currently requires local upload storage")
	}
	dir := os.Getenv("LOCAL_UPLOAD_DIR")
	if dir == "" {
		dir = "./data/uploads"
	}
	return os.OpenRoot(dir)
}
func readAttachment(address string) ([]byte, error) {
	u, err := url.Parse(address)
	if err != nil {
		return nil, ws.ErrOperation
	}
	if u.User != nil || !strings.HasPrefix(u.Path, "/uploads/") {
		return nil, errors.New("attachment is not in this center's local upload storage")
	}
	root, err := uploadRoot()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	key := strings.TrimPrefix(u.Path, "/uploads/")
	if key == "" || strings.HasSuffix(key, ".meta.json") || strings.HasSuffix(key, ".tmp") {
		return nil, ws.ErrOperation
	}
	f, err := root.Open(key)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 8<<20 {
		return nil, ws.ErrLimit
	}
	data, err := io.ReadAll(io.LimitReader(f, (8<<20)+1))
	if len(data) > 8<<20 {
		return nil, ws.ErrLimit
	}
	return data, err
}
func writeAttachment(f contentFile) (string, error) {
	root, err := uploadRoot()
	if err != nil {
		return "", err
	}
	defer root.Close()
	digest := sha256.Sum256(f.Data)
	key := "center-sync-" + hex.EncodeToString(digest[:])
	temporary := ".center-sync-" + uuid.NewString() + ".tmp"
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	defer root.Remove(temporary)
	_, err = file.Write(f.Data)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	// Atomic, no-clobber publication: interrupted staging never poisons a hash.
	err = root.Link(temporary, key)
	if errors.Is(err, os.ErrExist) {
		old, e := root.ReadFile(key)
		if e != nil {
			return "", e
		}
		if sha256.Sum256(old) != digest {
			return "", ws.ErrScope
		}
	} else if err != nil {
		return "", err
	}
	return strings.TrimSuffix(os.Getenv("LOCAL_UPLOAD_BASE_URL"), "/") + "/uploads/" + key, nil
}

func (h *Handler) exportContent(ctx context.Context, input mergeInput) (contentBundle, error) {
	var empty contentBundle
	if !validID(input.Workspace) || origin(input.Peer) != nil || input.Peer == h.config.Origin {
		return empty, ws.ErrScope
	}
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return empty, err
	}
	defer tx.Rollback(ctx)
	if err = h.owns(ctx, tx, input.Workspace, true); err != nil {
		return empty, err
	}
	b, err := h.snapshot(ctx, tx, input.Workspace, true)
	if err != nil {
		return empty, err
	}
	return b, tx.Commit(ctx)
}

func (h *Handler) mergeContent(ctx context.Context, input mergeInput) (mergeResult, error) {
	out := mergeResult{Workspace: input.Workspace, Conflicts: []mergeConflict{}}
	if input.Bundle == nil || input.Workspace != input.Bundle.Workspace || origin(input.Peer) != nil || input.Peer == h.config.Origin {
		return out, ws.ErrScope
	}
	b := *input.Bundle
	if err := validateBundle(b); err != nil {
		return out, ws.ErrOperation
	}
	if err := validateReferences(b); err != nil {
		return out, err
	}
	ordered, err := orderedRecords(b.Records)
	if err != nil {
		return out, ws.ErrScope
	}
	b.Records = ordered
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "center-content:"+b.Workspace); err != nil {
		return out, err
	}
	// Participate in the existing workspace teardown lock protocol. A delete
	// either waits for this merge or commits first and leaves a tombstone.
	if _, err = tx.Exec(ctx, `SELECT id FROM workspace WHERE id=$1 FOR KEY SHARE`, b.Workspace); err != nil {
		return out, err
	}
	if err = h.owns(ctx, tx, b.Workspace, true); err != nil {
		return out, err
	}
	local, err := h.snapshot(ctx, tx, b.Workspace, false)
	if err != nil {
		return out, err
	}
	if b.Owner != local.Owner {
		return out, ws.ErrDenied
	}
	current := map[string]contentRecord{}
	for _, r := range local.Records {
		current[recordKey(r)] = r
	}
	baseline := map[string]map[string]json.RawMessage{}
	rows, err := tx.Query(ctx, `SELECT record_key,baseline FROM center_content_merge WHERE workspace_id=$1 AND owner_id=$2 AND peer_origin=$3`, b.Workspace, h.config.Owner, input.Peer)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var key string
		var fields map[string]json.RawMessage
		if err = rows.Scan(&key, &fields); err != nil {
			rows.Close()
			return out, err
		}
		baseline[key] = fields
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	// Check both directions before writing: holding a deleted parent for review
	// must not still detach its children or resurrect it through a later record.
	incoming := map[string]bool{}
	for _, r := range b.Records {
		key := recordKey(r)
		incoming[key] = true
		if _, exists := current[key]; !exists && baseline[key] != nil {
			out.Conflicts = append(out.Conflicts, mergeConflict{r.Table, key, "$deleted", rawValue(nil), rawValue(r.Fields)})
		}
	}
	for key := range baseline {
		if !incoming[key] {
			if r, ok := current[key]; ok {
				out.Conflicts = append(out.Conflicts, mergeConflict{r.Table, key, "$deleted", rawValue(r.Fields), rawValue(nil)})
			}
		}
	}
	if len(out.Conflicts) != 0 {
		return out, nil // Entire workspace is unchanged pending deletion review.
	}
	// Validate the resulting graph, not just the incoming snapshot. Independent
	// edits to parent links must not combine into a cycle or an invalid actor.
	planned := map[string]contentRecord{}
	for key, r := range current {
		planned[key] = r
	}
	nextBaselines := map[string]map[string]json.RawMessage{}
	for _, r := range b.Records {
		key := recordKey(r)
		merged, next, conflicts := mergeFields(current[key].Fields, r.Fields, baseline[key], r.Table, key)
		out.Conflicts = append(out.Conflicts, conflicts...)
		planned[key] = contentRecord{r.Table, merged}
		nextBaselines[key] = next
	}
	combined := contentBundle{Records: []contentRecord{}}
	contentSize, uploadSize := 0, int64(0)
	for _, r := range planned {
		combined.Records = append(combined.Records, r)
		for _, value := range r.Fields {
			contentSize += len(value)
		}
		if r.Table == "attachment" {
			var size int64
			if json.Unmarshal(r.Fields["size_bytes"], &size) != nil || size < 0 || size > 8<<20 {
				return out, ws.ErrLimit
			}
			uploadSize += size
		}
	}
	if len(combined.Records) > mergeLimit || contentSize > 8<<20 || uploadSize > 16<<20 {
		return out, ws.ErrLimit
	}
	knownUsers := map[string]contentUser{}
	for _, u := range append(append([]contentUser{}, b.Users...), local.Users...) {
		knownUsers[u.ID] = u
	}
	for _, u := range knownUsers {
		combined.Users = append(combined.Users, u)
	}
	if err = validateReferences(combined); err != nil {
		return out, err
	}
	ordered, err = orderedRecords(combined.Records)
	if err != nil {
		return out, ws.ErrScope
	}
	users, err := h.mapContentUsers(ctx, tx, combined.Users)
	if err != nil {
		return out, err
	}
	fileURLs := map[string]string{}
	for _, file := range b.Files {
		address, e := writeAttachment(file)
		if e != nil {
			return out, e
		}
		fileURLs[contentHash(file.Data)] = address
	}
	// The complete selected workspace commits atomically. A failure never leaves
	// half a workspace or a success receipt; immutable uploaded bytes may remain.
	for _, r := range ordered {
		key := recordKey(r)
		if !incoming[key] {
			continue
		}
		old, exists := current[key]
		if !exists || !equalValue(rawValue(old.Fields), rawValue(r.Fields)) {
			if err = writeRecord(ctx, tx, r, b.Workspace, h.config.Owner, users, fileURLs, !exists); err != nil {
				var pgError *pgconn.PgError
				if errors.As(err, &pgError) && pgError.Code == "23505" {
					out.Updated = 0
					out.Conflicts = append(out.Conflicts, mergeConflict{r.Table, key, "$unique", rawValue("Existing identifier or name is retained"), rawValue(r.Fields)})
					return out, nil // deferred rollback; never overwrite a colliding identity
				}
				return out, err
			}
			out.Updated++
		}
		if _, err = tx.Exec(ctx, `INSERT INTO center_content_merge(workspace_id,owner_id,peer_origin,record_key,baseline) VALUES($1,$2,$3,$4,$5) ON CONFLICT(workspace_id,owner_id,peer_origin,record_key) DO UPDATE SET baseline=EXCLUDED.baseline`, b.Workspace, h.config.Owner, input.Peer, key, nextBaselines[key]); err != nil {
			return out, err
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO member(workspace_id,user_id,role) VALUES($1,$2,'owner') ON CONFLICT(workspace_id,user_id) DO NOTHING`, b.Workspace, h.config.Owner); err != nil {
		return out, err
	}
	memberConflicts, err := h.mergeMembers(ctx, tx, input.Peer, b, local.Users, users, baseline)
	if err != nil {
		return out, err
	}
	out.Conflicts = append(out.Conflicts, memberConflicts...)
	if _, err = tx.Exec(ctx, `UPDATE workspace SET issue_counter=GREATEST(issue_counter,COALESCE((SELECT max(number) FROM issue WHERE workspace_id=$1),0)) WHERE id=$1`, b.Workspace); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `UPDATE "user" SET onboarded_at=COALESCE(onboarded_at,now()) WHERE id=$1`, h.config.Owner); err != nil {
		return out, err
	}
	encoded, err := json.Marshal(out)
	if err != nil || len(encoded) > ws.MaxWireBytes || len(out.Conflicts) > 1000 {
		return mergeResult{}, ws.ErrLimit
	}
	if err = tx.Commit(ctx); err != nil {
		return out, err
	}
	if h.MembershipCache != nil {
		for _, id := range users {
			h.MembershipCache.Invalidate(ctx, id, b.Workspace)
		}
	}
	return out, nil
}

func (h *Handler) mergeMembers(ctx context.Context, tx pgx.Tx, peer string, b contentBundle, local []contentUser, users map[string]string, baseline map[string]map[string]json.RawMessage) ([]mergeConflict, error) {
	roles := map[string]string{}
	for _, u := range local {
		if u.Role != "" {
			roles[u.ID] = u.Role
		}
	}
	seen := map[string]bool{}
	var conflicts []mergeConflict
	for _, u := range b.Users {
		if u.Role == "" || u.ID == b.Owner {
			continue
		}
		key := "member:" + u.ID
		seen[key] = true
		var old map[string]json.RawMessage
		if role := roles[u.ID]; role != "" {
			old = map[string]json.RawMessage{"role": rawValue(role)}
		}
		incoming := map[string]json.RawMessage{"role": rawValue(u.Role)}
		if old == nil && baseline[key] != nil {
			conflicts = append(conflicts, mergeConflict{"member", key, "$deleted", rawValue(nil), rawValue(incoming)})
			continue
		}
		merged, next, review := mergeFields(old, incoming, baseline[key], "member", key)
		conflicts = append(conflicts, review...)
		if len(review) == 0 && textValue(merged["role"]) != "" {
			if _, err := tx.Exec(ctx, `INSERT INTO member(workspace_id,user_id,role) VALUES($1,$2,$3) ON CONFLICT(workspace_id,user_id) DO UPDATE SET role=EXCLUDED.role`, b.Workspace, users[u.ID], textValue(merged["role"])); err != nil {
				return nil, err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO center_content_merge(workspace_id,owner_id,peer_origin,record_key,baseline) VALUES($1,$2,$3,$4,$5) ON CONFLICT(workspace_id,owner_id,peer_origin,record_key) DO UPDATE SET baseline=EXCLUDED.baseline`, b.Workspace, h.config.Owner, peer, key, next); err != nil {
			return nil, err
		}
	}
	for key := range baseline {
		if strings.HasPrefix(key, "member:") && !seen[key] {
			if role := roles[strings.TrimPrefix(key, "member:")]; role != "" {
				conflicts = append(conflicts, mergeConflict{"member", key, "$deleted", rawValue(role), rawValue(nil)})
			}
		}
	}
	return conflicts, nil
}

func (h *Handler) mapContentUsers(ctx context.Context, tx pgx.Tx, incoming []contentUser) (map[string]string, error) {
	result := map[string]string{}
	for _, u := range incoming {
		rows, err := tx.Query(ctx, `SELECT id::text FROM "user" WHERE lower(email)=lower($1) LIMIT 2`, u.Email)
		if err != nil {
			return nil, err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		if len(ids) > 1 {
			return nil, ws.ErrScope
		}
		if len(ids) == 0 {
			var id string
			if err = tx.QueryRow(ctx, `INSERT INTO "user"(email,name) VALUES($1,$2) RETURNING id::text`, u.Email, u.Name).Scan(&id); err != nil {
				return nil, err
			}
			ids = append(ids, id)
		}
		result[u.ID] = ids[0]
	}
	return result, nil
}

func writeRecord(ctx context.Context, tx pgx.Tx, r contentRecord, workspace, owner string, users map[string]string, files map[string]string, create bool) error {
	t, _ := tableFor(r.Table)
	fields := map[string]json.RawMessage{}
	for k, v := range r.Fields {
		fields[k] = v
	}
	for field := range fields {
		if isUserField(r, field) && textValue(fields[field]) != "" {
			local, ok := users[textValue(fields[field])]
			if !ok {
				return ws.ErrScope
			}
			fields[field] = rawValue(local)
		}
	}
	if strings.Contains(t.scope, "t.workspace_id=$1") {
		fields["workspace_id"] = rawValue(workspace)
	}
	if create {
		switch r.Table {
		case "agent":
			fields["owner_id"] = rawValue(owner)
			fields["runtime_mode"] = rawValue("local")
			fields["visibility"] = rawValue("private")
			fields["permission_mode"] = rawValue("private")
			fields["status"] = rawValue("offline")
		case "skill":
			fields["created_by"] = rawValue(owner)
		case "squad":
			fields["creator_id"] = rawValue(owner)
		}
	}
	if r.Table == "attachment" {
		delete(fields, "content_sha256")
		if address := files[textValue(r.Fields["content_sha256"])]; address != "" {
			fields["url"] = rawValue(address)
		} else if create {
			return ws.ErrScope
		}
	}
	columns := make([]string, 0, len(fields))
	for k := range fields {
		columns = append(columns, k)
	}
	sort.Strings(columns)
	var names, selects, assigns, predicates []string
	for _, k := range columns {
		names = append(names, quoted(k))
		selects = append(selects, "v."+quoted(k))
		if !strings.Contains(","+t.keys+",", ","+k+",") {
			assigns = append(assigns, quoted(k)+"=v."+quoted(k))
		}
	}
	for _, k := range strings.Split(t.keys, ",") {
		predicates = append(predicates, "t."+quoted(k)+"=v."+quoted(k))
	}
	populate := "jsonb_populate_record(NULL::" + quoted(r.Table) + ",$1::jsonb) v"
	if create {
		// Bare INSERT intentionally rejects any global-ID/name collision. Never
		// upsert through an unscoped key into another owner's workspace.
		_, err := tx.Exec(ctx, "INSERT INTO "+quoted(r.Table)+" ("+strings.Join(names, ",")+") SELECT "+strings.Join(selects, ",")+" FROM "+populate, fields)
		return err
	}
	if len(assigns) == 0 {
		return nil
	}
	// Recheck workspace scope in the write, including join-table parent scopes.
	scope := strings.ReplaceAll(t.scope, "$1", "$2")
	query := "UPDATE " + quoted(r.Table) + " t SET " + strings.Join(assigns, ",") + " FROM " + populate + " WHERE " + strings.Join(predicates, " AND ") + " AND " + scope
	command, err := tx.Exec(ctx, query, fields, workspace)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return fmt.Errorf("merge record changed scope")
	}
	return nil
}

func validateReferences(b contentBundle) error {
	ids := map[string]map[string]bool{}
	users := map[string]bool{}
	for _, u := range b.Users {
		users[u.ID] = true
	}
	for _, r := range b.Records {
		if ids[r.Table] == nil {
			ids[r.Table] = map[string]bool{}
		}
		ids[r.Table][textValue(r.Fields["id"])] = true
	}
	refs := map[string]string{"skill_id": "skill", "agent_id": "agent", "label_id": "issue_label", "squad_id": "squad", "issue_id": "issue", "comment_id": "comment", "parent_issue_id": "issue", "project_id": "project", "leader_id": "agent", "depends_on_issue_id": "issue", "chat_session_id": "chat_session", "chat_message_id": "chat_message"}
	for _, r := range b.Records {
		for field, value := range r.Fields {
			id := textValue(value)
			if id == "" {
				continue
			}
			if isUserField(r, field) {
				if !users[id] {
					return ws.ErrScope
				}
				continue
			}
			ref := refs[field]
			if field == "parent_id" && r.Table == "comment" {
				ref = "comment"
			}
			for _, prefix := range []string{"assignee", "creator", "author", "resolved_by", "uploader", "member"} {
				if field == prefix+"_id" {
					ref = textValue(r.Fields[prefix+"_type"])
					if ref != "agent" && ref != "squad" {
						return ws.ErrScope
					}
				}
			}
			if ref != "" && !ids[ref][id] {
				return ws.ErrScope
			}
		}
	}
	return nil
}
