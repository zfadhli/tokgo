package tui

import (
	"context"
	"errors"
	"fmt"
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
	okCol   = lipgloss.AdaptiveColor{Light: "#0f7b3f", Dark: "#3ddc84"}
	badCol  = lipgloss.AdaptiveColor{Light: "#b3261e", Dark: "#ff6b6b"}
	warnCol = lipgloss.AdaptiveColor{Light: "#8a6100", Dark: "#ffd166"}

	titleStyle  = lipgloss.NewStyle().Foreground(accent).Bold(true)
	versionSty  = lipgloss.NewStyle().Foreground(dimCol)
	ruleStyle   = lipgloss.NewStyle().Foreground(dimCol)
	ghostStyle  = lipgloss.NewStyle().Foreground(dimCol)
	cyanStyle   = lipgloss.NewStyle().Foreground(cyan).Bold(true)
	lookupStyle = lipgloss.NewStyle().Foreground(cyan)
	recStyle    = lipgloss.NewStyle().Foreground(accent).Bold(true)
	warnStyle   = lipgloss.NewStyle().Foreground(warnCol)
	okStyle     = lipgloss.NewStyle().Foreground(okCol).Bold(true)
	badStyle    = lipgloss.NewStyle().Foreground(badCol).Bold(true)
	hintStyle   = lipgloss.NewStyle().Foreground(dimCol)
)

const (
	// phaseWidth is the phase column. The words are cue words, not Phase.String().
	phaseWidth = 6
	keysHint   = "enter submit · tab complete · /help · ctrl+c stop"
	emptyState = "no targets yet · type a username below"
)

func (m *Model) size() (int, int) {
	if m.width <= 0 || m.height <= 0 {
		return 80, 24
	}
	return m.width, m.height
}

// hintLine shows what tab would complete while a command is being typed, and
// the key reminder otherwise.
func (m *Model) hintLine() string {
	cands := m.candidates(m.input.Value())
	if len(cands) == 0 {
		return hintStyle.Render(keysHint)
	}
	return hintStyle.Render("tab  " + strings.Join(cands, " · "))
}

// View renders the header, the target table, the activity tail, the input and
// the hint as exactly h lines.
func (m *Model) View() string {
	w, h := m.size()
	m.input.Width = max(w-3, 1)
	rows, activity := m.budget(h)

	lines := make([]string, 0, h)
	lines = append(lines, cut(m.header(w), w), rule(w), ghostStyle.Render("targets"))
	lines = append(lines, m.targetLines(rows, w)...)
	lines = append(lines, ghostStyle.Render("activity"))
	lines = append(lines, m.logTail(activity, w)...)
	lines = append(lines,
		rule(w),
		cut(m.input.View(), w),
		cut(m.hintLine(), w),
	)
	for i := range lines {
		lines[i] = cut(lines[i], w)
	}

	// At least the bottom lines survive a terminal too short for the skeleton.
	if len(lines) > h {
		lines = lines[len(lines)-h:]
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// budget splits the height. The empty table keeps one row for its message;
// that row comes out of the activity pane, never out of the fixed skeleton.
func (m *Model) budget(h int) (rows, activity int) {
	rows = min(len(m.targets), max(1, (h-7)/3))
	rows = min(rows, 8)
	rows = min(rows, max(h-8, 0))
	rows = max(rows, 0)
	if len(m.targets) == 0 && h >= 8 {
		rows = max(rows, 1)
	}
	activity = max(h-7-rows, 0)
	return rows, activity
}

func (m *Model) header(w int) string {
	left := titleStyle.Render("tokgo") + versionSty.Render(" "+m.version+" · monitor")
	right := m.headerRight(w)
	gap := w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m *Model) headerRight(w int) string {
	if m.ctrl != nil {
		recording := 0
		for _, t := range m.targets {
			if t.phase == app.PhaseRecording {
				recording++
			}
		}
		dot := hintStyle.Render("●")
		if recording > 0 {
			dot = cyanStyle.Render("●")
		}
		lead := ""
		if m.finishing {
			lead = warnStyle.Render("stopping") + hintStyle.Render(" · ")
		}
		summary := fmt.Sprintf("%d recording · %d targets · %s", recording, len(m.targets), ui.Clock(m.clock()))
		if w < 64 {
			summary = fmt.Sprintf("%d/%d · %s", recording, len(m.targets), ui.Clock(m.clock()))
		}
		return lead + dot + " " + hintStyle.Render(summary)
	}
	if !m.ended.IsZero() {
		return m.outcome()
	}
	if len(m.list) > 0 {
		return hintStyle.Render(fmt.Sprintf("idle · %d saved", len(m.list)))
	}
	return hintStyle.Render("idle · " + ui.Clock(0))
}

// clock is the run clock, frozen once the run returned.
func (m *Model) clock() time.Duration {
	if m.started.IsZero() {
		return 0
	}
	end := m.now
	if !m.ended.IsZero() {
		end = m.ended
	}
	if end.Before(m.started) {
		return 0
	}
	return end.Sub(m.started)
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

func rule(w int) string {
	return ruleStyle.Render(strings.Repeat("─", max(w, 1)))
}

// targetLines sizes the name column from the longest label instead of from the
// terminal, so the numbers stay next to the names on a wide window.
func (m *Model) targetLines(n, w int) []string {
	if n <= 0 {
		return nil
	}
	withSize := w >= 60
	fixed := 17
	if withSize {
		fixed = 26
	}
	nameW := min(max(m.nameNeed(), 8), max(w-fixed, 8))

	out := make([]string, 0, n)
	switch {
	case len(m.targets) == 0:
		out = append(out, ghostStyle.Render(clip(emptyState, w)))
	case len(m.targets) > n:
		for i := 0; i < n-1; i++ {
			out = append(out, m.rowLine(m.targets[i], nameW, withSize))
		}
		out = append(out, ghostStyle.Render(fmt.Sprintf("+%d more", len(m.targets)-(n-1))))
	default:
		for i := 0; i < n; i++ {
			out = append(out, m.rowLine(m.targets[i], nameW, withSize))
		}
	}
	for len(out) < n {
		out = append(out, "")
	}
	return out
}

// nameNeed is the widest label on screen. The column is capped at 28 so one
// long url cannot push the phase and size columns off the table.
func (m *Model) nameNeed() int {
	need := 8
	for _, t := range m.targets {
		need = max(need, len([]rune(t.label)))
	}
	return min(need, 28)
}

// rowLine is the indent, label, phase, timer and size in fixed columns, so the
// table stays a table while names come and go.
func (m *Model) rowLine(t target, nameW int, withSize bool) string {
	word, style := m.phaseCell(t)
	line := "  " + padRight(clip(t.label, nameW), nameW) + " " +
		padRight(style.Render(word), phaseWidth) + " " +
		padLeft(m.timer(t), 7)
	if withSize {
		size := ""
		if t.bytes > 0 {
			size = ui.Bytes(t.bytes)
		}
		line += " " + padLeft(size, 8)
	}
	return line
}

// phaseCell gives the short cue word and its colour. A run being cancelled
// reads stop on every row that has not reached a terminal phase yet.
func (m *Model) phaseCell(t target) (string, lipgloss.Style) {
	if t.idle {
		return "idle", ghostStyle
	}
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
	case t.idle:
		return ""
	case t.phase == app.PhaseWaiting:
		left := m.runInterval - now.Sub(t.since)
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

// logTail renders the newest n lines at the bottom of the pane.
func (m *Model) logTail(n, width int) []string {
	if n < 1 {
		return nil
	}
	tail := m.logs
	if len(tail) > n {
		tail = tail[len(tail)-n:]
	}
	out := make([]string, 0, n)
	for len(out) < n-len(tail) {
		out = append(out, "")
	}
	for _, line := range tail {
		marker, style := markers(line.level)
		prefix := "  " + style.Render(marker) + " "
		out = append(out, cut(prefix+clip(line.text, max(width-lipgloss.Width(prefix), 1)), width))
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
