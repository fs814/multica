//go:build windows

package agent

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	cursorShimHelperArg = "--multica-cursor-shim-helper"
	piShimHelperArg     = "--multica-pi-shim-helper"
)

// writeTestExecutable is the Windows counterpart to the //go:build unix
// implementation in exec_fixture_unix_test.go. Windows cannot execute the
// POSIX shell fixtures used throughout this package directly. Store the
// script beside an independent copy of the native test binary; TestMain
// recognizes that sibling script and delegates to sh with stdio intact.
//
// The helper is referenced by claude_test.go / codex_test.go /
// kimi_test.go, so the absence of a Windows impl made
// `go test ./pkg/agent` fail to build on Windows. Lifted from #1719
// (Codex) with attribution.
func writeTestExecutable(tb testing.TB, path string, content []byte) {
	tb.Helper()
	if !strings.HasPrefix(string(content), "#!") {
		if err := os.WriteFile(path, content, 0o755); err != nil {
			tb.Fatalf("write native test executable %s: %v", path, err)
		}
		return
	}
	scriptPath := path + ".sh"
	if err := os.WriteFile(scriptPath, content, 0o755); err != nil {
		tb.Fatalf("write test script %s: %v", scriptPath, err)
	}

	testExecutable, err := os.Executable()
	if err != nil {
		tb.Fatalf("resolve test executable: %v", err)
	}
	fixtureExecutable := path + ".exe"
	if err := os.Remove(fixtureExecutable); err != nil && !os.IsNotExist(err) {
		tb.Fatalf("replace test fixture %s: %v", fixtureExecutable, err)
	}
	src, err := os.Open(testExecutable)
	if err != nil {
		tb.Fatalf("open test executable: %v", err)
	}
	defer src.Close()
	dst, err := os.Create(fixtureExecutable)
	if err != nil {
		tb.Fatalf("create test fixture %s: %v", fixtureExecutable, err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		tb.Fatalf("copy test fixture %s: %v", fixtureExecutable, err)
	}
	if err := dst.Close(); err != nil {
		tb.Fatalf("close test fixture %s: %v", fixtureExecutable, err)
	}
	if err := src.Close(); err != nil {
		tb.Fatalf("close test executable: %v", err)
	}
}

func runWindowsTestExecutableFixture() (int, bool) {
	executable, err := os.Executable()
	if err != nil {
		return 0, false
	}
	scriptPath := strings.TrimSuffix(executable, filepath.Ext(executable)) + ".sh"
	if _, err := os.Stat(scriptPath); err != nil {
		return 0, false
	}

	args := append([]string{filepath.ToSlash(scriptPath)}, os.Args[1:]...)
	cmd := exec.Command("sh", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if ok := errors.As(err, &exitErr); ok {
			return exitErr.ExitCode(), true
		}
		fmt.Fprintf(os.Stderr, "run Windows shell fixture: %v\n", err)
		return 127, true
	}
	return 0, true
}

func runWindowsPowerShellShimHelper() (int, bool) {
	type helperConfig struct {
		enabledEnv string
		argvFile   string
		stdinFile  string
		output     []string
	}
	var cfg helperConfig
	executable, _ := os.Executable()
	executableDir := filepath.Dir(executable)
	switch {
	case strings.EqualFold(filepath.Base(executable), "cursor-shim-helper.exe"):
		cfg = helperConfig{
			enabledEnv: cursorShimHelperArg,
			argvFile:   filepath.Join(executableDir, "argv.txt"),
			stdinFile:  filepath.Join(executableDir, "stdin.txt"),
			output: []string{
				`{"type":"result","subtype":"success","is_error":false,"result":"ok"}`,
			},
		}
	case strings.EqualFold(filepath.Base(executable), "pi-shim-helper.exe"):
		cfg = helperConfig{
			enabledEnv: piShimHelperArg,
			argvFile:   filepath.Join(executableDir, "argv.txt"),
			stdinFile:  filepath.Join(executableDir, "stdin.txt"),
			output: []string{
				`{"type":"agent_start"}`,
				`{"type":"turn_end","message":{"role":"assistant","model":"test","usage":{"input":1,"output":1,"cacheRead":0,"cacheWrite":0,"totalTokens":2}}}`,
			},
		}
	case len(os.Args) >= 4 && os.Args[1] == cursorShimHelperArg:
		cfg = helperConfig{
			enabledEnv: cursorShimHelperArg,
			argvFile:   os.Args[2],
			stdinFile:  os.Args[3],
			output: []string{
				`{"type":"result","subtype":"success","is_error":false,"result":"ok"}`,
			},
		}
	case len(os.Args) >= 4 && os.Args[1] == piShimHelperArg:
		cfg = helperConfig{
			enabledEnv: piShimHelperArg,
			argvFile:   os.Args[2],
			stdinFile:  os.Args[3],
			output: []string{
				`{"type":"agent_start"}`,
				`{"type":"turn_end","message":{"role":"assistant","model":"test","usage":{"input":1,"output":1,"cacheRead":0,"cacheWrite":0,"totalTokens":2}}}`,
			},
		}
	case os.Getenv(shimHelperEnv) == "1":
		cfg = helperConfig{
			enabledEnv: shimHelperEnv,
			argvFile:   os.Getenv(shimHelperArgvFile),
			stdinFile:  os.Getenv(shimHelperInFile),
			output: []string{
				`{"type":"result","subtype":"success","is_error":false,"result":"ok"}`,
			},
		}
	case os.Getenv(piShimHelperEnv) == "1":
		cfg = helperConfig{
			enabledEnv: piShimHelperEnv,
			argvFile:   os.Getenv(piShimHelperArgvFile),
			stdinFile:  os.Getenv(piShimHelperInFile),
			output: []string{
				`{"type":"agent_start"}`,
				`{"type":"turn_end","message":{"role":"assistant","model":"test","usage":{"input":1,"output":1,"cacheRead":0,"cacheWrite":0,"totalTokens":2}}}`,
			},
		}
	default:
		return 0, false
	}

	var forwarded []string
	for i, arg := range os.Args {
		if arg == "--" {
			forwarded = os.Args[i+1:]
			break
		}
	}
	if err := os.WriteFile(cfg.argvFile, []byte(strings.Join(forwarded, "\n")), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "%s: write argv: %v\n", cfg.enabledEnv, err)
		return 1, true
	}
	stdin, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: read stdin: %v\n", cfg.enabledEnv, err)
		return 1, true
	}
	if err := os.WriteFile(cfg.stdinFile, stdin, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "%s: write stdin: %v\n", cfg.enabledEnv, err)
		return 1, true
	}
	for _, line := range cfg.output {
		fmt.Println(line)
	}
	return 0, true
}

func assertTestFileMode(tb testing.TB, path string, want os.FileMode) {
	tb.Helper()
	info, err := os.Stat(path)
	if err != nil {
		tb.Fatalf("stat %s: %v", path, err)
	}
	// Windows DACLs are not represented by os.FileMode: regular files report
	// 0666. Keep the assertion active by checking the requested owner bits.
	got := info.Mode().Perm()
	if ownerWant := want.Perm() & 0o700; got&ownerWant != ownerWant {
		tb.Fatalf("%s owner permissions = %#o, want at least %#o", path, got, ownerWant)
	}
}
