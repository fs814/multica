package daemon

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/internal/worksync"
)

// WorkSyncTransport must bind requests to an authenticated account/actor/node.
// HTTPWorkSyncTransport supplies the production binding; enrollment is explicit.
type WorkSyncTransport interface {
	Pull(context.Context, worksync.Principal, worksync.Scope, int64, bool) (worksync.Batch, error)
	Push(context.Context, worksync.Principal, worksync.Scope, worksync.Operation) (worksync.Receipt, error)
}

type WorkSyncClient struct {
	Enabled   bool
	Replica   *worksync.Replica
	Transport WorkSyncTransport
	mu        sync.Mutex
}

// SyncOnce is bounded and synchronous. Reconnect callers may retry it with
// backoff; an auth or storage error stops immediately and leaves durable intent.
// No LocalIssue API, agent executable or task creation entry point is involved.
func (c *WorkSyncClient) SyncOnce(ctx context.Context) (err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.Enabled {
		return worksync.ErrDisabled
	}
	if c.Replica == nil || c.Transport == nil {
		return worksync.ErrDisabled
	}
	defer func() {
		if errors.Is(err, worksync.ErrDenied) {
			if purgeErr := c.Replica.Revoke(); purgeErr != nil {
				err = errors.Join(err, purgeErr)
			}
		}
	}()
	if h, ok := c.Transport.(interface {
		Handshake(context.Context, worksync.Principal, worksync.Scope) error
	}); ok {
		state, e := c.Replica.State()
		if e != nil {
			return e
		}
		if e = h.Handshake(ctx, state.Principal, state.Scope); e != nil {
			return e
		}
	}
	if err := c.pull(ctx); err != nil {
		return err
	}
	for i := 0; i < worksync.MaxBatch; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		s, err := c.Replica.State()
		if err != nil {
			return err
		}
		if len(s.Outbox) == 0 {
			break
		}
		receipt, err := c.Transport.Push(ctx, s.Principal, s.Scope, s.Outbox[0])
		if err != nil {
			return err
		}
		if err = c.Replica.Acknowledge(receipt); err != nil {
			return err
		}
	}
	return c.pull(ctx)
}

func (c *WorkSyncClient) pull(ctx context.Context) error {
	s, err := c.Replica.State()
	if err != nil {
		return err
	}
	b, err := c.Transport.Pull(ctx, s.Principal, s.Scope, s.Cursor, !s.Initialized)
	if err != nil {
		return err
	}
	return c.Replica.Apply(b)
}

// Run polls even without a websocket notification, backs off transient failures,
// and exits on authorization, protocol or storage failure. Restart uses the same
// checkpoint. Wake is optional and cannot bypass retry backoff.
func (c *WorkSyncClient) Run(ctx context.Context, poll time.Duration, wake func() <-chan struct{}) error {
	if poll <= 0 {
		poll = 30 * time.Second
	}
	backoff := time.Second
	for {
		var signal <-chan struct{}
		if wake != nil {
			signal = wake()
		}
		err := c.SyncOnce(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil && !errors.Is(err, ErrSyncUnavailable) {
			return err
		}
		delay := poll
		if err != nil {
			delay = backoff/2 + time.Duration(rand.Int64N(int64(backoff/2)+1))
			backoff = min(backoff*2, 2*time.Minute)
			signal = nil
		} else {
			backoff = time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-signal:
			timer.Stop()
		case <-timer.C:
		}
	}
}
