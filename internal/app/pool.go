package app

import (
	"context"
	"time"
)

// defaultPollInterval is the pool tick when the caller leaves it unset. It is
// also what an old settings file without a poll interval maps to.
const defaultPollInterval = 15 * time.Second

// pool paces liveness polls across every target of one run: at most one turn is
// granted per tick, so the request rate does not grow with the number of
// targets. A target that is waiting out its backoff does not ask for a turn,
// which is how a dark target drops out of the rotation without slowing the
// others.
type pool struct {
	tick time.Duration
	reqs chan chan struct{} // a waiter asks for a turn, and receives it here
}

// newPool makes a pool and normalises a missing tick, so a zero config cannot
// turn into a busy loop.
func newPool(tick time.Duration) *pool {
	if tick <= 0 {
		tick = defaultPollInterval
	}
	return &pool{tick: tick, reqs: make(chan chan struct{})}
}

// serve grants turns in arrival order, no faster than one per tick. The first
// turn is immediate: an idle pool has nothing to protect. It returns when ctx
// ends, releasing every waiter still queued.
func (p *pool) serve(ctx context.Context) {
	var queue []chan struct{}
	nextAt := time.Time{}
	ticker := time.NewTicker(p.tick)
	defer ticker.Stop()

	grant := func() {
		if len(queue) == 0 || time.Now().Before(nextAt) {
			return
		}
		close(queue[0])
		queue = queue[1:]
		nextAt = time.Now().Add(p.tick)
		ticker.Reset(p.tick)
	}

	for {
		select {
		case <-ctx.Done():
			for _, turn := range queue {
				close(turn)
			}
			return
		case turn := <-p.reqs:
			queue = append(queue, turn)
			grant()
		case <-ticker.C:
			grant()
		}
	}
}

// next reserves a turn and blocks until it is granted. A false return means ctx
// ended first; the reserved turn is released when the pool itself stops.
func (p *pool) next(ctx context.Context) bool {
	turn := make(chan struct{})
	select {
	case p.reqs <- turn:
	case <-ctx.Done():
		return false
	}
	select {
	case <-turn:
		return true
	case <-ctx.Done():
		return false
	}
}
