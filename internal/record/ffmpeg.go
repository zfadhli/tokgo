package record

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	unlockPoll  = 500 * time.Millisecond
	unlockLimit = 10 * time.Second
	stderrTail  = 2 << 10
)

// Convert remuxes the captured segments into the final mp4 in segment order. A
// nil error means the returned path is the usable recording; the segments and
// the concat list are removed on success. On failure every segment is kept and
// the last one is returned, so no captured byte is lost.
func Convert(ctx context.Context, segments []string, bitrate, ffmpegPath string) (string, error) {
	if len(segments) == 0 {
		return "", errors.New("no capture segments")
	}
	last := segments[len(segments)-1]
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}
	if err := waitReleasable(ctx, segments[0]); err != nil {
		return last, err
	}
	input, concat := segments[0], false
	if len(segments) > 1 {
		list, err := concatList(segments)
		if err != nil {
			return last, err
		}
		defer os.Remove(list)
		input, concat = list, true
	}
	out := outputPath(segments[0])
	cmd := exec.CommandContext(ctx, ffmpegPath, ffmpegArgs(input, out, bitrate, concat)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return last, fmt.Errorf("ffmpeg %s: %w: %s", segments[0], err, tail(output, stderrTail))
	}
	for _, seg := range segments {
		if err := os.Remove(seg); err != nil {
			return "", err
		}
	}
	return out, nil
}

func ffmpegArgs(in, out, bitrate string, concat bool) []string {
	args := []string{"-y"}
	if concat {
		args = append(args, "-f", "concat", "-safe", "0")
	}
	args = append(args, "-i", in)
	if bitrate != "" {
		args = append(args, "-c:v", "libx264", "-b:v", bitrate, "-c:a", "copy")
	} else {
		args = append(args, "-c", "copy")
	}
	return append(args, out)
}

// outputPath maps the first capture segment to the final mp4, dropping the
// per-connection index and the capture container: TK_u_ts.000.ts -> TK_u_ts.mp4.
func outputPath(segment string) string {
	base := strings.TrimSuffix(segment, filepath.Ext(segment))
	if i := strings.LastIndexByte(base, '.'); i >= 0 {
		if _, err := strconv.Atoi(base[i+1:]); err == nil {
			base = base[:i]
		}
	}
	return base + ".mp4"
}

// concatList writes the ffmpeg concat demuxer's list for the segments, in the
// same directory so a relative OutputDir still resolves.
func concatList(segments []string) (string, error) {
	var b strings.Builder
	for _, seg := range segments {
		abs, err := filepath.Abs(seg)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "file '%s'\n", abs)
	}
	f, err := os.CreateTemp(filepath.Dir(segments[0]), "tokgo-concat-*.txt")
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(b.String()); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// waitReleasable gives the recorder time to close the capture before ffmpeg
// reads it. Windows keeps an exclusive lock; the poll covers that case.
func waitReleasable(ctx context.Context, path string) error {
	deadline := time.Now().Add(unlockLimit)
	for {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err == nil {
			f.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s is still locked", path)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(unlockPoll):
		}
	}
}

func tail(b []byte, max int) string {
	s := strings.TrimSpace(string(b))
	if len(s) <= max {
		return s
	}
	return s[len(s)-max:]
}
