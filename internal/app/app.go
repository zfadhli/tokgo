// Package app drives the recording modes: one-shot capture and liveness polling.
// It owns resolution order and retry policy.
package app

import (
	"context"
	"errors"
	"sync"
	"time"

	"tokgo/internal/config"
	"tokgo/internal/logx"
	"tokgo/internal/record"
	"tokgo/internal/telegram"
	"tokgo/internal/tiktok"
	"tokgo/internal/ui"
)

// liveness is the part of the TikTok client the run needs. *tiktok.Client
// satisfies it, and tests substitute a fake to count polls.
type liveness interface {
	RoomIDForUser(context.Context, string) (string, error)
	UserForRoomID(context.Context, string) (string, error)
	UserAndRoomFromURL(context.Context, string) (string, string, error)
	RoomStatus(context.Context, string) (tiktok.RoomStatus, error)
	CountryBlocked(context.Context) (bool, error)
}

// Runner holds the wired dependencies for one program run.
type Runner struct {
	cfg     config.Config
	lookup  liveness       // proxied when a proxy is configured
	stream  *tiktok.Client // always direct, for bulk transfer
	rec     *record.Recorder
	uploads telegram.Config

	mu     sync.Mutex
	idle   *sync.Cond // Broadcast when the task map shrinks to empty
	runCtx context.Context
	tasks  map[string]context.CancelFunc
	errs   map[string]error // per-target result, for the single-target return path
	multi  bool             // more than one target registered in this run
	pool   *pool            // the run's shared poll pacer
	// registered counts every successful launch, so a run only takes the
	// multi-target path once a second target actually exists.
	registered int
	fails      int
	status     func(Status)
	work       func(context.Context, string) (string, error) // the body the current run's targets execute
}

// New wires a runner. The proxy, when set, is used for lookups only: pulling
// video through it would bottleneck the transfer.
func New(cfg config.Config, uploads telegram.Config) *Runner {
	lookup := tiktok.New(cfg.Proxy, cfg.Cookies)
	stream := lookup.Browser()
	r := &Runner{
		cfg:     cfg,
		lookup:  lookup,
		stream:  stream,
		uploads: uploads,
	}
	r.rec = record.New(stream, record.Options{
		OutputDir:  cfg.OutputDir,
		Duration:   cfg.Duration * 60,
		FFmpegPath: cfg.FFmpegPath,
		Report:     r.report,
	})
	return r
}

// Run executes the configured mode for the given users (one user unless the CLI
// was given a comma-separated list) and blocks until done or cancelled.
func (r *Runner) Run(ctx context.Context, users []string) error {
	return r.supervise(ctx, users, r.loop())
}

// loop picks the wait-for-live poll or the single immediate attempt.
func (r *Runner) loop() func(context.Context, string) (string, error) {
	if r.cfg.Once {
		return r.once
	}
	return r.automatic
}

// ErrReported marks failures that were already logged per user, so main can
// exit non-zero without printing the same message twice.
var ErrReported = errors.New("one or more recordings failed")

// resolve fills in whichever of user/roomID the caller did not supply, in the
// reference's order: url first, then door-from-user, then room-from-user.
func (r *Runner) resolve(ctx context.Context, user, roomID string) (string, string, error) {
	if r.cfg.URL != "" {
		u, id, err := r.lookup.UserAndRoomFromURL(ctx, r.cfg.URL)
		if err != nil {
			return "", "", err
		}
		return u, id, nil
	}
	if user == "" && roomID != "" {
		u, err := r.lookup.UserForRoomID(ctx, roomID)
		if err != nil {
			return "", "", err
		}
		user = u
	}
	if roomID == "" {
		id, err := r.lookup.RoomIDForUser(ctx, user)
		if err != nil {
			return "", "", err
		}
		roomID = id
	}
	return user, roomID, nil
}

// countryGuard turns TikTok's login wall into the variant that matches the mode.
func (r *Runner) countryGuard(ctx context.Context) error {
	blocked, err := r.lookup.CountryBlocked(ctx)
	if err != nil {
		return err
	}
	if !blocked {
		return nil
	}
	if r.cfg.Once {
		return tiktok.ErrCountryBlocked
	}
	return tiktok.ErrCountryAuto
}

func (r *Runner) once(ctx context.Context, target string) (string, error) {
	r.emit(Status{User: target, Phase: PhaseResolving})
	user, roomID, err := r.resolve(ctx, target, r.cfg.RoomID)
	if err != nil {
		return "", err
	}
	if err := r.countryGuard(ctx); err != nil {
		return "", err
	}

	status, err := r.lookup.RoomStatus(ctx, roomID)
	if err == nil && !status.Live {
		err = tiktok.ErrNotLive
	}
	if err != nil {
		return "", err
	}

	r.emit(Status{User: target, Phase: PhaseLive, Detail: roomID, RoomID: roomID})
	logLive(user, roomID, status)

	return r.capture(ctx, target, user, roomID)
}

// logLive announces the room, its uptime when TikTok's timestamp is recent, and
// the stream title.
func logLive(user, roomID string, status tiktok.RoomStatus) {
	suffix := ""
	if !status.StartedAt.IsZero() {
		suffix = " · up " + ui.Clock(time.Since(status.StartedAt))
	}
	logx.Infof("@%s · room %s · LIVE%s", user, roomID, suffix)
	if status.Title != "" {
		logx.Infof("title %q", status.Title)
	}
}

// capture records one session and hands it to Telegram when enabled. target is
// the name the run registered, user is what resolution found.
func (r *Runner) capture(ctx context.Context, target, user, roomID string) (string, error) {
	r.emit(Status{User: target, Phase: PhaseRecording, RoomID: roomID})
	path, err := r.rec.Record(ctx, user, roomID)
	if err != nil {
		return "", err
	}
	if !r.cfg.UploadTelegram {
		return path, nil
	}
	if err := telegram.Upload(ctx, r.uploads, path); err != nil {
		logx.Errorf("telegram upload of %s failed: %v", path, err)
	}
	return path, nil
}

// automatic waits for the account to go live, records it, then waits again.
// One poll costs two requests and every target of the run shares one pool, so
// at most one target is polled per tick. A target that stays dark sleeps out
// its backoff before taking a turn, which leaves the pool to the targets that
// are worth a check.
func (r *Runner) automatic(ctx context.Context, target string) (string, error) {
	r.emit(Status{User: target, Phase: PhaseResolving})
	user, _, err := r.resolve(ctx, target, r.cfg.RoomID)
	if err != nil {
		return "", err
	}
	if err := r.countryGuard(ctx); err != nil {
		return "", err
	}

	tick := r.cfg.PollInterval
	pool := r.currentPool()
	file := ""
	misses := 0
	gap := time.Duration(0)
	detail := ""
	for {
		r.emit(Status{User: target, Phase: PhaseWaiting, Detail: detail, Next: time.Now().Add(gap)})
		if gap > 0 && !wait(ctx, "waiting for next stream..", gap) {
			return file, nil
		}
		if !pool.next(ctx) {
			return file, nil
		}
		detail = ""

		roomID, err := r.lookup.RoomIDForUser(ctx, user)
		if err == nil {
			var status tiktok.RoomStatus
			status, err = r.lookup.RoomStatus(ctx, roomID)
			if err == nil && status.Live {
				r.emit(Status{User: target, Phase: PhaseLive, Detail: roomID, RoomID: roomID})
				logLive(user, roomID, status)
				misses, gap = 0, 0
				var path string
				path, err = r.capture(ctx, target, user, roomID)
				if path != "" {
					file = path
				}
				if err == nil {
					if ctx.Err() != nil {
						return file, nil
					}
					continue // a stream that just ended can come back at once
				}
			} else if err == nil {
				err = tiktok.ErrNotLive
			}
		}

		if ctx.Err() != nil {
			return file, nil
		}
		if tiktok.Fatal(err) {
			return file, err
		}
		if tiktok.Transient(err) {
			logx.Infof("@%s: %v", user, err)
		} else {
			logx.Errorf("connection problem: %v", err)
		}
		detail = err.Error()
		misses++
		gap = backoffGap(tick, misses)
	}
}

// currentPool is the poll pacer of the run in flight.
func (r *Runner) currentPool() *pool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pool
}

// wait shows the poll or the retry on the spinner for the whole sleep.
func wait(ctx context.Context, label string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	sp := logx.Spin(label, func() string {
		return ui.Duration(time.Until(deadline)) + " left"
	})
	ok := sleep(ctx, d)
	sp.Stop()
	return ok
}

func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
