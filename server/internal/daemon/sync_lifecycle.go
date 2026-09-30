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
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/worksync"
)

// WorkSyncSettings is loaded only when MULTICA_WORK_SYNC_ENABLED=1. It contains
// no credentials; each target points to a dedicated, owner-readable token file.
// Targets/scopes are explicit, never discovered from arbitrary local issues.
type WorkSyncSettings struct {
	Root    string           `json:"root"`
	Targets []WorkSyncTarget `json:"targets"`
}
type WorkSyncTarget struct {
	Scope     worksync.Scope `json:"scope"`
	Actor     string         `json:"actor"`
	TokenFile string         `json:"token_file"`
}

func loadWorkSyncSettings() (*WorkSyncSettings, error) {
	if os.Getenv("MULTICA_WORK_SYNC_ENABLED") != "1" {
		return nil, nil
	}
	f, err := os.Open(os.Getenv("MULTICA_WORK_SYNC_CONFIG"))
	if err != nil {
		return nil, fmt.Errorf("open work sync configuration: %w", err)
	}
	defer f.Close()
	var cfg WorkSyncSettings
	decoder := json.NewDecoder(io.LimitReader(f, 1<<20))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&cfg); err != nil {
		return nil, err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, worksync.ErrScope
	}
	if err = cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}
func (c WorkSyncSettings) validate() error {
	if !filepath.IsAbs(c.Root) || len(c.Targets) == 0 || len(c.Targets) > 32 {
		return worksync.ErrScope
	}
	seen := map[string]bool{}
	for _, target := range c.Targets {
		actor, err := uuid.Parse(target.Actor)
		if err != nil || actor == uuid.Nil || actor.String() != target.Actor || target.Scope.Validate() != nil || !filepath.IsAbs(target.TokenFile) || seen[target.Scope.Workspace] {
			return worksync.ErrScope
		}
		seen[target.Scope.Workspace] = true
	}
	return nil
}
func readWorkSyncToken(path string) (string, error) {
	// Unix mode bits do not establish an owner-only Windows ACL.
	if runtime.GOOS == "windows" {
		return "", worksync.ErrDisabled
	}
	f, err := os.Open(path)
	if err != nil {
		return "", errors.New("sync credential file unavailable")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		return "", errors.New("sync credential file must be private")
	}
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(data) > 4096 {
		return "", errors.New("invalid sync credential file")
	}
	token := strings.TrimSpace(string(data))
	if !strings.HasPrefix(token, "mdt_") {
		return "", worksync.ErrUnauthenticated
	}
	return token, nil
}

// startWorkSync owns and joins its goroutines. Run's shutdown cancels requests
// before closing stores, so no checkpoint lock or writer outlives the daemon.
func (d *Daemon) startWorkSync(ctx context.Context) (func(), error) {
	if d.cfg.WorkSync == nil {
		return func() {}, nil
	}
	if err := d.cfg.WorkSync.validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	var replicas []*worksync.Replica
	stop := func() {
		cancel()
		wg.Wait()
		for _, rep := range replicas {
			_ = rep.Close()
		}
	}
	origin := sha256.Sum256([]byte(d.cfg.ServerBaseURL))
	for _, target := range d.cfg.WorkSync.Targets {
		tokenFile := target.TokenFile
		transport, err := NewHTTPWorkSyncTransport(d.cfg.ServerBaseURL, func() (string, error) { return readWorkSyncToken(tokenFile) })
		if err != nil {
			stop()
			return nil, err
		}
		rep, err := worksync.OpenReplica(worksync.ReplicaConfig{Enabled: true, Root: filepath.Join(d.cfg.WorkSync.Root, hex.EncodeToString(origin[:])), Scope: target.Scope, Principal: worksync.Principal{Account: target.Actor, Actor: target.Actor, Node: d.cfg.DaemonID}})
		if err != nil {
			stop()
			return nil, err
		}
		replicas = append(replicas, rep)
		client := &WorkSyncClient{Enabled: true, Replica: rep, Transport: transport}
		workspace := target.Scope.Workspace
		wg.Add(1)
		go func() {
			defer wg.Done()
			var wake func() <-chan struct{}
			if d.reconcile != nil {
				wake = d.reconcile.notify
			}
			err := client.Run(ctx, 30*time.Second, wake)
			if err != nil && ctx.Err() == nil {
				d.logger.Warn("work sync stopped; operator action required", "workspace_id", workspace, "error", err)
			}
		}()
	}
	return stop, nil
}
