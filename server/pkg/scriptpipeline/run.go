package scriptpipeline

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

type StepResult struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	ExitCode   int    `json:"exit_code"`
	DurationMS int64  `json:"duration_ms"`
	Output     string `json:"output"`
	Truncated  bool   `json:"truncated"`
}
type Result struct {
	Directory string       `json:"directory"`
	Steps     []StepResult `json:"steps"`
	Error     string       `json:"error,omitempty"`
}

// Run uses bounded output and a finite deadline; cancellation kills the owned
// process tree. emit is called for progress and output on the caller's goroutine.
func Run(ctx context.Context, config *Config, emit func(string), environment ...map[string]string) Result {
	dir, plan, err := config.Plan(runtime.GOOS)
	if err != nil {
		return Result{Error: err.Error()}
	}
	result := Result{Directory: dir}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(config.TimeoutSeconds)*time.Second)
	defer cancel()
	if emit != nil {
		emit("Waiting for script directory execution slot")
	}
	release, err := lockDirectory(ctx, dir)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer release()
	wrapper := ""
	if runtime.GOOS == "windows" {
		f, err := os.CreateTemp("", "multica-pipeline-*.ps1")
		if err != nil {
			result.Error = err.Error()
			return result
		}
		wrapper = f.Name()
		defer os.Remove(wrapper)
		_, err = f.WriteString("param([string]$ScriptPath)\n$ErrorActionPreference='Stop'\n$PSNativeCommandUseErrorActionPreference=$true\n& $ScriptPath\nif ($null -ne $LASTEXITCODE) { exit $LASTEXITCODE }\n")
		closeErr := f.Close()
		if err != nil || closeErr != nil {
			result.Error = "could not prepare PowerShell runner"
			return result
		}
	}
	for _, step := range plan {
		if ctx.Err() != nil {
			result.Error = ctx.Err().Error()
			break
		}
		if emit != nil {
			emit(fmt.Sprintf("[%s] Starting %s", step.Name, step.Path))
		}
		executable := "bash"
		args := []string{"-e", step.Path}
		if runtime.GOOS == "windows" {
			executable = "pwsh"
			args = []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-File", wrapper, "-ScriptPath", step.Path}
		}
		cmd := exec.CommandContext(ctx, executable, args...)
		cmd.Dir = dir
		if len(environment) > 0 {
			cmd.Env = os.Environ()
			for key, value := range environment[0] {
				cmd.Env = append(cmd.Env, key+"="+value)
			}
		}
		configureProcess(cmd)
		cmd.WaitDelay = 2 * time.Second
		cmd.Cancel = func() error { return terminateProcess(cmd) }
		output := &boundedOutput{max: 128 * 1024}
		cmd.Stdout = output
		cmd.Stderr = output
		started := time.Now()
		err = cmd.Start()
		if err == nil {
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			ticker := time.NewTicker(time.Second)
			running := true
			for running {
				select {
				case err = <-done:
					running = false
				case <-ticker.C:
					if chunk := output.drain(); chunk != "" && emit != nil {
						emit(chunk)
					}
				}
			}
			ticker.Stop()
		}
		if chunk := output.drain(); chunk != "" && emit != nil {
			emit(chunk)
		}
		code := 0
		if err != nil {
			code = -1
			if cmd.ProcessState != nil {
				code = cmd.ProcessState.ExitCode()
			}
		}
		text, truncated := output.snapshot()
		result.Steps = append(result.Steps, StepResult{Name: step.Name, Path: step.Path, ExitCode: code, DurationMS: time.Since(started).Milliseconds(), Output: text, Truncated: truncated})
		if emit != nil {
			emit(fmt.Sprintf("[%s] Exit code %d", step.Name, code))
		}
		if err != nil {
			if ctx.Err() != nil {
				result.Error = ctx.Err().Error()
			} else {
				result.Error = fmt.Sprintf("%s failed (exit %d): %v", step.Name, code, err)
			}
			break
		}
	}
	return result
}

type boundedOutput struct {
	mu        sync.Mutex
	text      strings.Builder
	pending   strings.Builder
	max       int
	truncated bool
}

var _ io.Writer = (*boundedOutput)(nil)

func (b *boundedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	left := b.max - b.text.Len()
	if left > len(p) {
		left = len(p)
	}
	if left > 0 {
		b.text.Write(p[:left])
	}
	if left < len(p) {
		b.truncated = true
	}
	pendingLeft := 16*1024 - b.pending.Len()
	if pendingLeft > len(p) {
		pendingLeft = len(p)
	}
	if pendingLeft > 0 {
		b.pending.Write(p[:pendingLeft])
	}
	return n, nil
}
func (b *boundedOutput) drain() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.pending.String()
	b.pending.Reset()
	return s
}
func (b *boundedOutput) snapshot() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.text.String(), b.truncated
}
