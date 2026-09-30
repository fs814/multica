package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/multica-ai/multica/server/internal/centerrecovery"
	"github.com/multica-ai/multica/server/internal/cli"
)

// Recovery state outlives center switches and login-token replacement. Only an
// operator-provisioned source credential enables full-center snapshots.
func (d *Daemon) centerRecoveryLoop(ctx context.Context) {
	dir, err := cli.ProfileDir(d.cfg.Profile)
	if err != nil {
		d.logger.Warn("center recovery directory unavailable")
		return
	}
	root := filepath.Join(dir, "center-recovery")
	// Capture the authenticated center for this Run; never race later profile edits.
	origin := d.cfg.ServerBaseURL
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		source, err := centerrecovery.LoadSource(root, origin)
		if err == nil {
			var path string
			path, err = centerrecovery.Pull(ctx, root, source)
			if err == nil {
				d.logger.Info("center recovery snapshot saved", "path", path)
			}
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) && ctx.Err() == nil {
			d.logger.Warn("center recovery backup failed; previous snapshots preserved", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
