// Package projectmemory stores immutable, project-scoped knowledge snapshots.
// Publication belongs to the server's binding CAS, not to a task or its cwd.
package projectmemory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

const MaxBytes = 4 << 20

var ErrConflict = errors.New("project memory changed; read and merge before retrying")

type Binding struct {
	WorkspaceID     string `json:"workspace_id"`
	ProjectID       string `json:"project_id"`
	OwnerDaemonID   string `json:"owner_daemon_id"`
	Backend         string `json:"backend"`
	SourceRoot      string `json:"source_root,omitempty"`
	Revision        int64  `json:"binding_revision"`
	ContentRevision int64  `json:"content_revision"`
	Generation      string `json:"generation,omitempty"`
	Digest          string `json:"digest,omitempty"`
	State           string `json:"state"`
}

type Snapshot struct {
	SchemaVersion   int               `json:"schema_version"`
	WorkspaceID     string            `json:"workspace_id"`
	ProjectID       string            `json:"project_id"`
	BindingRevision int64             `json:"binding_revision"`
	ContentRevision int64             `json:"content_revision"`
	Files           map[string]string `json:"files"`
	Source          string            `json:"source"`
}

type Candidate struct {
	Generation      string `json:"generation"`
	Digest          string `json:"digest"`
	ContentRevision int64  `json:"content_revision"`
}

// Store is owned by a daemon, outside task/provider homes. A source binding
// always addresses the bound source root, never an execution worktree.
type Store struct {
	ManagedRoot string
	DaemonID    string
}

func validID(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && parsed != uuid.Nil && parsed.String() == id
}

func ValidateBinding(b Binding) error {
	if !validID(b.WorkspaceID) || !validID(b.ProjectID) || !validID(b.OwnerDaemonID) || b.Revision < 1 {
		return errors.New("invalid project memory identity")
	}
	if b.Backend != "source" && b.Backend != "managed" {
		return errors.New("unknown memory backend")
	}
	if b.Backend == "source" && !(filepath.IsAbs(b.SourceRoot) || (len(b.SourceRoot) > 2 && b.SourceRoot[1] == ':' && (b.SourceRoot[2] == '\\' || b.SourceRoot[2] == '/'))) {
		return errors.New("source memory requires an absolute binding root")
	}
	return nil
}

func ValidateName(name string) error {
	if name == "" || name == "." || !fs.ValidPath(name) || path.Clean(name) != name ||
		strings.ContainsAny(name, "\\:\x00") || strings.HasPrefix(name, "/") {
		return errors.New("memory path must be a relative slash-separated file name")
	}
	for _, part := range strings.Split(name, "/") {
		if strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return errors.New("ambiguous memory path")
		}
		base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" ||
			(len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '0' && base[3] <= '9') {
			return errors.New("reserved memory path")
		}
	}
	return nil
}

func rejectLinks(root string) error {
	abs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	for p := abs; ; p = filepath.Dir(p) {
		fi, e := os.Lstat(p)
		if e != nil && !os.IsNotExist(e) {
			return e
		}
		if e == nil && (fi.Mode()&os.ModeSymlink != 0 || unsafeReparse(fi)) {
			return errors.New("memory storage cannot traverse symbolic links or junctions")
		}
		parent := filepath.Dir(p)
		if parent == p {
			break
		}
	}
	return nil
}

func (s Store) namespace(b Binding) (string, error) {
	if err := ValidateBinding(b); err != nil {
		return "", err
	}
	if b.OwnerDaemonID != s.DaemonID {
		return "", errors.New("memory owner unavailable on this daemon")
	}
	base := s.ManagedRoot
	if b.Backend == "source" {
		base = filepath.Join(b.SourceRoot, "datas", "memory", "projects")
	}
	if base == "" || !filepath.IsAbs(base) {
		return "", errors.New("persistent memory root unavailable")
	}
	ns := filepath.Join(base, b.WorkspaceID, b.ProjectID)
	if err := rejectLinks(ns); err != nil {
		return "", err
	}
	return ns, nil
}

// Read verifies every byte of the selected immutable snapshot. External edits
// are a conflict; they are never silently overwritten or treated as reviewed.
func (s Store) Read(b Binding) (Snapshot, error) {
	ns, err := s.namespace(b)
	if err != nil {
		return Snapshot{}, err
	}
	result, err := s.readSnapshot(b, ns)
	if b.Backend != "source" || !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	return s.publishSelected(b, ns)
}

func (s Store) readSnapshot(b Binding, ns string) (Snapshot, error) {
	var result Snapshot
	if b.Generation == "" {
		return result, errors.New("project memory is not initialized")
	}
	if !validID(b.Generation) {
		return result, errors.New("invalid memory generation")
	}
	target := filepath.Join(ns, "versions", b.Generation, "snapshot.json")
	if err := rejectLinks(target); err != nil {
		return result, err
	}
	root, err := os.OpenRoot(ns)
	if err != nil {
		return result, err
	}
	defer root.Close()
	f, err := root.Open(filepath.Join("versions", b.Generation, "snapshot.json"))
	if err != nil {
		return result, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return result, err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxBytes*2 {
		return result, errors.New("invalid memory snapshot")
	}
	raw, err := io.ReadAll(io.LimitReader(f, MaxBytes*2+1))
	if len(raw) > MaxBytes*2 {
		return result, errors.New("memory snapshot exceeds limit")
	}
	if err != nil {
		return result, err
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != b.Digest {
		return result, ErrConflict
	}
	if err = json.Unmarshal(raw, &result); err != nil {
		return result, err
	}
	if result.SchemaVersion != 1 || result.WorkspaceID != b.WorkspaceID || result.ProjectID != b.ProjectID ||
		result.BindingRevision != b.Revision || result.ContentRevision != b.ContentRevision {
		return Snapshot{}, errors.New("memory manifest identity or revision mismatch")
	}
	if err := validateFiles(result.Files); err != nil {
		return Snapshot{}, err
	}
	return result, nil
}

func validateFiles(files map[string]string) error {
	total := 0
	if len(files) > 1024 {
		return errors.New("too many memory files")
	}
	folded := map[string]bool{}
	for name, body := range files {
		if err := ValidateName(name); err != nil {
			return err
		}
		key := strings.ToLower(name)
		if folded[key] {
			return errors.New("case-colliding memory paths")
		}
		folded[key] = true
		total += len(body)
	}
	for name := range folded {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if folded[parent] {
				return errors.New("memory file conflicts with a parent directory")
			}
		}
	}
	if total > MaxBytes {
		return errors.New("project memory exceeds size limit")
	}
	return nil
}

// Stage syncs a complete candidate with exclusive creation. POSIX also syncs
// directory entries; Windows guarantees process-crash persistence only (see
// syncSnapshotDirectories). Power-loss recovery still requires a consistent backup. It does
// not select that candidate: only the authenticated server may publish its
// generation after rechecking binding revision, expected content revision and
// the originating task's current context epoch. Orphan candidates are harmless.
func (s Store) Stage(b Binding, files map[string]string, source string) (Candidate, error) {
	var c Candidate
	ns, err := s.stagingNamespace(b)
	if err != nil {
		return c, err
	}
	if err := validateFiles(files); err != nil {
		return c, err
	}
	if strings.TrimSpace(source) == "" || len(source) > 2048 {
		return c, errors.New("memory write requires a bounded source reference")
	}
	snap := Snapshot{SchemaVersion: 1, WorkspaceID: b.WorkspaceID, ProjectID: b.ProjectID,
		BindingRevision: b.Revision, ContentRevision: b.ContentRevision + 1, Files: files, Source: source}
	raw, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return c, err
	}
	if len(raw) > MaxBytes*2 {
		return c, errors.New("encoded snapshot exceeds limit")
	}
	generation := uuid.NewString()
	dir := filepath.Join(ns, "versions", generation)
	ancestor := dir
	for {
		if _, e := os.Lstat(ancestor); e == nil {
			break
		} else if !os.IsNotExist(e) {
			return c, e
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return c, errors.New("no existing storage ancestor")
		}
		ancestor = parent
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return c, err
	}
	if err = rejectLinks(dir); err != nil {
		return c, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return c, err
	}
	defer root.Close()
	f, err := root.OpenFile("snapshot.json", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return c, err
	}
	_, writeErr := f.Write(raw)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil {
		return c, writeErr
	}
	if closeErr != nil {
		return c, closeErr
	}
	if err = syncSnapshotDirectories(dir, ancestor); err != nil {
		return c, err
	}
	// Nothing points at this generation until the server accepts this digest.
	sum := sha256.Sum256(raw)
	return Candidate{Generation: generation, Digest: hex.EncodeToString(sum[:]), ContentRevision: snap.ContentRevision}, nil
}

// ReadFile uses a validated logical key. Files are content inside the immutable
// snapshot, so untrusted entry names never become filesystem write targets.
func ReadFile(snapshot Snapshot, name string) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", err
	}
	body, ok := snapshot.Files[name]
	if !ok {
		return "", fmt.Errorf("memory file %q not found", name)
	}
	return body, nil
}
