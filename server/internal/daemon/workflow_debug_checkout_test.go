package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDebugCheckoutEvidenceObservesActualCommit(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "controlled")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("controlled git: %s %v", out, err)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "--quiet")
	git("-c", "user.name=Controlled", "-c", "user.email=controlled@example.invalid", "commit", "--allow-empty", "--no-gpg-sign", "-m", "controlled checkpoint")
	sha := git("rev-parse", "HEAD")
	evidence := string(debugCheckoutEvidence(context.Background(), root, "before_execution"))
	if !strings.Contains(evidence, sha) || strings.Contains(evidence, root) || !strings.Contains(evidence, `"directory":"controlled"`) {
		t.Fatalf("wrong checkout evidence: %s", evidence)
	}
	empty := string(debugCheckoutEvidence(context.Background(), t.TempDir(), "before_execution"))
	if !strings.Contains(empty, `"checkouts":[]`) {
		t.Fatal(empty)
	}
}
