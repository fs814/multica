package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Desktop owns this preference. Apply it only to the saved local profile;
// task-scoped credentials and unrelated terminal profiles remain isolated.
func applyCenterSettings(cfg CLIConfig, profile string) (CLIConfig, error) {
	if os.Getenv("MULTICA_LAUNCHED_BY") == "desktop" || os.Getenv(TaskConfigRootEnv) != "" || !strings.HasPrefix(profile, "desktop-") {
		return cfg, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return cfg, err
	}
	raw, err := os.ReadFile(filepath.Join(home, ".multica", "center.json"))
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	var saved struct {
		Version int    `json:"version"`
		URL     string `json:"url"`
		Profile string `json:"profile"`
	}
	if err := json.Unmarshal(raw, &saved); err != nil {
		return cfg, fmt.Errorf("invalid Desktop center settings")
	}
	if saved.Profile != profile {
		return cfg, nil
	}
	u, err := url.Parse(saved.URL)
	if err != nil || saved.Version != 1 || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return cfg, fmt.Errorf("invalid Desktop center address")
	}
	target := strings.TrimRight(saved.URL, "/")
	if strings.TrimRight(cfg.ServerURL, "/") != target {
		// Never forward another center's credentials or workspace identity.
		cfg.Token = ""
		cfg.WorkspaceID = ""
	}
	cfg.ServerURL = target
	cfg.AppURL = target
	return cfg, nil
}
