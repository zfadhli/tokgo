package app

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"tokgo/internal/config"
	"tokgo/internal/logx"
	"tokgo/internal/record"
)

// statusLog collects every status a runner emits and lets a test block until a
// particular one shows up, always under a deadline.
type statusLog struct {
	mu      sync.Mutex
	all     []Status
	changed chan struct{}
}

func newStatusLog() *statusLog { return &statusLog{changed: make(chan struct{})} }

func (l *statusLog) observe(s Status) {
	l.mu.Lock()
	l.all = append(l.all, s)
	close(l.changed)
	l.changed = make(chan struct{})
	l.mu.Unlock()
}

func (l *statusLog) snapshot() []Status {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Status(nil), l.all...)
}

func (l *statusLog) wait(t *testing.T, user string, phase Phase) Status {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		l.mu.Lock()
		for _, s := range l.all {
			if s.User == user && s.Phase == phase {
				l.mu.Unlock()
				return s
			}
		}
		next := l.changed
		l.mu.Unlock()
		select {
		case <-next:
		case <-deadline:
			t.Fatalf("no %s status for %q within 2s, got %+v", phase, user, l.snapshot())
		}
	}
}

// recv takes one value from ch and fails the test if it has to wait more than
// two seconds for it.
func recv[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(2 * time.Second):
		t.Fatal("nothing arrived within 2s")
	}
	var zero T
	return zero
}

// captureLog diverts every console line into a slice for the test's duration.
func captureLog(t *testing.T) func() []string {
	t.Helper()
	var (
		mu    sync.Mutex
		lines []string
	)
	logx.SetSink(func(_, text string) {
		mu.Lock()
		lines = append(lines, text)
		mu.Unlock()
	})
	t.Cleanup(func() { logx.SetSink(nil) })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), lines...)
	}
}

func TestPhaseNamesAndActivity(t *testing.T) {
	cases := []struct {
		phase  Phase
		name   string
		active bool
	}{
		{PhaseQueued, "queued", false},
		{PhaseResolving, "resolving", true},
		{PhaseWaiting, "waiting", true},
		{PhaseLive, "live", true},
		{PhaseRecording, "recording", true},
		{PhaseConverting, "remuxing", true},
		{PhaseDone, "done", false},
		{PhaseStopped, "stopped", false},
		{PhaseFailed, "failed", false},
		{Phase(99), "Phase(99)", false},
	}
	for _, tc := range cases {
		if got := tc.phase.String(); got != tc.name {
			t.Errorf("Phase(%d).String() = %q, want %q", int(tc.phase), got, tc.name)
		}
		if got := tc.phase.Active(); got != tc.active {
			t.Errorf("%s.Active() = %v, want %v", tc.name, got, tc.active)
		}
	}
}

func TestRunKeepsStatusesWithTheirTarget(t *testing.T) {
	r := &Runner{}
	log := newStatusLog()
	r.OnStatus(log.observe)

	release := make(chan struct{})
	started := make(chan string, 2)
	done := make(chan error, 1)
	go func() {
		done <- r.supervise(context.Background(), []string{"quick", "slow"}, func(_ context.Context, user string) (string, error) {
			started <- user
			if user == "slow" {
				<-release
			}
			return "/out/" + user + ".mp4", nil
		})
	}()

	recv(t, started)
	recv(t, started)
	close(release)

	if err := recv(t, done); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, user := range []string{"quick", "slow"} {
		log.wait(t, user, PhaseQueued)
		s := log.wait(t, user, PhaseDone)
		if want := "/out/" + user + ".mp4"; s.File != want {
			t.Fatalf("%s done file = %q, want %q", user, s.File, want)
		}
	}
	for _, s := range log.snapshot() {
		if s.Phase == PhaseDone && s.File != "/out/"+s.User+".mp4" {
			t.Fatalf("status bled across targets: %+v", s)
		}
	}
}

func TestAddUserJoinsTheRun(t *testing.T) {
	r := &Runner{}
	log := newStatusLog()
	r.OnStatus(log.observe)

	gate := make(chan struct{})
	started := make(chan string, 3)
	done := make(chan error, 1)
	go func() {
		done <- r.supervise(context.Background(), []string{"host"}, func(_ context.Context, user string) (string, error) {
			started <- user
			<-gate
			return "/out/" + user + ".mp4", nil
		})
	}()

	if got := recv(t, started); got != "host" {
		t.Fatalf("started %q, want the registered target", got)
	}
	if err := r.AddUser("guest"); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	if got := recv(t, started); got != "guest" {
		t.Fatalf("started %q, want the added target", got)
	}
	log.wait(t, "guest", PhaseQueued)
	// The run can only answer ErrUserRunning while it still holds the target,
	// so this proves the run is in flight without waiting on the clock.
	if err := r.AddUser("guest"); !errors.Is(err, ErrUserRunning) {
		t.Fatalf("AddUser(guest) during the run = %v, want ErrUserRunning", err)
	}

	close(gate)
	if err := recv(t, done); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if s := log.wait(t, "guest", PhaseDone); s.File != "/out/guest.mp4" {
		t.Fatalf("guest done file = %q, want /out/guest.mp4", s.File)
	}
}

func TestDropUserCancelsOnlyThatTarget(t *testing.T) {
	r := &Runner{}
	log := newStatusLog()
	r.OnStatus(log.observe)

	release := make(chan struct{})
	started := make(chan string, 2)
	kept := make(chan error, 1)
	done := make(chan error, 1)
	go func() {
		done <- r.supervise(context.Background(), []string{"drop", "keep"}, func(ctx context.Context, user string) (string, error) {
			started <- user
			if user == "drop" {
				<-ctx.Done()
				return "", nil
			}
			<-release
			kept <- ctx.Err()
			return "/out/keep.mp4", nil
		})
	}()

	recv(t, started)
	recv(t, started)
	if !r.DropUser("drop") {
		t.Fatal("DropUser(drop) = false, want the target")
	}
	log.wait(t, "drop", PhaseStopped)
	if r.DropUser("nobody") {
		t.Fatal("DropUser(nobody) = true, want false")
	}

	close(release)
	if err := recv(t, kept); err != nil {
		t.Fatalf("the kept target saw its context cancelled: %v", err)
	}
	if err := recv(t, done); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if s := log.wait(t, "keep", PhaseDone); s.File != "/out/keep.mp4" {
		t.Fatalf("keep done file = %q, want /out/keep.mp4", s.File)
	}
}

func TestAddUserAfterRunReportsNotRunning(t *testing.T) {
	r := &Runner{}
	log := newStatusLog()
	r.OnStatus(log.observe)

	err := r.supervise(context.Background(), []string{"solo"}, func(context.Context, string) (string, error) {
		return "", nil
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := r.AddUser("late"); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("AddUser after Run = %v, want ErrNotRunning", err)
	}
	for _, s := range log.snapshot() {
		if s.User == "late" {
			t.Fatalf("a finished run emitted %+v", s)
		}
	}
}

func TestAddUserRejectsADuplicate(t *testing.T) {
	r := &Runner{}
	gate := make(chan struct{})
	started := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- r.supervise(context.Background(), []string{"dup"}, func(context.Context, string) (string, error) {
			started <- "dup"
			<-gate
			return "", nil
		})
	}()

	recv(t, started)
	if err := r.AddUser("dup"); !errors.Is(err, ErrUserRunning) {
		t.Fatalf("AddUser(dup) = %v, want ErrUserRunning", err)
	}
	close(gate)
	recv(t, done)
}

// A repeated target is one target. Counting it twice made the run take the
// multi-target path, so its only failure was logged and reported as ErrReported
// instead of coming back wrapped the way a single target's does.
func TestRunDedupesRepeatedTargets(t *testing.T) {
	logs := captureLog(t)
	boom := errors.New("boom")
	r := &Runner{}
	ran := make(chan string, 2)

	err := r.supervise(context.Background(), []string{"alice", "alice"}, func(_ context.Context, user string) (string, error) {
		ran <- user
		return "", boom
	})

	if got := len(ran); got != 1 {
		t.Fatalf("%d target goroutines ran, want 1", got)
	}
	if err == nil || err.Error() != "@alice: boom" || !errors.Is(err, boom) {
		t.Fatalf("Run = %v, want @alice: boom wrapping the cause", err)
	}
	if lines := logs(); len(lines) != 0 {
		t.Fatalf("a single-target failure logged %q, want nothing", lines)
	}
}

// A url run resolves every target through the same url, so a second target
// would share the first one's user and room. It has exactly one target.
func TestAddUserRefusesOnAURLRun(t *testing.T) {
	r := &Runner{cfg: config.Config{URL: "https://www.tiktok.com/@host/live"}}
	log := newStatusLog()
	r.OnStatus(log.observe)

	gate := make(chan struct{})
	started := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- r.supervise(context.Background(), []string{""}, func(context.Context, string) (string, error) {
			started <- ""
			<-gate
			return "", nil
		})
	}()

	recv(t, started)
	if err := r.AddUser("someone"); !errors.Is(err, ErrURLRun) {
		t.Fatalf("AddUser on a url run = %v, want ErrURLRun", err)
	}
	if r.DropUser("someone") {
		t.Fatal("the refused target was registered anyway")
	}

	close(gate)
	if err := recv(t, done); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, s := range log.snapshot() {
		if s.User == "someone" {
			t.Fatalf("a refused target emitted %+v", s)
		}
	}
}

func TestRunWrapsASingleTargetError(t *testing.T) {
	logs := captureLog(t)
	boom := errors.New("boom")
	r := &Runner{}

	err := r.supervise(context.Background(), []string{"solo"}, func(context.Context, string) (string, error) {
		return "", boom
	})
	if err == nil || err.Error() != "@solo: boom" || !errors.Is(err, boom) {
		t.Fatalf("Run = %v, want @solo: boom wrapping the cause", err)
	}
	if lines := logs(); len(lines) != 0 {
		t.Fatalf("a single-target failure logged %q, want nothing", lines)
	}
}

func TestRunReturnsTheBareErrorForAnUnnamedTarget(t *testing.T) {
	logs := captureLog(t)
	boom := errors.New("boom")
	r := &Runner{}

	err := r.supervise(context.Background(), nil, func(context.Context, string) (string, error) {
		return "", boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Run = %v, want the bare cause", err)
	}
	if lines := logs(); len(lines) != 0 {
		t.Fatalf("an unnamed target failure logged %q, want nothing", lines)
	}
}

func TestRunReportsEveryFailureOnTheMultiPath(t *testing.T) {
	logs := captureLog(t)
	r := &Runner{}
	log := newStatusLog()
	r.OnStatus(log.observe)

	err := r.supervise(context.Background(), []string{"bad", "good"}, func(_ context.Context, user string) (string, error) {
		if user == "bad" {
			return "", errors.New("boom")
		}
		return "/out/good.mp4", nil
	})
	if !errors.Is(err, ErrReported) {
		t.Fatalf("Run = %v, want ErrReported", err)
	}
	if lines := logs(); !slices.Contains(lines, "@bad: boom") {
		t.Fatalf("logs = %q, want the @bad failure", lines)
	}
	if s := log.wait(t, "bad", PhaseFailed); s.Detail != "boom" {
		t.Fatalf("failed detail = %q, want boom", s.Detail)
	}
	if s := log.wait(t, "good", PhaseDone); s.File != "/out/good.mp4" {
		t.Fatalf("good done file = %q, want /out/good.mp4", s.File)
	}
}

func TestReportMapsRecorderProgressOntoTheTarget(t *testing.T) {
	r := &Runner{}
	log := newStatusLog()
	r.OnStatus(log.observe)
	r.tasks = map[string]context.CancelFunc{"skynews": func() {}}

	r.report(record.Progress{User: "skynews", Stage: record.StageCapturing, Segment: "/out/skynews=1.000.ts", Bytes: 2048})
	r.report(record.Progress{User: "skynews", Stage: record.StageConverting, Bytes: 4096})

	got := log.snapshot()
	if len(got) != 2 {
		t.Fatalf("emitted %d statuses, want one per update: %+v", len(got), got)
	}
	if want := (Status{User: "skynews", Phase: PhaseRecording, Detail: "skynews=1.000.ts", Bytes: 2048}); got[0] != want {
		t.Fatalf("capture status = %+v, want %+v", got[0], want)
	}
	if want := (Status{User: "skynews", Phase: PhaseConverting, Bytes: 4096}); got[1] != want {
		t.Fatalf("convert status = %+v, want %+v", got[1], want)
	}
}

func TestReportFallsBackToTheUnnamedTarget(t *testing.T) {
	r := &Runner{}
	log := newStatusLog()
	r.OnStatus(log.observe)
	r.tasks = map[string]context.CancelFunc{"": func() {}}

	r.report(record.Progress{User: "skynews", Stage: record.StageCapturing, Segment: "/out/skynews=1.000.ts"})

	if got := log.snapshot(); len(got) != 1 || got[0].User != "" {
		t.Fatalf("status = %+v, want the unnamed target", got)
	}
}
