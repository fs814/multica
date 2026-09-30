package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/centerrecovery"
	"github.com/multica-ai/multica/server/internal/cli"
)

func TestRecoveryConfigureUsesProfileAndStdin(t *testing.T) {
	t.Setenv(cli.TaskConfigRootEnv, t.TempDir())
	c := newRecoveryCommand()
	c.PersistentFlags().String("profile", "backup-test", "")
	token := strings.Repeat("a", 64)
	var out bytes.Buffer
	c.SetIn(strings.NewReader(token + "\n"))
	c.SetOut(&out)
	c.SetArgs([]string{"configure", "--source", "https://old.example"})
	if err := c.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), token) {
		t.Fatal("credential printed")
	}
	dir, err := cli.ProfileDir("backup-test")
	if err != nil {
		t.Fatal(err)
	}
	source, err := centerrecovery.LoadSource(filepath.Join(dir, "center-recovery"), "https://old.example")
	if err != nil || source.Token != token {
		t.Fatal("profile source was not saved", err)
	}
}
func TestRecoveryRestoreRequiresExplicitSelection(t *testing.T) {
	c := newRecoveryCommand()
	c.SetArgs([]string{"restore"})
	err := c.Execute()
	if err == nil || !strings.Contains(err.Error(), "--confirm-center") {
		t.Fatal("restore accepted implicit target", err)
	}
}
