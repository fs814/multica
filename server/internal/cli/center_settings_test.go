package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCenterSettingsStandaloneProfileAndCredentialBoundary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(TaskConfigRootEnv, "")
	t.Setenv("MULTICA_LAUNCHED_BY", "")
	if err := os.MkdirAll(filepath.Join(home, ".multica"), 0700); err != nil {
		t.Fatal(err)
	}
	save := func(url string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(home, ".multica", "center.json"), []byte(`{"version":1,"profile":"desktop-services","url":"`+url+`"}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := CLIConfig{ServerURL: "http://127.0.0.1:18080", Token: "test-only", WorkspaceID: "test-workspace", DeviceName: "node-a"}
	save(cfg.ServerURL)
	got, err := applyCenterSettings(cfg, "desktop-services")
	if err != nil || got.Token != cfg.Token {
		t.Fatalf("same center: %v", err)
	}
	save("http://[::1]:18080")
	got, err = applyCenterSettings(cfg, "desktop-services")
	if err != nil || got.ServerURL != "http://[::1]:18080" || got.Token != "" || got.WorkspaceID != "" || got.DeviceName != cfg.DeviceName {
		t.Fatalf("address switch did not isolate auth/preserve device")
	}
	got, err = applyCenterSettings(cfg, "unrelated")
	if err != nil || got.ServerURL != cfg.ServerURL || got.Token != cfg.Token {
		t.Fatal("unrelated profile changed")
	}
	t.Setenv(TaskConfigRootEnv, t.TempDir())
	got, err = applyCenterSettings(cfg, "desktop-services")
	if err != nil || got.ServerURL != cfg.ServerURL {
		t.Fatal("task profile changed")
	}
	t.Setenv(TaskConfigRootEnv, "")
	fresh, err := LoadCLIConfigForProfile("desktop-services")
	if err != nil || fresh.ServerURL != "http://[::1]:18080" || fresh.Token != "" {
		t.Fatal("standalone missing profile does not use saved settings")
	}
}
