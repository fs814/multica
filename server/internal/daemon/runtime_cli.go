package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/processtree"
)

// Machine-local CLI registry (TES-140).
//
// The registry file is the whitelist. It lives on the machine that runs the
// daemon, is owned by the runtime owner, and is re-read before every
// execution — the server never names an executable, so a compromised or
// merely buggy server cannot widen the set of runnable programs.
//
// Execution never passes through a shell. The registry declares an executable
// path (plus its content hash) and an argv template whose placeholders are
// whole arguments; parameter values are substituted into array slots and
// handed to exec.CommandContext as separate argv entries. There is no code
// path that builds a command line by string concatenation, so shell
// metacharacters in a parameter are just characters.

const (
	// cliRegistryRelativePath is the registry's location under the user's home
	// directory. The runtime owner edits this file directly; the daemon only
	// reads it.
	cliRegistryRelativePath = ".multica/clis.json"

	// cliRegistryMaxBytes bounds the registry read. It is configuration, not
	// data — a file larger than this is a mistake or an attack, not a registry.
	cliRegistryMaxBytes int64 = 1 << 20

	cliDefaultTimeoutSeconds       = 60
	cliMaxTimeoutSeconds           = 600
	cliDefaultMaxOutputBytes       = 64 << 10
	cliMaxOutputBytesCeiling       = 1 << 20
	cliEnvFileMaxBytes       int64 = 64 << 10
	cliMaxArgTemplateArgs          = 32
	cliMaxEnvKeys                  = 32
	cliKeyMaxLen                   = 32
	// cliWaitDelay bounds how long a cancelled process tree is given to die
	// after the context fires, before it is force-killed.
	cliWaitDelay = 3 * time.Second
)

var (
	cliRegistryKeyPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	cliRegistryParamPattern = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)
	cliEnvKeyPattern        = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	// cliTemplateToken matches an argument that is exactly one placeholder.
	// Embedded placeholders ("--q={query}") are rejected rather than
	// substituted: allowing them would reintroduce string building at the
	// argv level, which is the thing this design exists to avoid.
	cliTemplateToken = regexp.MustCompile(`^\{([a-z0-9_]{1,32})\}$`)
)

type cliRegistry struct {
	Version int        `json:"version"`
	CLIs    []cliEntry `json:"clis"`
}

type cliEntry struct {
	Key         string            `json:"key"`
	Label       string            `json:"label"`
	Description string            `json:"description"`
	Executable  cliExecutable     `json:"executable"`
	ArgTemplate []string          `json:"arg_template"`
	Params      []cliParam        `json:"params"`
	Cwd         string            `json:"cwd"`
	Env         map[string]string `json:"env"`
	EnvFile     string            `json:"env_file"`
	TimeoutSec  int               `json:"timeout_seconds"`
	MaxOutBytes int               `json:"max_output_bytes"`
	Shared      bool              `json:"shared"`
}

type cliExecutable struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
	// Interpreter is required on Windows for .cmd / .bat / .ps1 targets,
	// which CreateProcess cannot launch directly. When set, argv becomes
	// [interpreter, <script>, ...args] — still an array, still shell-free.
	Interpreter string `json:"interpreter"`
	// SHA256 pins the executable's content. A path alone is not a whitelist:
	// anything that can write to that path (a dropped file, a hijacked
	// installer, a PATH-style swap) would otherwise inherit the grant.
	SHA256 string `json:"sha256"`
}

type cliParam struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"` // "enum" | "string"
	Required bool     `json:"required"`
	Values   []string `json:"values"`
	MaxLen   int      `json:"max_len"`
	// Pattern is an optional RE2 anchored by the daemon; the panel does not
	// surface it, it only produces inputs that satisfy or fail it.
	Pattern string `json:"pattern"`
}

// cliExecutableMissingError marks an entry that is declared but not runnable
// on this machine right now. It is reported as `available: false` with a
// reason rather than as an error, so the panel can show a broken entry
// instead of hiding it.
type cliExecutableMissingError struct{ reason string }

func (e *cliExecutableMissingError) Error() string { return e.reason }

func cliRegistryPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home directory: %w", err)
	}
	return filepath.Join(home, filepath.FromSlash(cliRegistryRelativePath)), nil
}

// loadCLIRegistry reads and validates the registry file. A missing file is not
// an error: it means "no CLIs registered on this machine", which the panel
// renders as an empty list with the path to create.
func loadCLIRegistry() (*cliRegistry, string, error) {
	path, err := cliRegistryPath()
	if err != nil {
		return nil, "", err
	}
	reg, err := loadCLIRegistryAt(path)
	return reg, path, err
}

// loadCLIRegistryAt is the path-taking half, split out so tests can point at a
// fixture instead of the real home directory.
func loadCLIRegistryAt(path string) (*cliRegistry, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return &cliRegistry{Version: 1}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open CLI registry: %w", err)
	}
	defer f.Close()

	raw, err := io.ReadAll(io.LimitReader(f, cliRegistryMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read CLI registry: %w", err)
	}
	if int64(len(raw)) > cliRegistryMaxBytes {
		return nil, fmt.Errorf("CLI registry exceeds %d bytes", cliRegistryMaxBytes)
	}

	var reg cliRegistry
	if err := json.Unmarshal(raw, &reg); err != nil {
		return nil, fmt.Errorf("parse CLI registry: %w", err)
	}
	return &reg, nil
}

// findCLIEntry returns the entry for key, or nil when it is not registered.
func findCLIEntry(reg *cliRegistry, key string) *cliEntry {
	for i := range reg.CLIs {
		if reg.CLIs[i].Key == key {
			return &reg.CLIs[i]
		}
	}
	return nil
}

// expandCLIPath expands the registry's portable path spellings — %VAR% (the
// form the panel documents, matching how Windows users write paths), $VAR /
// ${VAR}, and a leading ~. It deliberately does NOT fall back to a PATH
// lookup: an entry must name an absolute file, or it is rejected.
func expandCLIPath(path string) string {
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), string(os.PathSeparator)))
		}
	}
	path = os.Expand(path, os.Getenv)
	// os.Expand handles $VAR and ${VAR}; %VAR% is the Windows-native spelling
	// and is expanded separately because it shares no syntax with os.Expand.
	for {
		start := strings.Index(path, "%")
		if start < 0 {
			break
		}
		end := strings.Index(path[start+1:], "%")
		if end < 0 {
			break
		}
		name := path[start+1 : start+1+end]
		value, ok := os.LookupEnv(name)
		if !ok {
			break
		}
		path = path[:start] + value + path[start+end+2:]
	}
	return path
}

// resolveCLIExecutable turns an entry's executable declaration into an
// absolute, hash-verified path. Every rejection here is fail-closed: an entry
// whose pin cannot be checked does not run.
func resolveCLIExecutable(entry *cliEntry) (string, error) {
	if entry.Executable.Kind != "path" {
		return "", &cliExecutableMissingError{reason: fmt.Sprintf("unsupported executable kind %q", entry.Executable.Kind)}
	}
	path := expandCLIPath(strings.TrimSpace(entry.Executable.Path))
	if path == "" {
		return "", &cliExecutableMissingError{reason: "executable.path is empty"}
	}
	if !filepath.IsAbs(path) {
		return "", &cliExecutableMissingError{reason: "executable.path must be absolute"}
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", &cliExecutableMissingError{reason: "executable not found"}
	}
	if err != nil {
		return "", &cliExecutableMissingError{reason: "executable not readable"}
	}
	if info.IsDir() {
		return "", &cliExecutableMissingError{reason: "executable.path is a directory"}
	}

	want := strings.ToLower(strings.TrimSpace(entry.Executable.SHA256))
	if want == "" {
		return "", &cliExecutableMissingError{reason: "executable.sha256 is required"}
	}
	got, err := fileSHA256(path)
	if err != nil {
		return "", &cliExecutableMissingError{reason: "executable could not be hashed"}
	}
	if got != want {
		return "", &cliExecutableMissingError{reason: "executable hash mismatch"}
	}
	return path, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// validateCLIParams checks the caller's values against the entry's declared
// slots. This runs on the daemon, against the authoritative registry — the
// server's shape check is a courtesy that keeps obvious junk off the wire, not
// the boundary.
func validateCLIParams(entry *cliEntry, provided map[string]string) error {
	declared := make(map[string]cliParam, len(entry.Params))
	for _, p := range entry.Params {
		if !cliRegistryParamPattern.MatchString(p.Name) {
			return fmt.Errorf("registry declares an invalid param name %q", p.Name)
		}
		declared[p.Name] = p
	}

	names := make([]string, 0, len(provided))
	for name := range provided {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, ok := declared[name]; !ok {
			return fmt.Errorf("unknown parameter %q", name)
		}
	}

	for _, p := range entry.Params {
		value, ok := provided[p.Name]
		if !ok || value == "" {
			if p.Required {
				return fmt.Errorf("parameter %q is required", p.Name)
			}
			continue
		}
		switch p.Type {
		case "enum":
			allowed := false
			for _, candidate := range p.Values {
				if value == candidate {
					allowed = true
					break
				}
			}
			if !allowed {
				return fmt.Errorf("parameter %q is not one of the allowed values", p.Name)
			}
		case "string":
			limit := p.MaxLen
			if limit <= 0 {
				limit = 200
			}
			if len(value) > limit {
				return fmt.Errorf("parameter %q exceeds %d characters", p.Name, limit)
			}
			if p.Pattern != "" {
				re, err := regexp.Compile(p.Pattern)
				if err != nil {
					return fmt.Errorf("registry declares an invalid pattern for %q", p.Name)
				}
				if !re.MatchString(value) {
					return fmt.Errorf("parameter %q does not match its declared pattern", p.Name)
				}
			}
		default:
			return fmt.Errorf("registry declares an unsupported param type %q", p.Type)
		}
	}
	return nil
}

// buildCLIArgv expands the argv template. A template element is either a
// literal or exactly one `{param}` placeholder — a placeholder embedded inside
// a larger literal is rejected, because that is string concatenation wearing
// an argv costume.
func buildCLIArgv(entry *cliEntry, params map[string]string) ([]string, error) {
	if len(entry.ArgTemplate) > cliMaxArgTemplateArgs {
		return nil, fmt.Errorf("arg_template exceeds %d entries", cliMaxArgTemplateArgs)
	}
	argv := make([]string, 0, len(entry.ArgTemplate))
	for _, element := range entry.ArgTemplate {
		if match := cliTemplateToken.FindStringSubmatch(element); match != nil {
			value, ok := params[match[1]]
			if !ok || value == "" {
				return nil, fmt.Errorf("parameter %q is required by arg_template but was not provided", match[1])
			}
			argv = append(argv, value)
			continue
		}
		if strings.ContainsAny(element, "{}") {
			return nil, fmt.Errorf("arg_template entry %q mixes a placeholder with literal text", element)
		}
		argv = append(argv, element)
	}
	return argv, nil
}

// buildCLIEnv constructs the child environment.
//
// It starts EMPTY and adds only what the entry declares — never the daemon's
// own environment. That is the point: the daemon process carries the
// credentials of every agent configured with a custom env on this machine, and
// a CLI launched from the panel must not be able to read them. An empty
// environment block is a supported way to launch a process on both Windows and
// Unix (verified: cmd.exe and powershell.exe both run under one); if a
// particular CLI needs a variable, the registry entry declares it.
//
// One addition is outside our control and worth knowing about: Go's os/exec
// appends SYSTEMROOT on Windows unconditionally when the caller supplies an
// explicit Env (os/exec/exec.go, "As a special case on Windows, SYSTEMROOT is
// always added if missing"). Its value is the OS directory path, not a
// credential, and it cannot be suppressed from this side. The property this
// function guarantees is therefore precise: no variable FROM THE DAEMON'S
// ENVIRONMENT is inherited, and SYSTEMROOT aside the child environment is
// exactly what the entry declared.
func buildCLIEnv(entry *cliEntry) ([]string, error) {
	env := make([]string, 0, len(entry.Env))
	seen := make(map[string]bool, len(entry.Env))

	if strings.TrimSpace(entry.EnvFile) != "" {
		fileEnv, err := readCLIEnvFile(expandCLIPath(entry.EnvFile))
		if err != nil {
			return nil, err
		}
		for key, value := range fileEnv {
			if seen[key] {
				continue
			}
			seen[key] = true
			env = append(env, key+"="+value)
		}
	}

	keys := make([]string, 0, len(entry.Env))
	for key := range entry.Env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !cliEnvKeyPattern.MatchString(key) {
			return nil, fmt.Errorf("registry declares an invalid env key %q", key)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		env = append(env, key+"="+entry.Env[key])
	}
	if len(env) > cliMaxEnvKeys {
		return nil, fmt.Errorf("entry declares more than %d environment entries", cliMaxEnvKeys)
	}
	return env, nil
}

// readCLIEnvFile reads a KEY=VALUE file. This is how a secret reaches a CLI
// without being pasted into an issue, a chat, or the registry itself: the
// owner writes it to a file and names the file in the entry.
func readCLIEnvFile(path string) (map[string]string, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("env_file not found")
	}
	if err != nil {
		return nil, fmt.Errorf("open env_file: %w", err)
	}
	defer f.Close()

	raw, err := io.ReadAll(io.LimitReader(f, cliEnvFileMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read env_file: %w", err)
	}
	if int64(len(raw)) > cliEnvFileMaxBytes {
		return nil, fmt.Errorf("env_file exceeds %d bytes", cliEnvFileMaxBytes)
	}

	out := make(map[string]string)
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, value, ok := strings.Cut(trimmed, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		value = strings.TrimSpace(value)
		// Strip one layer of matching quotes so `KEY="a b"` means the same
		// thing here as it does in every .env file the owner has met.
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		out[key] = value
	}
	return out, nil
}

// cliRunOutcome is the full result of one execution.
type cliRunOutcome struct {
	Status       string
	Output       string
	Truncated    bool
	ExitCode     *int
	DurationMs   int64
	OutputBytes  int64
	ResolvedArgv []string
	Err          string
}

// executeCLIEntry validates and runs one registry entry. It never returns an
// error: every failure is folded into the outcome so the report path stays
// uniform (and so a failure to run is still an audited, visible result).
func executeCLIEntry(ctx context.Context, entry *cliEntry, params map[string]string) cliRunOutcome {
	if err := validateCLIParams(entry, params); err != nil {
		return cliRunOutcome{Status: "failed", Err: err.Error()}
	}
	argv, err := buildCLIArgv(entry, params)
	if err != nil {
		return cliRunOutcome{Status: "failed", Err: err.Error()}
	}
	execPath, err := resolveCLIExecutable(entry)
	if err != nil {
		return cliRunOutcome{Status: "failed", Err: err.Error()}
	}
	env, err := buildCLIEnv(entry)
	if err != nil {
		return cliRunOutcome{Status: "failed", Err: err.Error()}
	}

	timeout := entry.TimeoutSec
	if timeout <= 0 {
		timeout = cliDefaultTimeoutSeconds
	}
	if timeout > cliMaxTimeoutSeconds {
		timeout = cliMaxTimeoutSeconds
	}
	maxOutput := entry.MaxOutBytes
	if maxOutput <= 0 {
		maxOutput = cliDefaultMaxOutputBytes
	}
	if maxOutput > cliMaxOutputBytesCeiling {
		maxOutput = cliMaxOutputBytesCeiling
	}

	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	// The interpreter, when declared, is itself resolved through the same
	// absolute-path requirement — a bare "powershell" would be a PATH lookup,
	// which is exactly the hijack this design refuses.
	program := execPath
	args := argv
	resolvedArgv := append([]string{execPath}, argv...)
	if interp := strings.TrimSpace(entry.Executable.Interpreter); interp != "" {
		interpPath := expandCLIPath(interp)
		if !filepath.IsAbs(interpPath) {
			return cliRunOutcome{Status: "failed", Err: "executable.interpreter must be an absolute path"}
		}
		args = append(interpArgs(interpPath), append([]string{execPath}, argv...)...)
		program = interpPath
		resolvedArgv = append([]string{interpPath}, args...)
	}

	cmd := exec.Command(program, args...)
	cmd.Env = env
	if cwd := expandCLIPath(strings.TrimSpace(entry.Cwd)); cwd != "" {
		if !filepath.IsAbs(cwd) {
			return cliRunOutcome{Status: "failed", Err: "cwd must be an absolute path"}
		}
		cmd.Dir = cwd
	} else if home, err := os.UserHomeDir(); err == nil {
		cmd.Dir = home
	}
	// processtree owns SysProcAttr: it attaches the child to a Windows Job
	// Object (KILL_ON_JOB_CLOSE) and sets HideWindow, so a panel-triggered run
	// gets no console window and a cancelled run takes its whole tree with it.

	capture := &cappedBuffer{limit: maxOutput}
	cmd.Stdout = capture
	cmd.Stderr = capture

	started := time.Now()
	runErr := processtree.Run(runCtx, cmd, cliWaitDelay)
	durationMs := time.Since(started).Milliseconds()

	outcome := cliRunOutcome{
		Output:       capture.String(),
		Truncated:    capture.truncated,
		DurationMs:   durationMs,
		OutputBytes:  capture.total,
		ResolvedArgv: resolvedArgv,
	}
	if runCtx.Err() == context.DeadlineExceeded {
		outcome.Status = "failed"
		outcome.Err = fmt.Sprintf("timed out after %d seconds", timeout)
		return outcome
	}
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			code := exitErr.ExitCode()
			outcome.Status = "completed"
			outcome.ExitCode = &code
			return outcome
		}
		outcome.Status = "failed"
		outcome.Err = runErr.Error()
		return outcome
	}
	zero := 0
	outcome.Status = "completed"
	outcome.ExitCode = &zero
	return outcome
}

// interpArgs returns the flags an interpreter needs before the script path.
func interpArgs(interp string) []string {
	switch strings.ToLower(filepath.Base(interp)) {
	case "powershell.exe", "powershell", "pwsh.exe", "pwsh":
		return []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File"}
	case "cmd.exe", "cmd":
		return []string{"/c"}
	default:
		return nil
	}
}

// cappedBuffer accumulates at most limit bytes while still counting everything
// it saw, so the panel can say "truncated" instead of silently presenting a
// clipped stream as the whole output.
type cappedBuffer struct {
	limit     int
	buf       []byte
	total     int64
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.total += int64(len(p))
	if remaining := b.limit - len(b.buf); remaining > 0 {
		if len(p) <= remaining {
			b.buf = append(b.buf, p...)
		} else {
			b.buf = append(b.buf, p[:remaining]...)
			b.truncated = true
		}
	} else if len(p) > 0 {
		b.truncated = true
	}
	// Always report a full write: a short write would make the child see an
	// I/O error and terminate early, turning "output too long" into "process
	// crashed".
	return len(p), nil
}

func (b *cappedBuffer) String() string { return string(b.buf) }

// cliRunsInFlight guards against two concurrent panel-triggered runs on the
// same runtime. The server also rejects an overlapping request, but that check
// cannot see a run another node already claimed; this one is authoritative for
// the machine.
var cliRunsInFlight sync.Map

// handleCLIRegistryList answers a registry-discovery request with the redacted
// entry list.
func (d *Daemon) handleCLIRegistryList(ctx context.Context, rt Runtime, requestID string) {
	d.logger.Info("runtime CLI registry requested", "runtime_id", rt.ID, "request_id", requestID)

	reg, path, err := loadCLIRegistry()
	if err != nil {
		d.reportCLIListResult(ctx, rt, requestID, map[string]any{
			"status": "failed",
			"error":  err.Error(),
		})
		return
	}
	d.reportCLIListResult(ctx, rt, requestID, map[string]any{
		"status":        "completed",
		"clis":          cliEntrySummaries(reg),
		"registry_path": path,
	})
}

// handleCLIRun executes one registry entry and reports the result.
func (d *Daemon) handleCLIRun(ctx context.Context, rt Runtime, pending PendingCLIRun) {
	if _, loaded := cliRunsInFlight.LoadOrStore(rt.ID, struct{}{}); loaded {
		d.reportCLIRunResult(ctx, rt, pending.ID, map[string]any{
			"status": "failed",
			"error":  "another CLI run is already in progress on this machine",
		})
		return
	}
	defer cliRunsInFlight.Delete(rt.ID)

	d.logger.Info("runtime CLI run requested", "runtime_id", rt.ID, "run_id", pending.ID, "cli_key", pending.CLIKey)

	reg, _, err := loadCLIRegistry()
	if err != nil {
		d.reportCLIRunResult(ctx, rt, pending.ID, map[string]any{
			"status": "failed",
			"error":  err.Error(),
		})
		return
	}
	entry := findCLIEntry(reg, pending.CLIKey)
	if entry == nil {
		// Not "no such command" — not registered. The registry is the
		// whitelist, so this is the whitelist refusing, not a lookup miss.
		d.reportCLIRunResult(ctx, rt, pending.ID, map[string]any{
			"status": "failed",
			"error":  fmt.Sprintf("CLI %q is not registered on this machine", pending.CLIKey),
		})
		return
	}

	outcome := executeCLIEntry(ctx, entry, pending.Params)

	payload := map[string]any{
		"status":        outcome.Status,
		"output":        outcome.Output,
		"truncated":     outcome.Truncated,
		"duration_ms":   outcome.DurationMs,
		"output_bytes":  outcome.OutputBytes,
		"resolved_argv": outcome.ResolvedArgv,
	}
	if outcome.ExitCode != nil {
		payload["exit_code"] = *outcome.ExitCode
	}
	if outcome.Err != "" {
		payload["error"] = outcome.Err
	}
	d.reportCLIRunResult(ctx, rt, pending.ID, payload)
}

func (d *Daemon) reportCLIListResult(ctx context.Context, rt Runtime, requestID string, payload map[string]any) {
	d.reportRuntimeResultWithRetry(ctx, "cli_list", rt.ID, requestID, func(ctx context.Context) error {
		return d.client.ReportCLIListResult(ctx, rt.ID, requestID, payload)
	})
}

func (d *Daemon) reportCLIRunResult(ctx context.Context, rt Runtime, runID string, payload map[string]any) {
	d.reportRuntimeResultWithRetry(ctx, "cli_run", rt.ID, runID, func(ctx context.Context) error {
		return d.client.ReportCLIRunResult(ctx, rt.ID, runID, payload)
	})
}

// cliEntrySummaries renders the registry for the panel. It is intentionally
// lossy: paths, hashes, interpreters, and every env value/key stay on the
// machine. Availability is computed here so a broken entry is visible rather
// than invisible.
func cliEntrySummaries(reg *cliRegistry) []map[string]any {
	out := make([]map[string]any, 0, len(reg.CLIs))
	for i := range reg.CLIs {
		entry := &reg.CLIs[i]
		if !cliRegistryKeyPattern.MatchString(entry.Key) {
			continue
		}
		params := make([]map[string]any, 0, len(entry.Params))
		for _, p := range entry.Params {
			item := map[string]any{
				"name":     p.Name,
				"type":     p.Type,
				"required": p.Required,
			}
			if p.Type == "enum" {
				item["values"] = p.Values
			}
			if p.Type == "string" && p.MaxLen > 0 {
				item["max_len"] = p.MaxLen
			}
			params = append(params, item)
		}
		timeout := entry.TimeoutSec
		if timeout <= 0 {
			timeout = cliDefaultTimeoutSeconds
		}
		maxOutput := entry.MaxOutBytes
		if maxOutput <= 0 {
			maxOutput = cliDefaultMaxOutputBytes
		}
		summary := map[string]any{
			"key":              entry.Key,
			"label":            entry.Label,
			"description":      entry.Description,
			"params":           params,
			"timeout_seconds":  timeout,
			"max_output_bytes": maxOutput,
			"available":        true,
		}
		if _, err := resolveCLIExecutable(entry); err != nil {
			summary["available"] = false
			var missing *cliExecutableMissingError
			if errors.As(err, &missing) {
				summary["unavailable_reason"] = missing.reason
			} else {
				summary["unavailable_reason"] = "executable could not be verified"
			}
		}
		out = append(out, summary)
	}
	return out
}
