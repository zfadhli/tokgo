// Package app drives the recording modes: one-shot capture and liveness polling.
// It owns resolution order and retry policy.
package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"tokgo/internal/config"
	"tokgo/internal/logx"
	"tokgo/internal/record"
	"tokgo/internal/telegram"
	"tokgo/internal/tiktok"
)

const (
	connectionBackoff = 2 * time.Minute
	streamStartGap    = 2500 * time.Millisecond
)

// Runner holds the wired dependencies for one program run.
type Runner struct {
	cfg     config.Config
	lookup  *tiktok.Client // proxied when a proxy is configured
	stream  *tiktok.Client // always direct, for bulk transfer
	rec     *record.Recorder
	uploads telegram.Config
}

// New wires a runner. The proxy, when set, is used for lookups only: pulling
// video through it would bottleneck the transfer.
func New(cfg config.Config, uploads telegram.Config) *Runner {
	lookup := tiktok.New(cfg.Proxy, cfg.Cookies)
	stream := lookup.Browser()
	return &Runner{
		cfg:    cfg,
		lookup: lookup,
		stream: stream,
		rec: record.New(stream, record.Options{
			OutputDir:  cfg.OutputDir,
			Duration:   cfg.Duration * 60,
			FFmpegPath: cfg.FFmpegPath,
		}),
		uploads: uploads,
	}
}

// Run executes the configured mode for the given users (one user unless the CLI
// was given a comma-separated list) and blocks until done or cancelled.
func (r *Runner) Run(ctx context.Context, users []string) error {
	if len(users) > 1 {
		return r.parallel(ctx, users)
	}
	user := ""
	if len(users) > 0 {
		user = users[0]
	}
	if err := r.loop()(ctx, user); err != nil && user != "" {
		return fmt.Errorf("@%s: %w", user, err)
	} else if err != nil {
		return err
	}
	return nil
}

// loop picks the wait-for-live poll or the single immediate attempt.
func (r *Runner) loop() func(context.Context, string) error {
	if r.cfg.Once {
		return r.once
	}
	return r.automatic
}

// ErrReported marks failures that were already logged per user, so main can
// exit non-zero without printing the same message twice.
var ErrReported = errors.New("one or more recordings failed")

func (r *Runner) parallel(ctx context.Context, users []string) error {
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		fails int
	)
	for _, user := range users {
		wg.Add(1)
		go func(user string) {
			defer wg.Done()
			if err := r.loop()(ctx, user); err != nil && ctx.Err() == nil {
				logx.Errorf("@%s: %v", user, err)
				mu.Lock()
				fails++
				mu.Unlock()
			}
		}(user)
	}
	wg.Wait()
	if fails > 0 {
		return ErrReported
	}
	return nil
}

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

func (r *Runner) once(ctx context.Context, user string) error {
	user, roomID, err := r.resolve(ctx, user, r.cfg.RoomID)
	if err != nil {
		return err
	}
	if err := r.countryGuard(ctx); err != nil {
		return err
	}

	logx.Infof("username: @%s", user)
	logx.Infof("room_id:  %s", roomID)

	alive, err := r.lookup.RoomAlive(ctx, roomID)
	if err == nil && !alive {
		err = tiktok.ErrNotLive
	}
	if err != nil {
		return err
	}

	return r.capture(ctx, user, roomID)
}

// capture records one session and hands it to Telegram when enabled.
func (r *Runner) capture(ctx context.Context, user, roomID string) error {
	path, err := r.rec.Record(ctx, user, roomID)
	if err != nil {
		return err
	}
	logx.Infof("saved %s", path)
	if !r.cfg.UploadTelegram {
		return nil
	}
	if err := telegram.Upload(ctx, r.uploads, path); err != nil {
		logx.Errorf("telegram upload of %s failed: %v", path, err)
	}
	return nil
}

func (r *Runner) automatic(ctx context.Context, user string) error {
	user, _, err := r.resolve(ctx, user, r.cfg.RoomID)
	if err != nil {
		return err
	}
	if err := r.countryGuard(ctx); err != nil {
		return err
	}

	interval := time.Duration(r.cfg.AutomaticInterval) * time.Minute
	for {
		roomID, err := r.lookup.RoomIDForUser(ctx, user)
		if err == nil {
			var alive bool
			alive, err = r.lookup.RoomAlive(ctx, roomID)
			if err == nil && alive {
				logx.Infof("username: @%s", user)
				logx.Infof("room_id:  %s", roomID)
				err = r.capture(ctx, user, roomID)
			} else if err == nil {
				err = tiktok.ErrNotLive
			}
		}

		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if tiktok.Transient(err) {
				logx.Infof("@%s: %v", user, err)
				logx.Infof("waiting %d minutes before rechecking", r.cfg.AutomaticInterval)
				if !sleep(ctx, interval) {
					return nil
				}
				continue
			}
			if tiktok.Fatal(err) {
				return err
			}
			logx.Errorf("connection problem: %v", err)
			if !sleep(ctx, connectionBackoff) {
				return nil
			}
			continue
		}

		logx.Infof("waiting %d minutes before rechecking", r.cfg.AutomaticInterval)
		if !sleep(ctx, interval) {
			return nil
		}
	}
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
