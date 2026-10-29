package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"tokgo/internal/app"
	"tokgo/internal/boot"
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

// savedList is a target list with every entry in the manual group.
func savedList(users ...string) []saved {
	out := make([]saved, 0, len(users))
	for _, user := range users {
		out = append(out, saved{user: user})
	}
	return out
}

// names is the usernames of a list, for a readable failure message.
func names(list []saved) []string {
	out := make([]string, 0, len(list))
	for _, s := range list {
		out = append(out, s.user)
	}
	return out
}

// autoList is a target list with every entry in the autostart group.
func autoList(users ...string) []saved {
	out := make([]saved, 0, len(users))
	for _, user := range users {
		out = append(out, saved{user: user, auto: true})
	}
	return out
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
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
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
	users    []string
	added    []string
	dropped  []string
	status   func(app.Status)
	addErr   error
	runErr   error
	dropMiss bool
	calls    int
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
	return !f.dropMiss
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

// TestCtrlCIsNotBound pins the rule: the screen only leaves on /quit, so a
// stray Ctrl-C cannot abandon a remux.
func TestCtrlCIsNotBound(t *testing.T) {
	m := newModel()
	ctx, cancel := context.WithCancel(context.Background())
	m.ctrl = &fakeCtrl{}
	m.cancel = cancel
	m.ctx = ctx

	_, cmd := m.Update(key("ctrl+c"))
	if cmd != nil || m.quit {
		t.Fatal("ctrl+c must not quit")
	}
	if ctx.Err() != nil || m.finishing {
		t.Fatal("ctrl+c must not stop the run")
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
		{"saved and idle", func() *Model {
			m := newModel()
			m.list = savedList("alpha", "beta", "gamma", "delta")
			m.syncRows()
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

// TestTargetsRoundTrip proves the saved list survives a restart: the same
// names, the same groups, the same order, in a directory saveTargets has to
// create.
func TestTargetsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokgo", "targets.txt")
	if got := loadTargets(path); got != nil {
		t.Fatalf("a missing file is an empty list, got %q", names(got))
	}

	users := []saved{{user: "alpha"}, {user: "beta", auto: true}}
	if err := saveTargets(path, users); err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := loadTargets(path); !slices.Equal(got, users) {
		t.Fatalf("round trip = %q, want %q", names(got), names(users))
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("file mode = %o, want 644", got)
	}
	dir, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if got := dir.Mode().Perm(); got != 0o755 {
		t.Fatalf("dir mode = %o, want 755", got)
	}
}

// TestLoadTargetsToleratesTheFile covers a hand edited list: blanks, duplicates,
// a leading @ and stray spaces. Names come back in file order.
func TestLoadTargetsToleratesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "targets.txt")
	body := "alpha\n\n@beta\nalpha\n  *gamma  \n@\n*\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	want := []saved{{user: "alpha"}, {user: "beta"}, {user: "gamma", auto: true}}
	if got := loadTargets(path); !slices.Equal(got, want) {
		t.Fatalf("loadTargets = %q, want %q", names(got), names(want))
	}
}

// TestPersistenceOffWithoutAPath proves an unset HOME never writes anything.
func TestPersistenceOffWithoutAPath(t *testing.T) {
	if got := loadTargets(""); got != nil {
		t.Fatalf("loadTargets(\"\") = %q, want nil", names(got))
	}
	if err := saveTargets("", []saved{{user: "alpha"}}); err != nil {
		t.Fatalf("saveTargets(\"\") = %v, want nil", err)
	}
}

func TestTargetFileFollowsTheConfigDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if got, want := targetFile(), filepath.Join(dir, "tokgo", "targets.txt"); got != want {
		t.Fatalf("targetFile = %q, want %q", got, want)
	}

	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	if got := targetFile(); got != "" {
		t.Fatalf("targetFile without a config dir = %q, want empty", got)
	}
}

// TestAddWritesTheListAndStartsNothing proves /add only remembers: no runner
// while idle, and the names reach the file.
func TestAddWritesTheListAndStartsNothing(t *testing.T) {
	m := newModel()
	m.userPath = filepath.Join(t.TempDir(), "targets.txt")
	cmd := submit(m, "/add @alpha, beta")
	if cmd != nil {
		t.Fatal("/add must not start a run")
	}
	if m.ctrl != nil {
		t.Fatal("/add must not install a runner while idle")
	}
	if got := loadTargets(m.userPath); !slices.Equal(got, savedList("alpha", "beta")) {
		t.Fatalf("saved = %q, want [alpha beta]", names(got))
	}
	for _, tgt := range m.targets {
		if !tgt.idle {
			t.Fatalf("a saved target must sit idle: %+v", tgt)
		}
	}
	if got := logText(m); !strings.Contains(got, "no run in progress") {
		if strings.Contains(got, "WARN") {
			t.Fatalf("unexpected warning:\n%s", got)
		}
	}
}

// TestLoadSavedBuildsIdleRows proves startup shows one idle row per name on
// disk, and the header counts them.
func TestLoadSavedBuildsIdleRows(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := saveTargets(filepath.Join(dir, "tokgo", "targets.txt"), savedList("alpha", "beta")); err != nil {
		t.Fatalf("seed: %v", err)
	}

	m := newModel()
	m.loadSaved()
	if !slices.Equal(m.list, savedList("alpha", "beta")) {
		t.Fatalf("list = %q, want [alpha beta]", names(m.list))
	}
	if len(m.targets) != 2 {
		t.Fatalf("want 2 rows, got %d", len(m.targets))
	}
	for _, want := range []string{"alpha", "beta"} {
		i, ok := m.byName[want]
		if !ok || !m.targets[i].idle || m.targets[i].phase.Active() {
			t.Fatalf("row for %s: %+v", want, m.targets)
		}
	}
	v := m.View()
	for _, want := range []string{"@alpha", "@beta", "idle", "2 saved"} {
		if !strings.Contains(v, want) {
			t.Fatalf("view missing %q:\n%s", want, v)
		}
	}
}

func TestStartCommand(t *testing.T) {
	cases := []struct {
		name       string
		line       string
		saved      []string
		running    bool
		wantUsers  []string
		wantAdded  []string
		wantQueued []string
		wantLog    string
	}{
		{name: "idle starts the one", line: "/start alpha", wantUsers: []string{"alpha"}},
		{name: "idle starts them all", line: "/start all", saved: []string{"alpha", "beta"}, wantUsers: []string{"alpha"}, wantQueued: []string{"beta"}},
		{name: "running adds the one", line: "/start beta", saved: []string{"alpha"}, running: true, wantAdded: []string{"beta"}},
		{name: "running adds them all", line: "/start all", saved: []string{"alpha", "beta"}, running: true, wantAdded: []string{"alpha"}, wantQueued: []string{"beta"}},
		{name: "all with nothing saved", line: "/start all", wantLog: "no saved targets · /add <name>"},
		{name: "bare start", line: "/start", wantLog: "start needs a username or all"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newModel()
			m.userPath = filepath.Join(t.TempDir(), "targets.txt")
			m.list = savedList(tc.saved...)
			m.syncRows()
			var ctrl *fakeCtrl
			if tc.running {
				ctrl = &fakeCtrl{}
				m.ctrl = ctrl
			}
			cmd := submit(m, tc.line)
			switch {
			case tc.wantLog != "":
				if got := logText(m); !strings.Contains(got, tc.wantLog) {
					t.Fatalf("activity missing %q:\n%s", tc.wantLog, got)
				}
				if cmd != nil {
					t.Fatal("a rejected command must not start a run")
				}
			case tc.running:
				if cmd != nil {
					t.Fatal("adding to a live run must not start a second run")
				}
				if !slices.Equal(ctrl.added, tc.wantAdded) {
					t.Fatalf("added = %q, want %q", ctrl.added, tc.wantAdded)
				}
				if !slices.Equal(m.pendingStart, tc.wantQueued) {
					t.Fatalf("queued = %q, want %q", m.pendingStart, tc.wantQueued)
				}
			default:
				if m.ctrl == nil {
					t.Fatal("the runner was not installed")
				}
				if !slices.Equal(m.users, tc.wantUsers) {
					t.Fatalf("run users = %q, want %q", m.users, tc.wantUsers)
				}
				if !slices.Equal(m.pendingStart, tc.wantQueued) {
					t.Fatalf("queued = %q, want %q", m.pendingStart, tc.wantQueued)
				}
				if cmd == nil {
					t.Fatal("starting a run must return the tea.Cmd that runs it")
				}
				if _, ok := cmd().(runDoneMsg); !ok {
					t.Fatal("the run command must report runDoneMsg")
				}
			}
		})
	}
}

// TestStartAllUsesTheSavedOrder proves /start all starts the first name at once
// and queues the rest in the order the file keeps.
func TestStartAllUsesTheSavedOrder(t *testing.T) {
	m := newModel()
	m.userPath = filepath.Join(t.TempDir(), "targets.txt")
	m.list = savedList("beta", "alpha")
	m.syncRows()
	submit(m, "/start all")
	if !slices.Equal(m.users, []string{"beta"}) {
		t.Fatalf("first run = %q, want beta", m.users)
	}
	if !slices.Equal(m.pendingStart, []string{"alpha"}) {
		t.Fatalf("queued = %q, want the list order", m.pendingStart)
	}
}

// TestSeedKeepsSavedRowsAndQueuesTheRun proves a new run clears the previous
// terminal phases without dropping a saved name.
func TestSeedKeepsSavedRowsAndQueuesTheRun(t *testing.T) {
	m := newModel()
	m.list = savedList("alpha", "beta")
	m.syncRows()
	m.applyStatus(app.Status{User: "alpha", Phase: app.PhaseRecording, Bytes: 4096})
	m.applyStatus(app.Status{User: "alpha", Phase: app.PhaseDone})

	m.seed(config.Config{}, []string{"alpha"}, m.now)
	if len(m.targets) != 2 {
		t.Fatalf("want 2 rows, got %d", len(m.targets))
	}
	alpha := m.targets[m.byName["alpha"]]
	if alpha.idle || alpha.phase != app.PhaseQueued || alpha.bytes != 0 {
		t.Fatalf("the run row must be reset: %+v", alpha)
	}
	beta := m.targets[m.byName["beta"]]
	if !beta.idle || beta.phase != app.PhaseQueued {
		t.Fatalf("a saved name must stay idle: %+v", beta)
	}
}

// TestRestartWhileRunningDropsThenReadds is the stop, remux, start again
// ordering: the name is only handed back to the runner once its own terminal
// status arrives.
func TestRestartWhileRunningDropsThenReadds(t *testing.T) {
	m := newModel()
	m.userPath = filepath.Join(t.TempDir(), "targets.txt")
	ctrl := &fakeCtrl{}
	m.ctrl = ctrl

	if cmd := submit(m, "/restart @alpha"); cmd != nil {
		t.Fatal("restart must not start a second run")
	}
	if !slices.Equal(ctrl.dropped, []string{"alpha"}) {
		t.Fatalf("dropped = %q, want [alpha]", ctrl.dropped)
	}
	if len(ctrl.added) != 0 {
		t.Fatalf("must not re-add before the run is gone: %q", ctrl.added)
	}
	if !slices.Equal(m.restarting, []string{"alpha"}) {
		t.Fatalf("restarting = %q, want [alpha]", m.restarting)
	}
	if got := logText(m); !strings.Contains(got, "restarting @alpha") {
		t.Fatalf("activity missing the restart line:\n%s", got)
	}

	m.applyStatus(app.Status{User: "beta", Phase: app.PhaseStopped})
	if len(ctrl.added) != 0 {
		t.Fatalf("another target's status must not restart alpha: %q", ctrl.added)
	}
	if !slices.Equal(m.restarting, []string{"alpha"}) {
		t.Fatalf("restarting = %q, want [alpha]", m.restarting)
	}

	m.applyStatus(app.Status{User: "alpha", Phase: app.PhaseRecording})
	if len(ctrl.added) != 0 || len(m.restarting) != 1 {
		t.Fatalf("an active status must not trigger the restart: added %q restarting %q", ctrl.added, m.restarting)
	}

	m.applyStatus(app.Status{User: "alpha", Phase: app.PhaseDone})
	if !slices.Equal(ctrl.added, []string{"alpha"}) {
		t.Fatalf("added = %q, want [alpha]", ctrl.added)
	}
	if len(m.restarting) != 0 {
		t.Fatalf("the queue must be cleared: %q", m.restarting)
	}
}

// TestRestartWithNothingToDropStartsIt proves a name the run does not hold is
// just added.
func TestRestartWithNothingToDropStartsIt(t *testing.T) {
	m := newModel()
	m.userPath = filepath.Join(t.TempDir(), "targets.txt")
	ctrl := &fakeCtrl{dropMiss: true}
	m.ctrl = ctrl

	if cmd := submit(m, "/restart alpha"); cmd != nil {
		t.Fatal("adding to the live run must not start a second run")
	}
	if !slices.Equal(ctrl.added, []string{"alpha"}) {
		t.Fatalf("added = %q, want [alpha]", ctrl.added)
	}
	if len(m.restarting) != 0 {
		t.Fatalf("nothing was stopped, so nothing is queued: %q", m.restarting)
	}
}

// TestRestartSurvivesTheRunBoundary proves a run that ends before the terminal
// status arrives still restarts the target.
func TestRestartSurvivesTheRunBoundary(t *testing.T) {
	m := newModel()
	m.userPath = filepath.Join(t.TempDir(), "targets.txt")
	first := &fakeCtrl{}
	m.ctrl = first
	m.makeRunner = func(config.Config, telegram.Config) controller {
		return &fakeCtrl{}
	}

	submit(m, "/restart alpha")
	if len(m.restarting) != 1 {
		t.Fatalf("restarting = %q, want [alpha]", m.restarting)
	}

	_, cmd := m.Update(runDoneMsg{})
	if m.ctrl == nil || m.ctrl == first {
		t.Fatal("runDoneMsg must launch a fresh run")
	}
	if cmd == nil {
		t.Fatal("the fresh run needs its own tea.Cmd")
	}
	if _, ok := cmd().(runDoneMsg); !ok {
		t.Fatal("the run command must report runDoneMsg")
	}
	if !slices.Equal(m.users, []string{"alpha"}) {
		t.Fatalf("run users = %q, want [alpha]", m.users)
	}
	if len(m.restarting) != 0 {
		t.Fatalf("the queue must be consumed: %q", m.restarting)
	}
}

func TestRestartWhenIdleStartsTheRun(t *testing.T) {
	m := newModel()
	m.userPath = filepath.Join(t.TempDir(), "targets.txt")
	cmd := submit(m, "/restart alpha")
	if m.ctrl == nil {
		t.Fatal("restart while idle must start the run")
	}
	if cmd == nil {
		t.Fatal("starting a run must return the tea.Cmd that runs it")
	}
	if !slices.Equal(m.users, []string{"alpha"}) {
		t.Fatalf("run users = %q, want [alpha]", m.users)
	}
	if !slices.Equal(loadTargets(m.userPath), savedList("alpha")) {
		t.Fatal("restart must remember the name")
	}
}

// TestStopClearsTheRestartQueue proves an explicit stop never restarts anything.
func TestDropCancelsAQueuedRestart(t *testing.T) {
	m := newModel()
	m.userPath = filepath.Join(t.TempDir(), "targets.txt")
	ctrl := &fakeCtrl{}
	m.ctrl = ctrl

	submit(m, "/restart alpha")
	if len(m.restarting) != 1 {
		t.Fatalf("restarting = %q, want [alpha]", m.restarting)
	}
	submit(m, "/drop alpha")
	if len(m.restarting) != 0 {
		t.Fatalf("a drop after a restart must cancel it: %q", m.restarting)
	}

	// The terminal status of the dropped target must not bring it back.
	m.applyStatus(app.Status{User: "alpha", Phase: app.PhaseStopped})
	if len(ctrl.added) != 0 {
		t.Fatalf("added = %q, want nothing", ctrl.added)
	}
}

func TestStopClearsTheRestartQueue(t *testing.T) {
	m := newModel()
	m.ctrl = &fakeCtrl{}
	m.restarting = []string{"alpha"}
	m.stopRun()
	if len(m.restarting) != 0 {
		t.Fatalf("stopping must drop the queue: %q", m.restarting)
	}

	_, cmd := m.Update(runDoneMsg{})
	if cmd != nil {
		t.Fatal("nothing may relaunch after a stop")
	}
	if m.ctrl != nil {
		t.Fatal("a stopped run must stay stopped")
	}
}

// TestEventAppliesTheStatus proves the event path keeps both the merge and the
// channel wait.
func TestEventAppliesTheStatus(t *testing.T) {
	m := newModel()
	st := app.Status{User: "alpha", Phase: app.PhaseWaiting}
	_, cmd := m.Update(event{st: &st})
	if cmd == nil {
		t.Fatal("the status event must keep waiting for the next event")
	}
	i, ok := m.byName["alpha"]
	if !ok || m.targets[i].phase != app.PhaseWaiting {
		t.Fatalf("the status was not merged: %+v", m.targets)
	}
}

func TestMaskSecret(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "••••"},
		{"abcd", "••••"},
		{"abcde", "••••bcde"},
		{"supersecretsession42", "••••on42"},
	}
	for _, tc := range cases {
		if got := maskSecret(tc.in); got != tc.want {
			t.Fatalf("maskSecret(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestCookiesCommandWritesTheJar proves a typed cookie reaches the next launch,
// and that neither the screen nor the history ring ever shows it.
func TestCookiesCommandWritesTheJar(t *testing.T) {
	t.Chdir(t.TempDir())
	const raw = "supersecretsession42"
	m := newModel()
	if err := os.WriteFile(boot.CookieFile, []byte("{}"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	submit(m, "/cookies "+raw)
	if got := m.cookies["sessionid_ss"]; got != raw {
		t.Fatalf("cookie = %q, want the typed value", got)
	}
	body, err := os.ReadFile(boot.CookieFile)
	if err != nil {
		t.Fatalf("read jar: %v", err)
	}
	if !strings.Contains(string(body), raw) {
		t.Fatalf("the jar is missing the value:\n%s", body)
	}
	info, err := os.Stat(boot.CookieFile)
	if err != nil {
		t.Fatalf("stat jar: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("jar mode = %o, want 600", got)
	}

	logs := logText(m)
	if strings.Contains(logs, raw) {
		t.Fatalf("the raw cookie reached the activity pane:\n%s", logs)
	}
	if !strings.Contains(logs, "••••on42") || !strings.Contains(logs, "saved to cookies.json") {
		t.Fatalf("activity missing the masked confirmation:\n%s", logs)
	}
	if slices.Contains(m.history, "/cookies "+raw) {
		t.Fatalf("the cookie must not enter the recall ring: %q", m.history)
	}
}

func TestCookiesCommandReportsTheMaskAlone(t *testing.T) {
	t.Chdir(t.TempDir())

	m := newModel()
	submit(m, "/cookies")
	if got := logText(m); !strings.Contains(got, "no sessionid_ss set · /cookies <value>") {
		t.Fatalf("activity should say what is missing:\n%s", got)
	}

	m.cookies = map[string]string{"sessionid_ss": "supersecretsession42"}
	submit(m, "/cookies")
	got := logText(m)
	if !strings.Contains(got, "sessionid_ss ••••on42") {
		t.Fatalf("activity should report the mask:\n%s", got)
	}
	if strings.Contains(got, "supersecretsession42") {
		t.Fatalf("the raw cookie reached the activity pane:\n%s", got)
	}
}

func TestCookiesWhileRunningAppliesToTheNextRun(t *testing.T) {
	t.Chdir(t.TempDir())
	m := newModel()
	m.ctrl = &fakeCtrl{}
	submit(m, "/cookies short")
	got := logText(m)
	if !strings.Contains(got, "cookies applies to the next run") {
		t.Fatalf("activity should say the change waits:\n%s", got)
	}
	if strings.Contains(got, "short") {
		t.Fatalf("the raw cookie reached the activity pane:\n%s", got)
	}
}

// TestHelpPrintsEveryCommand proves the help and the completion come from one
// table, so a command cannot be half documented.
func TestHelpPrintsEveryCommand(t *testing.T) {
	m := newModel()
	submit(m, "/help")
	help := logText(m)
	for _, c := range commands {
		line := padRight("/"+c.name+" "+c.args, 18) + " " + c.help
		if !strings.Contains(help, line) {
			t.Fatalf("help is missing %q:\n%s", line, help)
		}
	}
	if got, want := len(m.logs), len(commands); got != want {
		t.Fatalf("help printed %d lines, want %d", got, want)
	}
}

func TestCompletion(t *testing.T) {
	cases := []struct {
		name  string
		saved []string
		live  []string
		typed string
		tabs  int
		want  []string
	}{
		{name: "bare command fills the prefix", typed: "/st", tabs: 1, want: []string{"/start "}},
		{name: "single name completes fully", saved: []string{"alpha"}, typed: "/restart al", tabs: 1, want: []string{"/restart alpha"}},
		{name: "drop completes from the list", saved: []string{"alpha"}, typed: "/drop al", tabs: 1, want: []string{"/drop alpha"}},
		{name: "add completes from the list", saved: []string{"alpha"}, typed: "/add al", tabs: 1, want: []string{"/add alpha"}},
		{
			name:  "start cycles all and a name",
			saved: []string{"allan"},
			typed: "/start al",
			tabs:  4,
			want:  []string{"/start all", "/start all", "/start allan", "/start allan"},
		},
		{
			name:  "a live target completes too",
			live:  []string{"gamma"},
			typed: "/restart g",
			tabs:  1,
			want:  []string{"/restart gamma"},
		},
		{
			name:  "telegram fills the prefix then the first mode",
			typed: "/telegram ",
			tabs:  3,
			want:  []string{"/telegram o", "/telegram on", "/telegram on"},
		},
		{name: "plain text is left alone", typed: "alpha", tabs: 1, want: []string{"alpha"}},
		{name: "a second word is left alone", saved: []string{"alpha"}, typed: "/start alpha ", tabs: 1, want: []string{"/start alpha "}},
		{name: "a comma list is left alone", saved: []string{"alpha"}, typed: "/add al,be", tabs: 1, want: []string{"/add al,be"}},
		{name: "an unknown command has no argument completion", typed: "/nope ", tabs: 1, want: []string{"/nope "}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newModel()
			m.list = savedList(tc.saved...)
			m.syncRows()
			for _, user := range tc.live {
				m.applyStatus(app.Status{User: user, Phase: app.PhaseRecording})
			}
			m.input.SetValue(tc.typed)
			got := make([]string, 0, len(tc.want))
			for range tc.want {
				m.Update(key("tab"))
				got = append(got, m.input.Value())
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("tabs on %q = %q, want %q", tc.typed, got, tc.want)
			}
		})
	}
}

// TestCandidates covers the pool tab offers before any cycling: whole lines,
// in list order, then whatever the run holds.
func TestCandidates(t *testing.T) {
	cases := []struct {
		name  string
		saved []string
		live  []string
		value string
		want  []string
	}{
		{name: "command prefix", value: "/st", want: []string{"/start", "/stop"}},
		{name: "telegram modes", value: "/telegram ", want: []string{"/telegram on", "/telegram off"}},
		{name: "start lists all first", saved: []string{"allan"}, value: "/start ", want: []string{"/start all", "/start allan"}},
		{name: "start by prefix", saved: []string{"beta"}, value: "/start a", want: []string{"/start all"}},
		{name: "saved then live", saved: []string{"alpha"}, live: []string{"gamma"}, value: "/restart ", want: []string{"/restart alpha", "/restart gamma"}},
		{name: "drop", saved: []string{"alpha"}, value: "/drop ", want: []string{"/drop alpha"}},
		{name: "no plain text", value: "alpha"},
		{name: "no commas", saved: []string{"alpha"}, value: "/add al,be"},
		{name: "no second word", saved: []string{"alpha"}, value: "/start alpha "},
		{name: "no arguments for another command", value: "/nope "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newModel()
			m.list = savedList(tc.saved...)
			m.syncRows()
			for _, user := range tc.live {
				m.applyStatus(app.Status{User: user, Phase: app.PhaseRecording})
			}
			if got := m.candidates(tc.value); !slices.Equal(got, tc.want) {
				t.Fatalf("candidates(%q) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}
}

// TestTypingResetsTheCycle proves a key after a completion starts the cycle over
// instead of resuming mid list.
func TestTypingResetsTheCycle(t *testing.T) {
	m := newModel()
	m.list = savedList("allan")
	m.syncRows()
	m.input.SetValue("/start al")
	m.Update(key("tab"))
	m.Update(key("tab"))
	if m.compIdx == 0 {
		t.Fatal("the second tab must advance the cycle")
	}
	m.Update(key("x"))
	if m.compIdx != 0 {
		t.Fatalf("typing must reset the cycle, got %d", m.compIdx)
	}
}

func TestHintLine(t *testing.T) {
	m := newModel()
	if got := m.hintLine(); !strings.Contains(got, "/quit exit") {
		t.Fatalf("the empty input must show the key reminder, got %q", got)
	}

	m.list = savedList("alpha")
	m.syncRows()
	m.input.SetValue("/st")
	got := m.hintLine()
	for _, want := range []string{"tab", "/start"} {
		if !strings.Contains(got, want) {
			t.Fatalf("hint %q is missing %q", got, want)
		}
	}

	m.input.SetValue("/start a")
	if got := m.hintLine(); !strings.Contains(got, "/start alpha") {
		t.Fatalf("hint %q is missing the name candidate", got)
	}
}

// TestCompletionRespectsTheValue proves an argument already typed is not
// replaced with an unrelated name.
func TestCompletionLeavesAnExactValueAlone(t *testing.T) {
	m := newModel()
	m.list = savedList("alpha")
	m.syncRows()
	m.input.SetValue("/restart alpha")
	m.Update(key("tab"))
	if got := m.input.Value(); got != "/restart alpha" {
		t.Fatalf("input = %q, want the value unchanged", got)
	}
}

// TestRememberRefusesWhatCannotBeSaved proves a url or a room id still runs, but
// never lands in the list.
// TestRemoveTakesANameOffTheList proves a removed idle target disappears from
// both the file and the table.
func TestRemoveTakesANameOffTheList(t *testing.T) {
	m := newModel()
	m.userPath = filepath.Join(t.TempDir(), "targets.txt")
	submit(m, "/add alpha, beta")

	submit(m, "/remove @alpha")
	if !slices.Equal(m.list, savedList("beta")) {
		t.Fatalf("list = %q, want [beta]", names(m.list))
	}
	if got := loadTargets(m.userPath); !slices.Equal(got, savedList("beta")) {
		t.Fatalf("saved = %q, want [beta]", names(got))
	}
	if _, ok := m.byName["alpha"]; ok {
		t.Fatalf("the row must go with the name: %q", m.byName)
	}
	if !slices.Equal(m.targets, []target{{user: "beta", label: "@beta", idle: true, since: m.now}}) {
		t.Fatalf("rows = %+v, want only beta", m.targets)
	}
	if got := logText(m); !strings.Contains(got, "removed @alpha") {
		t.Fatalf("activity missing the removal:\n%s", got)
	}
}

// TestRemoveStopsARunningTarget keeps the row so the stopped capture stays
// visible with its size.
func TestRemoveStopsARunningTarget(t *testing.T) {
	m := newModel()
	m.userPath = filepath.Join(t.TempDir(), "targets.txt")
	ctrl := &fakeCtrl{}
	m.ctrl = ctrl
	m.list = savedList("alpha", "beta")
	m.syncRows()

	submit(m, "/remove alpha")
	if !slices.Equal(ctrl.dropped, []string{"alpha"}) {
		t.Fatalf("dropped = %q, want [alpha]", ctrl.dropped)
	}
	if !slices.Equal(m.list, savedList("beta")) {
		t.Fatalf("list = %q, want [beta]", names(m.list))
	}
	if _, ok := m.byName["alpha"]; !ok {
		t.Fatal("a running target keeps its row until it stops")
	}
}

func TestRemoveRefusesAnUnknownName(t *testing.T) {
	m := newModel()
	m.userPath = filepath.Join(t.TempDir(), "targets.txt")
	submit(m, "/remove ghost")
	if got := logText(m); !strings.Contains(got, "@ghost is not saved") {
		t.Fatalf("activity missing the warning:\n%s", got)
	}
}

func TestRemoveCancelsAQueuedRestart(t *testing.T) {
	m := newModel()
	m.userPath = filepath.Join(t.TempDir(), "targets.txt")
	m.ctrl = &fakeCtrl{}
	submit(m, "/restart alpha")
	if len(m.restarting) != 1 {
		t.Fatalf("restarting = %q, want [alpha]", m.restarting)
	}
	submit(m, "/remove alpha")
	if len(m.restarting) != 0 {
		t.Fatalf("a removed target must not restart: %q", m.restarting)
	}
}

// TestConfigShowsTheNextRun proves /config prints the settings the flags would
// take, with the cookie masked and the output directory resolved.
func TestConfigShowsTheNextRun(t *testing.T) {
	m := newModel()
	m.userPath = "/tmp/targets.txt"
	m.list = savedList("alpha")
	m.cookies = map[string]string{"sessionid_ss": "abcdef1234"}
	m.settings.duration = 12
	m.settings.proxy = "socks5://127.0.0.1:1080"

	submit(m, "/config")
	logs := logText(m)
	for _, want := range []string{
		"wait for live, poll 3 min",
		"12 min",
		"(default)",
		"socks5://127.0.0.1:1080",
		"telegram   off",
		"sessionid_ss ••••1234",
		"1 saved · 0 autostart · /tmp/targets.txt",
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("config missing %q:\n%s", want, logs)
		}
	}
	if strings.Contains(logs, "abcdef1234") {
		t.Fatalf("the cookie must stay masked:\n%s", logs)
	}
	if strings.Contains(logs, "in flight") {
		t.Fatalf("an idle screen has no run to explain:\n%s", logs)
	}

	m.ctrl = &fakeCtrl{}
	submit(m, "/config")
	if got := logText(m); !strings.Contains(got, "changes apply to the next run") {
		t.Fatalf("a live run must say the settings wait:\n%s", got)
	}
}

func TestRememberRefusesWhatCannotBeSaved(t *testing.T) {
	m := newModel()
	m.userPath = filepath.Join(t.TempDir(), "targets.txt")
	m.remember([]string{"7123456789012345678", "https://www.tiktok.com/@a/live", "bad/name", "x", "alpha"})
	if !slices.Equal(m.list, savedList("alpha")) {
		t.Fatalf("list = %q, want [alpha]", names(m.list))
	}
	if got := loadTargets(m.userPath); !slices.Equal(got, savedList("alpha")) {
		t.Fatalf("saved = %q, want [alpha]", names(got))
	}
	if got := logText(m); strings.Count(got, "only usernames are saved") != 2 {
		t.Fatalf("each rejected target needs its own warning:\n%s", got)
	}
	if got := logText(m); strings.Count(got, "not a username") != 2 {
		t.Fatalf("each malformed name needs its own warning:\n%s", got)
	}
}

func TestIsUsername(t *testing.T) {
	for _, name := range []string{"aljazeeraenglish", "donut_ntp.42", "a_b.c1", "AB"} {
		if !isUsername(name) {
			t.Errorf("isUsername(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"", "a", "bad/name", "two words", "tab\there", "a@b", strings.Repeat("a", 25)} {
		if isUsername(name) {
			t.Errorf("isUsername(%q) = true, want false", name)
		}
	}
}

// TestSettingsSurviveTheSession is the promise /output makes: the next launch
// already points at the directory this session chose.
func TestSettingsSurviveTheSession(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	const out = "/mnt/c/Users/envs4/Downloads/tiktok"

	m := newModel()
	m.loadSaved()
	for _, line := range []string{
		"/output " + out,
		"/duration 45",
		"/interval 7",
		"/proxy socks5://127.0.0.1:1080",
		"/telegram on",
		"/once",
	} {
		submit(m, line)
	}

	next := newModel()
	next.loadSaved()
	want := settings{once: true, duration: 45, interval: 7, output: out, proxy: "socks5://127.0.0.1:1080", telegram: true}
	if next.settings != want {
		t.Fatalf("settings = %+v, want %+v", next.settings, want)
	}
	if got := next.argv([]string{"alpha"}); !slices.Contains(got, out) {
		t.Fatalf("the next run's flags are missing the saved output: %q", got)
	}

	submit(next, "/output")
	if next.settings.output != "" {
		t.Fatalf("an empty /output must clear it: %+v", next.settings)
	}
	again := newModel()
	again.loadSaved()
	if again.settings.output != "" {
		t.Fatalf("a cleared output must stay cleared: %+v", again.settings)
	}
}

func TestSettingsFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "settings.json")
	in := storedSettings{Output: "/mnt/c/x", Interval: 7, Telegram: true}
	if err := writeSettings(path, in); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := readSettings(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != in {
		t.Fatalf("read back = %+v, want %+v", got, in)
	}
	if body, err := os.ReadFile(path); err != nil || !strings.HasSuffix(string(body), "\n") {
		t.Fatalf("the file must end with a newline: %q %v", body, err)
	}
	if got, err := readSettings(filepath.Join(t.TempDir(), "missing.json")); err != nil || got != (storedSettings{}) {
		t.Fatalf("a missing file is the defaults: %+v %v", got, err)
	}
	if got, err := readSettings(""); err != nil || got != (storedSettings{}) {
		t.Fatalf("no path disables persistence: %+v %v", got, err)
	}
	if err := writeSettings("", in); err != nil {
		t.Fatalf("writing without a path is a no-op: %v", err)
	}
}

// TestSavedSettingsRespectTheFlagLimits keeps a hand-edited file from breaking
// every launch.
func TestSavedSettingsRespectTheFlagLimits(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := filepath.Join(dir, "tokgo", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"interval": 0, "duration": -5, "output": "/tmp/rec"}`), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	m := newModel()
	m.loadSaved()
	want := settings{interval: 3, output: "/tmp/rec"}
	if m.settings != want {
		t.Fatalf("settings = %+v, want %+v", m.settings, want)
	}
}

func TestBrokenSettingsFileFallsBackToTheDefaults(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := filepath.Join(dir, "tokgo", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	m := newModel()
	m.loadSaved()
	if m.settings != (settings{interval: 3}) {
		t.Fatalf("settings = %+v, want the defaults", m.settings)
	}
	if got := logText(m); !strings.Contains(got, "settings.json") {
		t.Fatalf("the broken file needs a warning:\n%s", got)
	}
}

// TestAutostartCommandMarksTheGroup proves /autostart moves one target into the
// boot group and off moves it back, while /add always lands in the manual one.
func TestAutostartCommandMarksTheGroup(t *testing.T) {
	m := newModel()
	m.userPath = filepath.Join(t.TempDir(), "targets.txt")
	submit(m, "/add alpha, beta")
	if got := loadTargets(m.userPath); !slices.Equal(got, savedList("alpha", "beta")) {
		t.Fatalf("a new target must not autostart: %s", names(got))
	}

	submit(m, "/autostart @alpha")
	want := []saved{{user: "alpha", auto: true}, {user: "beta"}}
	if got := loadTargets(m.userPath); !slices.Equal(got, want) {
		t.Fatalf("saved = %+v, want %+v", got, want)
	}
	if !m.targets[m.byName["alpha"]].auto {
		t.Fatal("the idle row must show the group")
	}
	if len(m.pendingStart) != 0 {
		t.Fatalf("a mark is for the next launch, not this one: %q", m.pendingStart)
	}
	if got := logText(m); !strings.Contains(got, "starts with the next tokgo tui") {
		t.Fatalf("activity missing the confirmation:\n%s", got)
	}
	if v := m.View(); !strings.Contains(v, "auto") {
		t.Fatalf("the table must name the group:\n%s", v)
	}

	submit(m, "/autostart alpha off")
	if got := loadTargets(m.userPath); !slices.Equal(got, savedList("alpha", "beta")) {
		t.Fatalf("off must clear the flag: %+v", got)
	}
	if m.targets[m.byName["alpha"]].auto {
		t.Fatal("the idle row must go back to idle")
	}

	submit(m, "/autostart gamma")
	if got := loadTargets(m.userPath); !slices.Equal(got, []saved{{user: "alpha"}, {user: "beta"}, {user: "gamma", auto: true}}) {
		t.Fatalf("an unknown name is saved in the autostart group: %+v", got)
	}
}

// TestAutostartListingNamesTheGroups proves the bare command answers which
// group every saved target is in.
func TestAutostartListingNamesTheGroups(t *testing.T) {
	m := newModel()
	m.userPath = filepath.Join(t.TempDir(), "targets.txt")
	m.list = []saved{{user: "alpha"}, {user: "beta", auto: true}}
	submit(m, "/autostart")
	logs := logText(m)
	for _, want := range []string{"autostart 1 · @beta", "manual    1 · @alpha"} {
		if !strings.Contains(logs, want) {
			t.Fatalf("listing missing %q:\n%s", want, logs)
		}
	}

	m.list = nil
	submit(m, "/autostart")
	if got := logText(m); !strings.Contains(got, "no saved targets") {
		t.Fatalf("an empty list needs a warning:\n%s", got)
	}
}

// TestBootStaggersTheAutostartGroup proves opening the screen starts the group
// one name per stagger window instead of all at once.
func TestBootStaggersTheAutostartGroup(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := filepath.Join(dir, "tokgo", "targets.txt")
	if err := saveTargets(path, []saved{
		{user: "alpha", auto: true},
		{user: "beta", auto: true},
		{user: "gamma"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	m := newModel()
	ctrl := &fakeCtrl{}
	m.makeRunner = func(config.Config, telegram.Config) controller { return ctrl }
	m.loadSaved()
	if !slices.Equal(m.pendingStart, []string{"alpha", "beta"}) {
		t.Fatalf("queue = %q, want the autostart group only", m.pendingStart)
	}

	start := m.now
	_, cmd := m.Update(tickMsg(start.Add(time.Second)))
	if cmd == nil {
		t.Fatal("the first name must start the run")
	}
	if m.ctrl != ctrl {
		t.Fatal("the first name must install the runner")
	}
	if !slices.Equal(m.users, []string{"alpha"}) {
		t.Fatalf("first run = %q, want [alpha]", m.users)
	}

	for elapsed := 2; elapsed < 11; elapsed++ {
		m.Update(tickMsg(start.Add(time.Duration(elapsed) * time.Second)))
		if len(ctrl.added) != 0 {
			t.Fatalf("nothing may start before 10s, got %q at %ds", ctrl.added, elapsed)
		}
	}
	m.Update(tickMsg(start.Add(11 * time.Second)))
	if !slices.Equal(ctrl.added, []string{"beta"}) {
		t.Fatalf("added = %q, want [beta] after the stagger", ctrl.added)
	}
	if len(m.pendingStart) != 0 {
		t.Fatalf("the queue must drain, got %q", m.pendingStart)
	}
	if slices.Contains(ctrl.added, "gamma") {
		t.Fatalf("the manual group must not autostart: %q", ctrl.added)
	}
}

// TestRemoveAndDropCancelTheAutostartQueue proves a queued name never starts
// after it was removed or dropped.
func TestRemoveAndDropCancelTheAutostartQueue(t *testing.T) {
	m := newModel()
	m.userPath = filepath.Join(t.TempDir(), "targets.txt")
	m.list = autoList("alpha", "beta")
	m.pendingStart = m.autoNames()

	submit(m, "/remove beta")
	if slices.Contains(m.pendingStart, "beta") {
		t.Fatalf("a removed name must leave the queue: %q", m.pendingStart)
	}

	m.ctrl = &fakeCtrl{}
	submit(m, "/drop alpha")
	if slices.Contains(m.pendingStart, "alpha") {
		t.Fatalf("a dropped name must leave the queue: %q", m.pendingStart)
	}
}

// TestStartAllCoversBothGroups proves a manual start all walks the whole list,
// autostart group included, without queueing a name twice.
func TestStartAllCoversBothGroups(t *testing.T) {
	m := newModel()
	ctrl := &fakeCtrl{}
	m.makeRunner = func(config.Config, telegram.Config) controller { return ctrl }
	m.list = []saved{{user: "alpha"}, {user: "beta", auto: true}}
	m.pendingStart = m.autoNames()
	m.syncRows()
	submit(m, "/start all")
	if !slices.Equal(m.users, []string{"alpha"}) {
		t.Fatalf("users = %q, want the first name only", m.users)
	}
	if !slices.Equal(m.pendingStart, []string{"beta"}) {
		t.Fatalf("queued = %q, want the second name once", m.pendingStart)
	}
	m.Update(tickMsg(m.now.Add(autoStagger)))
	if !slices.Equal(ctrl.added, []string{"beta"}) {
		t.Fatalf("added = %q, want [beta] after the stagger", ctrl.added)
	}
	if len(m.pendingStart) != 0 {
		t.Fatalf("the queue must drain: %q", m.pendingStart)
	}
}
