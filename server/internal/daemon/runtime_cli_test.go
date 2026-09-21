package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Test helper process
//
// The registry refuses to launch anything but an absolute, hash-pinned
// executable, so the tests need a real binary to point at. os.Args[0] — this
// test binary — is exactly that: a genuine executable on an absolute path, no
// shell involved, identical on Windows and Unix.
//
// The helper is selected by an argv sentinel rather than an environment
// variable on purpose: the whole point of the environment tests is that the
// child inherits NOTHING, so a helper that needed an env var to activate could
// not be used to prove it.
// ---------------------------------------------------------------------------

const (
	cliHelperSentinel = "--multica-cli-helper"
	cliHelperEnvVar   = "MULTICA_CLI_TEST_DAEMON_SECRET"
)

func cliHelperArgs(mode string, extra ...string) []string {
	args := append([]string{"-test.run=TestCLIHelperProcess", "--", cliHelperSentinel, mode}, extra...)
	return args
}

// TestCLIHelperProcess is the child side. It is a no-op in a normal test run.
func TestCLIHelperProcess(t *testing.T) {
	mode, payload := parseCLIHelperArgs(os.Args)
	if mode == "" {
		return
	}
	switch mode {
	case "env":
		// Print every inherited variable, one per line, so the parent can
		// assert on absence rather than on a curated subset.
		for _, kv := range os.Environ() {
			fmt.Println(kv)
		}
	case "args":
		fmt.Println(strings.Join(payload, "|"))
	case "cwd":
		wd, _ := os.Getwd()
		fmt.Println(wd)
	case "exit":
		os.Exit(3)
	case "sleep":
		time.Sleep(60 * time.Second)
	case "flood":
		for i := 0; i < 20000; i++ {
			fmt.Println("0123456789012345678901234567890123456789")
		}
	default:
		fmt.Println("unknown-helper-mode:", mode)
	}
	os.Exit(0)
}

func parseCLIHelperArgs(args []string) (string, []string) {
	for i, arg := range args {
		if arg == cliHelperSentinel && i+1 < len(args) {
			return args[i+1], args[i+2:]
		}
	}
	return "", nil
}

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// helperBinaryPath returns the absolute path of this test binary.
func helperBinaryPath(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	abs, err := filepath.Abs(exe)
	if err != nil {
		t.Fatalf("absolutize test executable: %v", err)
	}
	return abs
}

func sha256File(t *testing.T, path string) string {
	t.Helper()
	sum, err := fileSHA256(path)
	if err != nil {
		t.Fatalf("hash %s: %v", path, err)
	}
	return sum
}

// helperEntry builds a registry entry that runs this test binary in `mode`.
func helperEntry(t *testing.T, mode string, params ...cliParam) *cliEntry {
	t.Helper()
	path := helperBinaryPath(t)
	return &cliEntry{
		Key:   "helper",
		Label: "test helper",
		Executable: cliExecutable{
			Kind:   "path",
			Path:   path,
			SHA256: sha256File(t, path),
		},
		ArgTemplate: cliHelperArgs(mode),
		Params:      params,
	}
}

func mustRun(t *testing.T, entry *cliEntry, params map[string]string) cliRunOutcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	return executeCLIEntry(ctx, entry, params)
}

// ---------------------------------------------------------------------------
// Registry loading
// ---------------------------------------------------------------------------

func TestCLIRegistryMissingFileIsAnEmptyRegistry(t *testing.T) {
	reg, err := loadCLIRegistryAt(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err != nil {
		t.Fatalf("a missing registry must not be an error, got %v", err)
	}
	if len(reg.CLIs) != 0 {
		t.Fatalf("expected no entries, got %d", len(reg.CLIs))
	}
}

func TestCLIRegistryRejectsMalformedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clis.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write registry: %v", err)
	}
	if _, err := loadCLIRegistryAt(path); err == nil {
		t.Fatal("expected a parse error for malformed registry JSON")
	}
}

// ---------------------------------------------------------------------------
// Executable pinning
// ---------------------------------------------------------------------------

// A path alone is not a whitelist: anything that can write to the pinned path
// would inherit the grant. The hash pin is what closes that.
func TestCLIExecutableRejectsHashMismatch(t *testing.T) {
	entry := helperEntry(t, "args")
	entry.Executable.SHA256 = strings.Repeat("0", 64)

	_, err := resolveCLIExecutable(entry)
	if err == nil {
		t.Fatal("expected a hash-mismatch rejection")
	}
	if !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("expected a hash mismatch, got %v", err)
	}
}

func TestCLIExecutableRequiresAHash(t *testing.T) {
	entry := helperEntry(t, "args")
	entry.Executable.SHA256 = ""

	if _, err := resolveCLIExecutable(entry); err == nil {
		t.Fatal("an entry without a content hash must not be runnable")
	}
}

func TestCLIExecutableRequiresAnAbsolutePath(t *testing.T) {
	entry := helperEntry(t, "args")
	entry.Executable.Path = "helper-binary"

	if _, err := resolveCLIExecutable(entry); err == nil {
		t.Fatal("a bare name would be a PATH lookup, which must be refused")
	}
}

func TestCLIExecutableReportsAMissingFileAsUnavailable(t *testing.T) {
	entry := helperEntry(t, "args")
	entry.Executable.Path = filepath.Join(t.TempDir(), "nope.exe")

	_, err := resolveCLIExecutable(entry)
	if err == nil {
		t.Fatal("expected a not-found rejection")
	}
	var missing *cliExecutableMissingError
	if !asMissing(err, &missing) {
		t.Fatalf("expected a cliExecutableMissingError, got %T", err)
	}
	if missing.reason != "executable not found" {
		t.Fatalf("unexpected reason %q", missing.reason)
	}
}

func asMissing(err error, target **cliExecutableMissingError) bool {
	m, ok := err.(*cliExecutableMissingError)
	if ok {
		*target = m
	}
	return ok
}

// A broken entry stays visible in the panel with a reason rather than
// disappearing from the list.
func TestCLIEntrySummariesMarkUnavailableEntries(t *testing.T) {
	ok := helperEntry(t, "args")
	ok.Key = "helper-ok"

	broken := helperEntry(t, "args")
	broken.Key = "helper-broken"
	broken.Executable.Path = filepath.Join(t.TempDir(), "absent.exe")

	summaries := cliEntrySummaries(&cliRegistry{Version: 1, CLIs: []cliEntry{*ok, *broken}})
	if len(summaries) != 2 {
		t.Fatalf("expected 2 summaries, got %d", len(summaries))
	}
	if summaries[0]["available"] != true {
		t.Fatalf("expected the healthy entry to be available: %+v", summaries[0])
	}
	if summaries[1]["available"] != false {
		t.Fatalf("expected the broken entry to be unavailable: %+v", summaries[1])
	}
	if summaries[1]["unavailable_reason"] != "executable not found" {
		t.Fatalf("unexpected reason %v", summaries[1]["unavailable_reason"])
	}
}

// The panel must never receive machine-secret material.
func TestCLIEntrySummariesLeakNoSecrets(t *testing.T) {
	entry := helperEntry(t, "args")
	entry.Env = map[string]string{"ZHIHU_ACCESS_SECRET": "super-secret-value"}
	entry.Executable.SHA256 = sha256File(t, helperBinaryPath(t))

	raw, err := json.Marshal(cliEntrySummaries(&cliRegistry{Version: 1, CLIs: []cliEntry{*entry}}))
	if err != nil {
		t.Fatalf("marshal summaries: %v", err)
	}
	blob := string(raw)
	for _, forbidden := range []string{"super-secret-value", "ZHIHU_ACCESS_SECRET", entry.Executable.SHA256, entry.Executable.Path} {
		if strings.Contains(blob, forbidden) {
			t.Fatalf("summary leaked %q: %s", forbidden, blob)
		}
	}
}

// ---------------------------------------------------------------------------
// Parameter validation
// ---------------------------------------------------------------------------

func TestCLIParamsEnumRejectsValuesOutsideTheList(t *testing.T) {
	entry := helperEntry(t, "args", cliParam{
		Name: "command", Type: "enum", Required: true, Values: []string{"status", "hot"},
	})
	err := validateCLIParams(entry, map[string]string{"command": "rm -rf /"})
	if err == nil {
		t.Fatal("expected an enum rejection")
	}
}

func TestCLIParamsStringEnforcesLength(t *testing.T) {
	entry := helperEntry(t, "args", cliParam{Name: "query", Type: "string", MaxLen: 5})
	if err := validateCLIParams(entry, map[string]string{"query": "12345"}); err != nil {
		t.Fatalf("a value at the limit must pass, got %v", err)
	}
	if err := validateCLIParams(entry, map[string]string{"query": "123456"}); err == nil {
		t.Fatal("expected a length rejection")
	}
}

func TestCLIParamsRejectsUndeclaredNames(t *testing.T) {
	entry := helperEntry(t, "args", cliParam{Name: "query", Type: "string", MaxLen: 10})
	if err := validateCLIParams(entry, map[string]string{"query": "ok", "extra": "x"}); err == nil {
		t.Fatal("expected an unknown-parameter rejection")
	}
}

func TestCLIParamsRequiresRequiredSlots(t *testing.T) {
	entry := helperEntry(t, "args", cliParam{Name: "query", Type: "string", MaxLen: 10, Required: true})
	if err := validateCLIParams(entry, map[string]string{}); err == nil {
		t.Fatal("expected a missing-required-parameter rejection")
	}
}

// ---------------------------------------------------------------------------
// Argv construction — the anti-injection boundary
// ---------------------------------------------------------------------------

// A placeholder embedded in literal text is string concatenation wearing an
// argv costume, and is refused outright.
func TestCLIArgvRejectsEmbeddedPlaceholders(t *testing.T) {
	entry := helperEntry(t, "args", cliParam{Name: "query", Type: "string", MaxLen: 100})
	entry.ArgTemplate = []string{"--search={query}"}

	if _, err := buildCLIArgv(entry, map[string]string{"query": "x"}); err == nil {
		t.Fatal("expected an embedded-placeholder rejection")
	}
}

func TestCLIArgvSubstitutesWholeArgumentSlots(t *testing.T) {
	entry := helperEntry(t, "args", cliParam{Name: "query", Type: "string", MaxLen: 100})
	entry.ArgTemplate = []string{"literal", "{query}"}

	argv, err := buildCLIArgv(entry, map[string]string{"query": "hello world"})
	if err != nil {
		t.Fatalf("build argv: %v", err)
	}
	want := []string{"literal", "hello world"}
	if len(argv) != len(want) || argv[0] != want[0] || argv[1] != want[1] {
		t.Fatalf("got %#v, want %#v", argv, want)
	}
}

// The headline security property: shell metacharacters in a parameter are
// characters, not syntax. The child receives them verbatim as one argv element.
func TestCLIShellMetacharactersArePassedVerbatim(t *testing.T) {
	for _, payload := range []string{
		"a; echo pwned",
		"a && echo pwned",
		"a | echo pwned",
		"$(echo pwned)",
		"`echo pwned`",
		"a\n echo pwned",
		"a\" & echo pwned",
	} {
		t.Run(payload, func(t *testing.T) {
			entry := helperEntry(t, "args", cliParam{Name: "query", Type: "string", MaxLen: 4096})
			entry.ArgTemplate = append(cliHelperArgs("args"), "{query}")

			outcome := mustRun(t, entry, map[string]string{"query": payload})
			if outcome.Status != "completed" {
				t.Fatalf("run failed: %s / %s", outcome.Status, outcome.Err)
			}
			if outcome.Output != payload+"\n" && strings.TrimRight(outcome.Output, "\r\n") != payload {
				t.Fatalf("payload was not passed verbatim: got %q want %q", outcome.Output, payload)
			}
			if strings.Contains(outcome.Output, "pwned\n") && !strings.Contains(payload, "echo pwned") {
				t.Fatalf("unexpected output %q", outcome.Output)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Environment hygiene — the leak this design exists to prevent
// ---------------------------------------------------------------------------

// The daemon process holds the credentials of every agent configured with a
// custom env. A panel-triggered CLI must not be able to read them.
//
// The assertion is "nothing from the daemon's environment", not "an empty
// block": Go's os/exec appends SYSTEMROOT on Windows regardless of what the
// caller passes (see buildCLIEnv). SYSTEMROOT is the OS directory path, so the
// one variable we cannot remove is also one that carries no credential — the
// test pins that down rather than tolerating it silently.
func TestCLIChildDoesNotInheritTheDaemonEnvironment(t *testing.T) {
	t.Setenv(cliHelperEnvVar, "daemon-credential-value")
	t.Setenv("PATH", "/should-not-leak")

	entry := helperEntry(t, "env")
	outcome := mustRun(t, entry, nil)
	if outcome.Status != "completed" {
		t.Fatalf("run failed: %s / %s", outcome.Status, outcome.Err)
	}

	inherited := map[string]string{}
	for _, line := range strings.Split(outcome.Output, "\n") {
		if key, value, ok := strings.Cut(strings.TrimRight(line, "\r"), "="); ok {
			inherited[key] = value
		}
	}
	if value, ok := inherited[cliHelperEnvVar]; ok {
		t.Fatalf("the daemon's own environment leaked into the CLI child: %s=%s", cliHelperEnvVar, value)
	}
	if _, ok := inherited["PATH"]; ok {
		t.Fatal("PATH leaked into the CLI child")
	}

	allowed := map[string]bool{}
	if runtime.GOOS == "windows" {
		// Injected by os/exec itself; value is the Windows directory.
		allowed["SYSTEMROOT"] = true
	}
	for key := range inherited {
		if !allowed[strings.ToUpper(key)] {
			t.Fatalf("unexpected variable in the CLI child environment: %s=%s", key, inherited[key])
		}
	}
}

func TestCLIChildReceivesOnlyDeclaredEnv(t *testing.T) {
	t.Setenv(cliHelperEnvVar, "daemon-credential-value")

	entry := helperEntry(t, "env")
	entry.Env = map[string]string{"DECLARED_ONE": "one", "DECLARED_TWO": "two"}

	outcome := mustRun(t, entry, nil)
	if outcome.Status != "completed" {
		t.Fatalf("run failed: %s / %s", outcome.Status, outcome.Err)
	}
	got := strings.ReplaceAll(outcome.Output, "\r\n", "\n")
	if !strings.Contains(got, "DECLARED_ONE=one\n") || !strings.Contains(got, "DECLARED_TWO=two\n") {
		t.Fatalf("declared env missing from child: %q", got)
	}
	if strings.Contains(got, "daemon-credential-value") {
		t.Fatal("the daemon environment leaked alongside the declared keys")
	}
}

func TestCLIEnvFileSuppliesValuesWithoutTouchingTheRegistry(t *testing.T) {
	t.Setenv(cliHelperEnvVar, "daemon-credential-value")

	envFile := filepath.Join(t.TempDir(), "zhihu.env")
	if err := os.WriteFile(envFile, []byte("# comment\nACCESS_SECRET=\"from-file\"\n\n"), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}

	entry := helperEntry(t, "env")
	entry.EnvFile = envFile

	outcome := mustRun(t, entry, nil)
	if outcome.Status != "completed" {
		t.Fatalf("run failed: %s / %s", outcome.Status, outcome.Err)
	}
	got := strings.ReplaceAll(outcome.Output, "\r\n", "\n")
	if !strings.Contains(got, "ACCESS_SECRET=from-file\n") {
		t.Fatalf("env_file value missing from child: %q", got)
	}
	if strings.Contains(got, "daemon-credential-value") {
		t.Fatal("the daemon environment leaked in through the env_file path")
	}
}

// ---------------------------------------------------------------------------
// Execution semantics
// ---------------------------------------------------------------------------

func TestCLIRunReportsRealOutputAndExitCode(t *testing.T) {
	entry := helperEntry(t, "args", cliParam{Name: "query", Type: "string", MaxLen: 100})
	entry.ArgTemplate = append(cliHelperArgs("args"), "{query}")

	outcome := mustRun(t, entry, map[string]string{"query": "zhihu"})
	if outcome.Status != "completed" {
		t.Fatalf("expected completed, got %s (%s)", outcome.Status, outcome.Err)
	}
	if strings.TrimRight(outcome.Output, "\r\n") != "zhihu" {
		t.Fatalf("unexpected output %q", outcome.Output)
	}
	if outcome.ExitCode == nil || *outcome.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %v", outcome.ExitCode)
	}
	// The audit record needs the command line that actually ran, because the
	// server never knew it.
	if len(outcome.ResolvedArgv) == 0 || !strings.HasSuffix(outcome.ResolvedArgv[0], filepath.Base(helperBinaryPath(t))) {
		t.Fatalf("resolved_argv should lead with the pinned executable: %#v", outcome.ResolvedArgv)
	}
}

func TestCLIRunSurfacesANonZeroExitCode(t *testing.T) {
	entry := helperEntry(t, "exit")
	outcome := mustRun(t, entry, nil)
	if outcome.Status != "completed" {
		t.Fatalf("a non-zero exit is a completed run, got %s (%s)", outcome.Status, outcome.Err)
	}
	if outcome.ExitCode == nil || *outcome.ExitCode != 3 {
		t.Fatalf("expected exit code 3, got %v", outcome.ExitCode)
	}
}

// The registry is the whitelist: an unregistered key is refused by the daemon,
// not merely by the server.
func TestCLIRunRefusesAnUnregisteredKey(t *testing.T) {
	entry := helperEntry(t, "args")
	reg := &cliRegistry{Version: 1, CLIs: []cliEntry{*entry}}
	if findCLIEntry(reg, "not-registered") != nil {
		t.Fatal("expected an unregistered key to resolve to no entry")
	}
}

func TestCLITimeoutTerminatesTheProcess(t *testing.T) {
	entry := helperEntry(t, "sleep")
	entry.TimeoutSec = 1

	started := time.Now()
	outcome := mustRun(t, entry, nil)
	elapsed := time.Since(started)

	if outcome.Status != "failed" {
		t.Fatalf("expected a timeout failure, got %s (%s)", outcome.Status, outcome.Err)
	}
	if !strings.Contains(outcome.Err, "timed out") {
		t.Fatalf("expected a timeout message, got %q", outcome.Err)
	}
	// The helper sleeps for 60s; a bounded run proves the tree was killed
	// rather than merely detached from.
	if elapsed > 30*time.Second {
		t.Fatalf("the run was not terminated promptly: %s", elapsed)
	}
}

func TestCLIOutputIsTruncatedAndFlagged(t *testing.T) {
	entry := helperEntry(t, "flood")
	entry.MaxOutBytes = 1024

	outcome := mustRun(t, entry, nil)
	if outcome.Status != "completed" {
		t.Fatalf("expected completed, got %s (%s)", outcome.Status, outcome.Err)
	}
	if !outcome.Truncated {
		t.Fatal("expected truncated=true")
	}
	if len(outcome.Output) != 1024 {
		t.Fatalf("expected the captured output to stop at the limit, got %d bytes", len(outcome.Output))
	}
	// The reported byte count must describe what the process produced, not
	// what we kept — otherwise "output_bytes" silently equals the cap.
	if outcome.OutputBytes <= int64(len(outcome.Output)) {
		t.Fatalf("expected output_bytes to exceed the cap, got %d", outcome.OutputBytes)
	}
}

func TestCLIWorkingDirectoryDefaultsToTheUserHome(t *testing.T) {
	entry := helperEntry(t, "cwd")
	outcome := mustRun(t, entry, nil)
	if outcome.Status != "completed" {
		t.Fatalf("run failed: %s / %s", outcome.Status, outcome.Err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	got := strings.TrimSpace(strings.TrimRight(outcome.Output, "\r\n"))
	if !strings.EqualFold(filepath.Clean(got), filepath.Clean(home)) {
		t.Fatalf("expected cwd %q, got %q", home, got)
	}
}

// An entry that fails validation still produces a reportable outcome — a
// refused run is a visible result, not a silent drop.
func TestCLIRunReportsValidationFailuresAsOutcomes(t *testing.T) {
	entry := helperEntry(t, "args", cliParam{Name: "command", Type: "enum", Required: true, Values: []string{"status"}})

	outcome := mustRun(t, entry, map[string]string{"command": "not-allowed"})
	if outcome.Status != "failed" {
		t.Fatalf("expected failed, got %s", outcome.Status)
	}
	if outcome.Err == "" {
		t.Fatal("expected an error message")
	}
}

// Verify the hash helper itself agrees with the standard library, so the pin
// is comparing like with like.
func TestFileSHA256MatchesTheStandardLibrary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blob")
	content := []byte("multica cli registry pin")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write blob: %v", err)
	}
	sum := sha256.Sum256(content)
	want := hex.EncodeToString(sum[:])

	got, err := fileSHA256(path)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}
