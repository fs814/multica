package execenv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
)

// ProjectMemoryStorePath never imports unscoped host memory into a project.
func ProjectMemoryStorePath(profile, agentID, sourceHome string, task TaskContextForEnv) string {
	if task.ProjectMemory == nil {
		return HermesMemoryStorePath(profile, agentID, sourceHome)
	}
	root, err := cli.ProfileDir(profile)
	if err != nil {
		return ""
	}
	return filepath.Join(root, "project-native-memory", sanitizePathSegment(task.ProjectMemory.WorkspaceID),
		sanitizePathSegment(task.ProjectMemory.ProjectID), sanitizePathSegment(agentID), hermesMemoryProfileSegment(sourceHome))
}
func projectSessionSegment(task TaskContextForEnv) string {
	if task.ProjectMemory == nil {
		return ""
	}
	sum := sha256.Sum256([]byte(task.ProjectMemory.Namespace()))
	return "project_" + hex.EncodeToString(sum[:])
}
func writePrivateProjectMemory(root string, task TaskContextForEnv) error {
	if task.ProjectMemory == nil {
		return nil
	}
	dir := filepath.Join(root, "project-memory")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	for name, value := range map[string]any{"context.json": task.ProjectMemory, "snapshot.json": task.ProjectMemorySnapshot} {
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			return err
		}
	}
	return nil
}
func checkProjectMemoryProvider(provider string, task TaskContextForEnv) error {
	if task.ProjectMemory == nil {
		return nil
	}
	if provider == "codex" && codexMemoryEnabled() {
		return fmt.Errorf("Codex native memory is enabled but has no verified project adapter; disable MULTICA_CODEX_MEMORY")
	}
	return nil
}

// LockProjectNativeMemory serializes native whole-file writes across local daemon processes.
func LockProjectNativeMemory(ctx context.Context, store string) (func(), error) {
	if err := os.MkdirAll(store, 0700); err != nil {
		return nil, err
	}
	f, err := openLockFile(filepath.Join(store, ".multica-native-memory.lock"))
	if err != nil {
		return nil, err
	}
	for {
		ok, err := lockFileExclusiveNonBlocking(f)
		if err != nil {
			f.Close()
			return nil, err
		}
		if ok {
			return func() { unlockFile(f); f.Close() }, nil
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}
