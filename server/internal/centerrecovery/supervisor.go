package centerrecovery

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

func rollbackActivation(root string) error {
	var old activation
	if err := readJSON(filepath.Join(root, "previous.json"), &old); err != nil {
		return err
	}
	if err := writePrivateJSON(filepath.Join(root, "active.json"), old); err != nil {
		return err
	}
	var status ImportStatus
	if readJSON(filepath.Join(root, "status.json"), &status) == nil {
		status.State = "failed"
		status.Message = "The restored center could not start. The previous database was reactivated."
		_ = writePrivateJSON(filepath.Join(root, "status.json"), status)
	}
	return nil
}
func waitForReady(ctx context.Context, bootID string) bool {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return false
	}
	client := &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	timer := time.NewTicker(250 * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
		}
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+port+"/readyz", nil)
		response, err := client.Do(request)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == 200 && response.Header.Get("X-Multica-Recovery-Boot") == bootID {
				return true
			}
		}
	}
}

// Supervise keeps the launcher-owned process alive across a deliberate import.
// A child must drain and exit before restoration starts. Failed candidate boots
// roll back the configuration; the original database is never overwritten.
func Supervise(root string) int {
	if !filepath.IsAbs(root) {
		slog.Error("MULTICA_RECOVERY_STATE_DIR must be an absolute persistent path")
		return 1
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		slog.Error("cannot create recovery state directory")
		return 1
	}
	binary, err := os.Executable()
	if err != nil {
		return 1
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	if _, err = os.Stat(filepath.Join(root, "pending.json")); err == nil {
		var plan importPlan
		if readJSON(filepath.Join(root, "pending.json"), &plan) == nil {
			_ = writePrivateJSON(filepath.Join(root, "status.json"), ImportStatus{JobID: plan.JobID, State: "failed", CenterID: plan.CenterID, Message: "Import was interrupted. Select the backup again to retry."})
		}
		_ = os.Remove(filepath.Join(root, "pending.json"))
	}
	var savedStatus ImportStatus
	activating := readJSON(filepath.Join(root, "status.json"), &savedStatus) == nil && savedStatus.State == "activating"
	for {
		child := exec.Command(binary, os.Args[1:]...)
		bootBytes := make([]byte, 16)
		if _, err = rand.Read(bootBytes); err != nil {
			return 1
		}
		bootID := hex.EncodeToString(bootBytes)
		child.Env = append(os.Environ(), ChildEnvironment+"=1", "MULTICA_RECOVERY_BOOT_ID="+bootID)
		child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err = child.Start(); err != nil {
			slog.Error("cannot start managed center")
			return 1
		}
		done := make(chan error, 1)
		go func() { done <- child.Wait() }()
		var ready <-chan bool
		readyCtx, cancelReady := context.WithTimeout(context.Background(), 90*time.Second)
		if activating {
			channel := make(chan bool, 1)
			ready = channel
			go func() { channel <- waitForReady(readyCtx, bootID) }()
		}
		childFinished := false
		for !childFinished {
			select {
			case sig := <-signals:
				cancelReady()
				if err = child.Process.Signal(sig); err != nil {
					_ = child.Process.Kill()
				}
				<-done
				return 0
			case healthy := <-ready:
				cancelReady()
				ready = nil
				if healthy {
					var status ImportStatus
					if readJSON(filepath.Join(root, "status.json"), &status) == nil {
						status.State = "complete"
						status.Message = ""
						if writePrivateJSON(filepath.Join(root, "status.json"), status) != nil {
							slog.Error("cannot save import completion")
						}
					}
					activating = false
				} else {
					_ = child.Process.Kill()
					<-done
					if rollbackActivation(root) != nil {
						slog.Error("cannot restore previous activation")
						return 1
					}
					activating = false
					childFinished = true
				}
			case err = <-done:
				cancelReady()
				childFinished = true
				if activating {
					if rollbackActivation(root) != nil {
						slog.Error("cannot restore previous activation")
						return 1
					}
					activating = false
					continue
				}
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != RestartExitCode {
					if err != nil {
						return 1
					}
					return 0
				}
				importCtx, cancelImport := context.WithTimeout(context.Background(), 10*time.Minute)
				finished := make(chan error, 1)
				go func() { finished <- ProcessPending(importCtx, root) }()
				select {
				case <-signals:
					cancelImport()
					<-finished
					return 0
				case err = <-finished:
					cancelImport()
					activating = err == nil
					if err != nil {
						slog.Error("center import failed; restarting previous configuration")
					}
				}
			}
		}
		cancelReady()
	}
}
