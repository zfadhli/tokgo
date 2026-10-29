package logx

import (
	"io"
	"os"
	"testing"

	"tokgo/internal/ui"
)

// captureStdout runs fn with os.Stdout on a pipe and returns everything the
// pipe received. A pipe is not a character device, so the spinner takes its
// plain path.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()

	fn()

	os.Stdout = old
	w.Close()
	body, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	return string(body)
}

func TestSpinPlainWhenStdoutIsAPipe(t *testing.T) {
	SetSink(nil)
	ui.SetQuiet(false)
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("CLICOLOR", "")

	got := captureStdout(t, func() {
		sp := Spin("working..", nil)
		sp.Stop()
		sp.Stop()
	})
	if want := "o working..\n"; got != want {
		t.Fatalf("Spinner wrote %q, want %q", got, want)
	}
}

func TestNilSpinnerStop(t *testing.T) {
	var sp *Spinner
	sp.Stop()
}

func TestSpinSilentUnderASink(t *testing.T) {
	SetSink(func(string, string) {})
	defer SetSink(nil)

	got := captureStdout(t, func() {
		sp := Spin("working..", nil)
		sp.Stop()
	})
	if got != "" {
		t.Fatalf("Spinner wrote %q under a sink, want nothing", got)
	}
}
