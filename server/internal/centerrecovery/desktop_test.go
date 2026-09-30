package centerrecovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/middleware"
)

const desktopOwner = "a2e38b32-ef14-4384-b2e6-907b93bf87a6"

func desktopSession(t *testing.T, subject string, expires time.Time) string {
	t.Helper()
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": subject, "email": "recovery@example.test", "exp": expires.Unix(),
	}).SignedString(auth.JWTSecret())
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func desktopRequest(method, path, token string, data []byte) *http.Request {
	r := httptest.NewRequest(method, DesktopPrefix+path, bytes.NewReader(data))
	r.Header.Set("X-User-ID", desktopOwner)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}

func TestDesktopSnapshotRequiresConfiguredLiveOwnerSession(t *testing.T) {
	t.Setenv("MULTICA_RECOVERY_TOKEN", "")
	ownerToken := desktopSession(t, desktopOwner, time.Now().Add(time.Hour))
	lookupCalls, captureCalls := 0, 0
	a := desktopAuthority{owner: desktopOwner, lookup: func(context.Context, string) (string, error) {
		lookupCalls++
		return "recovery@example.test", nil
	}}
	capture := func(context.Context) ([]byte, error) { captureCalls++; return fixture(t, nil), nil }
	for _, tc := range []struct {
		name, owner, token, actor string
		code                      int
	}{
		{"owner", desktopOwner, ownerToken, "", 200},
		{"unconfigured", "", ownerToken, "", 403},
		{"malformed owner", "not-a-uuid", ownerToken, "", 403},
		{"no login", desktopOwner, "", "", 403},
		{"recovery bearer", desktopOwner, strings.Repeat("a", 64), "", 403},
		{"PAT", desktopOwner, "mul_not-a-session", "", 403},
		{"machine", desktopOwner, ownerToken, "task", 403},
		{"other user", desktopOwner, desktopSession(t, "cfe25f42-443c-493f-b9bf-72c515a6a99c", time.Now().Add(time.Hour)), "", 403},
		{"expired", desktopOwner, desktopSession(t, desktopOwner, time.Now().Add(-time.Hour)), "", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := a
			policy.owner = tc.owner
			r := desktopRequest("GET", "/snapshot", tc.token, nil)
			r.Header.Set("X-Actor-Source", tc.actor)
			w := httptest.NewRecorder()
			snapshotHandler(policy.authorize, capture)(w, r)
			if w.Code != tc.code {
				t.Fatalf("status=%d want=%d", w.Code, tc.code)
			}
		})
	}
	if captureCalls != 1 || lookupCalls != 1 {
		t.Fatal("unauthorized request reached capture or account lookup")
	}
	a.lookup = func(context.Context, string) (string, error) { return "", errors.New("removed account") }
	w := httptest.NewRecorder()
	snapshotHandler(a.authorize, capture)(w, desktopRequest("GET", "/snapshot", ownerToken, nil))
	if w.Code != 403 || captureCalls != 1 {
		t.Fatal("deleted account retained export authority")
	}
	// The legacy operator endpoint must not accept an ordinary session.
	w = httptest.NewRecorder()
	Handler(strings.Repeat("a", 64), capture)(w, desktopRequest("GET", "/snapshot", ownerToken, nil))
	if w.Code != 401 || captureCalls != 1 {
		t.Fatal("Desktop login bypassed operator token policy")
	}
}

func TestDesktopMiddlewareRejectsSpoofedOwner(t *testing.T) {
	a := desktopAuthority{owner: desktopOwner, lookup: func(context.Context, string) (string, error) { return "recovery@example.test", nil }}
	h := middleware.Auth(nil, nil, nil, nil)(snapshotHandler(a.authorize, func(context.Context) ([]byte, error) { return fixture(t, nil), nil }))
	for _, tc := range []struct {
		token string
		code  int
	}{
		{"", 401}, {"forged-token", 401},
		{desktopSession(t, "cfe25f42-443c-493f-b9bf-72c515a6a99c", time.Now().Add(time.Hour)), 403},
		{desktopSession(t, desktopOwner, time.Now().Add(time.Hour)), 200},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, desktopRequest("GET", "/snapshot", tc.token, nil))
		if w.Code != tc.code {
			t.Fatalf("status=%d want=%d", w.Code, tc.code)
		}
	}
}

func TestDesktopImportBindsStatusWithoutRecoveryToken(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MULTICA_RECOVERY_TOKEN", "")
	t.Setenv(ChildEnvironment, "1")
	for _, key := range []string{"REDIS_URL", "DATABASE_REPLICA_URL", "S3_BUCKET"} {
		t.Setenv(key, "")
	}
	ownerExists := true
	a := desktopAuthority{owner: desktopOwner, root: root, lookup: func(context.Context, string) (string, error) {
		if !ownerExists {
			return "", errors.New("replaced account table")
		}
		return "recovery@example.test", nil
	}}
	token := desktopSession(t, desktopOwner, time.Now().Add(time.Hour))
	restarts := 0
	importData, status := managedHandlers(root, func() { restarts++ }, a.authorize, a.authorizeStatus, a.bindStatus)
	r := desktopRequest("POST", "/import", token, fixture(t, nil))
	w := httptest.NewRecorder()
	importData(w, r)
	if w.Code != 400 || restarts != 0 {
		t.Fatal("missing destructive confirmation accepted")
	}
	r = desktopRequest("POST", "/import", token, fixture(t, nil))
	r.Header.Set("X-Multica-Recovery-Confirm", "replace-and-use")
	w = httptest.NewRecorder()
	importData(w, r)
	if w.Code != 202 || restarts != 1 {
		t.Fatalf("import failed: %d %s", w.Code, w.Body.String())
	}
	var state ImportStatus
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	grantPath := filepath.Join(root, "desktop-import-authority.json")
	grantData, err := os.ReadFile(grantPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(grantData, []byte(token)) {
		t.Fatal("plaintext login persisted")
	}
	info, err := os.Stat(grantPath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("status authority permissions", err)
	}
	ownerExists = false
	// Simulate restart with a fresh handler and a replaced user table. Only the
	// already authorized job is readable; the old session cannot export again.
	_, restartedStatus := managedHandlers(root, nil, a.authorize, a.authorizeStatus, a.bindStatus)
	status = middleware.Auth(nil, nil, nil, nil)(restartedStatus).ServeHTTP
	check := func(session, jobID string, expected int) {
		t.Helper()
		w := httptest.NewRecorder()
		status(w, desktopRequest("GET", "/import-status?job_id="+jobID, session, nil))
		if w.Code != expected {
			t.Fatalf("status=%d want=%d body=%s", w.Code, expected, w.Body.String())
		}
	}
	check(token, state.JobID, 200)
	check(token, strings.Repeat("b", 32), 403)
	check(desktopSession(t, desktopOwner, time.Now().Add(2*time.Hour)), state.JobID, 403)
	check("", state.JobID, 401)
	w = httptest.NewRecorder()
	a.authorize(w, desktopRequest("GET", "/snapshot", token, nil))
	if w.Code != 403 {
		t.Fatal("status grant authorized new backup operation")
	}
	var grant desktopImportAuthority
	if err := json.Unmarshal(grantData, &grant); err != nil {
		t.Fatal(err)
	}
	grant.ExpiresAt = time.Now().Add(-time.Second)
	if err := writePrivateJSON(grantPath, grant); err != nil {
		t.Fatal(err)
	}
	check(token, state.JobID, 403)
}

func TestMissingAuthorizationPolicyFailsClosed(t *testing.T) {
	importData, status := managedHandlers(t.TempDir(), nil, nil, nil, nil)
	for _, h := range []http.HandlerFunc{snapshotHandler(nil, nil), importData, status} {
		w := httptest.NewRecorder()
		h(w, desktopRequest("GET", "/snapshot", "", nil))
		if w.Code != 403 {
			t.Fatalf("nil policy returned %d", w.Code)
		}
	}
}

func TestDesktopStatusDoesNotExposeReplacedJob(t *testing.T) {
	root := t.TempDir()
	oldJob, newJob := strings.Repeat("a", 32), strings.Repeat("b", 32)
	// Simulate another importer replacing the status after the grant check.
	authorize := func(w http.ResponseWriter, r *http.Request) bool {
		if err := writePrivateJSON(filepath.Join(root, "status.json"), ImportStatus{JobID: newJob, State: "pending", CenterID: "another-center"}); err != nil {
			t.Fatal(err)
		}
		return true
	}
	_, status := managedHandlers(root, nil, nil, authorize, func(*http.Request, string) error { return nil })
	w := httptest.NewRecorder()
	status(w, desktopRequest("GET", "/import-status?job_id="+oldJob, "", nil))
	if w.Code != 404 || strings.Contains(w.Body.String(), "another-center") {
		t.Fatal("status leaked another job")
	}
}
