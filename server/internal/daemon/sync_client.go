package daemon

import (
	"context"
	"sync"

	"github.com/multica-ai/multica/server/internal/worksync"
)

// WorkSyncTransport must bind requests to an authenticated account/actor/node.
// There is deliberately no default HTTP transport, scheduler or enrollment:
// production authorization and replica grants remain an activation gate.
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
func (c *WorkSyncClient) SyncOnce(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.Enabled {
		return worksync.ErrDisabled
	}
	if c.Replica == nil || c.Transport == nil {
		return worksync.ErrDisabled
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
