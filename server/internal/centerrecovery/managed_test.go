package centerrecovery

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManagedImportStagesAndRequiresConfirmation(t *testing.T) {
	root := t.TempDir()
	token := strings.Repeat("a", 64)
	t.Setenv("MULTICA_RECOVERY_STATE_DIR", root)
	t.Setenv("MULTICA_RECOVERY_TOKEN", token)
	t.Setenv(ChildEnvironment, "1")
	for _, env := range []string{"REDIS_URL", "DATABASE_REPLICA_URL", "S3_BUCKET"} {
		t.Setenv(env, "")
	}
	calls := 0
	handler, status := ManagedHandlers(func() { calls++ })
	request := func(credential, confirmation string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", ImportEndpoint, bytes.NewReader(fixture(t, nil)))
		r.Header.Set("Authorization", "Bearer "+credential)
		r.Header.Set("X-Multica-Recovery-Confirm", confirmation)
		w := httptest.NewRecorder()
		handler(w, r)
		return w
	}
	if got := request("wrong", "replace-and-use").Code; got != 401 {
		t.Fatal(got)
	}
	if got := request(token, "").Code; got != 400 {
		t.Fatal(got)
	}
	if calls != 0 {
		t.Fatal("unauthorized request restarted server")
	}
	response := request(token, "replace-and-use")
	if response.Code != 202 {
		t.Fatal(response.Code, response.Body.String())
	}
	var state ImportStatus
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.State != "pending" || calls != 1 {
		t.Fatal("incorrect staged import")
	}
	var plan importPlan
	if err := readJSON(filepath.Join(root, "pending.json"), &plan); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Read(filepath.Join(root, "incoming"), plan.Snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "active.json")); !os.IsNotExist(err) {
		t.Fatal("activation changed before restore")
	}
	if got := request(token, "replace-and-use").Code; got != 409 {
		t.Fatal("duplicate import accepted", got)
	}
	r := httptest.NewRequest("GET", StatusEndpoint, nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	status(w, r)
	if w.Code != 200 || strings.Contains(w.Body.String(), "database_url") {
		t.Fatal("invalid public status")
	}
}
func TestManagedImportUnavailableWithoutSupervisor(t *testing.T) {
	t.Setenv("MULTICA_RECOVERY_TOKEN", strings.Repeat("a", 64))
	t.Setenv("MULTICA_RECOVERY_STATE_DIR", t.TempDir())
	t.Setenv(ChildEnvironment, "")
	handler, _ := ManagedHandlers(func() { t.Fatal("restart") })
	r := httptest.NewRequest("POST", ImportEndpoint, nil)
	r.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 64))
	w := httptest.NewRecorder()
	handler(w, r)
	if w.Code != 409 {
		t.Fatal(w.Code)
	}
}
func TestActivationRollbackPreservesDatabasePointers(t *testing.T) {
	root := t.TempDir()
	previous := activation{Version: 1, DatabaseURL: "postgres://test@localhost/previous", UploadDir: root, CenterID: "previous", Keys: map[string]string{}}
	current := previous
	current.DatabaseURL = "postgres://test@localhost/candidate"
	if err := writePrivateJSON(filepath.Join(root, "previous.json"), previous); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateJSON(filepath.Join(root, "active.json"), current); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateJSON(filepath.Join(root, "status.json"), ImportStatus{JobID: strings.Repeat("a", 32), State: "activating", CenterID: "source"}); err != nil {
		t.Fatal(err)
	}
	if err := rollbackActivation(root); err != nil {
		t.Fatal(err)
	}
	var restored activation
	if err := readJSON(filepath.Join(root, "active.json"), &restored); err != nil {
		t.Fatal(err)
	}
	if restored.DatabaseURL != previous.DatabaseURL {
		t.Fatal("rollback did not restore previous pointer")
	}
	var status ImportStatus
	if err := readJSON(filepath.Join(root, "status.json"), &status); err != nil || status.State != "failed" {
		t.Fatal("rollback reported success", err)
	}
}

func TestReadinessRequiresCandidateBootIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Multica-Recovery-Boot", "expected")
		w.WriteHeader(200)
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	t.Setenv("PORT", u.Port())
	ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
	defer cancel()
	if waitForReady(ctx, "another-process") {
		t.Fatal("accepted unrelated server readiness")
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if !waitForReady(ctx2, "expected") {
		t.Fatal("rejected matching ready process")
	}
}
