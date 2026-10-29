// Package tui is the interactive front end: one terminal screen with a target
// table, an activity pane and an input line. It drives the same cli.Parse and
// app.Runner as the flags path, so validation and behaviour cannot drift.
package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
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
	idle   bool          // saved, not part of any run
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
	list        []string // saved targets, in the order they were added
	userPath    string   // where list persists; "" disables saving
	restarting  []string // targets to start again once their run is gone
	compIdx     int      // cycling index into the live completion set
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
	m.loadSaved()
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
		var cmd tea.Cmd
		if msg.st != nil {
			cmd = m.applyStatus(*msg.st)
		}
		return m, tea.Batch(m.waitEvent(), cmd)

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
		m.finishing = false
		pending := m.restarting
		m.restarting = nil
		if len(pending) == 0 {
			return m, nil
		}
		if err := m.launch(pending); err != nil {
			m.warn(err.Error())
			return m, nil
		}
		return m, m.runCmd()

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
	case "tab":
		m.complete()
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.compIdx = 0
	return m, cmd
}

func (m *Model) submit() tea.Cmd {
	line := strings.TrimSpace(m.input.Value())
	if line == "" {
		return nil
	}
	if !strings.HasPrefix(line, "/cookies ") {
		m.history = append(m.history, line)
		m.histPos = len(m.history)
	}
	m.input.SetValue("")
	if strings.HasPrefix(line, "/") {
		return m.command(line)
	}
	return m.startUsers(splitTargets(line))
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
	m.restarting = nil
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
	m.seed(cfg, parsed.Users, now)
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

// startUsers starts a run when idle and adds to the live one otherwise. Both
// paths remember the names first, so what the screen shows is what it saves.
func (m *Model) startUsers(names []string) tea.Cmd {
	if len(names) == 0 {
		return nil
	}
	m.remember(names)
	if m.ctrl != nil {
		m.addToRun(names)
		return nil
	}
	if err := m.launch(names); err != nil {
		m.warn(err.Error())
		return nil
	}
	return m.runCmd()
}

func (m *Model) addToRun(names []string) {
	for _, name := range names {
		if err := m.ctrl.AddUser(name); err != nil && !errors.Is(err, app.ErrUserRunning) {
			m.warn(err.Error())
		}
	}
}

// loadSaved reads the target list and lists it, so the screen opens on what the
// last session saved.
func (m *Model) loadSaved() {
	m.userPath = targetFile()
	m.list = loadTargets(m.userPath)
	m.syncRows()
}

// syncRows gives every saved name a row. Rows already on screen, running or
// terminal, are left alone.
func (m *Model) syncRows() {
	now := m.now
	if now.IsZero() {
		now = time.Now()
	}
	for _, name := range m.list {
		if _, ok := m.byName[name]; ok {
			continue
		}
		m.targets = append(m.targets, target{user: name, label: statusLabel(name), idle: true, since: now})
		m.byName[name] = len(m.targets) - 1
	}
}

// remember appends unseen usernames to the saved list and writes it back. A url
// or a room id cannot be saved: they only ever run once.
func (m *Model) remember(names []string) {
	added := false
	for _, name := range names {
		if isRoomID(name) || strings.Contains(name, "tiktok.com") {
			m.warn("only usernames are saved · " + name + " runs once")
			continue
		}
		if !isUsername(name) {
			m.warn("not a username · " + name)
			continue
		}
		if slices.Contains(m.list, name) {
			continue
		}
		m.list = append(m.list, name)
		added = true
	}
	if !added {
		return
	}
	if m.userPath != "" {
		if err := saveTargets(m.userPath, m.list); err != nil {
			m.warn("cannot save the target list: " + err.Error())
		}
	}
	m.syncRows()
}

// restart stops one target and starts it again once its recording is gone. The
// terminal status only arrives after the remux, so the two never overlap.
func (m *Model) restart(raw string) tea.Cmd {
	user := strings.TrimPrefix(strings.TrimSpace(raw), "@")
	if user == "" {
		m.warn("restart needs a username")
		return nil
	}
	m.remember([]string{user})
	if m.ctrl == nil {
		return m.startUsers([]string{user})
	}
	if !m.ctrl.DropUser(user) {
		m.addToRun([]string{user})
		return nil
	}
	m.restarting = append(m.restarting, user)
	m.appendLog(logLine{level: "START", text: "restarting @" + user})
	return nil
}

// remove takes a target off the saved list, and stops it when it is still
// recording: a name you removed should not keep writing files.
func (m *Model) remove(raw string) {
	user := strings.TrimPrefix(strings.TrimSpace(raw), "@")
	if user == "" {
		m.warn("remove needs a username")
		return
	}
	i := slices.Index(m.list, user)
	if i < 0 {
		m.warn("@" + user + " is not saved")
		return
	}
	m.list = slices.Delete(m.list, i, i+1)
	if err := saveTargets(m.userPath, m.list); err != nil {
		m.warn("cannot save the target list: " + err.Error())
	}
	m.forgetRestart(user)
	// A target the run still holds keeps its row until the terminal status, so
	// the stopped capture stays visible with its size.
	if !(m.ctrl != nil && m.ctrl.DropUser(user)) {
		m.dropRow(user)
	}
	m.appendLog(logLine{level: "INFO", text: "removed @" + user})
}

// dropRow takes one row out of the table and closes the gap it leaves.
func (m *Model) dropRow(user string) {
	i, ok := m.byName[user]
	if !ok {
		return
	}
	m.targets = slices.Delete(m.targets, i, i+1)
	m.byName = make(map[string]int, len(m.targets))
	for j, t := range m.targets {
		m.byName[t.user] = j
	}
}

// showConfig prints what the next run would use. The live run keeps the values
// it started with, so it says so rather than pretending they changed.
func (m *Model) showConfig() {
	mode := fmt.Sprintf("wait for live, poll %d min", m.settings.interval)
	if m.settings.once {
		mode = "record once"
	}
	duration := "no cap"
	if m.settings.duration > 0 {
		duration = fmt.Sprintf("%d min", m.settings.duration)
	}
	proxy := m.settings.proxy
	if proxy == "" {
		proxy = "none"
	}
	telegram := "off"
	if m.settings.telegram {
		telegram = "on"
	}
	rows := [][2]string{
		{"mode", mode},
		{"duration", duration},
		{"output", m.outputDir()},
		{"proxy", proxy},
		{"telegram", telegram},
		{"cookies", cli.CookieLine(m.cookies)},
		{"targets", fmt.Sprintf("%d saved · %s", len(m.list), m.userPath)},
	}
	for _, row := range rows {
		value := row[1]
		if value == "" {
			value = "not saved"
		}
		m.appendLog(logLine{level: "INFO", text: padRight(row[0], 10) + " " + value})
	}
	if m.ctrl != nil {
		m.appendLog(logLine{level: "INFO", text: "a run is in flight · changes apply to the next run"})
	}
}

// outputDir is the directory recordings land in, defaulting to the working
// directory the way the flags do.
func (m *Model) outputDir() string {
	if m.settings.output != "" {
		return m.settings.output
	}
	if wd, err := os.Getwd(); err == nil {
		return wd + " (default)"
	}
	return "the current directory"
}

// setCookies stores the TikTok login cookie and writes it back for the next
// launch. The value itself never reaches the activity pane.
func (m *Model) setCookies(raw string) {
	value := strings.TrimSpace(raw)
	if value == "" {
		if set := m.cookies["sessionid_ss"]; set != "" {
			m.appendLog(logLine{level: "INFO", text: "sessionid_ss " + maskSecret(set)})
			return
		}
		m.warn("no sessionid_ss set · /cookies <value>")
		return
	}
	if m.cookies == nil {
		m.cookies = map[string]string{}
	}
	m.cookies["sessionid_ss"] = value
	if err := boot.SaveCookies(boot.CookieFile, m.cookies); err != nil {
		m.warn("cannot write cookies.json: " + err.Error())
	} else {
		m.appendLog(logLine{level: "INFO", text: "sessionid_ss " + maskSecret(value) + " saved to cookies.json"})
	}
	m.nextRun("cookies")
}

// maskSecret keeps a credential out of the activity pane. The last four bytes
// are enough to tell two values apart.
func maskSecret(v string) string {
	if len(v) > 4 {
		return "••••" + v[len(v)-4:]
	}
	return "••••"
}

func (m *Model) warn(text string) {
	m.appendLog(logLine{level: "WARN", text: text})
}

// command is one input-line command: what the help prints, and what tab
// completes.
type command struct {
	name string
	args string // argument hint; "" when the command takes none
	help string
}

var commands = []command{
	{"add", "<name,...>", "save names to the list, and add them to a live run"},
	{"start", "<name|all>", "start one saved target, or every saved target"},
	{"restart", "<name>", "stop one target, let it remux, then start it again"},
	{"remove", "<name>", "take one target off the saved list"},
	{"drop", "<name>", "stop one target, keep it in the list"},
	{"config", "", "show the settings the next run will use"},
	{"cookies", "<sessionid_ss>", "set the TikTok login cookie, saved to cookies.json"},
	{"once", "", "record one session per target"},
	{"wait", "", "wait for the next stream (default)"},
	{"duration", "<min>", "cap each session in minutes, 0 = no cap"},
	{"interval", "<min>", "minutes between liveness polls, minimum 1"},
	{"output", "<dir>", "output directory"},
	{"proxy", "<url>", "proxy for lookups, alone clears it"},
	{"telegram", "on|off", "upload each recording when it is written"},
	{"stop", "", "stop the run and remux what was captured"},
	{"quit", "", "leave now, without finishing a remux"},
	{"help", "", "show this list"},
}

// candidates lists whole replacement lines for tab: the command names while the
// first word is typed, then the argument of the command that is spelled out.
func (m *Model) candidates(value string) []string {
	if !strings.HasPrefix(value, "/") {
		return nil
	}
	name, arg, hasArg := strings.Cut(value, " ")
	if !hasArg {
		var out []string
		typed := strings.TrimPrefix(value, "/")
		for _, c := range commands {
			if strings.HasPrefix(c.name, typed) {
				out = append(out, "/"+c.name)
			}
		}
		return out
	}
	if strings.ContainsAny(arg, " ,") {
		return nil
	}
	var out []string
	names := func() {
		for _, user := range m.usernames() {
			if strings.HasPrefix(user, arg) {
				out = append(out, name+" "+user)
			}
		}
	}
	switch name {
	case "/start":
		if strings.HasPrefix("all", arg) {
			out = append(out, "/start all")
		}
		names()
	case "/restart", "/remove", "/drop", "/add":
		names()
	case "/telegram":
		for _, mode := range []string{"on", "off"} {
			if strings.HasPrefix(mode, arg) {
				out = append(out, "/telegram "+mode)
			}
		}
	}
	return out
}

// usernames is the completion pool: the saved list in order, then whatever the
// run holds that the list does not.
func (m *Model) usernames() []string {
	out := append([]string(nil), m.list...)
	for _, t := range m.targets {
		if t.user == "" || slices.Contains(out, t.user) {
			continue
		}
		out = append(out, t.user)
	}
	return out
}

// complete fills the longest shared prefix, then cycles the candidates on each
// further tab. A bare command gets a trailing space so the next word is clean.
func (m *Model) complete() {
	cands := m.candidates(m.input.Value())
	if len(cands) == 0 {
		return
	}
	value := cands[0]
	for _, cand := range cands[1:] {
		value = commonPrefix(value, cand)
	}
	if len(value) > len(m.input.Value()) {
		m.compIdx = 0
	} else {
		value = cands[m.compIdx%len(cands)]
		m.compIdx++
	}
	if !strings.Contains(value, " ") {
		value += " "
	}
	m.input.SetValue(value)
	m.input.CursorEnd()
}

func commonPrefix(a, b string) string {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	return a[:i]
}

func (m *Model) command(line string) tea.Cmd {
	name, rest, _ := strings.Cut(strings.TrimPrefix(line, "/"), " ")
	rest = strings.TrimSpace(rest)
	switch name {
	case "add":
		names := splitTargets(rest)
		if len(names) == 0 {
			m.warn("add needs a username")
			break
		}
		m.remember(names)
		if m.ctrl != nil {
			m.addToRun(names)
		}
	case "start":
		if rest == "" {
			m.warn("start needs a username or all")
			break
		}
		if rest != "all" {
			return m.startUsers(splitTargets(rest))
		}
		names := append([]string(nil), m.list...)
		if len(names) == 0 {
			m.warn("no saved targets · /add <name>")
			break
		}
		return m.startUsers(names)
	case "restart":
		return m.restart(rest)
	case "remove":
		m.remove(rest)
	case "config":
		m.showConfig()
	case "cookies":
		m.setCookies(rest)
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
		for _, c := range commands {
			m.appendLog(logLine{level: "INFO", text: padRight("/"+c.name+" "+c.args, 18) + " " + c.help})
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
	m.forgetRestart(user)
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
// the screen never opens empty: the url or room id of this run, the saved names
// still idle, then the names this run holds.
func (m *Model) seed(cfg config.Config, users []string, now time.Time) {
	m.targets = nil
	m.byName = make(map[string]int, len(m.list)+len(users)+1)
	add := func(row target) {
		m.byName[row.user] = len(m.targets)
		m.targets = append(m.targets, row)
	}
	switch {
	case cfg.URL != "":
		add(target{label: cfg.URL, since: now})
	case cfg.RoomID != "":
		add(target{label: "room " + cfg.RoomID, since: now})
	}
	for _, name := range m.list {
		if _, ok := m.byName[name]; ok {
			continue
		}
		add(target{user: name, label: statusLabel(name), idle: true, since: now})
	}
	for _, user := range users {
		i, ok := m.byName[user]
		if !ok {
			add(target{user: user, label: statusLabel(user), phase: app.PhaseQueued, since: now})
			continue
		}
		t := &m.targets[i]
		t.idle = false
		t.phase = app.PhaseQueued
		t.bytes = 0
		t.active = 0
		t.since = now
	}
}

// applyStatus merges one status into the rows. An unknown user becomes a row,
// so a target added mid-run shows up without a separate list. A terminal status
// is also the moment a restarted target is handed back to the runner.
func (m *Model) applyStatus(s app.Status) tea.Cmd {
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
	t.idle = false
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
	m.resume(s.User, s.Phase)
	return nil
}

// forgetRestart drops a queued restart, so a later drop wins over an earlier
// restart.
func (m *Model) forgetRestart(user string) {
	if i := slices.Index(m.restarting, user); i >= 0 {
		m.restarting = slices.Delete(m.restarting, i, i+1)
	}
}

// resume hands a restarting target back to the live run. A run that is already
// gone keeps the name queued for runDoneMsg, which launches the next one.
func (m *Model) resume(user string, phase app.Phase) {
	if !phase.Terminal() || m.ctrl == nil {
		return
	}
	i := slices.Index(m.restarting, user)
	if i < 0 {
		return
	}
	switch err := m.ctrl.AddUser(user); {
	case err == nil:
		m.restarting = slices.Delete(m.restarting, i, i+1)
	case errors.Is(err, app.ErrNotRunning):
		return
	default:
		m.restarting = slices.Delete(m.restarting, i, i+1)
		m.warn(err.Error())
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

// isUsername accepts what TikTok allows in a handle: 2 to 24 letters, digits,
// underscores and periods. Room ids and urls are refused before it is asked.
func isUsername(s string) bool {
	if len(s) < 2 || len(s) > 24 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}
