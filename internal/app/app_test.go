package app

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"tokgo/internal/logx"
	"tokgo/internal/tiktok"
)

// captureInfo diverts console log lines into the returned slice.
func captureInfo(t *testing.T) *[]string {
	t.Helper()
	var lines []string
	logx.SetSink(func(level, text string) {
		if level == "INFO" {
			lines = append(lines, text)
		}
	})
	t.Cleanup(func() { logx.SetSink(nil) })
	return &lines
}

func TestLogLiveCarriesUptimeAndTitle(t *testing.T) {
	lines := captureInfo(t)

	logLive("skynews", "7702058376456391427", tiktok.RoomStatus{
		Live:      true,
		StartedAt: time.Now().Add(-90 * time.Second),
		Title:     "Your budget questions answered",
	})

	if len(*lines) != 2 {
		t.Fatalf("logged %d lines, want the room line and the title: %q", len(*lines), *lines)
	}
	const head = "@skynews · room 7702058376456391427 · LIVE · up "
	if !strings.HasPrefix((*lines)[0], head) {
		t.Fatalf("room line = %q, want the %q prefix", (*lines)[0], head)
	}
	if clock := strings.TrimPrefix((*lines)[0], head); !regexp.MustCompile(`^0[12]:\d\d$`).MatchString(clock) {
		t.Errorf("uptime = %q, want the 90s probe as mm:ss", clock)
	}
	if want := `title "Your budget questions answered"`; (*lines)[1] != want {
		t.Errorf("title line = %q, want %q", (*lines)[1], want)
	}
}

func TestLogLiveOmitsMissingUptimeAndEmptyTitle(t *testing.T) {
	lines := captureInfo(t)

	logLive("skynews", "7702058376456391427", tiktok.RoomStatus{Live: true})

	if len(*lines) != 1 {
		t.Fatalf("logged %d lines, want only the room line: %q", len(*lines), *lines)
	}
	if want := "@skynews · room 7702058376456391427 · LIVE"; (*lines)[0] != want {
		t.Errorf("room line = %q, want %q", (*lines)[0], want)
	}
}
