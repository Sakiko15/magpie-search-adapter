package resilience

import (
	"context"
	"sync"
)

type call struct {
	done chan struct{}
	val  any
	err  error
}

// group runs one call per key and hands its result to every waiter.
type group struct {
	mu    sync.Mutex
	calls map[string]*call
}

func newGroup() *group {
	return &group{calls: make(map[string]*call)}
}

// do runs fn once per key. Every waiter selects on its own context while
// waiting, so one caller giving up does not cancel the shared call.
func (g *group) do(key string, fn func() (any, error), waitCtx context.Context) (any, error, bool) {
	g.mu.Lock()
	if existing, ok := g.calls[key]; ok {
		g.mu.Unlock()
		select {
		case <-existing.done:
			return existing.val, existing.err, true
		case <-waitCtx.Done():
			return nil, waitCtx.Err(), true
		}
	}
	current := &call{done: make(chan struct{})}
	g.calls[key] = current
	g.mu.Unlock()

	// A panic in fn must not leave a dead entry behind: waiters would
	// block on a channel that is never closed.
	defer func() {
		g.mu.Lock()
		delete(g.calls, key)
		g.mu.Unlock()
		close(current.done)
	}()
	current.val, current.err = fn()
	return current.val, current.err, false
}
