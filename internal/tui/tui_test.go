package tui

import (
	"context"
	"errors"
	"slices"
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
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

func submit(m *Model, line string) tea.Cmd {
	m.input.SetValue(line)
	_, cmd := m.Update(key("enter"))
	return cmd
}

func logText(m *Model) string {
	var b strings.Builder
	for _, line := range m.logs {
		b.WriteString(line.text)
		b.WriteByte('\n')
	}
	return b.String()
}

// fakeCtrl records what the screen asks the runner to do.
type fakeCtrl struct {
	users   []string
	added   []string
	dropped []string
	status  func(app.Status)
	addErr  error
	runErr  error
	calls   int
}

func (f *fakeCtrl) Run(_ context.Context, users []string) error {
	f.calls++
	f.users = append([]string(nil), users...)
	return f.runErr
}

func (f *fakeCtrl) OnStatus(fn func(app.Status)) { f.status = fn }

func (f *fakeCtrl) AddUser(user string) error {
	if f.addErr != nil {
		return f.addErr
	}
	f.added = append(f.added, user)
	return nil
}

func (f *fakeCtrl) DropUser(user string) bool {
	f.dropped = append(f.dropped, user)
	return true
}

// TestArgv checks the settings render the exact flags the CLI takes.
func TestArgv(t *testing.T) {
	base := settings{interval: 3}
	cases := []struct {
		name  string
		set   settings
		names []string
		want  []string
	}{
		{
			name:  "multi names",
			set:   base,
			names: []string{"a", "b", "c"},
			want:  []string{"-user", "a,b,c", "-duration", "0"},
		},
		{
			name:  "single room id",
			set:   base,
			names: []string{"7123456789012345678"},
			want:  []string{"-room_id", "7123456789012345678", "-duration", "0"},
		},
		{
			name:  "single url",
			set:   base,
			names: []string{"https://www.tiktok.com/@user/live"},
			want:  []string{"-url", "https://www.tiktok.com/@user/live", "-duration", "0"},
		},
		{
			name:  "single user",
			set:   base,
			names: []string{"someone"},
			want:  []string{"-user", "someone", "-duration", "0"},
		},
		{
			name: "every knob",
			set: settings{
				once:     true,
				duration: 10,
				interval: 5,
				output:   "/tmp/rec",
				proxy:    "socks5://127.0.0.1:1080",
				telegram: true,
			},
			names: []string{"someone"},
			want: []string{"-once", "-user", "someone", "-duration", "10", "-automatic_interval", "5",
				"-output", "/tmp/rec", "-proxy", "socks5://127.0.0.1:1080", "-telegram"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newModel()
			m.settings = tc.set
			if got := m.argv(tc.names); !slices.Equal(got, tc.want) {
				t.Fatalf("argv = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestMultiTargetArgvParses proves a space separated input still becomes a
// username list, because cli.Parse only splits on commas.
func TestMultiTargetArgvParses(t *testing.T) {
	m := newModel()
	names := splitTargets("a, @b c")
	parsed, err := cli.Parse(m.argv(names))
	if err != nil {
		t.Fatalf("parse %q: %v", m.argv(names), err)
	}
	if got := strings.Join(parsed.Users, ","); got != "a,b,c" {
		t.Fatalf("users: got %q want %q", got, "a,b,c")
	}
}

func TestSettingsCommands(t *testing.T) {
	base := settings{interval: 3}
	cases := []struct {
		name    string
		start   settings
		line    string
		want    settings
		wantLog string
	}{
		{"once", base, "/once", settings{interval: 3, once: true}, ""},
		{"wait", settings{interval: 3, once: true}, "/wait", base, ""},
		{"duration", base, "/duration 12", settings{interval: 3, duration: 12}, ""},
		{"duration rejects text", base, "/duration x", base, "invalid argument"},
		{"duration rejects negative", base, "/duration -1", base, "-duration cannot be negative"},
		{"interval", base, "/interval 7", settings{interval: 7}, ""},
		{"interval rejects zero", base, "/interval 0", base, "-automatic_interval must be at least 1 minute"},
		{"output", base, "/output /tmp/rec", settings{interval: 3, output: "/tmp/rec"}, ""},
		{"proxy", base, "/proxy socks5://127.0.0.1:1080", settings{interval: 3, proxy: "socks5://127.0.0.1:1080"}, ""},
		{"proxy alone clears", settings{interval: 3, proxy: "socks5://127.0.0.1:1080"}, "/proxy", base, ""},
		{"telegram on", base, "/telegram on", settings{interval: 3, telegram: true}, ""},
		{"telegram off", settings{interval: 3, telegram: true}, "/telegram off", base, ""},
		{"unknown", base, "/nope", base, "unknown command /nope"},
		{"help", base, "/help", base, "/duration"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newModel()
			m.settings = tc.start
			submit(m, tc.line)
			if m.settings != tc.want {
				t.Fatalf("settings = %+v, want %+v", m.settings, tc.want)
			}
			if tc.wantLog != "" && !strings.Contains(logText(m), tc.wantLog) {
				t.Fatalf("activity missing %q:\n%s", tc.wantLog, logText(m))
			}
		})
	}
}

func TestOnceDuringARunAppliesToTheNextRun(t *testing.T) {
	m := newModel()
	ctrl := &fakeCtrl{}
	m.ctrl = ctrl
	submit(m, "/once")
	if !m.settings.once {
		t.Fatal("once must still be recorded while a run is in flight")
	}
	if got := logText(m); !strings.Contains(got, "once applies to the next run") {
		t.Fatalf("activity should say the change waits:\n%s", got)
	}
	if ctrl.calls != 0 {
		t.Fatal("/once must not restart the run")
	}
}

func TestBareEntryStartsARun(t *testing.T) {
	m := newModel()
	ctrl := &fakeCtrl{}
	var got config.Config
	m.makeRunner = func(cfg config.Config, _ telegram.Config) controller {
		got = cfg
		return ctrl
	}

	cmd := submit(m, "@alpha, beta")
	if m.ctrl != ctrl {
		t.Fatal("the runner was not installed")
	}
	if got.Once || got.Duration != 0 || got.AutomaticInterval != 3 {
		t.Fatalf("runner config = %+v", got)
	}
	if !slices.Equal(m.users, []string{"alpha", "beta"}) {
		t.Fatalf("run users = %q, want [alpha beta]", m.users)
	}
	if cmd == nil {
		t.Fatal("starting a run must return the tea.Cmd that runs it")
	}
	if _, ok := cmd().(runDoneMsg); !ok {
		t.Fatal("the run command must report runDoneMsg")
	}
	if !slices.Equal(ctrl.users, []string{"alpha", "beta"}) {
		t.Fatalf("runner users = %q, want [alpha beta]", ctrl.users)
	}
}

func TestBareEntryAddsWhileRunning(t *testing.T) {
	m := newModel()
	ctrl := &fakeCtrl{}
	m.ctrl = ctrl

	cmd := submit(m, "gamma, @delta")
	if cmd != nil {
		t.Fatal("adding to a live run must not start a second run")
	}
	if !slices.Equal(ctrl.added, []string{"gamma", "delta"}) {
		t.Fatalf("added = %q, want [gamma delta]", ctrl.added)
	}
}

func TestAddCommandUsesTheTargetPath(t *testing.T) {
	m := newModel()
	ctrl := &fakeCtrl{}
	m.ctrl = ctrl
	submit(m, "/add @gamma")
	if !slices.Equal(ctrl.added, []string{"gamma"}) {
		t.Fatalf("added = %q, want [gamma]", ctrl.added)
	}
}

func TestBareEntryReportsLaunchErrors(t *testing.T) {
	m := newModel()
	m.settings.interval = 0
	submit(m, "alpha")
	if m.ctrl != nil {
		t.Fatal("a rejected run must not install a runner")
	}
	if got := logText(m); !strings.Contains(got, "-automatic_interval must be at least 1 minute") {
		t.Fatalf("CLI error missing from activity:\n%s", got)
	}
}

func TestInputStaysFocused(t *testing.T) {
	m := newModel()
	if !m.input.Focused() {
		t.Fatal("the input must start focused")
	}
	submit(m, "/duration 5")
	if !m.input.Focused() {
		t.Fatal("submitting must not blur the input")
	}
}

func TestBareEntryReportsRunnerErrors(t *testing.T) {
	m := newModel()
	ctrl := &fakeCtrl{addErr: app.ErrUserRunning}
	m.ctrl = ctrl
	submit(m, "alpha")
	if got := logText(m); strings.Contains(got, app.ErrUserRunning.Error()) {
		t.Fatalf("ErrUserRunning is silent:\n%s", got)
	}

	ctrl.addErr = errors.New("runner busy")
	submit(m, "alpha")
	if got := logText(m); !strings.Contains(got, "runner busy") {
		t.Fatalf("runner error missing from activity:\n%s", got)
	}
}

func TestDropAndStopReachTheRunner(t *testing.T) {
	m := newModel()
	ctrl := &fakeCtrl{}
	ctx, cancel := context.WithCancel(context.Background())
	m.ctrl = ctrl
	m.cancel = cancel
	m.ctx = ctx

	submit(m, "/drop @alpha")
	if !slices.Equal(ctrl.dropped, []string{"alpha"}) {
		t.Fatalf("dropped = %q, want [alpha]", ctrl.dropped)
	}
	if ctx.Err() != nil {
		t.Fatal("drop must not stop the whole run")
	}

	submit(m, "/stop")
	if ctx.Err() == nil {
		t.Fatal("stop must cancel the run")
	}
	if !m.finishing {
		t.Fatal("stop must mark the run as stopping")
	}
}

func TestQuitSetsTheFlag(t *testing.T) {
	m := newModel()
	cmd := submit(m, "/quit")
	if !m.quit {
		t.Fatal("/quit must set the quit flag")
	}
	if cmd == nil {
		t.Fatal("/quit must return tea.Quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("/quit must return the quit command")
	}
}

func TestHistoryRecall(t *testing.T) {
	m := newModel()
	submit(m, "alpha")
	submit(m, "beta")
	if m.input.Value() != "" {
		t.Fatal("a submitted line must clear the input")
	}

	m.Update(key("up"))
	if got := m.input.Value(); got != "beta" {
		t.Fatalf("first up = %q, want beta", got)
	}
	m.Update(key("up"))
	if got := m.input.Value(); got != "alpha" {
		t.Fatalf("second up = %q, want alpha", got)
	}
	m.Update(key("down"))
	if got := m.input.Value(); got != "beta" {
		t.Fatalf("first down = %q, want beta", got)
	}
	m.Update(key("down"))
	if got := m.input.Value(); got != "" {
		t.Fatalf("second down = %q, want empty", got)
	}

	submit(m, "gamma")
	if m.histPos != len(m.history) {
		t.Fatalf("submitting must reset the walk: pos %d of %d", m.histPos, len(m.history))
	}
	m.Update(key("up"))
	if got := m.input.Value(); got != "gamma" {
		t.Fatalf("up after submit = %q, want gamma", got)
	}
}

func TestEscClearsWhenIdleAndStopsWhenRunning(t *testing.T) {
	m := newModel()
	m.input.SetValue("draft")
	m.Update(key("esc"))
	if m.input.Value() != "" {
		t.Fatal("esc must clear the input when no run is in flight")
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.ctrl = &fakeCtrl{}
	m.cancel = cancel
	m.ctx = ctx
	m.input.SetValue("draft")
	m.Update(key("esc"))
	if ctx.Err() == nil {
		t.Fatal("esc must stop the run in flight")
	}
	if m.input.Value() != "draft" {
		t.Fatal("stopping must not throw away the typed line")
	}
}

func TestCtrlCStopsThenQuits(t *testing.T) {
	m := newModel()
	ctx, cancel := context.WithCancel(context.Background())
	m.ctrl = &fakeCtrl{}
	m.cancel = cancel
	m.ctx = ctx

	_, cmd := m.Update(key("ctrl+c"))
	if cmd != nil {
		t.Fatal("the first ctrl+c only stops the run")
	}
	if ctx.Err() == nil {
		t.Fatal("the first ctrl+c must stop the run")
	}
	if !m.finishing {
		t.Fatal("the first ctrl+c must mark the run stopping")
	}

	_, cmd = m.Update(key("ctrl+c"))
	if !m.quit || cmd == nil {
		t.Fatal("the second ctrl+c must quit")
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
	if tgt.bytes != 4096 || tgt.phase != app.PhaseDone {
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

func TestWaitingShowsTheCountdown(t *testing.T) {
	m := newModel()
	m.runInterval = 3 * time.Minute
	m.settings.interval = 10 // a change for the next run must not move this one
	m.applyStatus(app.Status{User: "a", Phase: app.PhaseWaiting})
	m.now = m.now.Add(30 * time.Second)
	if got := m.timer(m.targets[0]); got != "-02:30" {
		t.Fatalf("got %q want %q", got, "-02:30")
	}
}

func TestLogTailKeepsNewestAtTheBottom(t *testing.T) {
	m := newModel()
	for i := 0; i < 20; i++ {
		m.logs = append(m.logs, logLine{level: "INFO", text: strings.Repeat("x", 200)})
	}
	m.logs = append(m.logs, logLine{level: "ERROR", text: "the newest line"})

	tail := m.logTail(25, 60)
	if len(tail) != 25 {
		t.Fatalf("want 25 rows, got %d", len(tail))
	}
	if tail[0] != "" {
		t.Fatalf("a short tail must leave blank space above, got %q", tail[0])
	}
	if !strings.Contains(tail[24], "the newest line") {
		t.Fatalf("the newest line must be at the bottom: %q", tail)
	}
	for _, row := range tail {
		if n := lipgloss.Width(row); n > 60 {
			t.Fatalf("row wider than the pane: %d > 60", n)
		}
	}
}

func TestHeaderNamesTheRunState(t *testing.T) {
	idle := newModel()
	if v := idle.View(); !strings.Contains(v, "idle · 00:00") {
		t.Fatalf("idle header missing:\n%s", v)
	}

	running := newModel()
	running.ctrl = &fakeCtrl{}
	running.started = running.now
	running.applyStatus(app.Status{User: "a", Phase: app.PhaseRecording})
	running.applyStatus(app.Status{User: "b", Phase: app.PhaseWaiting})
	v := running.View()
	for _, want := range []string{"●", "1 recording", "2 targets", "00:00"} {
		if !strings.Contains(v, want) {
			t.Fatalf("running header missing %q:\n%s", want, v)
		}
	}

	outcomes := []struct {
		name      string
		result    error
		finishing bool
		want      string
	}{
		{"finished", nil, false, "finished"},
		{"stopped", context.Canceled, false, "stopped"},
		{"stopped on a nil return", nil, true, "stopped"},
		{"failed", errors.New("login required"), false, "failed"},
	}
	for _, tc := range outcomes {
		t.Run(tc.name, func(t *testing.T) {
			m := newModel()
			m.started = m.now
			m.ended = m.now.Add(time.Minute)
			m.result = tc.result
			m.finishing = tc.finishing
			if v := m.View(); !strings.Contains(v, tc.want) {
				t.Fatalf("header missing %q:\n%s", tc.want, v)
			}
		})
	}
}

func TestEmptyStateLine(t *testing.T) {
	m := newModel()
	const empty = "no targets yet · type a username below"
	if got := strings.Count(m.View(), empty); got != 1 {
		t.Fatalf("empty state appears %d times, want once:\n%s", got, m.View())
	}
}

// TestLongNamesWidenTheList proves the account name survives at 80 columns:
// the table takes the width instead of clipping the identifier.
func TestLongNamesWidenTheList(t *testing.T) {
	m := newModel()
	m.ctrl = &fakeCtrl{}
	m.started = m.now
	m.applyStatus(app.Status{User: "aljazeeraenglish", Phase: app.PhaseRecording, Bytes: 1 << 20})
	v := m.View()
	if !strings.Contains(v, "@aljazeeraenglish") {
		t.Fatalf("the name was clipped at 80 columns:\n%s", v)
	}
	for _, line := range strings.Split(v, "\n") {
		if got := lipgloss.Width(line); got > 80 {
			t.Fatalf("line is %d wide:\n%s", got, v)
		}
	}
}

// TestLayoutFitsTheTerminal guards the whole screen: exactly h lines and
// nothing wider than w in every state.
// TestTableColumnsStayTogether proves the wide window does not stretch the name
// column: the phase and size sit next to the label, not at the far edge.
func TestTableColumnsStayTogether(t *testing.T) {
	m := newModel()
	m.width, m.height = 120, 24
	m.applyStatus(app.Status{User: "natgeo", Phase: app.PhaseWaiting})
	m.applyStatus(app.Status{User: "aljazeeraenglish", Phase: app.PhaseRecording, Bytes: 1 << 20})
	for _, line := range strings.Split(m.View(), "\n") {
		if !strings.HasPrefix(line, "  @") {
			continue
		}
		if strings.Contains(line, strings.Repeat(" ", 20)) {
			t.Fatalf("the table is stretched across the terminal:\n%s", line)
		}
	}
}

func TestLayoutFitsTheTerminal(t *testing.T) {
	states := []struct {
		name  string
		model func() *Model
	}{
		{"idle", func() *Model { return newModel() }},
		{"targets and logs", func() *Model {
			m := newModel()
			for _, user := range []string{"alpha", "beta"} {
				m.applyStatus(app.Status{User: user, Phase: app.PhaseRecording, Bytes: 1 << 20})
			}
			m.logs = []logLine{
				{level: "INFO", text: strings.Repeat("log line ", 20)},
				{level: "ERROR", text: "boom"},
			}
			return m
		}},
		{"running", func() *Model {
			m := newModel()
			m.ctrl = &fakeCtrl{}
			m.started = m.now
			for _, user := range []string{"a", "b", "c", "d", "e", "f"} {
				m.applyStatus(app.Status{User: user, Phase: app.PhaseRecording, Bytes: 1048000, File: "/out/" + user + ".mp4"})
			}
			return m
		}},
		{"finished", func() *Model {
			m := newModel()
			m.started = m.now
			m.ended = m.now.Add(5 * time.Minute)
			m.applyStatus(app.Status{User: "a", Phase: app.PhaseDone, Bytes: 4096, File: "/out/a.mp4"})
			return m
		}},
	}
	for _, w := range []int{40, 80, 120} {
		for _, h := range []int{7, 8, 9, 10, 24, 40} {
			for _, st := range states {
				m := st.model()
				m.width, m.height = w, h
				v := m.View()
				lines := strings.Split(v, "\n")
				if len(lines) != h {
					t.Fatalf("%s at %dx%d: %d lines, want %d\n%s", st.name, w, h, len(lines), h, v)
				}
				for i, line := range lines {
					if got := lipgloss.Width(line); got > w {
						t.Fatalf("%s at %dx%d: line %d is %d wide\n%s", st.name, w, h, i+1, got, v)
					}
				}
			}
		}
	}
}
