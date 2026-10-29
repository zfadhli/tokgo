package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	"tokgo/internal/logx"
	"tokgo/internal/record"
)

// Phase is the lifecycle stage of one target's run.
type Phase int

const (
	PhaseQueued     Phase = iota // added, goroutine not started yet
	PhaseResolving               // turning url / room id / username into a room
	PhaseWaiting                 // polling, waiting for the next live stream
	PhaseLive                    // live confirmed, capture about to start
	PhaseRecording               // bytes are reaching the disk
	PhaseConverting              // remuxing the captured segments to mp4
	PhaseDone                    // finished cleanly
	PhaseStopped                 // ended because the run was cancelled
	PhaseFailed                  // ended with an error
)

// String names the phase the way the UI shows it. Remuxing is the name for the
// converting stage: it is the operation, not the format.
func (p Phase) String() string {
	switch p {
	case PhaseQueued:
		return "queued"
	case PhaseResolving:
		return "resolving"
	case PhaseWaiting:
		return "waiting"
	case PhaseLive:
		return "live"
	case PhaseRecording:
		return "recording"
	case PhaseConverting:
		return "remuxing"
	case PhaseDone:
		return "done"
	case PhaseStopped:
		return "stopped"
	case PhaseFailed:
		return "failed"
	}
	return fmt.Sprintf("Phase(%d)", int(p))
}

// Active reports whether the phase is in flight. A queued target has not
// started and the three terminal phases are over.
func (p Phase) Active() bool {
	return p >= PhaseResolving && p <= PhaseConverting
}

// Status is one observation about one target. Every field except User and Phase
// may be empty; a consumer must treat each Status as a full snapshot of what it
// shows, not a delta.
type Status struct {
	User   string // username without the leading @
	Phase  Phase
	Detail string // short note: room id, title, reason, or the error text
	RoomID string
	File   string // output path, once capture has produced one
	Bytes  int64  // bytes captured during the current capture
}

var (
	// ErrNotRunning reports a target change with no run in flight.
	ErrNotRunning = errors.New("no run in progress")
	// ErrUserRunning reports a target that this run already holds.
	ErrUserRunning = errors.New("user is already a target")
	// ErrURLRun reports a target added to a url run, which has exactly one.
	ErrURLRun = errors.New("a url run has a single target")
)

// OnStatus installs the observer for per-target status updates. It is safe to
// call before Run, and a nil f clears the observer. The observer is never called
// while the runner holds its mutex, so it may block briefly.
func (r *Runner) OnStatus(f func(Status)) {
	r.mu.Lock()
	r.status = f
	r.mu.Unlock()
}

// AddUser starts one more target while Run is in flight. It applies only to
// username runs: a url run resolves every target through its url, so it has
// exactly one target by construction.
func (r *Runner) AddUser(user string) error {
	if r.cfg.URL != "" {
		return ErrURLRun
	}
	return r.launch(user)
}

// DropUser cancels one target's run without touching the others. It reports
// whether the user was a target of the current run.
func (r *Runner) DropUser(user string) bool {
	r.mu.Lock()
	cancel, ok := r.tasks[user]
	r.mu.Unlock()
	if !ok {
		return false
	}
	cancel()
	return true
}

// supervise registers every target, drains them, and turns their outcomes into
// the error the caller of Run sees. work is the body one target runs.
func (r *Runner) supervise(ctx context.Context, users []string, work func(context.Context, string) (string, error)) error {
	if len(users) == 0 {
		users = []string{""}
	}
	r.mu.Lock()
	r.runCtx = ctx
	r.work = work
	r.idle = sync.NewCond(&r.mu)
	r.tasks = make(map[string]context.CancelFunc)
	r.errs = make(map[string]error)
	r.multi = false
	r.registered = 0
	r.fails = 0
	r.mu.Unlock()

	seen := make(map[string]struct{}, len(users))
	for _, user := range users {
		if _, dup := seen[user]; dup {
			continue
		}
		seen[user] = struct{}{}
		if err := r.launch(user); err != nil {
			r.mu.Lock()
			r.errs[user] = err
			r.fails++
			r.mu.Unlock()
		}
	}

	r.mu.Lock()
	for len(r.tasks) > 0 {
		r.idle.Wait()
	}
	errs, multi, fails := r.errs, r.multi, r.fails
	r.runCtx = nil
	r.work = nil
	r.tasks = nil
	r.errs = nil
	r.idle = nil
	r.mu.Unlock()

	if multi {
		if fails > 0 {
			for user, err := range errs {
				logx.Errorf("@%s: %v", user, err)
			}
			return ErrReported
		}
		return nil
	}
	for user, err := range errs {
		if err == nil {
			continue
		}
		if user != "" {
			return fmt.Errorf("@%s: %w", user, err)
		}
		return err
	}
	return nil
}

// launch registers one target and starts its goroutine. The registration count
// is what turns the run multi, so a target added mid-run flips it once and a
// target that never registered cannot.
func (r *Runner) launch(user string) error {
	r.mu.Lock()
	if r.runCtx == nil {
		r.mu.Unlock()
		return ErrNotRunning
	}
	if _, ok := r.tasks[user]; ok {
		r.mu.Unlock()
		return ErrUserRunning
	}
	ctx, cancel := context.WithCancel(r.runCtx)
	r.tasks[user] = cancel
	r.registered++
	r.multi = r.registered > 1
	work, status := r.work, r.status
	r.mu.Unlock()

	if status != nil {
		status(Status{User: user, Phase: PhaseQueued})
	}
	go r.target(ctx, user, work)
	return nil
}

// target is one target goroutine. Its map deletion is the last thing it does,
// so a non-empty task map always means a live goroutine.
func (r *Runner) target(ctx context.Context, user string, work func(context.Context, string) (string, error)) {
	file, err := work(ctx, user)
	r.emit(terminal(user, ctx, file, err))

	r.mu.Lock()
	logged := err != nil && ctx.Err() == nil && r.multi
	r.mu.Unlock()
	if logged {
		logx.Errorf("@%s: %v", user, err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tasks, user)
	if err != nil {
		if ctx.Err() == nil {
			r.fails++
		}
		// A cancelled failure never reaches the multi-target log. Every other
		// failure is kept until Run decides whether it was single or multi.
		switch {
		case logged:
		case ctx.Err() != nil && r.multi:
		default:
			r.errs[user] = err
		}
	}
	r.idle.Broadcast()
}

// terminal is the last status of a target. A cancelled run is stopped even when
// its loop chose to return no error.
func terminal(user string, ctx context.Context, file string, err error) Status {
	s := Status{User: user, Phase: PhaseDone, File: file}
	switch {
	case ctx.Err() != nil:
		s.Phase = PhaseStopped
	case err != nil:
		s.Phase = PhaseFailed
		s.Detail = err.Error()
	}
	return s
}

// emit delivers one status without holding the runner mutex.
func (r *Runner) emit(s Status) {
	r.mu.Lock()
	f := r.status
	r.mu.Unlock()
	if f != nil {
		f(s)
	}
}

// report turns one recorder update into a status for the target that owns the
// capture. A url or room id target is registered without a username, so an
// unknown user falls back to that unnamed target.
func (r *Runner) report(p record.Progress) {
	user := p.User
	r.mu.Lock()
	if _, ok := r.tasks[user]; !ok {
		if _, unnamed := r.tasks[""]; unnamed {
			user = ""
		}
	}
	r.mu.Unlock()

	phase, detail := PhaseRecording, filepath.Base(p.Segment)
	if p.Stage == record.StageConverting {
		phase, detail = PhaseConverting, ""
	}
	r.emit(Status{User: user, Phase: phase, Detail: detail, Bytes: p.Bytes})
}
