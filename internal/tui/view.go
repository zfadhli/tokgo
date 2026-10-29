package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
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
	chipOn      = lipgloss.NewStyle().Foreground(lipgloss.Color("#111111")).Background(cyan).Bold(true).Padding(0, 1)
	chipOff     = lipgloss.NewStyle().Foreground(dimCol).Padding(0, 1)
	startStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#111111")).Background(accent).Bold(true).Padding(0, 1)
	startHotSty = lipgloss.NewStyle().Foreground(lipgloss.Color("#111111")).Background(accent).Bold(true).Padding(0, 1).Underline(true)
	hintStyle   = lipgloss.NewStyle().Foreground(dimCol)
	errStyle    = lipgloss.NewStyle().Foreground(badCol)
	okStyle     = lipgloss.NewStyle().Foreground(okCol).Bold(true)
	badStyle    = lipgloss.NewStyle().Foreground(badCol).Bold(true)
)

// View renders the active screen.
func (m *Model) View() string {
	switch m.screen {
	case screenRunning:
		return m.runningView()
	case screenDone:
		return m.doneView()
	default:
		return m.formView()
	}
}

func (m *Model) size() (int, int) {
	if m.width <= 0 || m.height <= 0 {
		return 80, 24
	}
	return m.width, m.height
}

func (m *Model) header(right string, w int) string {
	left := titleStyle.Render("tokgo") + versionSty.Render(" "+m.version+" · tui")
	gap := w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

func rule(w int) string {
	return ruleStyle.Render(strings.Repeat("─", max(w, 1)))
}

func (m *Model) formView() string {
	w, _ := m.size()
	cw := max(w-4, 20)
	lines := []string{
		m.header(valueStyle.Render("setup"), cw),
		rule(cw),
		"",
		m.modeRow(),
		m.fieldLine(0),
		m.fieldLine(1),
		m.fieldLine(2),
		m.fieldLine(3),
		"",
	}

	toggle := "[ ]"
	if m.upload {
		toggle = "[x]"
	}
	toggleLine := toggle + " upload to Telegram after each recording"
	if m.focus == fieldTelegram {
		lines = append(lines, labelHot.Render("Telegram")+cyanStyle.Render(toggleLine))
	} else {
		lines = append(lines, labelStyle.Render("Telegram")+valueStyle.Render(toggleLine))
	}

	lines = append(lines, "")
	if m.focus == fieldStart {
		lines = append(lines, labelStyle.Render("")+startHotSty.Render("▸ Start"))
	} else {
		lines = append(lines, labelStyle.Render("")+startStyle.Render("▸ Start"))
	}

	if m.err != nil {
		lines = append(lines, "", errStyle.Render(m.err.Error()))
	}

	lines = append(lines, "", hintStyle.Render(
		"tab next · shift+tab back · ←/→ mode · space toggle · enter start · esc quit"))

	return lipgloss.NewStyle().Padding(0, 2).Render(strings.Join(lines, "\n"))
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
	labels := [4]string{"Target", "Duration", "Output", "Proxy"}
	focus := fieldTarget + i
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

func (m *Model) runningView() string {
	w, h := m.size()
	state := "recording"
	if m.finishing {
		state = "finishing, keep the terminal open"
	}
	head := m.header(cyanStyle.Render(state), w)
	meta := fmt.Sprintf("  %s · %s · %s%s",
		valueStyle.Render(m.targetLabel()),
		hintStyle.Render(modeNames[m.mode]),
		hintStyle.Render("elapsed "),
		valueStyle.Render(elapsed(m.now.Sub(m.started))))

	sub := meta
	if m.capSecs > 0 {
		sub += hintStyle.Render(" / cap " + elapsed(time.Duration(m.capSecs)*time.Second))
	}

	lines := []string{head, rule(w), sub, ""}
	lines = append(lines, m.logTail(h-8)...)
	lines = append(lines, "", rule(w), hintStyle.Render("  ctrl+c finish and remux · press again to quit now"))
	return strings.Join(lines, "\n")
}

func (m *Model) doneView() string {
	w, _ := m.size()
	lines := []string{m.header("", w), rule(w), ""}

	switch {
	case m.result == nil:
		lines = append(lines, okStyle.Render("  ✓ finished ")+hintStyle.Render(elapsed(m.now.Sub(m.started))))
	case errors.Is(m.result, context.Canceled):
		lines = append(lines, warnSty("  ■ stopped ")+hintStyle.Render(elapsed(m.now.Sub(m.started))))
	case m.finishing:
		lines = append(lines, warnSty("  ■ stopped ")+hintStyle.Render(elapsed(m.now.Sub(m.started))))
	default:
		lines = append(lines, badStyle.Render("  ✗ "+m.result.Error()))
	}

	lines = append(lines, "")
	lines = append(lines, m.logTail(10)...)
	lines = append(lines, "", rule(w), hintStyle.Render("  r new run · q quit"))
	return strings.Join(lines, "\n")
}

func warnSty(s string) string {
	return lipgloss.NewStyle().Foreground(warnCol).Bold(true).Render(s)
}

// logTail renders the last n log lines clipped to the terminal width.
func (m *Model) logTail(n int) []string {
	if n < 1 {
		n = 1
	}
	w, _ := m.size()
	width := max(w-6, 20)
	tail := m.logs
	if len(tail) > n {
		tail = tail[len(tail)-n:]
	}
	out := make([]string, 0, len(tail))
	for _, line := range tail {
		marker, style := markers(line.level)
		text := lipgloss.NewStyle().MaxWidth(width).Render(line.text)
		out = append(out, "  "+style.Render(marker)+" "+text)
	}
	for len(out) < n {
		out = append(out, "")
	}
	return out
}

func markers(level string) (string, lipgloss.Style) {
	switch level {
	case "START":
		return "▶", lipgloss.NewStyle().Foreground(cyan)
	case "STOP":
		return "■", lipgloss.NewStyle().Foreground(warnCol)
	case "DONE":
		return "✓", lipgloss.NewStyle().Foreground(okCol)
	case "WARN":
		return "!", lipgloss.NewStyle().Foreground(warnCol)
	case "ERROR":
		return "!", lipgloss.NewStyle().Foreground(badCol).Bold(true)
	case "DEBUG":
		return "·", ghostStyle
	default:
		return "•", lipgloss.NewStyle().Foreground(cyan)
	}
}

func elapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d.Seconds())
	h, rem := total/3600, total%3600
	minutes, seconds := rem/60, rem%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, minutes, seconds)
	}
	return fmt.Sprintf("%02d:%02d", minutes, seconds)
}
