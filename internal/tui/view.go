package tui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"tokgo/internal/app"
	"tokgo/internal/ui"
)

var (
	accent  = lipgloss.AdaptiveColor{Light: "#d7083a", Dark: "#fe2c55"}
	cyan    = lipgloss.AdaptiveColor{Light: "#0b7f7f", Dark: "#25f4ee"}
	dimCol  = lipgloss.AdaptiveColor{Light: "#8a8f98", Dark: "#6b7280"}
	bodyCol = lipgloss.AdaptiveColor{Light: "#1f2328", Dark: "#e6e8eb"}
	okCol   = lipgloss.AdaptiveColor{Light: "#0f7b3f", Dark: "#3ddc84"}
	badCol  = lipgloss.AdaptiveColor{Light: "#b3261e", Dark: "#ff6b6b"}
	warnCol = lipgloss.AdaptiveColor{Light: "#8a6100", Dark: "#ffd166"}

	titleStyle  = lipgloss.NewStyle().Foreground(accent).Bold(true)
	versionSty  = lipgloss.NewStyle().Foreground(dimCol)
	ruleStyle   = lipgloss.NewStyle().Foreground(dimCol)
	labelStyle  = lipgloss.NewStyle().Foreground(dimCol).Width(10)
	labelHot    = lipgloss.NewStyle().Foreground(cyan).Bold(true).Width(10)
	valueStyle  = lipgloss.NewStyle().Foreground(bodyCol)
	ghostStyle  = lipgloss.NewStyle().Foreground(dimCol)
	cyanStyle   = lipgloss.NewStyle().Foreground(cyan).Bold(true)
	lookupStyle = lipgloss.NewStyle().Foreground(cyan)
	recStyle    = lipgloss.NewStyle().Foreground(accent).Bold(true)
	warnStyle   = lipgloss.NewStyle().Foreground(warnCol)
	okStyle     = lipgloss.NewStyle().Foreground(okCol).Bold(true)
	badStyle    = lipgloss.NewStyle().Foreground(badCol).Bold(true)
	chipOn      = lipgloss.NewStyle().Foreground(lipgloss.Color("#111111")).Background(cyan).Bold(true).Padding(0, 1)
	chipOff     = lipgloss.NewStyle().Foreground(dimCol).Padding(0, 1)
	startStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#111111")).Background(accent).Bold(true).Padding(0, 1)
	startHotSty = lipgloss.NewStyle().Foreground(lipgloss.Color("#111111")).Background(accent).Bold(true).Padding(0, 1).Underline(true)
	hintStyle   = lipgloss.NewStyle().Foreground(dimCol)
	errStyle    = lipgloss.NewStyle().Foreground(badCol)
)

// setupKeys is the one hint line the setup screen shows twice: body and footer.
const setupKeys = "tab next · ←/→ mode · enter start · esc quit"

// phaseWidth is the phase column. The words are cue words, not Phase.String().
const phaseWidth = 6

// View renders the active screen.
func (m *Model) View() string {
	switch m.screen {
	case screenMonitor, screenDone:
		return m.monitorView()
	default:
		return m.setupView()
	}
}

func (m *Model) size() (int, int) {
	if m.width <= 0 || m.height <= 0 {
		return 80, 24
	}
	return m.width, m.height
}

// clock is the run clock, frozen once the run returned.
func (m *Model) clock() time.Duration {
	end := m.now
	if !m.ended.IsZero() {
		end = m.ended
	}
	if end.Before(m.started) {
		return 0
	}
	return end.Sub(m.started)
}

func (m *Model) header(right string, w int) string {
	left := titleStyle.Render("tokgo") + versionSty.Render(" "+m.version+" · monitor")
	gap := w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

func rule(w int) string {
	return ruleStyle.Render(strings.Repeat("─", max(w, 1)))
}

// frame wraps a body into exactly h lines: header, rule, h-3 body lines, footer.
func (m *Model) frame(head, foot string, body []string) string {
	w, h := m.size()
	lines := make([]string, 0, max(h, 1))
	lines = append(lines, cut(head, w), rule(w))
	for i := 0; i < h-3 && i < len(body); i++ {
		lines = append(lines, cut(body[i], w))
	}
	for len(lines) < h-1 {
		lines = append(lines, "")
	}
	lines = append(lines, cut(foot, w))
	if len(lines) > h {
		lines = lines[:h]
	}
	return strings.Join(lines, "\n")
}

func (m *Model) setupView() string {
	w, _ := m.size()
	content := max(w-4, 8)

	body := []string{
		"",
		m.modeRow(),
		m.fieldLine(0),
		strings.Repeat(" ", 11) + m.preview(content-11),
		m.fieldLine(1),
		m.fieldLine(2),
		m.fieldLine(3),
		m.fieldLine(4),
		"",
		m.toggleLine(),
		"",
		m.startLine(),
	}
	if m.err != nil {
		body = append(body, "", errStyle.Render(m.err.Error()))
	}
	body = append(body, "", hintStyle.Render(setupKeys))
	for i, line := range body {
		body[i] = "  " + cut(line, content)
	}
	return m.frame(m.header(valueStyle.Render("setup"), w), hintStyle.Render(setupKeys), body)
}

func (m *Model) modeRow() string {
	label := labelStyle.Render("Mode")
	if m.focus == fieldMode {
		label = labelHot.Render("Mode")
	}
	chips := make([]string, len(modeNames))
	for i, name := range modeNames {
		if i == m.mode {
			chips[i] = chipOn.Render(name)
			continue
		}
		chips[i] = chipOff.Render(name)
	}
	row := strings.Join(chips, " ")
	if m.focus == fieldMode {
		row = hintStyle.Render("‹ ") + row + hintStyle.Render(" ›")
	}
	return label + " " + row
}

func (m *Model) fieldLine(i int) string {
	labels := [5]string{"Targets", "Duration", "Interval", "Output", "Proxy"}
	focus := fieldTargets + i
	label := labelStyle.Render(labels[i])
	if m.focus == focus {
		label = labelHot.Render(labels[i])
	}
	value := m.inputs[i].View()
	if m.focus != focus {
		raw := m.inputs[i].Value()
		if raw == "" {
			raw = m.inputs[i].Placeholder
		}
		value = ghostStyle.Render(raw)
	}
	return label + " " + value
}

// preview names the targets the field will run, so a typo shows before start.
func (m *Model) preview(width int) string {
	raw := strings.TrimSpace(m.inputs[0].Value())
	targets := splitTargets(raw)
	var line string
	switch {
	case len(targets) == 0:
		line = "no targets yet"
	case len(targets) > 1:
		labels := make([]string, len(targets))
		for i, name := range targets {
			labels[i] = "@" + name
		}
		line = fmt.Sprintf("%d targets: %s", len(targets), strings.Join(labels, " "))
	case isRoomID(targets[0]):
		line = "1 target: room " + targets[0]
	case strings.Contains(targets[0], "tiktok.com"):
		line = "1 target: " + targets[0]
	default:
		line = "1 target: @" + targets[0]
	}
	return ghostStyle.Render(clip(line, max(width, 1)))
}

func (m *Model) toggleLine() string {
	toggle := "[ ]"
	style := labelStyle
	if m.upload {
		toggle = "[x]"
	}
	if m.focus == fieldTelegram {
		return labelHot.Render("Telegram") + " " + cyanStyle.Render(toggle+" upload after each recording")
	}
	return style.Render("Telegram") + " " + valueStyle.Render(toggle+" upload after each recording")
}

func (m *Model) startLine() string {
	label := labelStyle.Render("")
	if m.focus == fieldStart {
		return label + " " + startHotSty.Render("▸ Start")
	}
	return label + " " + startStyle.Render("▸ Start")
}

func (m *Model) monitorView() string {
	w, h := m.size()
	return m.frame(m.monitorHeader(w), m.monitorFooter(), m.monitorBody(w, h-3))
}

func (m *Model) monitorHeader(w int) string {
	if m.screen == screenDone {
		return m.header(m.outcome(), w)
	}
	recording := 0
	for _, t := range m.targets {
		if t.phase == app.PhaseRecording {
			recording++
		}
	}
	dot := ""
	if recording > 0 {
		dot = cyanStyle.Render("●") + " "
	}
	state := ""
	if m.finishing {
		state = warnStyle.Render("stopping") + hintStyle.Render(" · ")
	}
	summary := fmt.Sprintf("%d recording · %d targets · %s", recording, len(m.targets), ui.Clock(m.clock()))
	return m.header(state+dot+hintStyle.Render(summary), w)
}

func (m *Model) outcome() string {
	clock := hintStyle.Render(" · " + ui.Clock(m.clock()))
	switch {
	case errors.Is(m.result, context.Canceled), m.finishing:
		return warnStyle.Render("stopped") + clock
	case m.result != nil:
		return badStyle.Render("failed") + clock
	default:
		return okStyle.Render("finished") + clock
	}
}

func (m *Model) monitorFooter() string {
	if m.prompting {
		return m.prompt.View()
	}
	if m.notice != "" && m.now.Sub(m.noticeAt) < 3*time.Second {
		return errStyle.Render(m.notice)
	}
	if m.screen == screenDone {
		return hintStyle.Render("r new run · q quit")
	}
	if m.finishing {
		return hintStyle.Render("stopping · ctrl+c again to quit")
	}
	return hintStyle.Render("↑↓ select · a add · x drop · ctrl+c stop · q quit")
}

// monitorBody splits the width between the target list and the activity pane.
// Under 72 columns the log pane is dropped rather than squeezed.
func (m *Model) monitorBody(w, body int) []string {
	body = max(body, 0)
	if w < 72 {
		return m.leftPane(w, body)
	}
	leftW := min(max(w*37/100, 36), 56)
	rightW := w - leftW - 1
	left := m.leftPane(leftW, body)
	right := m.rightPane(rightW, body)
	out := make([]string, body)
	for i := range out {
		var l, r string
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		out[i] = padRight(cut(l, leftW), leftW) + " " + cut(r, rightW)
	}
	return out
}

// leftPane is the target table plus the selected row's detail. The detail block
// shrinks before the table does, and the table window follows the selection.
func (m *Model) leftPane(w, body int) []string {
	if body < 1 {
		return nil
	}
	lines := []string{ghostStyle.Render("targets")}
	detail := min(3, max(body-4, 0))
	rows := body - 1 - detail
	if rows < 1 {
		rows, detail = 0, 0
	}
	start := 0
	if m.sel >= rows && rows > 0 {
		start = m.sel - rows + 1
	}
	start = min(max(start, 0), max(len(m.targets)-rows, 0))

	nameW := max(w-(phaseWidth+20), 1)
	for i := start; i < len(m.targets) && len(lines) < 1+rows; i++ {
		lines = append(lines, m.rowLine(m.targets[i], i == m.sel, nameW))
	}
	lines = append(lines, m.detailLines(detail, w)...)
	for len(lines) < body {
		lines = append(lines, "")
	}
	return lines[:body]
}

// rowLine is marker, label, phase, timer, size in fixed columns, so the table
// stays a table while names come and go.
func (m *Model) rowLine(t target, selected bool, nameW int) string {
	marker := " "
	if selected {
		marker = cyanStyle.Render("▸")
	}
	word, style := m.phaseCell(t)
	size := ""
	if t.bytes > 0 {
		size = ui.Bytes(t.bytes)
	}
	return marker + " " +
		padRight(clip(t.label, nameW), nameW) + " " +
		padRight(style.Render(word), phaseWidth) + " " +
		padLeft(m.timer(t), 7) + " " +
		padLeft(size, 8)
}

// phaseCell gives the short cue word and its colour. A run being cancelled
// reads stop on every row that has not reached a terminal phase yet.
func (m *Model) phaseCell(t target) (string, lipgloss.Style) {
	if m.finishing && t.phase.Active() {
		return "stop", warnStyle
	}
	switch t.phase {
	case app.PhaseResolving:
		return "lookup", lookupStyle
	case app.PhaseWaiting:
		return "wait", ghostStyle
	case app.PhaseLive:
		return "live", cyanStyle
	case app.PhaseRecording:
		return "REC", recStyle
	case app.PhaseConverting:
		return "remux", warnStyle
	case app.PhaseDone:
		return "done", okStyle
	case app.PhaseStopped:
		return "stop", warnStyle
	case app.PhaseFailed:
		return "fail", badStyle
	default:
		return "queue", ghostStyle
	}
}

// timer counts the current phase, the poll countdown while waiting, and the
// total active time once the target is terminal. The terminal row keeps the
// total rather than the last phase, so a recording that ended in a one second
// remux still reads as its real length.
func (m *Model) timer(t target) string {
	now := m.now
	if now.IsZero() {
		now = time.Now()
	}
	switch {
	case t.phase == app.PhaseWaiting:
		left := m.interval - now.Sub(t.since)
		if left < 0 {
			left = 0
		}
		return "-" + ui.Clock(left)
	case t.phase == app.PhaseDone, t.phase == app.PhaseStopped, t.phase == app.PhaseFailed:
		return ui.Clock(t.active)
	default:
		return ui.Clock(now.Sub(t.since))
	}
}

func (m *Model) detailLines(n, w int) []string {
	t := m.selected()
	if t == nil || n <= 0 {
		return nil
	}
	first := t.label
	if t.room != "" {
		first += " · room " + t.room
	}
	out := []string{valueStyle.Render(clip(first, w))}
	if n > 1 && t.file != "" {
		out = append(out, ghostStyle.Render(clip(filepath.Base(t.file), w)))
	}
	if n > 2 && t.detail != "" && t.detail != t.room {
		out = append(out, hintStyle.Render(clip(t.detail, w)))
	}
	return out
}

func (m *Model) rightPane(w, body int) []string {
	if body < 1 {
		return nil
	}
	lines := []string{ghostStyle.Render("activity")}
	return append(lines, m.logTail(body-1, w)...)
}

// logTail renders the last n log lines clipped to the pane width.
func (m *Model) logTail(n, width int) []string {
	if n < 1 {
		n = 1
	}
	tail := m.logs
	if len(tail) > n {
		tail = tail[len(tail)-n:]
	}
	out := make([]string, 0, n)
	for _, line := range tail {
		marker, style := markers(line.level)
		text := clip(line.text, max(width-4, 1))
		out = append(out, "  "+style.Render(marker)+" "+text)
	}
	for len(out) < n {
		out = append(out, "")
	}
	return out[:n]
}

func markers(level string) (string, lipgloss.Style) {
	switch level {
	case "START":
		return "▶", lookupStyle
	case "STOP":
		return "■", warnStyle
	case "DONE":
		return "✓", lipgloss.NewStyle().Foreground(okCol)
	case "WARN":
		return "!", warnStyle
	case "ERROR":
		return "!", badStyle
	case "DEBUG":
		return "·", ghostStyle
	default:
		return "•", lookupStyle
	}
}

// cut trims a rendered line to the terminal width. It is the last resort before
// the line reaches the screen.
func cut(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	return lipgloss.NewStyle().MaxWidth(w).Render(s)
}

// clip shortens plain text with an ellipsis, for the fixed table columns.
func clip(s string, n int) string {
	r := []rune(s)
	if n <= 0 {
		return ""
	}
	if len(r) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}

func padRight(s string, n int) string {
	if d := n - lipgloss.Width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

func padLeft(s string, n int) string {
	if d := n - lipgloss.Width(s); d > 0 {
		return strings.Repeat(" ", d) + s
	}
	return s
}
