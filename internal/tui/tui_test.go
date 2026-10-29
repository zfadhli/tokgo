package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"tokgo/internal/telegram"
)

func newModel() *Model {
	return New("test", telegram.Config{}, nil, make(chan logLine))
}

// TestArgv checks the form renders the same flags the CLI accepts.
func TestArgv(t *testing.T) {
	cases := []struct {
		name   string
		mode   int
		target string
		dur    string
		out    string
		proxy  string
		upload bool
		want   []string
	}{
		{
			name:   "username waits for live by default",
			target: "@aljazeeraenglish",
			dur:    "60",
			want:   []string{"-user", "@aljazeeraenglish", "-duration", "60"},
		},
		{
			name:   "room id wins over username shape",
			target: "7123456789012345678",
			dur:    "0",
			want:   []string{"-room_id", "7123456789012345678", "-duration", "0"},
		},
		{
			name:   "url",
			target: "https://www.tiktok.com/@user/live",
			dur:    "0",
			want:   []string{"-url", "https://www.tiktok.com/@user/live", "-duration", "0"},
		},
		{
			name:   "record once carries the rest of the form",
			mode:   1,
			target: "@someone",
			dur:    "0",
			upload: true,
			out:    "/tmp/rec",
			proxy:  "socks5://127.0.0.1:1080",
			want: []string{"-once", "-user", "@someone", "-duration", "0", "-output", "/tmp/rec",
				"-proxy", "socks5://127.0.0.1:1080", "-telegram"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newModel()
			m.mode = tc.mode
			m.inputs[0].SetValue(tc.target)
			m.inputs[1].SetValue(tc.dur)
			m.inputs[2].SetValue(tc.out)
			m.inputs[3].SetValue(tc.proxy)
			m.upload = tc.upload
			if got := strings.Join(m.argv(), " "); got != strings.Join(tc.want, " ") {
				t.Fatalf("argv\n got %q\nwant %q", got, strings.Join(tc.want, " "))
			}
		})
	}
}

// TestStartRejectsEmptyTarget proves the form defers to cli.Parse instead of
// reimplementing validation.
func TestStartRejectsEmptyTarget(t *testing.T) {
	m := newModel()
	m.inputs[1].SetValue("0")
	m.start()
	if m.err == nil || !strings.Contains(m.err.Error(), "missing -url, -user or -room_id") {
		t.Fatalf("want the CLI validation error, got %v", m.err)
	}
	if m.screen != screenForm {
		t.Fatalf("a rejected form must stay on the form, got screen %v", m.screen)
	}
}

func TestFormViewMentionsMode(t *testing.T) {
	m := newModel()
	m.width, m.height = 100, 30
	v := m.View()
	for _, want := range []string{"tokgo", "wait for live", "record once", "Target", "Start"} {
		if !strings.Contains(v, want) {
			t.Fatalf("form view missing %q:\n%s", want, v)
		}
	}
}

func TestLogTailKeepsNewestAndPads(t *testing.T) {
	m := newModel()
	m.width, m.height = 60, 24
	for i := 0; i < 20; i++ {
		m.logs = append(m.logs, logLine{level: "INFO", text: strings.Repeat("x", 200)})
	}
	m.logs = append(m.logs, logLine{level: "ERROR", text: "the newest line"})
	tail := m.logTail(3)
	if len(tail) != 3 {
		t.Fatalf("want 3 rows, got %d", len(tail))
	}
	if !strings.Contains(strings.Join(tail, "\n"), "the newest line") {
		t.Fatal("tail dropped the newest line")
	}
	for _, row := range tail {
		if n := len([]rune(row)); n > 60 {
			t.Fatalf("row wider than the terminal: %d > 60", n)
		}
	}
}

func TestDoneViewDistinguishesCancelFromFailure(t *testing.T) {
	m := newModel()
	m.screen = screenDone
	m.width, m.height = 80, 24
	m.result = context.Canceled
	if v := m.View(); !strings.Contains(v, "stopped") {
		t.Fatalf("cancel should read as stopped:\n%s", v)
	}
	m.result = errors.New("login required")
	if v := m.View(); !strings.Contains(v, "login required") {
		t.Fatalf("failure should show its reason:\n%s", v)
	}
}

func TestElapsedFormat(t *testing.T) {
	for _, tc := range []struct {
		secs int
		want string
	}{{0, "00:00"}, {61, "01:01"}, {3661, "1:01:01"}} {
		if got := elapsed(time.Duration(tc.secs) * time.Second); got != tc.want {
			t.Fatalf("%ds: got %q want %q", tc.secs, got, tc.want)
		}
	}
}
