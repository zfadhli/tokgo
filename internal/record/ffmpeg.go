package record

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	unlockPoll  = 500 * time.Millisecond
	unlockLimit = 10 * time.Second
	stderrTail  = 2 << 10
)

// Convert remuxes the captured file into the final mp4. A nil error means the
// returned path is the usable recording; the raw capture is removed on success.
func Convert(ctx context.Context, in, bitrate, ffmpegPath string) (string, error) {
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}
	if err := waitReleasable(ctx, in); err != nil {
		return "", err
	}
	out := strings.Replace(in, "_flv.mp4", ".mp4", 1)
	cmd := exec.CommandContext(ctx, ffmpegPath, ffmpegArgs(in, out, bitrate)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("ffmpeg %s: %w: %s", in, err, tail(output, stderrTail))
	}
	if err := os.Remove(in); err != nil {
		return "", err
	}
	return out, nil
}

func ffmpegArgs(in, out, bitrate string) []string {
	args := []string{"-y", "-i", in}
	if bitrate != "" {
		args = append(args, "-c:v", "libx264", "-b:v", bitrate, "-c:a", "copy")
	} else {
		args = append(args, "-c", "copy")
	}
	return append(args, out)
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
