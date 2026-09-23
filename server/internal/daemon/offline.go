package daemon

import (
	"context"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
)

// Published atomically while startup owns the mutable client and configuration.
// Task loops are not launched until startup completes. Health reads this snapshot
// instead of the changing server URL until the final Store(nil) publishes it.
type offlineState struct {
	Reason    string
	ServerURL string
}

func (d *Daemon) connectWhenConfigured(ctx context.Context, interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	previous := ""
	lastAgentRefresh := time.Now()
	for {
		if time.Since(lastAgentRefresh) >= 30*time.Second {
			d.refreshAgentAvailability()
			lastAgentRefresh = time.Now()
		}
		state := offlineState{Reason: "unconfigured"}
		cfg, err := cli.LoadCLIConfigForProfile(d.cfg.Profile)
		if err != nil {
			state.Reason = "invalid_configuration"
		} else if cfg.ServerURL != "" {
			state.ServerURL, err = NormalizeServerBaseURL(cfg.ServerURL)
			if err != nil {
				state.Reason = "invalid_configuration"
			} else if cfg.Token == "" {
				state.Reason = "unauthenticated"
			} else {
				state.Reason = "connecting"
				connecting := state
				d.offline.Store(&connecting)
				// No task goroutines exist yet; replace both URL and token together.
				d.cfg.ServerBaseURL = state.ServerURL
				d.client.baseURL = state.ServerURL
				d.client.SetToken(cfg.Token)
				if err = d.preflightAuth(ctx); err == nil {
					d.offline.Store(nil)
					d.logger.Info("Center connected; starting task services")
					return nil
				}
				state = offlineState{Reason: "unreachable", ServerURL: state.ServerURL}
				if isUnauthorizedError(err) {
					state.Reason = "unauthenticated"
				}
			}
		}
		d.offline.Store(&state)
		if state.Reason != previous {
			d.logger.Info("daemon running offline", "reason", state.Reason, "profile", d.cfg.Profile)
			previous = state.Reason
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
