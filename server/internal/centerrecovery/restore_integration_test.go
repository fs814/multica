package centerrecovery

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// Both databases must be isolated, managed test environments. The source must
// have migrations applied; the destination must have no application schema.
func TestRecoveryPostgresRoundTrip(t *testing.T) {
	sourceURL := os.Getenv("MULTICA_RECOVERY_TEST_SOURCE_URL")
	destinationURL := os.Getenv("MULTICA_RECOVERY_TEST_DESTINATION_URL")
	if sourceURL == "" || destinationURL == "" {
		t.Skip("set isolated recovery source and destination test URLs")
	}
	if sourceURL == destinationURL {
		t.Fatal("source and destination must differ")
	}
	ctx := context.Background()
	source, err := pgxpool.New(ctx, sourceURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(source.Close)
	destination, err := pgxpool.New(ctx, destinationURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(destination.Close)
	dbfx := testutil.New(source, "", "")
	user := dbfx.User(t, "Recovery owner", "recovery-roundtrip@example.test")
	workspace := dbfx.Workspace(t, "Recovery workspace", "recovery-roundtrip", testutil.Cols{"issue_prefix": "RCV"})
	dbfx.UserID, dbfx.WorkspaceID = user, workspace
	dbfx.Member(t, workspace, user, "owner")
	agent := dbfx.Agent(t, "Recovery agent", "", testutil.Cols{"custom_env": `{"NRC_API_KEY":"test-credential"}`})
	squad := dbfx.Squad(t, "Recovery squad", agent)
	member := dbfx.SquadMember(t, squad, "agent", agent)
	issue := dbfx.Issue(t, "Recovery issue")
	comment := dbfx.Comment(t, issue, "Preserved discussion")
	uploads := t.TempDir()
	if err = os.WriteFile(filepath.Join(uploads, "attachment.txt"), []byte("preserved upload"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := Capture(ctx, CaptureOptions{CenterID: "roundtrip-center", DatabaseURL: sourceURL, UploadDir: uploads, PGDump: os.Getenv("MULTICA_RECOVERY_TEST_PG_DUMP")})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	file, err := Save(root, "https://source.example", data)
	if err != nil {
		t.Fatal(err)
	}
	plain, _, err := Read(root, file)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(t.TempDir(), "restored")
	if err = Restore(ctx, plain, directory, destinationURL, "roundtrip-center", os.Getenv("MULTICA_RECOVERY_TEST_PG_RESTORE")); err != nil {
		t.Fatal(err)
	}
	for table, id := range map[string]string{"user": user, "workspace": workspace, "agent": agent, "squad": squad, "squad_member": member, "issue": issue, "comment": comment} {
		var exists bool
		if err = destination.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM "`+table+`" WHERE id=$1)`, id).Scan(&exists); err != nil || !exists {
			t.Fatalf("%s identity not preserved: %v", table, err)
		}
	}
	var savedAgent, savedMember, savedCredential string
	if err = destination.QueryRow(ctx, `SELECT leader_id FROM squad WHERE id=$1`, squad).Scan(&savedAgent); err != nil || savedAgent != agent {
		t.Fatal("squad leader relationship lost", err)
	}
	if err = destination.QueryRow(ctx, `SELECT member_id FROM squad_member WHERE id=$1`, member).Scan(&savedMember); err != nil || savedMember != agent {
		t.Fatal("squad member relationship lost", err)
	}
	if err = destination.QueryRow(ctx, `SELECT custom_env->>'NRC_API_KEY' FROM agent WHERE id=$1`, agent).Scan(&savedCredential); err != nil || savedCredential != "test-credential" {
		t.Fatal("agent credentials not preserved", err)
	}
	attachment, err := os.ReadFile(filepath.Join(directory, "uploads", "attachment.txt"))
	if err != nil || string(attachment) != "preserved upload" {
		t.Fatal("upload not preserved", err)
	}
	secondDir := filepath.Join(t.TempDir(), "second")
	err = Restore(ctx, plain, secondDir, destinationURL, "roundtrip-center", os.Getenv("MULTICA_RECOVERY_TEST_PG_RESTORE"))
	if err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatal("populated destination was not protected", err)
	}
	if _, err = os.Stat(secondDir); !os.IsNotExist(err) {
		t.Fatal("refused restore wrote files")
	}
}
