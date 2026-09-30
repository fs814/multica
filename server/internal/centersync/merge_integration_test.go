package centersync

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// Both URLs must name separately provisioned managed test databases, never live
// centers. There is deliberately no fallback to DATABASE_URL or a local default.
func TestContentMergeTwoDatabases(t *testing.T) {
	aURL, bURL := os.Getenv("MULTICA_MERGE_TEST_SOURCE_URL"), os.Getenv("MULTICA_MERGE_TEST_PEER_URL")
	if aURL == "" || bURL == "" {
		t.Skip("two isolated managed merge databases required")
	}
	if aURL == bURL {
		t.Fatal("test needs separate databases")
	}
	ctx := context.Background()
	email := uuid.NewString() + "@example.test"
	type center struct {
		h       *Handler
		fx      *testutil.Fixture
		uploads string
	}
	setup := func(address, origin string) center {
		pool, err := pgxpool.New(ctx, address)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		fx := testutil.New(pool, "", "")
		fx.UserID = fx.User(t, "merge owner", email)
		h, err := New(pool, Config{Owner: fx.UserID, Origin: origin, Root: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		return center{h, fx, t.TempDir()}
	}
	a, b := setup(aURL, "https://a.example.test"), setup(bURL, "https://b.example.test")
	a.fx.WorkspaceID = a.fx.Workspace(t, "Merged workspace", "merge-"+uuid.NewString(), testutil.Cols{"issue_prefix": "MRG"})
	workspace := a.fx.WorkspaceID
	a.fx.Member(t, workspace, a.fx.UserID, "owner")
	project := a.fx.Project(t, "project")
	agent := a.fx.Agent(t, "Portable agent", "", testutil.Cols{"instructions": "Keep these instructions", "custom_env": testutil.Raw(`'{"SECRET":"source-only"}'::jsonb`)})
	chat := a.fx.Insert(t, "chat_session", testutil.Cols{"workspace_id": workspace, "agent_id": agent, "creator_id": a.fx.UserID, "title": "Portable chat", "session_id": "machine-local-provider-session", "work_dir": "/machine-local-path"})
	message := a.fx.Insert(t, "chat_message", testutil.Cols{"chat_session_id": chat, "role": "user", "content": "chat history"})
	issue := a.fx.Issue(t, "original", testutil.Cols{"project_id": project, "assignee_type": "agent", "assignee_id": agent})
	a.fx.Issue(t, "child", testutil.Cols{"parent_issue_id": issue})
	comment := a.fx.Comment(t, issue, "portable comment")
	a.fx.Comment(t, issue, "reply", testutil.Cols{"parent_id": comment})
	skill := a.fx.Insert(t, "skill", testutil.Cols{"workspace_id": workspace, "name": "Portable skill", "content": "skill instructions", "created_by": a.fx.UserID})
	a.fx.Insert(t, "skill_file", testutil.Cols{"skill_id": skill, "path": "references/example.txt", "content": "skill reference"})
	a.fx.InsertNoID(t, "agent_skill", testutil.Cols{"agent_id": agent, "skill_id": skill, "enabled": true}, "agent_id=$1 AND skill_id=$2", agent, skill)
	if err := os.WriteFile(filepath.Join(a.uploads, "fixture.txt"), []byte("attachment payload"), 0600); err != nil {
		t.Fatal(err)
	}
	attachment := a.fx.Insert(t, "attachment", testutil.Cols{"workspace_id": workspace, "issue_id": issue, "comment_id": comment, "uploader_type": "member", "uploader_id": a.fx.UserID, "filename": "fixture.txt", "url": "https://a.example.test/uploads/fixture.txt", "content_type": "text/plain", "size_bytes": 18})
	for _, c := range []center{a, b} {
		c.fx.Cleanup(t, `DELETE FROM center_content_merge WHERE workspace_id=$1`, workspace)
	}
	// Register imported rows with the shared fixture's cleanup facility.
	for _, table := range contentTables {
		b.fx.Cleanup(t, "DELETE FROM "+quoted(table.name)+" t WHERE "+table.scope, workspace)
	}
	b.fx.Cleanup(t, `DELETE FROM member WHERE workspace_id=$1`, workspace)
	use := func(c center) {
		t.Setenv("LOCAL_UPLOAD_DIR", c.uploads)
		t.Setenv("LOCAL_UPLOAD_BASE_URL", c.h.config.Origin)
		t.Setenv("S3_BUCKET", "")
	}
	export := func(from, to center) contentBundle {
		use(from)
		bundle, err := from.h.exportContent(ctx, mergeInput{Workspace: workspace, Peer: to.h.config.Origin})
		if err != nil {
			t.Fatal(err)
		}
		return bundle
	}
	apply := func(to, from center, bundle contentBundle) mergeResult {
		use(to)
		result, err := to.h.mergeContent(ctx, mergeInput{Workspace: workspace, Peer: from.h.config.Origin, Bundle: &bundle})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	round := func() (mergeResult, mergeResult) {
		aa, bb := export(a, b), export(b, a)
		var ar, br mergeResult
		if len(aa.Records) > 0 {
			br = apply(b, a, aa)
		}
		if len(bb.Records) > 0 {
			ar = apply(a, b, bb)
		}
		return ar, br
	}
	round()
	round()
	var count int
	b.fx.QueryRow(t, `SELECT count(*) FROM member WHERE workspace_id=$1 AND user_id=$2 AND role='owner'`, workspace, b.fx.UserID).Scan(&count)
	if count != 1 {
		t.Fatal("workspace not visible to destination account")
	}
	var onboarded bool
	b.fx.QueryRow(t, `SELECT onboarded_at IS NOT NULL FROM "user" WHERE id=$1`, b.fx.UserID).Scan(&onboarded)
	if !onboarded {
		t.Fatal("onboarding not completed for imported workspace owner")
	}
	var instructions, secret, status string
	var unbound bool
	b.fx.QueryRow(t, `SELECT instructions,custom_env::text,status,runtime_id IS NULL FROM agent WHERE id=$1`, agent).Scan(&instructions, &secret, &status, &unbound)
	if instructions != "Keep these instructions" || strings.Contains(secret, "source-only") || status != "offline" || !unbound {
		t.Fatal("agent content/credential/runtime boundary broken")
	}
	var chatContent string
	b.fx.QueryRow(t, `SELECT content FROM chat_message WHERE id=$1`, message).Scan(&chatContent)
	if chatContent != "chat history" {
		t.Fatal("chat history missing")
	}
	b.fx.QueryRow(t, `SELECT session_id IS NULL AND work_dir IS NULL AND runtime_id IS NULL FROM chat_session WHERE id=$1`, chat).Scan(&unbound)
	if !unbound {
		t.Fatal("chat execution session leaked")
	}
	var address string
	b.fx.QueryRow(t, `SELECT url FROM attachment WHERE id=$1`, attachment).Scan(&address)
	use(b)
	data, err := readAttachment(address)
	if err != nil || string(data) != "attachment payload" {
		t.Fatalf("attachment missing: %v", err)
	}
	b.fx.Exec(t, `UPDATE agent SET custom_env='{"SECRET":"destination-only"}'::jsonb WHERE id=$1`, agent)
	a.fx.Exec(t, `UPDATE issue SET title='source edit' WHERE id=$1`, issue)
	b.fx.Exec(t, `UPDATE project SET title='peer edit' WHERE id=$1`, project)
	round()
	round()
	var title string
	b.fx.QueryRow(t, `SELECT title FROM issue WHERE id=$1`, issue).Scan(&title)
	if title != "source edit" {
		t.Fatal("source edit not merged")
	}
	a.fx.QueryRow(t, `SELECT title FROM project WHERE id=$1`, project).Scan(&title)
	if title != "peer edit" {
		t.Fatal("peer edit not merged")
	}
	b.fx.QueryRow(t, `SELECT custom_env::text FROM agent WHERE id=$1`, agent).Scan(&secret)
	if !strings.Contains(secret, "destination-only") {
		t.Fatal("local credentials overwritten")
	}
	a.fx.Exec(t, `UPDATE issue SET title='a conflict' WHERE id=$1`, issue)
	b.fx.Exec(t, `UPDATE issue SET title='b conflict' WHERE id=$1`, issue)
	ar, br := round()
	if len(ar.Conflicts) == 0 || len(br.Conflicts) == 0 {
		t.Fatal("concurrent edit not reported")
	}
	a.fx.QueryRow(t, `SELECT title FROM issue WHERE id=$1`, issue).Scan(&title)
	if title != "a conflict" {
		t.Fatal("source conflict overwritten")
	}
	b.fx.QueryRow(t, `SELECT title FROM issue WHERE id=$1`, issue).Scan(&title)
	if title != "b conflict" {
		t.Fatal("peer conflict overwritten")
	}
	bad := export(a, b)
	for i := range bad.Records {
		if bad.Records[i].Table == "agent" {
			bad.Records[i].Fields["custom_env"] = json.RawMessage(`{"SECRET":"injected"}`)
		}
	}
	use(b)
	if _, err = b.h.mergeContent(ctx, mergeInput{Workspace: workspace, Peer: a.h.config.Origin, Bundle: &bad}); err == nil {
		t.Fatal("accepted credential injection")
	}
	var queues int
	b.fx.QueryRow(t, `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1`, agent).Scan(&queues)
	if queues != 0 {
		t.Fatal("merge scheduled execution")
	}
	// References to another workspace must fail before any account/file writes.
	bad = export(a, b)
	for i := range bad.Records {
		if bad.Records[i].Table == "issue" {
			bad.Records[i].Fields["project_id"] = rawValue(uuid.NewString())
			break
		}
	}
	use(b)
	if _, err = b.h.mergeContent(ctx, mergeInput{Workspace: workspace, Peer: a.h.config.Origin, Bundle: &bad}); err == nil {
		t.Fatal("accepted a cross-workspace reference")
	}
	// Resolve the deliberate title conflict before testing independent cases.
	a.fx.Exec(t, `UPDATE issue SET title='resolved' WHERE id=$1`, issue)
	b.fx.Exec(t, `UPDATE issue SET title='resolved' WHERE id=$1`, issue)
	round()
	round()
	if result := apply(b, a, export(a, b)); result.Updated != 0 || len(result.Conflicts) != 0 {
		t.Fatal("replaying a confirmed snapshot is not idempotent")
	}
	// A same-size attachment edit is still content, and concurrent edits retain
	// each center's bytes rather than replacing a file through its metadata.
	if err = os.WriteFile(filepath.Join(a.uploads, "fixture.txt"), []byte("replacement bytes!"), 0600); err != nil {
		t.Fatal(err)
	}
	round()
	round()
	b.fx.QueryRow(t, `SELECT url FROM attachment WHERE id=$1`, attachment).Scan(&address)
	use(b)
	data, err = readAttachment(address)
	if err != nil || string(data) != "replacement bytes!" {
		t.Fatalf("same-size attachment update was lost: %v", err)
	}
	use(a)
	aAddress, err := writeAttachment(contentFile{attachment, []byte("source conflicting")})
	if err != nil {
		t.Fatal(err)
	}
	a.fx.Exec(t, `UPDATE attachment SET url=$1 WHERE id=$2`, aAddress, attachment)
	use(b)
	bAddress, err := writeAttachment(contentFile{attachment, []byte("peer conflict data")})
	if err != nil {
		t.Fatal(err)
	}
	b.fx.Exec(t, `UPDATE attachment SET url=$1 WHERE id=$2`, bAddress, attachment)
	ar, br = round()
	if len(ar.Conflicts) == 0 || len(br.Conflicts) == 0 {
		t.Fatal("attachment conflict not reported")
	}
	b.fx.QueryRow(t, `SELECT url FROM attachment WHERE id=$1`, attachment).Scan(&address)
	if address != bAddress {
		t.Fatal("conflicting local attachment replaced")
	}
	// Match content again without copying either server's local path.
	use(a)
	aAddress, err = writeAttachment(contentFile{attachment, []byte("peer conflict data")})
	if err != nil {
		t.Fatal(err)
	}
	a.fx.Exec(t, `UPDATE attachment SET url=$1 WHERE id=$2`, aAddress, attachment)
	round()
	round()
	memberEmail := uuid.NewString() + "@example.test"
	aMember, bMember := a.fx.User(t, "member", memberEmail), b.fx.User(t, "existing profile", memberEmail)
	a.fx.Member(t, workspace, aMember, "member")
	round()
	round()
	b.fx.QueryRow(t, `SELECT count(*) FROM member WHERE workspace_id=$1 AND user_id=$2 AND role='member'`, workspace, bMember).Scan(&count)
	if count != 1 {
		t.Fatal("membership did not map to the existing local account")
	}
	b.fx.Exec(t, `DELETE FROM member WHERE workspace_id=$1 AND user_id=$2`, workspace, bMember)
	ar, br = round()
	if len(ar.Conflicts) == 0 || len(br.Conflicts) == 0 {
		t.Fatal("membership revocation not reported")
	}
	b.fx.QueryRow(t, `SELECT count(*) FROM member WHERE workspace_id=$1 AND user_id=$2`, workspace, bMember).Scan(&count)
	if count != 0 {
		t.Fatal("revoked membership was silently restored")
	}
	// A remote parent deletion must not detach the receiving issue or partially
	// commit unrelated changes while the parent remains held for review.
	a.fx.Exec(t, `UPDATE issue SET project_id=NULL WHERE id=$1`, issue)
	a.fx.Exec(t, `DELETE FROM project WHERE id=$1`, project)
	a.fx.Exec(t, `UPDATE agent SET instructions='must not partly apply' WHERE id=$1`, agent)
	br = apply(b, a, export(a, b))
	if br.Updated != 0 || len(br.Conflicts) == 0 {
		t.Fatal("parent deletion did not stop workspace application")
	}
	var retained string
	b.fx.QueryRow(t, `SELECT project_id::text FROM issue WHERE id=$1`, issue).Scan(&retained)
	if retained != project {
		t.Fatal("child detached despite unresolved parent deletion")
	}
	b.fx.QueryRow(t, `SELECT instructions FROM agent WHERE id=$1`, agent).Scan(&instructions)
	if instructions != "Keep these instructions" {
		t.Fatal("deletion conflict partially committed workspace changes")
	}
	// Remove the deletion conflict only in the incoming test snapshot, then add
	// a distinct issue with a colliding per-workspace number. All earlier writes
	// in the transaction must roll back when that late insert fails.
	bad = export(a, b)
	peerSnapshot := export(b, a)
	for _, r := range peerSnapshot.Records {
		if r.Table == "project" {
			bad.Records = append(bad.Records, r)
		}
	}
	for _, r := range bad.Records {
		if r.Table == "issue" && textValue(r.Fields["id"]) == issue {
			copyFields := map[string]json.RawMessage{}
			for k, v := range r.Fields {
				copyFields[k] = v
			}
			copyFields["id"] = rawValue(uuid.NewString())
			bad.Records = append(bad.Records, contentRecord{"issue", copyFields})
			break
		}
	}
	br = apply(b, a, bad)
	if br.Updated != 0 || len(br.Conflicts) == 0 {
		t.Fatal("unique issue number collision did not roll back")
	}
	b.fx.QueryRow(t, `SELECT instructions FROM agent WHERE id=$1`, agent).Scan(&instructions)
	if instructions != "Keep these instructions" {
		t.Fatal("unique collision partially committed earlier records")
	}
	// Workspace deletion leaves only an empty baseline marker, which must deny
	// re-import even if the peer address changes or the workspace is recreated.
	b.fx.Exec(t, `UPDATE center_content_merge SET baseline='{}'::jsonb WHERE workspace_id=$1 AND record_key='workspace:' || $1::uuid::text`, workspace)
	use(b)
	if _, err = b.h.mergeContent(ctx, mergeInput{Workspace: workspace, Peer: "https://third.example.test", Bundle: &bad}); err == nil {
		t.Fatal("deleted workspace tombstone did not block re-import")
	}
}
