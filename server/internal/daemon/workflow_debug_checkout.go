package daemon

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

var debugCommitSHA = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// Observe real checkouts, never turn a symbolic ref into a promise about a SHA.
// Only the prepared work directory and its immediate children are inspected;
// no repository, credential file, or user directory is traversed recursively.
func debugCheckoutEvidence(ctx context.Context, workdir, phase string) []byte {
	type checkout struct {
		Directory string `json:"directory"`
		CommitSHA string `json:"commit_sha"`
	}
	evidence := struct {
		Phase     string     `json:"phase"`
		Checkouts []checkout `json:"checkouts"`
	}{phase, []checkout{}}
	paths := []string{"."}
	if entries, err := os.ReadDir(workdir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
				paths = append(paths, entry.Name())
			}
		}
	}
	for _, relative := range paths {
		directory := filepath.Join(workdir, relative)
		if _, err := os.Stat(filepath.Join(directory, ".git")); err != nil {
			continue
		}
		probe, cancel := context.WithTimeout(ctx, 2*time.Second)
		output, err := exec.CommandContext(probe, "git", "-C", directory, "rev-parse", "--verify", "HEAD").Output()
		cancel()
		sha := strings.TrimSpace(string(output))
		if err == nil && debugCommitSHA.MatchString(sha) {
			evidence.Checkouts = append(evidence.Checkouts, checkout{relative, sha})
		}
	}
	raw, _ := json.Marshal(evidence)
	return raw
}
func (d *Daemon) reportDebugCheckoutEvidence(ctx context.Context, task Task, workdir, phase string, seq *atomic.Int32) error {
	if task.WorkflowExecutionMode != "draft_test" {
		return nil
	}
	evidence := debugCheckoutEvidence(ctx, workdir, phase)
	return d.client.ReportTaskMessages(ctx, task.ID, []TaskMessageData{{Seq: int(seq.Add(1)), Type: "text", Content: "Draft trial checkout observation: " + string(evidence), CreatedAt: time.Now()}})
}
