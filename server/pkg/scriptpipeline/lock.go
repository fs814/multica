package scriptpipeline

import (
	"context"
	"runtime"
	"strings"
	"sync"
)

var directoryLocks = struct {
	sync.Mutex
	items map[string]*directoryLock
}{items: map[string]*directoryLock{}}

type directoryLock struct {
	slot  chan struct{}
	users int
}

func lockDirectory(ctx context.Context, key string) (func(), error) {
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	directoryLocks.Lock()
	item := directoryLocks.items[key]
	if item == nil {
		item = &directoryLock{slot: make(chan struct{}, 1)}
		directoryLocks.items[key] = item
	}
	item.users++
	directoryLocks.Unlock()
	drop := func() {
		directoryLocks.Lock()
		defer directoryLocks.Unlock()
		item.users--
		if item.users == 0 {
			delete(directoryLocks.items, key)
		}
	}
	select {
	case item.slot <- struct{}{}:
		return func() { <-item.slot; drop() }, nil
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	}
}
