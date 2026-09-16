package execenv

import (
	"os"
	"path/filepath"
	"strings"
)

type sessionStoreDirectory struct {
	path string
	info os.DirEntry
}

// Old conversations and new project/conversation leaves share one reservation
// unit. Never pass a project container to recursive removal or the active guard.
func sessionStoreDirectories(parent string) ([]sessionStoreDirectory, error) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return nil, err
	}
	result := []sessionStoreDirectory{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(parent, entry.Name())
		if strings.HasPrefix(entry.Name(), "project_") {
			leaves, err := os.ReadDir(path)
			if err != nil {
				continue
			}
			for _, leaf := range leaves {
				if leaf.IsDir() {
					result = append(result, sessionStoreDirectory{filepath.Join(path, leaf.Name()), leaf})
				}
			}
		} else {
			result = append(result, sessionStoreDirectory{path, entry})
		}
	}
	return result, nil
}
func removeEmptyProjectSessionNamespaces(parent string) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "project_") {
			_ = os.Remove(filepath.Join(parent, entry.Name()))
		}
	}
}
