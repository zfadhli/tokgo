package logx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSinkDivertsConsoleKeepsFile proves the TUI hook takes the console line
// while the debug file still gets it, and that the call does not deadlock.
func TestSinkDivertsConsoleKeepsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.log")
	Init(path)
	defer Close()
	defer SetSink(nil)

	lines := make(chan string, 4)
	SetSink(func(level, text string) { lines <- level + ":" + text })

	Infof("hello %d", 7)

	select {
	case got := <-lines:
		if got != "INFO:hello 7" {
			t.Fatalf("sink got %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("sink was never called")
	}

	Close()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("debug file: %v", err)
	}
	if !strings.Contains(string(body), "[INFO] hello 7") {
		t.Fatalf("debug file lost the line:\n%s", body)
	}
}
