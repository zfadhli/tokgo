// Package record captures a live stream to disk and remuxes the result to mp4.
package record

import (
	"context"
	"errors"
	"fmt"
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
	convertTimeout  = 30 * time.Minute
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

// session owns one recording's capture segments. Every stream connection gets
// its own, because a reconnect repeats the stream timestamps: appending its
// bytes into the first file makes ffmpeg drop that connection's video.
type session struct {
	ffmpegPath string
	base       string
	raw        bool // the muxer could not start, so captures go straight to .flv
	next       int
	segments   []string
}

// open starts the sink for the next connection, falling back to a raw file when
// the muxer cannot start. A broken ffmpeg must not cost a single CDN byte.
func (s *session) open() (captureSink, string, error) {
	if !s.raw {
		segment := fmt.Sprintf("%s.%03d.ts", s.base, s.next)
		m, err := startMuxer(s.ffmpegPath, segment)
		if err == nil {
			s.next++
			return m, segment, nil
		}
		logx.Warnf("live muxer unavailable, capturing raw instead: %v", err)
		s.raw = true
	}
	segment := fmt.Sprintf("%s.%03d.flv", s.base, s.next)
	f, err := os.Create(segment)
	if err != nil {
		return nil, "", err
	}
	s.next++
	return &rawFile{f: f}, segment, nil
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

	sess := &session{
		ffmpegPath: r.opts.FFmpegPath,
		base:       filepath.Join(r.opts.OutputDir, "TK_"+user+"_"+start.Format("2006.01.02_15-04-05")),
	}
	var deadline time.Time
	if r.opts.Duration > 0 {
		deadline = start.Add(time.Duration(r.opts.Duration) * time.Second)
	}
	logx.Infof("recording %s to %s", user, sess.base)

	for i, url := range urls {
		if expired(deadline) {
			break
		}
		kept := len(sess.segments)
		written, err := r.capture(ctx, sess, roomID, url, deadline)
		if err != nil {
			var writeErr *writeError
			if !errors.As(err, &writeErr) {
				return "", err
			}
			logx.Errorf("capture write failed, remuxing the segments already on disk: %v", writeErr)
			break
		}
		if written >= minStreamBytes || (written > 0 && ctx.Err() != nil) {
			break
		}
		// A candidate we refuse still wrote its segment, and a truncated
		// segment makes ffmpeg reject the whole concat list.
		discarded := sess.segments[kept:]
		sess.segments = sess.segments[:kept]
		for _, segment := range discarded {
			if err := os.Remove(segment); err != nil && !os.IsNotExist(err) {
				logx.Warnf("could not remove the discarded segment %s: %v", segment, err)
			}
		}
		logx.Warnf("live stream url %d/%d delivered %d bytes, discarded its %d segment(s), trying the next one", i+1, len(urls), written, len(discarded))
	}
	if len(sess.segments) == 0 {
		return "", tiktok.ErrLiveURL
	}

	// Name the count, not a segment path: the segment is removed once the mp4
	// exists, so printing it would point at a file that no longer is there.
	logx.Infof("recording finished: %d segment(s) captured", len(sess.segments))

	// The remux must survive the cancellation that stopped the capture: Ctrl-C
	// is the normal way a recording ends, so ffmpeg gets its own budget.
	convertCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), convertTimeout)
	defer cancel()
	converted, err := Convert(convertCtx, sess.segments, r.opts.Bitrate, r.opts.FFmpegPath)
	if err != nil {
		logx.Errorf("conversion failed, keeping the raw capture: %v", err)
	}
	return converted, nil
}

// capture fills a segment from one candidate url, reconnecting after each clean
// end of stream until the room dies, the deadline passes, or ctx is cancelled.
func (r *Recorder) capture(ctx context.Context, sess *session, roomID, url string, deadline time.Time) (int64, error) {
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

		sink, segment, err := sess.open()
		if err != nil {
			body.Close()
			return written, err
		}
		n, writeErr := copyStream(ctx, sink, body, deadline)
		body.Close()
		written += n
		exitErr := sink.Finish()
		if n > 0 {
			sess.segments = append(sess.segments, segment)
		} else {
			os.Remove(segment)
		}
		switch {
		case exitErr == nil:
		case interrupted(ctx, deadline):
			// Cutting stdin mid-tag makes ffmpeg report a truncated packet on
			// every clean stop. Keep the stderr for post-mortems, not the console.
			logx.Debugf("muxer for %s stopped mid-tag: %v", segment, exitErr)
		case n > 0:
			logx.Warnf("muxer for %s exited with an error, keeping its %d bytes: %v", segment, n, exitErr)
		default:
			logx.Warnf("capture of %s produced nothing: %v", segment, exitErr)
		}
		if writeErr != nil {
			return written, writeErr
		}
		if n == 0 {
			return written, nil
		}
		zeroAttempts = 0
	}
}

// copyStream moves body into sink through one reusable 512 KiB buffer, stopping
// when ctx is cancelled or the deadline passes. The interrupt closes the body and
// the sink. Closing the body alone only releases a pending read; a goroutine
// blocked writing into a stalled ffmpeg pipe would stay blocked forever, voiding
// both the duration cap and Ctrl-C.
func copyStream(ctx context.Context, sink captureSink, body io.ReadCloser, deadline time.Time) (int64, error) {
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
			case <-done:
				return
			}
			body.Close()
			sink.Interrupt()
		}()
	}
	go func() {
		select {
		case <-ctx.Done():
		case <-done:
			return
		}
		body.Close()
		sink.Interrupt()
	}()

	buf := make([]byte, bufferSize)
	var written int64
	for {
		n, readErr := body.Read(buf)
		if n > 0 {
			if _, err := sink.Write(buf[:n]); err != nil {
				if interrupted(ctx, deadline) {
					return written, nil
				}
				return written, &writeError{err: err}
			}
			written += int64(n)
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) && !interrupted(ctx, deadline) {
				logx.Warnf("stream read failed, reconnecting: %v", readErr)
			}
			return written, nil
		}
	}
}

func interrupted(ctx context.Context, deadline time.Time) bool {
	return ctx.Err() != nil || expired(deadline)
}

func expired(deadline time.Time) bool {
	return !deadline.IsZero() && !time.Now().Before(deadline)
}
