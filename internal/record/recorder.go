// Package record captures a live stream to disk and remuxes the result to mp4.
package record

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"tokgo/internal/logx"
	"tokgo/internal/tiktok"
)

const (
	bufferSize      = 512 << 10
	minStreamBytes  = 4096
	maxZeroAttempts = 5
	retryDelay      = 2 * time.Second
)

// API is the slice of the TikTok client a recording needs. Declared here, on
// the consumer side, so tests can feed canned streams.
type API interface {
	RoomAlive(ctx context.Context, roomID string) (bool, error)
	LiveURLs(ctx context.Context, roomID, user string) ([]string, error)
	OpenStream(ctx context.Context, url string) (io.ReadCloser, error)
}

// Options carries the recording knobs resolved from config.Config.
type Options struct {
	OutputDir  string
	Duration   int // seconds, 0 means no cap
	Bitrate    string
	FFmpegPath string
}

// Recorder writes one live session per Record call.
type Recorder struct {
	api  API
	opts Options
}

// New builds a Recorder. The caller owns the API implementation.
func New(api API, opts Options) *Recorder {
	return &Recorder{api: api, opts: opts}
}

// Record captures one live session and returns the final mp4 path.
func (r *Recorder) Record(ctx context.Context, user, roomID string) (string, error) {
	start := time.Now()
	urls, err := r.api.LiveURLs(ctx, roomID, user)
	if err != nil {
		return "", err
	}
	if len(urls) == 0 {
		return "", tiktok.ErrLiveURL
	}

	out := filepath.Join(r.opts.OutputDir, "TK_"+user+"_"+start.Format("2006.01.02_15-04-05")+"_flv.mp4")
	var deadline time.Time
	if r.opts.Duration > 0 {
		deadline = start.Add(time.Duration(r.opts.Duration) * time.Second)
	}
	logx.Infof("recording %s to %s", user, out)

	var (
		file    *os.File
		written int64
	)
	for i, url := range urls {
		if expired(deadline) {
			break
		}
		f, err := os.Create(out)
		if err != nil {
			return "", err
		}
		written, err = r.capture(ctx, f, roomID, url, deadline)
		if err != nil {
			f.Close()
			return "", err
		}
		if written >= minStreamBytes || (written > 0 && ctx.Err() != nil) {
			file = f
			break
		}
		logx.Warnf("live stream url %d/%d delivered %d bytes, trying the next one", i+1, len(urls), written)
		f.Close()
	}
	if file == nil {
		os.Remove(out)
		return "", tiktok.ErrLiveURL
	}
	if err := file.Close(); err != nil {
		return "", err
	}

	path, err := filepath.Abs(out)
	if err != nil {
		path = out
	}
	logx.Infof("recording finished: %s", path)

	converted, err := Convert(ctx, out, r.opts.Bitrate, r.opts.FFmpegPath)
	if err != nil {
		logx.Errorf("conversion failed, keeping the raw capture: %v", err)
		return out, nil
	}
	return converted, nil
}

// capture fills f from one candidate url, reconnecting after each clean end of
// stream until the room dies, the deadline passes, or ctx is cancelled.
func (r *Recorder) capture(ctx context.Context, f *os.File, roomID, url string, deadline time.Time) (int64, error) {
	var written int64
	zeroAttempts := 0
	for {
		if ctx.Err() != nil || expired(deadline) {
			return written, nil
		}
		alive, err := r.api.RoomAlive(ctx, roomID)
		if ctx.Err() != nil {
			return written, nil
		}
		if (err == nil && !alive) || errors.Is(err, tiktok.ErrNotLive) {
			logx.Infof("user is no longer live, stopping recording")
			return written, nil
		}
		if err != nil {
			logx.Warnf("liveness check failed, keeping the capture: %v", err)
		}

		body, err := r.api.OpenStream(ctx, url)
		if err != nil {
			if ctx.Err() != nil {
				return written, nil
			}
			zeroAttempts++
			logx.Warnf("stream open failed (attempt %d/%d): %v", zeroAttempts, maxZeroAttempts, err)
			if zeroAttempts >= maxZeroAttempts {
				return written, nil
			}
			select {
			case <-ctx.Done():
				return written, nil
			case <-time.After(retryDelay):
			}
			continue
		}
		n, err := copyStream(ctx, f, body, deadline)
		body.Close()
		written += n
		if err != nil {
			return written, err
		}
		if n == 0 {
			return written, nil
		}
		zeroAttempts = 0
	}
}

// copyStream moves body into f through one reusable 512 KiB buffer, stopping
// when ctx is cancelled or the deadline passes. Cancellation closes the body,
// which unblocks the pending read without dropping bytes already written.
func copyStream(ctx context.Context, f io.Writer, body io.ReadCloser, deadline time.Time) (int64, error) {
	done := make(chan struct{})
	defer close(done)

	if !deadline.IsZero() {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return 0, nil
		}
		timer := time.NewTimer(remaining)
		defer timer.Stop()
		go func() {
			select {
			case <-timer.C:
				body.Close()
			case <-done:
			}
		}()
	}
	go func() {
		select {
		case <-ctx.Done():
			body.Close()
		case <-done:
		}
	}()

	n, err := io.CopyBuffer(f, body, make([]byte, bufferSize))
	interrupted := ctx.Err() != nil || expired(deadline)
	if err != nil && !interrupted && !errors.Is(err, io.EOF) {
		logx.Warnf("stream read failed, reconnecting: %v", err)
	}
	return n, nil
}

func expired(deadline time.Time) bool {
	return !deadline.IsZero() && !time.Now().Before(deadline)
}
