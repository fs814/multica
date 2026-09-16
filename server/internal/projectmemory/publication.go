package projectmemory

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// Windows cannot rename a directory while another reader exports its files.
var sourcePublicationMu sync.Mutex

// Unselected source candidates belong to ignored intermediate storage.
func (s Store) stagingNamespace(b Binding) (string, error) {
	ns, err := s.namespace(b)
	if err != nil || b.Backend != "source" {
		return ns, err
	}
	ns = filepath.Join(b.SourceRoot, ".multica", "project-memory-candidates", b.WorkspaceID, b.ProjectID)
	if err := rejectLinks(ns); err != nil {
		return "", err
	}
	return ns, nil
}

// The server-selected binding is the publication authority. A fresh owner can
// finish promotion after a crash or lost callback, without publishing an orphan.
func (s Store) publishSelected(b Binding, finalNS string) (Snapshot, error) {
	sourcePublicationMu.Lock()
	defer sourcePublicationMu.Unlock()
	if selected, err := s.readSnapshot(b, finalNS); err == nil {
		return selected, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, err
	}

	stagedNS, err := s.stagingNamespace(b)
	if err != nil {
		return Snapshot{}, err
	}
	snap, err := s.readSnapshot(b, stagedNS)
	if err != nil {
		// Another reader may have completed the atomic rename.
		if errors.Is(err, os.ErrNotExist) {
			return s.readSnapshot(b, finalNS)
		}
		return Snapshot{}, err
	}
	staged := filepath.Join(stagedNS, "versions", b.Generation)
	final := filepath.Join(finalNS, "versions", b.Generation)
	// Export logical entries next to the authoritative snapshot for Git review.
	// These are derived copies; edits must still go through the Memory API.
	if err := exportSnapshotFiles(staged, snap.Files); err != nil {
		if selected, readErr := s.readSnapshot(b, finalNS); readErr == nil {
			return selected, nil
		}
		return Snapshot{}, err
	}
	if err := os.MkdirAll(filepath.Dir(final), 0700); err != nil {
		return Snapshot{}, err
	}
	if err := rejectLinks(final); err != nil {
		return Snapshot{}, err
	}
	if err := os.Rename(staged, final); err != nil {
		if selected, readErr := s.readSnapshot(b, finalNS); readErr == nil {
			return selected, nil
		}
		return Snapshot{}, err
	}
	if err := syncSnapshotDirectories(final, b.SourceRoot); err != nil {
		return Snapshot{}, err
	}
	if err := syncSnapshotDirectories(filepath.Dir(staged), b.SourceRoot); err != nil {
		return Snapshot{}, err
	}
	return s.readSnapshot(b, finalNS)
}

func exportSnapshotFiles(generation string, files map[string]string) error {
	dir := filepath.Join(generation, "files")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := rejectLinks(dir); err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	for name, body := range files {
		if err := ValidateName(name); err != nil {
			return err
		}
		if err := root.MkdirAll(filepath.Dir(name), 0700); err != nil {
			return err
		}
		if err := rejectLinks(filepath.Join(dir, name)); err != nil {
			return err
		}
		f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
		if err != nil {
			return err
		}
		_, writeErr := f.WriteString(body)
		if writeErr == nil {
			writeErr = f.Sync()
		}
		closeErr := f.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
		if err := syncSnapshotDirectories(filepath.Dir(filepath.Join(dir, name)), generation); err != nil {
			return err
		}
	}
	return nil
}
