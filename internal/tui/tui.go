// Package tui is the interactive front end: one terminal screen with a target
// table, an activity pane and an input line. It drives the same cli.Parse and
// app.Runner as the flags path, so validation and behaviour cannot drift.
package tui

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"tokgo/internal/app"
	"tokgo/internal/boot"
	"tokgo/internal/cli"
	"tokgo/internal/config"
	"tokgo/internal/logx"
	"tokgo/internal/telegram"
)

// maxLogLines bounds the retained scrollback; the view only ever shows the tail.
const maxLogLines = 2000

type logLine struct {
	level string
	text  string
}

// event is one push onto the UI channel. Exactly one field is set: a console
// line, or a status for one target.
type event struct {
	line *logLine
	st   *app.Status
}

type (
	runDoneMsg struct{ err error }
	tickMsg    time.Time
)

// target is one row of the table: a username, or the single url / room id
// target keyed "".
type target struct {
	user   string
	label  string
	phase  app.Phase
	bytes  int64
	since  time.Time     // when the current phase started
	active time.Duration // time spent in active phases, kept for the terminal row
}

// settings is what the input commands change between runs.
type settings struct {
	once     bool
	duration int
	interval int
	output   string
	proxy    string
	telegram bool
}

// controller is the slice of the runner the screen needs, so tests can fake it.
type controller interface {
	Run(context.Context, []string) error
	OnStatus(func(app.Status))
	AddUser(string) error
	DropUser(string) bool
}

func defaultRunner(cfg config.Config, uploads telegram.Config) controller {
	return app.New(cfg, uploads)
}

// Model is the whole TUI state.
type Model struct {
	version string
	uploads telegram.Config
	cookies map[string]string
	in      <-chan event
	push    func(event)

	width  int
	height int
	now    time.Time

	settings settings

	targets     []target
	byName      map[string]int
	logs        []logLine
	started     time.Time
	ended       time.Time
	runInterval time.Duration // the in-flight run's poll interval, not next-run settings
	finishing   bool
	result      error
	quit        bool
	cancel      context.CancelFunc
	ctx         context.Context
	users       []string

	input   textinput.Model
	history []string
	histPos int

	makeRunner func(config.Config, telegram.Config) controller
	ctrl       controller
}

// New builds the one screen with the defaults the flags would give.
func New(version string, uploads telegram.Config, cookies map[string]string, in <-chan event) *Model {
	m := &Model{
		version:     version,
		uploads:     uploads,
		cookies:     cookies,
		in:          in,
		now:         time.Now(),
		settings:    settings{interval: 3},
		runInterval: 3 * time.Minute,
		byName:      make(map[string]int),
		makeRunner:  defaultRunner,
	}
	m.input = textinput.New()
	m.input.Prompt = "› "
	m.input.CharLimit = 400
	m.input.Width = 40
	m.input.Focus()
	return m
}

// Run starts the UI. It owns the log sink for the whole session so the runner's
// output lands in the view instead of corrupting it.
func Run(version string) error {
	ch := make(chan event, 512)
	push := func(ev event) {
		select {
		case ch <- ev:
		default: // a slow UI must never stall a capture or a status emitter
		}
	}
	logx.SetSink(func(level, text string) {
		push(event{line: &logLine{level: level, text: text}})
	})
	defer logx.SetSink(nil)

	if err := boot.CheckFFmpeg(""); err != nil {
		return err
	}
	cookies := boot.ReadCookies(boot.CookieFile)
	uploads, err := telegram.Load("telegram.json")
	if err != nil {
		logx.Warnf("telegram.json: %v", err)
	}

	m := New(version, uploads, cookies, ch)
	m.push = push
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err = p.Run()
	m.stop()
	if errors.Is(err, tea.ErrProgramKilled) {
		return nil
	}
	return err
}

// stop aborts a run that is still in flight, so force-quitting the UI still
// ends the capture instead of orphaning it.
func (m *Model) stop() {
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
}

func (m *Model) Init() tea.Cmd { return tea.Batch(m.waitEvent(), tickCmd(), textinput.Blink) }

func (m *Model) waitEvent() tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-m.in
		if !ok {
			return nil
		}
		return ev
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.input.Width = max(msg.Width-3, 1)
		return m, nil

	case event:
		if msg.line != nil {
			m.appendLog(*msg.line)
		}
		if msg.st != nil {
			m.applyStatus(*msg.st)
		}
		return m, m.waitEvent()

	case tickMsg:
		m.now = time.Time(msg)
		return m, tickCmd()

	case runDoneMsg:
		m.result = msg.err
		m.ended = time.Now()
		m.ctrl = nil
		m.cancel = nil
		m.ctx = nil
		m.users = nil
		return m, nil

	case tea.KeyMsg:
		return m.key(msg)
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *Model) appendLog(line logLine) {
	line.text = strings.ReplaceAll(line.text, "\n", " ")
	m.logs = append(m.logs, line)
	if len(m.logs) > maxLogLines {
		m.logs = m.logs[len(m.logs)-maxLogLines:]
	}
}

func (m *Model) key(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		if m.ctrl != nil && !m.finishing {
			m.stopRun()
			return m, nil
		}
		m.quit = true
		return m, tea.Quit
	case "esc":
		if m.ctrl != nil {
			m.stopRun()
			return m, nil
		}
		m.input.SetValue("")
		m.histPos = len(m.history)
		return m, nil
	case "up":
		m.recall(-1)
		return m, nil
	case "down":
		m.recall(1)
		return m, nil
	case "enter":
		return m, m.submit()
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *Model) submit() tea.Cmd {
	line := strings.TrimSpace(m.input.Value())
	if line == "" {
		return nil
	}
	m.history = append(m.history, line)
	m.histPos = len(m.history)
	m.input.SetValue("")
	if strings.HasPrefix(line, "/") {
		return m.command(line)
	}
	return m.enterTargets(splitTargets(line))
}

func (m *Model) recall(delta int) {
	if len(m.history) == 0 {
		return
	}
	next := min(max(m.histPos+delta, 0), len(m.history))
	m.histPos = next
	if next == len(m.history) {
		m.input.SetValue("")
		return
	}
	m.input.SetValue(m.history[next])
}

func (m *Model) stopRun() {
	if m.ctrl == nil {
		return
	}
	m.finishing = true
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
}

// launch validates the names through the CLI and installs the run's runner. It
// returns only setup errors; the caller returns runCmd so the blocking Run
// executes as a tea.Cmd and reports back through runDoneMsg.
func (m *Model) launch(names []string) error {
	parsed, err := cli.Parse(m.argv(names))
	if err != nil {
		return err
	}
	cfg := parsed.Config
	cfg.Cookies = m.cookies
	if cfg.OutputDir != "" {
		if err := os.MkdirAll(cfg.OutputDir, 0o755); err != nil {
			return err
		}
	}

	now := m.now
	if now.IsZero() {
		now = time.Now()
	}
	ctx, cancel := context.WithCancel(context.Background())
	runner := m.makeRunner(cfg, m.uploads)
	m.ctrl = runner
	m.cancel = cancel
	m.ctx = ctx
	m.users = parsed.Users
	m.started = now
	m.now = now
	m.runInterval = time.Duration(cfg.AutomaticInterval) * time.Minute
	m.ended = time.Time{}
	m.finishing = false
	m.result = nil
	m.targets, m.byName = m.seed(cfg, parsed.Users, now)
	runner.OnStatus(func(s app.Status) {
		if m.push != nil {
			m.push(event{st: &s})
		}
	})
	return nil
}

func (m *Model) runCmd() tea.Cmd {
	ctrl, ctx, users := m.ctrl, m.ctx, m.users
	return func() tea.Msg {
		return runDoneMsg{ctrl.Run(ctx, users)}
	}
}

func (m *Model) enterTargets(names []string) tea.Cmd {
	if len(names) == 0 {
		return nil
	}
	if m.ctrl != nil {
		for _, name := range names {
			if err := m.ctrl.AddUser(name); err != nil && !errors.Is(err, app.ErrUserRunning) {
				m.warn(err.Error())
			}
		}
		return nil
	}
	if err := m.launch(names); err != nil {
		m.warn(err.Error())
		return nil
	}
	return m.runCmd()
}

func (m *Model) warn(text string) {
	m.appendLog(logLine{level: "WARN", text: text})
}

var helpLines = []string{
	"/add <name,...>  add targets, or start a run when idle",
	"/once             record one session per target",
	"/wait             wait for the next stream (default)",
	"/duration <min>   cap each session in minutes, 0 = no cap",
	"/interval <min>   minutes between liveness polls, minimum 1",
	"/output <dir>     output directory",
	"/proxy <url>      proxy for lookups, alone clears it",
	"/telegram on|off  upload each recording when it is written",
	"/drop <user>      stop recording one target",
	"/stop             stop the run and remux what was captured",
	"/quit             leave now, without finishing a remux",
	"/help             show this list",
}

func (m *Model) command(line string) tea.Cmd {
	name, rest, _ := strings.Cut(strings.TrimPrefix(line, "/"), " ")
	rest = strings.TrimSpace(rest)
	switch name {
	case "add":
		return m.enterTargets(splitTargets(rest))
	case "once":
		m.settings.once = true
		m.nextRun("once")
	case "wait":
		m.settings.once = false
		m.nextRun("wait")
	case "duration":
		m.applySetting("-duration", rest, "duration", func(cfg config.Config) {
			m.settings.duration = cfg.Duration
		})
	case "interval":
		m.applySetting("-automatic_interval", rest, "interval", func(cfg config.Config) {
			m.settings.interval = cfg.AutomaticInterval
		})
	case "output":
		m.applySetting("-output", rest, "", func(cfg config.Config) {
			m.settings.output = cfg.OutputDir
		})
	case "proxy":
		m.applySetting("-proxy", rest, "", func(cfg config.Config) {
			m.settings.proxy = cfg.Proxy
		})
	case "telegram":
		m.setTelegram(rest)
	case "drop":
		m.drop(rest)
	case "stop":
		m.stopCommand()
	case "quit":
		m.stopRun()
		m.quit = true
		return tea.Quit
	case "help":
		for _, help := range helpLines {
			m.appendLog(logLine{level: "INFO", text: help})
		}
	default:
		m.warn("unknown command /" + name)
	}
	return nil
}

func (m *Model) applySetting(flag, raw, label string, set func(config.Config)) {
	cfg, err := m.check(flag, raw)
	if err != nil {
		m.warn(err.Error())
		return
	}
	set(cfg)
	if label != "" {
		m.nextRun(label)
	}
}

func (m *Model) nextRun(label string) {
	if m.ctrl != nil {
		m.warn(label + " applies to the next run")
	}
}

func (m *Model) setTelegram(raw string) {
	switch strings.ToLower(raw) {
	case "on":
		m.settings.telegram = true
	case "off":
		m.settings.telegram = false
	default:
		m.warn("telegram takes on or off")
	}
}

func (m *Model) drop(raw string) {
	user := strings.TrimPrefix(strings.TrimSpace(raw), "@")
	if user == "" {
		m.warn("drop needs a username")
		return
	}
	if m.ctrl == nil {
		m.warn(app.ErrNotRunning.Error())
		return
	}
	m.ctrl.DropUser(user)
}

func (m *Model) stopCommand() {
	if m.ctrl == nil {
		m.warn(app.ErrNotRunning.Error())
		return
	}
	m.stopRun()
}

// check runs the candidate settings through the CLI with a placeholder target,
// so a command accepts exactly what the flags accept.
func (m *Model) check(flag, raw string) (config.Config, error) {
	args := withFlag(m.argv([]string{"placeholder"}), flag, raw)
	parsed, err := cli.Parse(args)
	return parsed.Config, err
}

func withFlag(args []string, flag, raw string) []string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] != flag {
			continue
		}
		out := append([]string(nil), args[:i+1]...)
		out = append(out, raw)
		return append(out, args[i+2:]...)
	}
	return append(args, flag, raw)
}

// seed lists the targets the settings promised before any status arrives, so
// the screen never opens empty.
func (m *Model) seed(cfg config.Config, users []string, now time.Time) ([]target, map[string]int) {
	var rows []target
	add := func(user, label string) {
		rows = append(rows, target{user: user, label: label, since: now})
	}
	switch {
	case len(users) > 1:
		for _, user := range users {
			add(user, "@"+user)
		}
	case cfg.URL != "":
		add("", cfg.URL)
	case cfg.RoomID != "":
		add("", "room "+cfg.RoomID)
	case len(users) == 1:
		add(users[0], "@"+users[0])
	}
	byName := make(map[string]int, len(rows))
	for i, row := range rows {
		byName[row.user] = i
	}
	return rows, byName
}

// applyStatus merges one status into the rows. An unknown user becomes a row,
// so a target added mid-run shows up without a separate list.
func (m *Model) applyStatus(s app.Status) {
	now := m.now
	if now.IsZero() {
		now = time.Now()
	}
	i, ok := m.byName[s.User]
	if !ok {
		m.targets = append(m.targets, target{user: s.User, label: statusLabel(s.User), since: now})
		i = len(m.targets) - 1
		m.byName[s.User] = i
	}
	t := &m.targets[i]
	if t.phase != s.Phase {
		if t.phase.Active() {
			t.active += now.Sub(t.since)
		}
		t.phase = s.Phase
		t.since = now
		if s.Phase == app.PhaseRecording {
			t.bytes = 0
		}
	}
	if s.Bytes > 0 {
		t.bytes = s.Bytes
	}
}

func statusLabel(user string) string {
	if user == "" {
		return "stream"
	}
	return "@" + user
}

// argv renders the settings plus the names into the exact flags the CLI takes.
func (m *Model) argv(names []string) []string {
	var args []string
	if m.settings.once {
		args = append(args, "-once")
	}
	switch {
	case len(names) > 1:
		args = append(args, "-user", strings.Join(names, ","))
	case len(names) == 1 && isRoomID(names[0]):
		args = append(args, "-room_id", names[0])
	case len(names) == 1 && strings.Contains(names[0], "tiktok.com"):
		args = append(args, "-url", names[0])
	case len(names) == 1:
		args = append(args, "-user", names[0])
	}
	args = append(args, "-duration", strconv.Itoa(m.settings.duration))
	if m.settings.interval != 3 {
		args = append(args, "-automatic_interval", strconv.Itoa(m.settings.interval))
	}
	if m.settings.output != "" {
		args = append(args, "-output", m.settings.output)
	}
	if m.settings.proxy != "" {
		args = append(args, "-proxy", m.settings.proxy)
	}
	if m.settings.telegram {
		args = append(args, "-telegram")
	}
	return args
}

// splitTargets accepts the input line's commas and spaces, and drops the @ the
// runner does not want.
func splitTargets(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimPrefix(field, "@")
		if field != "" {
			out = append(out, field)
		}
	}
	return out
}

func isRoomID(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
