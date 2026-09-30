package centerrecovery

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
)

const ImportEndpoint = "/api/center/recovery/import"
const StatusEndpoint = "/api/center/recovery/import-status"
const RestartExitCode = 75
const ChildEnvironment = "MULTICA_RECOVERY_MANAGED_CHILD"

// Operator and Desktop routes stage into the same recovery directory.
var managedImportMu sync.Mutex

type ImportStatus struct {
	JobID    string `json:"job_id"`
	State    string `json:"state"`
	CenterID string `json:"center_id"`
	Message  string `json:"message,omitempty"`
}
type importPlan struct {
	JobID    string `json:"job_id"`
	Snapshot string `json:"snapshot"`
	CenterID string `json:"center_id"`
}
type activation struct {
	Version     int               `json:"version"`
	DatabaseURL string            `json:"database_url"`
	UploadDir   string            `json:"upload_dir"`
	CenterID    string            `json:"center_id"`
	Keys        map[string]string `json:"keys"`
}

func writePrivateJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".pending-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
func readJSON(path string, v any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil {
		return err
	}
	if len(raw) > 65536 {
		return errors.New("recovery state exceeds limit")
	}
	return json.Unmarshal(raw, v)
}
func StateDir() string { return os.Getenv("MULTICA_RECOVERY_STATE_DIR") }

// ApplyActivation is called before constructing pools and workers. The parent
// supervisor remains on the original environment and reads this file on each boot.
func ApplyActivation(root string) error {
	if root == "" {
		return nil
	}
	var a activation
	if err := readJSON(filepath.Join(root, "active.json"), &a); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return errors.New("cannot read center activation")
	}
	if a.Version != 1 || a.CenterID == "" || !filepath.IsAbs(a.UploadDir) {
		return errors.New("invalid center activation")
	}
	if _, _, err := DatabaseEnvironment(a.DatabaseURL); err != nil {
		return err
	}
	for _, key := range DeploymentKeys {
		if err := os.Setenv(key, a.Keys[key]); err != nil {
			return err
		}
	}
	for key, value := range map[string]string{"DATABASE_URL": a.DatabaseURL, "LOCAL_UPLOAD_DIR": a.UploadDir, "MULTICA_RECOVERY_CENTER_ID": a.CenterID, "DATABASE_REPLICA_URL": ""} {
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return nil
}

// ManagedHandlers stages an encrypted import before asking main to drain and
// exit. The parent restores only after every child worker has exited.
func ManagedHandlers(restart func()) (http.HandlerFunc, http.HandlerFunc) {
	root, token := StateDir(), os.Getenv("MULTICA_RECOVERY_TOKEN")
	authenticated := func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Cache-Control", "no-store")
		if !authorizedRecovery(r, token) {
			http.Error(w, "recovery credential required", 401)
			return false
		}
		return true
	}
	return managedHandlers(root, restart, authenticated, authenticated, nil)
}

// Desktop and operator entry points share staging and activation mechanics,
// but retain separate authorization policies. A Desktop job binds its status
// reader before staging is acknowledged or the server is restarted.
func managedHandlers(root string, restart func(), authorizeImport, authorizeStatus func(http.ResponseWriter, *http.Request) bool, bindStatus func(*http.Request, string) error) (http.HandlerFunc, http.HandlerFunc) {
	status := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if authorizeStatus == nil {
			http.Error(w, "authorization unavailable", http.StatusForbidden)
			return
		}
		if !authorizeStatus(w, r) {
			return
		}
		var state ImportStatus
		if root == "" || readJSON(filepath.Join(root, "status.json"), &state) != nil {
			http.Error(w, "no import status", 404)
			return
		}
		// A subsequent import may replace status.json after authorization. Never
		// expose its status through an earlier Desktop job's session grant.
		if bindStatus != nil && state.JobID != r.URL.Query().Get("job_id") {
			http.Error(w, "import status changed", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(state)
	}
	importData := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if authorizeImport == nil {
			http.Error(w, "authorization unavailable", http.StatusForbidden)
			return
		}
		if !authorizeImport(w, r) {
			return
		}
		if restart == nil || root == "" || !filepath.IsAbs(root) || os.Getenv(ChildEnvironment) != "1" || os.Getenv("REDIS_URL") != "" || os.Getenv("DATABASE_REPLICA_URL") != "" || os.Getenv("S3_BUCKET") != "" {
			http.Error(w, "Desktop import requires a managed single-node center with MULTICA_RECOVERY_STATE_DIR and local uploads", 409)
			return
		}
		if !managedImportMu.TryLock() {
			http.Error(w, "another import is in progress", 409)
			return
		}
		defer managedImportMu.Unlock()
		if _, err := os.Stat(filepath.Join(root, "pending.json")); !os.IsNotExist(err) {
			http.Error(w, "another import is pending", 409)
			return
		}
		if r.Header.Get("X-Multica-Recovery-Confirm") != "replace-and-use" {
			http.Error(w, "explicit import confirmation required", 400)
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxArchiveSize))
		if err != nil {
			http.Error(w, "backup is incomplete or too large", 400)
			return
		}
		manifest, err := Inspect(raw)
		if err != nil {
			http.Error(w, "invalid center backup", 400)
			return
		}
		snapshot, err := Save(filepath.Join(root, "incoming"), "desktop-import", raw)
		if err != nil {
			http.Error(w, "could not stage backup", 503)
			return
		}
		idBytes := make([]byte, 16)
		if _, err = rand.Read(idBytes); err != nil {
			http.Error(w, "could not allocate import", 503)
			return
		}
		id := hex.EncodeToString(idBytes)
		if bindStatus != nil {
			if err := bindStatus(r, id); err != nil {
				http.Error(w, "could not authorize import status", 503)
				return
			}
		}
		plan := importPlan{JobID: id, Snapshot: snapshot, CenterID: manifest.CenterID}
		state := ImportStatus{JobID: id, State: "pending", CenterID: manifest.CenterID}
		if err = writePrivateJSON(filepath.Join(root, "status.json"), state); err == nil {
			err = writePrivateJSON(filepath.Join(root, "pending.json"), plan)
		}
		if err != nil {
			http.Error(w, "could not save import state", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(state)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		restart()
	}
	return importData, status
}

// ProcessPending runs in the parent, with no API workers alive. A failed restore
// never changes active.json; the supervisor restarts the original center.
func ProcessPending(ctx context.Context, root string) error {
	var plan importPlan
	if err := readJSON(filepath.Join(root, "pending.json"), &plan); err != nil {
		return errors.New("cannot read pending import")
	}
	status := ImportStatus{JobID: plan.JobID, State: "failed", CenterID: plan.CenterID, Message: "Import failed. The previous database remains available."}
	defer func() {
		_ = os.Remove(filepath.Join(root, "pending.json"))
		_ = writePrivateJSON(filepath.Join(root, "status.json"), status)
	}()
	if len(plan.JobID) != 32 || strings.Trim(plan.JobID, "0123456789abcdef") != "" {
		return errors.New("invalid import identity")
	}
	data, manifest, err := Read(filepath.Join(root, "incoming"), plan.Snapshot)
	if err != nil {
		return errors.New("cannot decrypt staged backup")
	}
	if manifest.CenterID != plan.CenterID {
		return errors.New("source center changed")
	}
	databaseURL := os.Getenv("DATABASE_URL")
	// A previous import may already have changed the active database.
	var old activation
	if e := readJSON(filepath.Join(root, "active.json"), &old); e == nil {
		databaseURL = old.DatabaseURL
	} else if !os.IsNotExist(e) {
		return errors.New("cannot read existing activation")
	}
	if _, _, err = DatabaseEnvironment(databaseURL); err != nil {
		return err
	}
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		return errors.New("cannot connect to current database")
	}
	defer conn.Close(ctx)
	var running int
	if err = conn.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE status='running'`).Scan(&running); err != nil {
		return errors.New("cannot verify current center has no running tasks")
	}
	if running != 0 {
		status.Message = "Wait for running tasks to finish before importing."
		return errors.New("current center has running tasks")
	}
	var otherConnections bool
	if err = conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND backend_type='client backend')`).Scan(&otherConnections); err != nil {
		return errors.New("cannot verify exclusive center access")
	}
	if otherConnections {
		status.Message = "Stop other center processes and database clients before importing."
		return errors.New("other database connections remain")
	}
	name := "multica_restore_" + plan.JobID
	if _, err = conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		status.Message = "The center database account needs permission to create a recovery database."
		return errors.New("could not create recovery database")
	}
	u, _ := url.Parse(databaseURL)
	u.Path = "/" + name
	u.RawPath = ""
	directory := filepath.Join(root, "restored", plan.JobID)
	if err = os.MkdirAll(filepath.Dir(directory), 0700); err != nil {
		return err
	}
	if err = Restore(ctx, data, directory, u.String(), plan.CenterID, os.Getenv("MULTICA_RECOVERY_PG_RESTORE")); err != nil {
		status.Message = "Restore failed. The previous database is still active; check PostgreSQL tools and storage."
		return err
	}
	keys := map[string]string{}
	if err = readJSON(filepath.Join(directory, "deployment-keys.json"), &keys); err != nil {
		return errors.New("invalid deployment keys")
	}
	a := activation{Version: 1, DatabaseURL: u.String(), UploadDir: filepath.Join(directory, "uploads"), CenterID: plan.CenterID, Keys: keys}
	if err = os.MkdirAll(a.UploadDir, 0700); err != nil {
		return err
	}
	// Retain an explicit rollback pointer, including before the first import.
	if old.Version == 0 {
		old = activation{Version: 1, DatabaseURL: databaseURL, UploadDir: os.Getenv("LOCAL_UPLOAD_DIR"), CenterID: os.Getenv("MULTICA_RECOVERY_CENTER_ID"), Keys: map[string]string{}}
		if old.UploadDir == "" {
			old.UploadDir = "./data/uploads"
		}
		old.UploadDir, _ = filepath.Abs(old.UploadDir)
		for _, key := range DeploymentKeys {
			old.Keys[key] = os.Getenv(key)
		}
	}
	if err = writePrivateJSON(filepath.Join(root, "previous.json"), old); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err = writePrivateJSON(filepath.Join(root, "active.json"), a); err != nil {
		return err
	}
	status.State = "activating"
	status.Message = ""
	return nil
}
