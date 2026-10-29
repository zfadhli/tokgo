// Package tui is the interactive front end: a setup form and a live view of a
// recording run. It drives the same cli.Parse and app.Runner as the flags path,
// so validation and behaviour cannot drift.
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
	"tokgo/internal/logx"
	"tokgo/internal/telegram"
)

type screen int

const (
	screenForm screen = iota
	screenRunning
	screenDone
)

// Focus ring: the mode row, the four text fields, the telegram toggle, start.
const (
	fieldMode = iota
	fieldTarget
	fieldDuration
	fieldOutput
	fieldProxy
	fieldTelegram
	fieldStart
	fieldCount
)

// inputFor maps a focus index to the text field it edits, or -1.
func inputFor(focus int) int {
	if focus >= fieldTarget && focus <= fieldProxy {
		return focus - fieldTarget
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

type (
	logMsg     logLine
	runDoneMsg struct{ err error }
	tickMsg    time.Time
)

// Model is the whole TUI state.
type Model struct {
	version string
	uploads telegram.Config
	cookies map[string]string
	in      <-chan logLine

	screen screen
	width  int
	height int

	// form
	mode   int
	inputs [4]textinput.Model
	focus  int
	upload bool
	err    error

	// running and done
	logs      []logLine
	started   time.Time
	now       time.Time
	capSecs   int
	finishing bool
	result    error
	cancel    context.CancelFunc
}

// New builds the form with the defaults the flags would give.
func New(version string, uploads telegram.Config, cookies map[string]string, in <-chan logLine) *Model {
	m := &Model{
		version: version,
		uploads: uploads,
		cookies: cookies,
		in:      in,
		now:     time.Now(),
	}
	hints := [4]string{
		"@user, tiktok.com url or room id",
		"0",
		"current directory",
		"optional, lookups only",
	}
	for i := range m.inputs {
		in := textinput.New()
		in.Prompt = "› "
		in.Placeholder = hints[i]
		in.CharLimit = 200
		in.Width = 46
		m.inputs[i] = in
	}
	m.inputs[1].SetValue("0")
	m.focusInput()
	return m
}

// Run starts the UI. It owns the log sink for the whole session so the runner's
// output lands in the view instead of corrupting it.
func Run(version string) error {
	ch := make(chan logLine, 512)
	logx.SetSink(func(level, text string) {
		select {
		case ch <- logLine{level: level, text: text}:
		default: // a stalled UI must never block a recording
		}
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

func (m *Model) Init() tea.Cmd { return tea.Batch(m.waitLog(), tickCmd()) }

func (m *Model) waitLog() tea.Cmd {
	return func() tea.Msg {
		line, ok := <-m.in
		if !ok {
			return nil
		}
		return logMsg(line)
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

	case logMsg:
		m.appendLog(logLine(msg))
		return m, m.waitLog()

	case tickMsg:
		m.now = time.Time(msg)
		return m, tickCmd()

	case runDoneMsg:
		m.result = msg.err
		m.screen = screenDone
		m.finishing = false
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
	switch m.screen {
	case screenForm:
		return m.formKey(msg)
	case screenRunning:
		return m.runningKey(msg)
	default:
		return m.doneKey(msg)
	}
}

func (m *Model) formKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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

func (m *Model) runningKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q", "esc":
		if m.finishing {
			return m, tea.Quit
		}
		m.finishing = true
		m.stop()
		return m, nil
	}
	return m, nil
}

func (m *Model) doneKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "r", "enter":
		m.screen = screenForm
		m.logs = nil
		m.result = nil
		m.finishing = false
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
	m.screen = screenRunning
	m.started = time.Now()
	m.now = m.started
	m.capSecs = cfg.Duration
	m.logs = nil
	m.result = nil
	m.finishing = false

	runner := app.New(cfg, m.uploads)
	users := parsed.Users
	return func() tea.Msg {
		return runDoneMsg{runner.Run(ctx, users)}
	}
}

// argv renders the form as the equivalent command line. Everything the form can
// express goes through the same parser the flags use.
func (m *Model) argv() []string {
	var args []string
	if m.mode == 1 {
		args = append(args, "-once")
	}
	target := strings.TrimSpace(m.inputs[0].Value())
	switch {
	case target == "":
	case isRoomID(target):
		args = append(args, "-room_id", target)
	case strings.Contains(target, "tiktok.com"):
		args = append(args, "-url", target)
	default:
		args = append(args, "-user", target)
	}
	for i, flag := range []string{"-duration", "-output", "-proxy"} {
		if v := strings.TrimSpace(m.inputs[i+1].Value()); v != "" {
			args = append(args, flag, v)
		}
	}
	if m.upload {
		args = append(args, "-telegram")
	}
	return args
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

// targetLabel describes what is being recorded, for the running/done headers.
func (m *Model) targetLabel() string {
	if t := strings.TrimSpace(m.inputs[0].Value()); t != "" {
		return t
	}
	return "unset"
}
