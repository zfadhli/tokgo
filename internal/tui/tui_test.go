package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"tokgo/internal/app"
	"tokgo/internal/cli"
	"tokgo/internal/config"
	"tokgo/internal/telegram"
)

func newModel() *Model {
	m := New("test", telegram.Config{}, nil, make(chan event))
	m.width, m.height = 80, 24
	m.now = time.Unix(1700000000, 0)
	return m
}

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

// fakeCtrl records what the monitor asks the runner to do.
type fakeCtrl struct {
	added   []string
	dropped []string
	err     error
}

func (f *fakeCtrl) AddUser(user string) error {
	if f.err != nil {
		return f.err
	}
	f.added = append(f.added, user)
	return nil
}

func (f *fakeCtrl) DropUser(user string) bool {
	f.dropped = append(f.dropped, user)
	return true
}

// TestArgv checks the form renders the flags the CLI accepts.
func TestArgv(t *testing.T) {
	cases := []struct {
		name     string
		mode     int
		targets  string
		dur      string
		interval string
		out      string
		proxy    string
		upload   bool
		want     []string
	}{
		{
			name:     "username waits for live by default",
			targets:  "@aljazeeraenglish",
			dur:      "0",
			interval: "3",
			want:     []string{"-user", "@aljazeeraenglish", "-duration", "0"},
		},
		{
			name:     "multi target list",
			targets:  "a, @b c",
			dur:      "0",
			interval: "3",
			want:     []string{"-user", "a,b,c", "-duration", "0"},
		},
		{
			name:     "room id wins over username shape",
			targets:  "7123456789012345678",
			dur:      "0",
			interval: "3",
			want:     []string{"-room_id", "7123456789012345678", "-duration", "0"},
		},
		{
			name:     "url",
			targets:  "https://www.tiktok.com/@user/live",
			dur:      "0",
			interval: "3",
			want:     []string{"-url", "https://www.tiktok.com/@user/live", "-duration", "0"},
		},
		{
			name:     "record once carries the rest of the form",
			mode:     1,
			targets:  "@someone",
			dur:      "0",
			interval: "5",
			upload:   true,
			out:      "/tmp/rec",
			proxy:    "socks5://127.0.0.1:1080",
			want: []string{"-once", "-user", "@someone", "-duration", "0", "-automatic_interval", "5",
				"-output", "/tmp/rec", "-proxy", "socks5://127.0.0.1:1080", "-telegram"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newModel()
			m.mode = tc.mode
			m.inputs[0].SetValue(tc.targets)
			m.inputs[1].SetValue(tc.dur)
			m.inputs[2].SetValue(tc.interval)
			m.inputs[3].SetValue(tc.out)
			m.inputs[4].SetValue(tc.proxy)
			m.upload = tc.upload
			if got := strings.Join(m.argv(), " "); got != strings.Join(tc.want, " ") {
				t.Fatalf("argv\n got %q\nwant %q", got, strings.Join(tc.want, " "))
			}
		})
	}
}

// TestMultiTargetArgvParses proves a space separated field still becomes a
// username list, because cli.Parse only splits on commas.
func TestMultiTargetArgvParses(t *testing.T) {
	m := newModel()
	m.inputs[0].SetValue("a, @b c")
	parsed, err := cli.Parse(m.argv())
	if err != nil {
		t.Fatalf("parse %q: %v", m.argv(), err)
	}
	if got := strings.Join(parsed.Users, ","); got != "a,b,c" {
		t.Fatalf("users: got %q want %q", got, "a,b,c")
	}
}

// TestStartRejectsEmptyTarget proves the form defers to cli.Parse instead of
// reimplementing validation.
func TestStartRejectsEmptyTarget(t *testing.T) {
	m := newModel()
	m.start()
	if m.err == nil || !strings.Contains(m.err.Error(), "missing -url, -user or -room_id") {
		t.Fatalf("want the CLI validation error, got %v", m.err)
	}
	if m.screen != screenSetup {
		t.Fatalf("a rejected form must stay on the form, got screen %v", m.screen)
	}
}

func TestSetupViewShowsTheForm(t *testing.T) {
	m := newModel()
	m.inputs[0].SetValue("a, @b")
	v := m.View()
	for _, want := range []string{"tokgo", "wait for live", "record once", "Targets", "Start", "2 targets: @a @b"} {
		if !strings.Contains(v, want) {
			t.Fatalf("setup view missing %q:\n%s", want, v)
		}
	}
}

// TestApplyStatusKeepsTargetsApart is the core merge rule: one status only
// touches its own row.
func TestApplyStatusKeepsTargetsApart(t *testing.T) {
	m := newModel()
	m.applyStatus(app.Status{User: "a", Phase: app.PhaseWaiting})
	m.applyStatus(app.Status{User: "b", Phase: app.PhaseResolving})
	m.applyStatus(app.Status{User: "a", Phase: app.PhaseRecording, RoomID: "111", Bytes: 2048, File: "/out/a.mp4"})
	m.applyStatus(app.Status{User: "b", Phase: app.PhaseRecording, Bytes: 10, File: "/out/b.mp4"})

	if len(m.targets) != 2 {
		t.Fatalf("want 2 rows, got %d", len(m.targets))
	}
	a, b := m.targets[m.byName["a"]], m.targets[m.byName["b"]]
	if a.phase != app.PhaseRecording || b.phase != app.PhaseRecording {
		t.Fatalf("phases: a=%v b=%v", a.phase, b.phase)
	}
	if a.bytes != 2048 || b.bytes != 10 {
		t.Fatalf("bytes: a=%d b=%d", a.bytes, b.bytes)
	}
	if a.file != "/out/a.mp4" || b.file != "/out/b.mp4" {
		t.Fatalf("files: a=%q b=%q", a.file, b.file)
	}
	if a.room != "111" || b.room != "" {
		t.Fatalf("rooms: a=%q b=%q", a.room, b.room)
	}
}

func TestRecordingRestartsTheByteCount(t *testing.T) {
	m := newModel()
	m.applyStatus(app.Status{User: "a", Phase: app.PhaseRecording, Bytes: 2048})
	m.now = m.now.Add(time.Minute)
	m.applyStatus(app.Status{User: "a", Phase: app.PhaseConverting})
	m.applyStatus(app.Status{User: "a", Phase: app.PhaseRecording, Bytes: 512})
	if got := m.targets[0].bytes; got != 512 {
		t.Fatalf("a new capture must restart the size, got %d", got)
	}
}

func TestTerminalStatusKeepsTheLastSize(t *testing.T) {
	m := newModel()
	m.applyStatus(app.Status{User: "a", Phase: app.PhaseRecording, Bytes: 4096, File: "/out/a.mp4"})
	m.now = m.now.Add(30 * time.Second)
	m.applyStatus(app.Status{User: "a", Phase: app.PhaseDone, File: "/out/a.mp4"})

	tgt := m.targets[0]
	if tgt.bytes != 4096 || tgt.file != "/out/a.mp4" || tgt.phase != app.PhaseDone {
		t.Fatalf("terminal row lost its result: %+v", tgt)
	}
	if got := m.timer(tgt); got != "00:30" {
		t.Fatalf("terminal clock should freeze: got %q want %q", got, "00:30")
	}
}

// TestTerminalClockKeepsTheWholeRun proves the terminal row shows how long the
// target ran, not the last phase. The remux is usually the shortest one, so
// freezing on it would make every recording look like it lasted a second.
func TestTerminalClockKeepsTheWholeRun(t *testing.T) {
	m := newModel()
	m.applyStatus(app.Status{User: "a", Phase: app.PhaseRecording})
	m.now = m.now.Add(50 * time.Second)
	m.applyStatus(app.Status{User: "a", Phase: app.PhaseConverting})
	m.now = m.now.Add(time.Second)
	m.applyStatus(app.Status{User: "a", Phase: app.PhaseStopped})
	if got := m.timer(m.targets[0]); got != "00:51" {
		t.Fatalf("terminal clock lost the run: got %q want %q", got, "00:51")
	}
}

// TestTerminalStatusClearsTheCaptureDetail proves a finished target does not
// keep showing the segment it was writing when it stopped: the detail line is
// for why it is where it is now, and a stopped target has no reason left.
func TestTerminalStatusClearsTheCaptureDetail(t *testing.T) {
	m := newModel()
	m.applyStatus(app.Status{User: "a", Phase: app.PhaseRecording, Detail: "a=20261030_000014.000.ts"})
	m.applyStatus(app.Status{User: "a", Phase: app.PhaseConverting})
	m.applyStatus(app.Status{User: "a", Phase: app.PhaseStopped, File: "/out/a.mp4"})
	if got := m.targets[0].detail; got != "" {
		t.Fatalf("terminal row kept a stale detail: %q", got)
	}
}

// TestLiveRoomIsNotRepeatedAsDetail keeps the detail block from printing the
// room id twice: the live status carries it in both fields.
func TestLiveRoomIsNotRepeatedAsDetail(t *testing.T) {
	m := newModel()
	m.applyStatus(app.Status{User: "a", Phase: app.PhaseLive, Detail: "7699631452921154317", RoomID: "7699631452921154317"})
	if lines := m.detailLines(3, 60); len(lines) != 1 {
		t.Fatalf("want the label line only, got %d: %q", len(lines), lines)
	}
}

func TestWaitingShowsTheCountdown(t *testing.T) {
	m := newModel()
	m.interval = 3 * time.Minute
	m.applyStatus(app.Status{User: "a", Phase: app.PhaseWaiting})
	m.now = m.now.Add(30 * time.Second)
	if got := m.timer(m.targets[0]); got != "-02:30" {
		t.Fatalf("got %q want %q", got, "-02:30")
	}
}

func TestAddPromptCommitsEveryName(t *testing.T) {
	m := newModel()
	m.screen = screenMonitor
	ctrl := &fakeCtrl{}
	m.ctrl = ctrl

	m.Update(key("a"))
	m.Update(key("foo, @bar"))
	if !m.prompting {
		t.Fatal("a must open the add prompt")
	}
	m.Update(key("enter"))

	if got := strings.Join(ctrl.added, ","); got != "foo,bar" {
		t.Fatalf("added %q, want %q", got, "foo,bar")
	}
	if m.prompting {
		t.Fatal("enter must close the prompt")
	}
}

func TestAddPromptReportsARunnerError(t *testing.T) {
	m := newModel()
	m.screen = screenMonitor
	m.ctrl = &fakeCtrl{err: errors.New("no run in progress")}

	m.Update(key("a"))
	m.Update(key("foo"))
	m.Update(key("enter"))

	if m.prompting {
		t.Fatal("enter must close the prompt even when the runner refuses")
	}
	if v := m.View(); !strings.Contains(v, "no run in progress") {
		t.Fatalf("the runner error belongs in the footer:\n%s", v)
	}
}

func TestAddPromptEscCancels(t *testing.T) {
	m := newModel()
	m.screen = screenMonitor
	ctrl := &fakeCtrl{}
	m.ctrl = ctrl

	m.Update(key("a"))
	m.Update(key("nope"))
	m.Update(key("esc"))

	if m.prompting {
		t.Fatal("esc must close the prompt")
	}
	if len(ctrl.added) != 0 {
		t.Fatalf("esc must not add targets, got %v", ctrl.added)
	}
}

func TestDropActsOnTheSelectedTarget(t *testing.T) {
	m := newModel()
	m.screen = screenMonitor
	ctrl := &fakeCtrl{}
	m.ctrl = ctrl
	m.applyStatus(app.Status{User: "a", Phase: app.PhaseRecording})
	m.applyStatus(app.Status{User: "b", Phase: app.PhaseRecording})

	m.Update(key("down"))
	m.Update(key("x"))

	if got := strings.Join(ctrl.dropped, ","); got != "b" {
		t.Fatalf("dropped %q, want %q", got, "b")
	}
}

func TestLogTailKeepsNewestAndPads(t *testing.T) {
	m := newModel()
	for i := 0; i < 20; i++ {
		m.logs = append(m.logs, logLine{level: "INFO", text: strings.Repeat("x", 200)})
	}
	m.logs = append(m.logs, logLine{level: "ERROR", text: "the newest line"})

	tail := m.logTail(3, 60)
	if len(tail) != 3 {
		t.Fatalf("want 3 rows, got %d", len(tail))
	}
	if !strings.Contains(strings.Join(tail, "\n"), "the newest line") {
		t.Fatal("tail dropped the newest line")
	}
	for _, row := range tail {
		if n := lipgloss.Width(row); n > 60 {
			t.Fatalf("row wider than the pane: %d > 60", n)
		}
	}
}

func TestDoneViewNamesTheOutcome(t *testing.T) {
	m := newModel()
	m.screen = screenDone
	m.result = context.Canceled
	if v := m.View(); !strings.Contains(v, "stopped") {
		t.Fatalf("cancel should read as stopped:\n%s", v)
	}

	m.result = errors.New("login required")
	m.applyStatus(app.Status{User: "a", Phase: app.PhaseFailed, Detail: "login required"})
	if v := m.View(); !strings.Contains(v, "login required") {
		t.Fatalf("failure should show its reason:\n%s", v)
	}
}

// TestStoppedRunThatReturnsNil pins the intent: the user asked to stop, so a
// clean return is still a stop.
func TestStoppedRunThatReturnsNilReadsStopped(t *testing.T) {
	m := newModel()
	m.screen = screenMonitor
	m.finishing = true
	m.Update(runDoneMsg{})
	if v := m.View(); !strings.Contains(v, "stopped") {
		t.Fatalf("a stopped run must not read as finished:\n%s", v)
	}
}

// TestLayoutFitsTheTerminal guards the whole redesign: a small terminal gets
// exactly h lines and nothing wider than w, on every screen.
func TestLayoutFitsTheTerminal(t *testing.T) {
	monitor := func() *Model {
		m := newModel()
		m.screen = screenMonitor
		m.started = m.now
		m.seed(config.Config{URL: "https://www.tiktok.com/@averyveryverylongusername/live"}, nil)
		for _, user := range []string{"alpha", "beta", "gamma"} {
			m.applyStatus(app.Status{User: user, Phase: app.PhaseRecording, Bytes: 1 << 20, File: "/out/" + user + ".mp4"})
		}
		m.logs = []logLine{
			{level: "INFO", text: strings.Repeat("log line ", 20)},
			{level: "ERROR", text: "boom"},
		}
		return m
	}
	screens := []struct {
		name  string
		model func() *Model
	}{
		{"setup", newModel},
		{"monitor", monitor},
		{"done", func() *Model {
			m := monitor()
			m.screen = screenDone
			m.result = errors.New("one or more recordings failed")
			return m
		}},
	}
	for _, w := range []int{40, 80, 120} {
		for _, h := range []int{10, 24, 40} {
			for _, s := range screens {
				m := s.model()
				m.width, m.height = w, h
				v := m.View()
				lines := strings.Split(v, "\n")
				if len(lines) != h {
					t.Fatalf("%s at %dx%d: %d lines, want %d\n%s", s.name, w, h, len(lines), h, v)
				}
				for i, line := range lines {
					if got := lipgloss.Width(line); got > w {
						t.Fatalf("%s at %dx%d: line %d is %d wide\n%s", s.name, w, h, i+1, got, v)
					}
				}
			}
		}
	}
}
