package app

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"tokgo/internal/config"
	"tokgo/internal/logx"
	"tokgo/internal/tiktok"
)

// fakeLiveness answers every lookup and counts the polls per user, so a test can
// assert the rate the runner actually produced.
type fakeLiveness struct {
	mu    sync.Mutex
	polls map[string]int
}

func newFakeLiveness(t *testing.T) *fakeLiveness {
	t.Helper()
	// The run logs every not-live poll; keep the test output about the rate.
	logx.SetSink(func(string, string) {})
	t.Cleanup(func() { logx.SetSink(nil) })
	return &fakeLiveness{polls: make(map[string]int)}
}

func (f *fakeLiveness) RoomIDForUser(context.Context, string) (string, error) {
	return "", nil
}

func (f *fakeLiveness) UserForRoomID(context.Context, string) (string, error) { return "", nil }

func (f *fakeLiveness) UserAndRoomFromURL(context.Context, string) (string, string, error) {
	return "", "", tiktok.ErrNotLive
}

func (f *fakeLiveness) CountryBlocked(context.Context) (bool, error) { return false, nil }

// RoomStatus counts the poll: one poll is one room id lookup plus one status
// lookup, and this is the second half of every one of them.
func (f *fakeLiveness) RoomStatus(_ context.Context, roomID string) (tiktok.RoomStatus, error) {
	user := strings.TrimPrefix(roomID, "room-")
	f.mu.Lock()
	f.polls[user]++
	f.mu.Unlock()
	return tiktok.RoomStatus{Live: false}, nil
}

func (f *fakeLiveness) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	sum := 0
	for _, n := range f.polls {
		sum += n
	}
	return sum
}

func (f *fakeLiveness) users() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.polls)
}

// countUser is the poll count of one registered name.
func (f *fakeLiveness) countUser(user string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.polls[user]
}

// startRun runs automatic polling for users and returns a stop function that
// cancels the run and waits for it to drain.
func startRun(t *testing.T, fake *fakeLiveness, tick time.Duration, users []string) (*Runner, context.CancelFunc) {
	t.Helper()
	r := &Runner{cfg: config.Config{PollInterval: tick}, lookup: fake}
	r.lookup = &roomed{fake}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.supervise(ctx, users, r.automatic)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return r, cancel
}

// roomed turns a user into the room id the fake's status lookup expects.
type roomed struct{ *fakeLiveness }

func (r *roomed) RoomIDForUser(_ context.Context, user string) (string, error) {
	return "room-" + user, nil
}

func TestPoolCapsPollsAcrossTargets(t *testing.T) {
	const tick = 25 * time.Millisecond
	const window = 200 * time.Millisecond
	users := []string{"a", "b", "c", "d", "e"}

	fake := newFakeLiveness(t)
	_, cancel := startRun(t, fake, tick, users)
	time.Sleep(window)
	cancel()

	if got := fake.users(); got != len(users) {
		t.Fatalf("polled %d of %d targets in %v, want every target seen once", got, len(users), window)
	}
	// One turn per tick is the ceiling; the two extra turns cover the immediate
	// first grant and scheduling slack.
	ceiling := 1 + int(window/tick) + 2
	if got := fake.total(); got > ceiling {
		t.Fatalf("polls = %d in %v at a %v tick, want at most %d: the pool must not scale with targets", got, window, tick, ceiling)
	}
}

func TestDarkTargetBacksOff(t *testing.T) {
	const tick = 5 * time.Millisecond
	const window = 400 * time.Millisecond

	fake := newFakeLiveness(t)
	startRun(t, fake, tick, []string{"dark"})
	time.Sleep(window)

	got := fake.countUser("dark")
	if bare := 1 + int(window/tick); got > bare/4 {
		t.Fatalf("polls = %d in %v, want well under the %d a bare pool allows", got, window, bare)
	}
	if got < 4 {
		t.Fatalf("polls = %d in %v, want the target checked at least a few times", got, window)
	}
}

func TestWaitingCarriesTheNextPollTime(t *testing.T) {
	const tick = 40 * time.Millisecond

	fake := newFakeLiveness(t)
	var mu sync.Mutex
	var waits []Status
	r := &Runner{cfg: config.Config{PollInterval: tick}, lookup: &roomed{fake}}
	r.OnStatus(func(s Status) {
		if s.Phase != PhaseWaiting {
			return
		}
		mu.Lock()
		waits = append(waits, s)
		mu.Unlock()
	})

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.supervise(ctx, []string{"a"}, r.automatic)
	}()
	defer func() {
		cancel()
		<-done
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(waits)
		var first, last Status
		if n > 0 {
			first, last = waits[0], waits[n-1]
		}
		mu.Unlock()
		if n >= 3 {
			if first.Next.IsZero() {
				t.Fatal("the first waiting status carries no next poll time")
			}
			if spread := last.Next.Sub(first.Next); spread < tick*9/10 {
				t.Fatalf("next poll moved %v across three statuses, want at least one %v tick", spread, tick)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("saw %d waiting statuses in 2s, want 3", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
