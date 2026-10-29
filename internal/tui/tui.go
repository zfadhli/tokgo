// Package tui is the interactive front end: a setup form and a live monitor of
// a run that may record several accounts at once. It drives the same cli.Parse
// and app.Runner as the flags path, so validation and behaviour cannot drift.
package tui

import (
	"context"
	"errors"
	"os"
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

type screen int

const (
	screenSetup screen = iota
	screenMonitor
	screenDone
)

// Focus ring: the mode row, the five text fields, the telegram toggle, start.
const (
	fieldMode = iota
	fieldTargets
	fieldDuration
	fieldInterval
	fieldOutput
	fieldProxy
	fieldTelegram
	fieldStart
	fieldCount
)

// inputFor maps a focus index to the text field it edits, or -1.
func inputFor(focus int) int {
	if focus >= fieldTargets && focus <= fieldProxy {
		return focus - fieldTargets
	}
	return -1
}

var modeNames = []string{"wait for live", "record once"}

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

// target is one row of the monitor: a username, or the single url / room id
// target keyed "".
type target struct {
	user   string
	label  string
	phase  app.Phase
	detail string
	room   string
	file   string
	bytes  int64
	since  time.Time     // when the current phase started
	active time.Duration // time spent in active phases, kept for the terminal row
}

// controller is the slice of the runner the monitor needs, so tests can fake it.
type controller interface {
	AddUser(string) error
	DropUser(string) bool
}

// Model is the whole TUI state.
type Model struct {
	version string
	uploads telegram.Config
	cookies map[string]string
	in      <-chan event
	push    func(event)

	screen screen
	width  int
	height int

	// setup
	mode   int
	inputs [5]textinput.Model
	focus  int
	upload bool
	err    error

	// monitor and done
	targets   []target
	byName    map[string]int
	sel       int
	interval  time.Duration
	logs      []logLine
	started   time.Time
	ended     time.Time
	now       time.Time
	finishing bool
	result    error
	cancel    context.CancelFunc
	ctrl      controller
	prompt    textinput.Model
	prompting bool
	notice    string
	noticeAt  time.Time
}

// New builds the form with the defaults the flags would give.
func New(version string, uploads telegram.Config, cookies map[string]string, in <-chan event) *Model {
	m := &Model{
		version:  version,
		uploads:  uploads,
		cookies:  cookies,
		in:       in,
		now:      time.Now(),
		byName:   make(map[string]int),
		interval: 3 * time.Minute,
	}
	hints := [5]string{
		"@user, or a comma or space separated list",
		"minutes, 0 = no cap",
		"minutes between liveness checks",
		"current directory",
		"optional, lookups only",
	}
	for i := range m.inputs {
		in := textinput.New()
		in.Prompt = "› "
		in.Placeholder = hints[i]
		in.CharLimit = 400
		in.Width = 46
		m.inputs[i] = in
	}
	m.inputs[1].SetValue("0")
	m.inputs[2].SetValue("3")

	m.prompt = textinput.New()
	m.prompt.Prompt = "add target › "
	m.prompt.CharLimit = 400
	m.prompt.Width = 40

	m.focusInput()
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

func (m *Model) Init() tea.Cmd { return tea.Batch(m.waitEvent(), tickCmd()) }

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
		m.screen = screenDone
		m.stop()
		return m, nil

	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m *Model) appendLog(line logLine) {
	m.logs = append(m.logs, line)
	if len(m.logs) > maxLogLines {
		m.logs = m.logs[len(m.logs)-maxLogLines:]
	}
}

func (m *Model) key(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.prompting {
		return m.promptKey(msg)
	}
	switch m.screen {
	case screenSetup:
		return m.setupKey(msg)
	case screenMonitor:
		return m.monitorKey(msg)
	default:
		return m.doneKey(msg)
	}
}

func (m *Model) setupKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		return m, tea.Quit
	case "tab", "down":
		m.focus = (m.focus + 1) % fieldCount
		m.focusInput()
		return m, nil
	case "shift+tab", "up":
		m.focus = (m.focus + fieldCount - 1) % fieldCount
		m.focusInput()
		return m, nil
	case "left":
		if m.focus == fieldMode {
			m.mode = (m.mode + len(modeNames) - 1) % len(modeNames)
		}
		return m, nil
	case "right":
		if m.focus == fieldMode {
			m.mode = (m.mode + 1) % len(modeNames)
		}
		return m, nil
	case " ":
		if m.focus == fieldTelegram {
			m.upload = !m.upload
			return m, nil
		}
	case "enter":
		switch m.focus {
		case fieldTelegram:
			m.upload = !m.upload
			return m, nil
		case fieldStart:
			return m, m.start()
		default:
			m.focus = (m.focus + 1) % fieldCount
			m.focusInput()
			return m, nil
		}
	}

	if i := inputFor(m.focus); i >= 0 {
		var cmd tea.Cmd
		m.inputs[i], cmd = m.inputs[i].Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *Model) monitorKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		if m.finishing {
			return m, tea.Quit
		}
		m.finishing = true
		m.stop()
		return m, nil
	case "up", "k":
		m.move(-1)
		return m, nil
	case "down", "j", "tab":
		m.move(1)
		return m, nil
	case "a":
		m.prompting = true
		m.prompt.SetValue("")
		m.prompt.Focus()
		return m, nil
	case "x":
		if t := m.selected(); t != nil && m.ctrl != nil {
			m.ctrl.DropUser(t.user)
		}
		return m, nil
	}
	return m, nil
}

func (m *Model) promptKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		m.commitPrompt()
		return m, nil
	case "esc":
		m.closePrompt()
		return m, nil
	case "ctrl+c":
		m.closePrompt()
		return m.monitorKey(msg)
	}
	var cmd tea.Cmd
	m.prompt, cmd = m.prompt.Update(msg)
	return m, cmd
}

// commitPrompt turns the typed line into targets. A name the run already holds
// is not an error: the user retyped a target that is on screen.
func (m *Model) commitPrompt() {
	m.prompting = false
	m.prompt.Blur()
	for _, name := range splitTargets(m.prompt.Value()) {
		if m.ctrl == nil {
			return
		}
		if err := m.ctrl.AddUser(name); err != nil && !errors.Is(err, app.ErrUserRunning) {
			m.notice = err.Error()
			m.noticeAt = m.now
		}
	}
}

func (m *Model) closePrompt() {
	m.prompting = false
	m.prompt.Blur()
}

func (m *Model) move(delta int) {
	if len(m.targets) == 0 {
		return
	}
	m.sel = min(max(m.sel+delta, 0), len(m.targets)-1)
}

func (m *Model) selected() *target {
	if len(m.targets) == 0 {
		return nil
	}
	m.sel = min(max(m.sel, 0), len(m.targets)-1)
	return &m.targets[m.sel]
}

func (m *Model) doneKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "r", "enter":
		m.screen = screenSetup
		m.targets = nil
		m.byName = make(map[string]int)
		m.sel = 0
		m.logs = nil
		m.result = nil
		m.finishing = false
		m.ctrl = nil
		m.focusInput()
		return m, nil
	default:
		return m, tea.Quit
	}
}

func (m *Model) focusInput() {
	for i := range m.inputs {
		if inputFor(m.focus) == i {
			m.inputs[i].Focus()
			continue
		}
		m.inputs[i].Blur()
	}
}

// start validates the form through the CLI parser and launches the run.
func (m *Model) start() tea.Cmd {
	parsed, err := cli.Parse(m.argv())
	if err != nil {
		m.err = err
		return nil
	}
	cfg := parsed.Config
	cfg.Cookies = m.cookies
	if cfg.OutputDir != "" {
		if err := os.MkdirAll(cfg.OutputDir, 0o755); err != nil {
			m.err = err
			return nil
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.err = nil
	m.screen = screenMonitor
	m.started = time.Now()
	m.now = m.started
	m.ended = time.Time{}
	m.interval = time.Duration(cfg.AutomaticInterval) * time.Minute
	m.logs = nil
	m.result = nil
	m.finishing = false
	m.notice = ""
	m.targets, m.byName = m.seed(cfg, parsed.Users)
	m.sel = 0

	runner := app.New(cfg, m.uploads)
	m.ctrl = runner
	if m.push != nil {
		runner.OnStatus(func(s app.Status) { m.push(event{st: &s}) })
	}
	return func() tea.Msg {
		return runDoneMsg{runner.Run(ctx, parsed.Users)}
	}
}

// seed lists the targets the form promised before any status arrives, so the
// monitor never opens empty.
func (m *Model) seed(cfg config.Config, users []string) ([]target, map[string]int) {
	var rows []target
	add := func(user, label string) {
		rows = append(rows, target{user: user, label: label, since: m.now})
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
	if s.Detail != "" || !s.Phase.Active() {
		t.detail = s.Detail
	}
	if s.RoomID != "" {
		t.room = s.RoomID
	}
	if s.File != "" {
		t.file = s.File
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

// argv renders the form as the equivalent command line. Everything the form can
// express goes through the same parser the flags use.
func (m *Model) argv() []string {
	var args []string
	if m.mode == 1 {
		args = append(args, "-once")
	}
	raw := strings.TrimSpace(m.inputs[0].Value())
	targets := splitTargets(raw)
	switch {
	case len(targets) == 0:
	case len(targets) == 1 && isRoomID(targets[0]):
		args = append(args, "-room_id", targets[0])
	case len(targets) == 1 && strings.Contains(targets[0], "tiktok.com"):
		args = append(args, "-url", targets[0])
	case len(targets) == 1:
		args = append(args, "-user", raw)
	default:
		args = append(args, "-user", strings.Join(targets, ","))
	}
	for i, flag := range []string{"-duration", "-automatic_interval", "-output", "-proxy"} {
		v := strings.TrimSpace(m.inputs[i+1].Value())
		if flag == "-automatic_interval" && (v == "" || v == "3") {
			continue
		}
		if v != "" {
			args = append(args, flag, v)
		}
	}
	if m.upload {
		args = append(args, "-telegram")
	}
	return args
}

// splitTargets accepts the one field's commas and spaces, and drops the @ the
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
