// Package ui renders the coloured console lines the CLI prints. It is a leaf
// package: the standard library and lipgloss only.
package ui

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/lipgloss"
)

var (
	cyanCol = lipgloss.Color("#25f4ee")
	dimCol  = lipgloss.Color("#6b7280")
	bodyCol = lipgloss.Color("#e6e8eb")
	okCol   = lipgloss.Color("#3ddc84")
	warnCol = lipgloss.Color("#ffd166")
	badCol  = lipgloss.Color("#ff6b6b")

	labelStyle = lipgloss.NewStyle().Foreground(dimCol)
	valueStyle = lipgloss.NewStyle().Foreground(bodyCol)
	pathStyle  = lipgloss.NewStyle().Foreground(bodyCol).Underline(true)
	boldStyle  = lipgloss.NewStyle().Bold(true)
)

var quiet atomic.Bool

// SetQuiet forces the plain rendering. logx sets it while a full-screen UI owns
// the console.
func SetQuiet(q bool) { quiet.Store(q) }

// Enabled reports whether console output should carry colour and motion. It is
// computed per call so the environment and the quiet flag stay testable.
func Enabled() bool {
	if quiet.Load() || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" || os.Getenv("CLICOLOR") == "0" {
		return false
	}
	info, err := os.Stdout.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func paint(style lipgloss.Style, s string) string {
	if !Enabled() {
		return s
	}
	return style.Render(s)
}

// Tag renders a log level or lifecycle kind in a fixed field, so text starts at
// the same column for every kind with or without escapes.
func Tag(kind, text string) string {
	tag := "[" + kind + "]"
	if n := len(tag); n < 7 {
		tag += strings.Repeat(" ", 7-n)
	}
	var style lipgloss.Style
	switch kind {
	case "INFO":
		style = lipgloss.NewStyle().Foreground(cyanCol)
	case "START":
		style = lipgloss.NewStyle().Foreground(cyanCol).Bold(true)
	case "STOP":
		style = lipgloss.NewStyle().Foreground(warnCol).Bold(true)
	case "DONE":
		style = lipgloss.NewStyle().Foreground(okCol).Bold(true)
	case "WARN":
		style = lipgloss.NewStyle().Foreground(warnCol)
	case "ERROR":
		style = lipgloss.NewStyle().Foreground(badCol).Bold(true)
	default:
		style = labelStyle
	}
	return paint(style, tag) + " " + text
}

// Label renders a header field name padded to an 8-column field.
func Label(s string) string {
	if n := len(s); n < 8 {
		s += strings.Repeat(" ", 8-n)
	}
	return paint(labelStyle, s)
}

// Value renders body text.
func Value(s string) string { return paint(valueStyle, s) }

// Dim renders secondary text.
func Dim(s string) string { return paint(labelStyle, s) }

// Path renders a filesystem path.
func Path(s string) string { return paint(pathStyle, s) }

// Bold renders emphasised text.
func Bold(s string) string { return paint(boldStyle, s) }

// Duration formats an elapsed time as 41s, 4min 12s or 1h 02min.
func Duration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d / time.Second)
	h, m, s := total/3600, total/60%60, total%60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh %02dmin", h, m)
	case m > 0 && s == 0:
		return fmt.Sprintf("%dmin", m)
	case m > 0:
		return fmt.Sprintf("%dmin %ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

// Clock formats an elapsed time the way a stopwatch reads: 00:41, 13:00,
// 1:02:05.
func Clock(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d / time.Second)
	h, m, s := total/3600, total/60%60, total%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}

// Bytes formats a size with one decimal in 1024-based units, the way df -h
// and Finder report it, dropping a trailing .0: 869.8 MB, 846.9 GB.
func Bytes(n int64) string {
	units := [...]string{"B", "kB", "MB", "GB", "TB", "PB"}
	v := float64(n)
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	return strings.TrimSuffix(strconv.FormatFloat(v, 'f', 1, 64), ".0") + " " + units[i]
}
