package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Use the already initialized app-server so the probe inherits the same task
// home, custom arguments and managed permissions as the subsequent turn.
// No provider request, user files, shell profile or sandbox override is needed.
func codexSandboxPreflight(ctx context.Context, goos, workDir string, request func(context.Context, string, any) (json.RawMessage, error)) error {
	if goos != "windows" {
		return nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	command := "cmd.exe"
	if root := os.Getenv("SystemRoot"); root != "" {
		command = filepath.Join(root, "System32", command)
	}
	raw, err := request(probeCtx, "command/exec", map[string]any{
		"command":   []string{command, "/d", "/c", "exit 0"},
		"cwd":       workDir,
		"timeoutMs": 10000,
	})
	const prefix = "codex sandbox preflight failed: "
	if ctx.Err() != nil {
		return fmt.Errorf("%s%w", prefix, ctx.Err())
	}
	if err != nil {
		// Persist only a known diagnostic, not arbitrary RPC text that may
		// contain provider credentials or task environment values.
		if strings.Contains(err.Error(), "1385") {
			return fmt.Errorf("%sWindows logon error 1385. Repair the Codex sandbox account logon rights, or explicitly approve windows.sandbox=unelevated for this agent; isolation was not changed", prefix)
		}
		return fmt.Errorf("%sWindows command probe could not run; check Codex sandbox setup and runtime logs; isolation was not changed", prefix)
	}
	var result struct {
		ExitCode *int `json:"exitCode"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || result.ExitCode == nil {
		return fmt.Errorf("%scommand/exec returned no valid exit code; check the installed Codex version", prefix)
	}
	if *result.ExitCode != 0 {
		return fmt.Errorf("%sWindows command probe exited with code %d; repair the execution environment before retrying", prefix, *result.ExitCode)
	}
	return nil
}
