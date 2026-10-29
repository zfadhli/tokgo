package logx

import (
	"fmt"
	"os"
	"sync"
	"time"

	"tokgo/internal/ui"
)

const spinInterval = 100 * time.Millisecond

var frames = [...]string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// active is the spinner owning the last console line. It shares mu with write
// so a log line and a spinner redraw cannot interleave mid-line.
var active *Spinner

// Spinner animates one status line until Stop. detail, when set, is sampled on
// every frame to refresh the parenthesised suffix.
type Spinner struct {
	label  string
	detail func() string

	// frame is guarded by the package mutex; the rest by mu.
	frame int

	mu      sync.Mutex
	done    chan struct{}
	exited  chan struct{}
	stopped bool
}

// Spin starts an animated status line. Without a colour console it prints the
// plain one-shot form instead, except under a UI sink, which owns the screen.
func Spin(label string, detail func() string) *Spinner {
	s := &Spinner{label: label, detail: detail}

	mu.Lock()
	defer mu.Unlock()
	if sinkFn != nil {
		return s
	}
	if !ui.Enabled() {
		fmt.Fprintf(os.Stdout, "o %s\n", label)
		return s
	}
	s.done = make(chan struct{})
	s.exited = make(chan struct{})
	active = s
	s.drawLocked()
	go s.run()
	return s
}

func (s *Spinner) run() {
	defer close(s.exited)
	t := time.NewTicker(spinInterval)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
			mu.Lock()
			if active == s {
				s.frame++
				s.drawLocked()
			}
			mu.Unlock()
		}
	}
}

// drawLocked repaints the line in place. detail runs under the package mutex,
// so it must not log.
func (s *Spinner) drawLocked() {
	line := "\r\x1b[K" + frames[s.frame%len(frames)] + " " + s.label
	if s.detail != nil {
		if d := s.detail(); d != "" {
			line += " " + ui.Dim("("+d+")")
		}
	}
	fmt.Fprint(os.Stdout, line)
}

// Stop erases the line and waits for the ticker to exit. It is idempotent and
// safe on a nil Spinner; the caller prints the line that replaces it.
func (s *Spinner) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.done == nil || s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	close(s.done)
	s.mu.Unlock()

	<-s.exited

	mu.Lock()
	if active == s {
		active = nil
		fmt.Fprint(os.Stdout, "\r\x1b[K")
	}
	mu.Unlock()
}
