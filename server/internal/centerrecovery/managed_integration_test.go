package centerrecovery

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// Both URLs must reference isolated managed test databases with migrations.
// No real agent CLIs are resolved or invoked. The server has telemetry disabled.
func TestManagedCenterImportRestartsAndUsesRestoredData(t *testing.T) {
	sourceURL, destinationURL, binary := os.Getenv("MULTICA_RECOVERY_TEST_SOURCE_URL"), os.Getenv("MULTICA_RECOVERY_TEST_DESTINATION_URL"), os.Getenv("MULTICA_RECOVERY_TEST_SERVER_BINARY")
	if sourceURL == "" || destinationURL == "" || binary == "" {
		t.Skip("isolated source/destination and managed server binary required")
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
	fx := testutil.New(source, "", "")
	fx.UserID = fx.User(t, "Recovered owner", "managed-recovery@example.test")
	fx.WorkspaceID = fx.Workspace(t, "Recovered workspace", "managed-recovery", testutil.Cols{"issue_prefix": "RCV"})
	fx.Member(t, fx.WorkspaceID, fx.UserID, "owner")
	agent := fx.Agent(t, "Recovered agent", "", testutil.Cols{"custom_env": `{"NRC_API_KEY":"test-only-secret"}`})
	squad := fx.Squad(t, "Recovered squad", agent)
	fx.SquadMember(t, squad, "agent", agent)
	issue := fx.Issue(t, "Recovered issue")
	uploads := t.TempDir()
	if err = os.WriteFile(filepath.Join(uploads, "proof.txt"), []byte("recovered file"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := Capture(ctx, CaptureOptions{CenterID: "source-test", DatabaseURL: sourceURL, UploadDir: uploads, PGDump: os.Getenv("MULTICA_RECOVERY_TEST_PG_DUMP")})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strings.Split(listener.Addr().String(), ":")[1]
	listener.Close()
	root := t.TempDir()
	token := strings.Repeat("a", 64)
	env := map[string]string{"DATABASE_URL": destinationURL, "PORT": port, "MULTICA_RECOVERY_TOKEN": token, "MULTICA_RECOVERY_CENTER_ID": "destination-test", "MULTICA_RECOVERY_STATE_DIR": root, "MULTICA_RECOVERY_PG_DUMP": os.Getenv("MULTICA_RECOVERY_TEST_PG_DUMP"), "MULTICA_RECOVERY_PG_RESTORE": os.Getenv("MULTICA_RECOVERY_TEST_PG_RESTORE"), "LOCAL_UPLOAD_DIR": t.TempDir(), "REDIS_URL": "", "DATABASE_REPLICA_URL": "", "S3_BUCKET": "", "APP_ENV": "test", "JWT_SECRET": "test-only-managed-center-secret-32-characters", "DO_NOT_TRACK": "1", ChildEnvironment: ""}
	cmd := exec.Command(binary)
	cmd.Dir, err = filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	cmd.Env = os.Environ()
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	log, err := os.Create(filepath.Join(root, "server.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	cmd.Stdout, cmd.Stderr = log, log
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		select {
		case <-stopped:
		case <-time.After(15 * time.Second):
			_ = cmd.Process.Kill()
			<-stopped
		}
	})
	client := &http.Client{Timeout: 3 * time.Second}
	base := "http://127.0.0.1:" + port
	wait := func(check func() bool) {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			if check() {
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Fatalf("managed center did not reach expected state; log: %s", log.Name())
	}
	wait(func() bool {
		r, e := client.Get(base + "/readyz")
		if e != nil {
			return false
		}
		defer r.Body.Close()
		return r.StatusCode == 200
	})
	request, _ := http.NewRequest("POST", base+ImportEndpoint, bytes.NewReader(snapshot))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-Multica-Recovery-Confirm", "replace-and-use")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 202 {
		t.Fatalf("import response %d: %s", response.StatusCode, body)
	}
	var pending ImportStatus
	if err = json.Unmarshal(body, &pending); err != nil {
		t.Fatal(err)
	}
	wait(func() bool {
		request, _ := http.NewRequest("GET", base+StatusEndpoint, nil)
		request.Header.Set("Authorization", "Bearer "+token)
		r, e := client.Do(request)
		if e != nil {
			return false
		}
		defer r.Body.Close()
		var s ImportStatus
		if json.NewDecoder(r.Body).Decode(&s) != nil {
			return false
		}
		if s.State == "failed" {
			t.Fatalf("import failed: %s; log %s", s.Message, log.Name())
		}
		return s.JobID == pending.JobID && s.State == "complete"
	})
	var active activation
	if err = readJSON(filepath.Join(root, "active.json"), &active); err != nil {
		t.Fatal(err)
	}
	if active.DatabaseURL == destinationURL || active.CenterID != "source-test" {
		t.Fatal("database did not switch")
	}
	restored, err := pgx.Connect(ctx, active.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	for table, id := range map[string]string{"agent": agent, "squad": squad, "issue": issue} {
		var found bool
		if err = restored.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM "+pgx.Identifier{table}.Sanitize()+" WHERE id=$1)", id).Scan(&found); err != nil || !found {
			t.Fatalf("%s identity lost: %v", table, err)
		}
	}
	restored.Close(ctx)
	old, err := pgx.Connect(ctx, destinationURL)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err = old.QueryRow(ctx, `SELECT count(*) FROM agent WHERE id=$1`, agent).Scan(&count); err != nil || count != 0 {
		t.Fatal("original database was overwritten", err)
	}
	old.Close(ctx)
	got, err := os.ReadFile(filepath.Join(active.UploadDir, "proof.txt"))
	if err != nil || string(got) != "recovered file" {
		t.Fatal("uploads were not activated", err)
	}
	// A valid envelope containing an invalid pg_dump must restart the existing
	// center without changing its active database pointer.
	badRequest, _ := http.NewRequest("POST", base+ImportEndpoint, bytes.NewReader(fixture(t, nil)))
	badRequest.Header.Set("Authorization", "Bearer "+token)
	badRequest.Header.Set("X-Multica-Recovery-Confirm", "replace-and-use")
	badResponse, e := client.Do(badRequest)
	if e != nil {
		t.Fatal(e)
	}
	var failed ImportStatus
	e = json.NewDecoder(badResponse.Body).Decode(&failed)
	badResponse.Body.Close()
	if e != nil || badResponse.StatusCode != 202 {
		t.Fatal("failed-restore test was not staged", e)
	}
	wait(func() bool {
		r, _ := http.NewRequest("GET", base+StatusEndpoint, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		response, e := client.Do(r)
		if e != nil {
			return false
		}
		defer response.Body.Close()
		var state ImportStatus
		if json.NewDecoder(response.Body).Decode(&state) != nil {
			return false
		}
		return state.JobID == failed.JobID && state.State == "failed"
	})
	var retained activation
	if err = readJSON(filepath.Join(root, "active.json"), &retained); err != nil || retained.DatabaseURL != active.DatabaseURL {
		t.Fatal("failed restore changed active database", err)
	}
	if path := os.Getenv("MULTICA_RECOVERY_TEST_RESULT"); path != "" {
		if err = os.WriteFile(path+".failed-db", []byte("multica_restore_"+failed.JobID), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Expose the feature-created database name for cleanup via managed scripts.
	if path := os.Getenv("MULTICA_RECOVERY_TEST_RESULT"); path != "" {
		if err = writePrivateJSON(path, active); err != nil {
			t.Fatal(err)
		}
	}
}
