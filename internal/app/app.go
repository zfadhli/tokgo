// Package app drives the recording modes: one-shot capture, liveness polling,
// and follow-everyone capture. It owns resolution order and retry policy.
package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
			Duration:   cfg.Duration,
			Bitrate:    cfg.Bitrate,
			FFmpegPath: cfg.FFmpegPath,
		}),
		uploads: uploads,
	}
}

// Run executes the configured mode for the given users (one user unless the CLI
// was given a comma-separated list) and blocks until done or cancelled.
func (r *Runner) Run(ctx context.Context, users []string) error {
	if r.cfg.Mode == config.Followers {
		return r.followers(ctx)
	}
	if len(users) > 1 {
		return r.parallel(ctx, users)
	}
	user := ""
	if len(users) > 0 {
		user = users[0]
	}
	if r.cfg.Mode == config.Automatic {
		return r.automatic(ctx, user)
	}
	return r.manual(ctx, user)
}

func (r *Runner) parallel(ctx context.Context, users []string) error {
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	for _, user := range users {
		wg.Add(1)
		go func(user string) {
			defer wg.Done()
			var err error
			if r.cfg.Mode == config.Automatic {
				err = r.automatic(ctx, user)
			} else {
				err = r.manual(ctx, user)
			}
			if err != nil && ctx.Err() == nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("@%s: %w", user, err))
				mu.Unlock()
			}
		}(user)
	}
	wg.Wait()
	for _, err := range errs {
		logx.Errorf("%v", err)
	}
	return errors.Join(errs...)
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
	switch r.cfg.Mode {
	case config.Followers:
		return tiktok.ErrCountryFollow
	case config.Automatic:
		return tiktok.ErrCountryAuto
	default:
		return tiktok.ErrCountryBlocked
	}
}

func (r *Runner) manual(ctx context.Context, user string) error {
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
		return fmt.Errorf("@%s: %w", user, err)
	}

	return r.capture(ctx, user, roomID)
}

// capture records one session and hands it to Telegram when enabled.
func (r *Runner) capture(ctx context.Context, user, roomID string) error {
	path, err := r.rec.Record(ctx, user, roomID)
	if err != nil {
		return err
	}
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

func (r *Runner) followers(ctx context.Context) error {
	secUID, err := r.lookup.SecUID(ctx)
	if err != nil {
		return err
	}
	if secUID == "" {
		return errors.New("failed to retrieve sec_uid from the logged-out TikTok session, add cookies.json")
	}
	if err := r.countryGuard(ctx); err != nil {
		return err
	}
	logx.Infof("followers mode activated")

	active := newRegistry()
	interval := time.Duration(r.cfg.AutomaticInterval) * time.Minute

	for {
		followers, err := r.lookup.Followers(ctx, secUID)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			logx.Errorf("followers list: %v", err)
			if !sleep(ctx, interval) {
				return nil
			}
			continue
		}

		for _, follower := range followers {
			if ctx.Err() != nil {
				return nil
			}
			if active.running(follower) {
				continue
			}

			roomID, err := r.lookup.RoomIDForUser(ctx, follower)
			if err != nil || roomID == "" {
				continue
			}
			alive, err := r.lookup.RoomAlive(ctx, roomID)
			if err != nil || !alive {
				continue
			}

			logx.Infof("@%s is live, starting recording", follower)
			childCtx, cancel := context.WithCancel(ctx)
			active.start(follower, cancel)
			go func() {
				defer func() {
					cancel()
					active.finish(follower)
				}()
				if err := r.capture(childCtx, follower, roomID); err != nil && childCtx.Err() == nil {
					logx.Errorf("@%s: %v", follower, err)
				}
				logx.Infof("recording of @%s finished", follower)
			}()

			if !sleep(ctx, streamStartGap) {
				return nil
			}
		}

		logx.Infof("waiting %d minutes for the next followers check", r.cfg.AutomaticInterval)
		if !sleep(ctx, interval) {
			return nil
		}
	}
}

// registry tracks which followers already have a recording in flight. Capture
// goroutines mark themselves done from their own goroutine, so every access is
// guarded.
type registry struct {
	mu sync.Mutex
	in map[string]context.CancelFunc
}

func newRegistry() *registry { return &registry{in: map[string]context.CancelFunc{}} }

func (g *registry) running(follower string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, busy := g.in[strings.ToLower(follower)]
	return busy
}

func (g *registry) start(follower string, cancel context.CancelFunc) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.in[strings.ToLower(follower)] = cancel
}

func (g *registry) finish(follower string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.in, strings.ToLower(follower))
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
