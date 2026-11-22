package app

import (
	"context"
	"testing"
	"time"
)

// startPool serves a pool for the test and stops it on cleanup.
func startPool(t *testing.T, tick time.Duration) (*pool, context.Context) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	p := newPool(tick)
	go p.serve(ctx)
	t.Cleanup(cancel)
	return p, ctx
}

func TestPoolGrantsOneTurnPerTick(t *testing.T) {
	const tick = 20 * time.Millisecond
	p, ctx := startPool(t, tick)

	var marks []time.Time
	for range 4 {
		if !p.next(ctx) {
			t.Fatal("turn was not granted")
		}
		marks = append(marks, time.Now())
	}

	for i := 1; i < len(marks); i++ {
		if gap := marks[i].Sub(marks[i-1]); gap < tick*9/10 {
			t.Fatalf("turn %d came %v after the previous one, want at least %v", i, gap, tick*9/10)
		}
	}
	if span, want := marks[len(marks)-1].Sub(marks[0]), tick*3*9/10; span < want {
		t.Fatalf("four turns spanned %v, want at least %v", span, want)
	}
}

func TestPoolFirstTurnIsImmediate(t *testing.T) {
	p, ctx := startPool(t, time.Minute)

	start := time.Now()
	if !p.next(ctx) {
		t.Fatal("turn was not granted")
	}
	if waited := time.Since(start); waited > 100*time.Millisecond {
		t.Fatalf("idle pool held the first turn for %v, want it at once", waited)
	}
}

func TestPoolGrantsInArrivalOrder(t *testing.T) {
	const tick = 25 * time.Millisecond
	p, ctx := startPool(t, tick)

	order := make(chan int, 5)
	for i := range 5 {
		go func() {
			if p.next(ctx) {
				order <- i
			}
		}()
		time.Sleep(4 * time.Millisecond) // arrivals reach the queue in this order
	}

	for want := range 5 {
		select {
		case got := <-order:
			if got != want {
				t.Fatalf("turn %d went to arrival %d, want first in first out", want, got)
			}
		case <-time.After(time.Second):
			t.Fatalf("arrival %d never got a turn", want)
		}
	}
}

func TestPoolReleasesWaitersOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	p := newPool(time.Hour)
	go p.serve(ctx)

	done := make(chan bool, 3)
	for range 3 {
		go func() { done <- p.next(ctx) }()
	}
	time.Sleep(20 * time.Millisecond) // let all three reach the pool
	cancel()

	granted := 0
	for range 3 {
		select {
		case ok := <-done:
			if ok {
				granted++
			}
		case <-time.After(time.Second):
			t.Fatal("a waiter did not return after the run ended")
		}
	}
	if granted != 1 {
		t.Fatalf("grants after cancel = %d, want only the one turn the idle pool had already handed out", granted)
	}
}

func TestNewPoolDefaultsTheTick(t *testing.T) {
	if got := newPool(0).tick; got != defaultPollInterval {
		t.Fatalf("tick = %v, want %v", got, defaultPollInterval)
	}
	if got := newPool(-time.Second).tick; got != defaultPollInterval {
		t.Fatalf("tick = %v, want %v", got, defaultPollInterval)
	}
}
