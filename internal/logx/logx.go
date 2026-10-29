// Package logx is the process logger: INFO to stdout, warnings and errors to
// stderr, and a rotating debug log file for post-mortems.
package logx

import (
	"fmt"
	"os"
	"sync"
	"time"
)

const (
	// DefaultPath is where the debug log lives (relative to the working dir).
	DefaultPath = "tiktok-recorder.log"
	maxBytes    = 5 << 20
	backups     = 3
)

var (
	mu     sync.Mutex
	file   *os.File
	size   int64
	path   = DefaultPath
	sinkFn func(level, text string)
)

// SetSink diverts the console copies of every log line to f, keeping the debug
// file untouched. It exists so a full-screen UI can own stdout. A nil f
// restores the normal console output.
func SetSink(f func(level, text string)) {
	mu.Lock()
	sinkFn = f
	mu.Unlock()
}

// Init opens the debug log, rotating it when it grows past 5 MiB and keeping
// three generations. A failure to open the file is reported and ignored so a
// read-only working directory cannot stop a recording.
func Init(p string) {
	mu.Lock()
	defer mu.Unlock()
	if p == "" {
		p = DefaultPath
	}
	path = p
	rotateLocked()
}

func rotateLocked() {
	if file != nil {
		file.Close()
		file = nil
	}
	if info, err := os.Stat(path); err == nil && info.Size() >= maxBytes {
		os.Remove(fmt.Sprintf("%s.%d", path, backups))
		for i := backups - 1; i >= 1; i-- {
			os.Rename(fmt.Sprintf("%s.%d", path, i), fmt.Sprintf("%s.%d", path, i+1))
		}
		os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[!] cannot open log file %s: %v\n", path, err)
		return
	}
	file = f
	if info, err := f.Stat(); err == nil {
		size = info.Size()
	}
}

// Close flushes and closes the debug log.
func Close() {
	mu.Lock()
	defer mu.Unlock()
	if file != nil {
		file.Sync()
		file.Close()
		file = nil
	}
}

func write(level, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	now := time.Now().Format("2006-01-02 15:04:05")

	mu.Lock()
	sink := sinkFn

	if sink == nil {
		switch level {
		case "INFO":
			fmt.Fprintf(os.Stdout, "[*] %s - %s", now, msg)
			if len(msg) == 0 || msg[len(msg)-1] != '\n' {
				fmt.Fprintln(os.Stdout)
			}
		case "WARN", "ERROR":
			fmt.Fprintf(os.Stderr, "[!] %s - %s", now, msg)
			if len(msg) == 0 || msg[len(msg)-1] != '\n' {
				fmt.Fprintln(os.Stderr)
			}
		}
	}

	if file == nil {
		mu.Unlock()
		if sink != nil {
			sink(level, trimNewline(msg))
		}
		return
	}
	line := fmt.Sprintf("%s [%s] %s\n", now, level, trimNewline(msg))
	if size+int64(len(line)) >= maxBytes {
		rotateLocked()
	}
	var n int
	if file != nil {
		n, _ = file.WriteString(line)
		size += int64(n)
	}
	mu.Unlock()

	if sink != nil {
		sink(level, trimNewline(msg))
	}
}

func trimNewline(s string) string {
	for len(s) > 0 && s[len(s)-1] == '\n' {
		s = s[:len(s)-1]
	}
	return s
}

// Infof logs progress to stdout and the debug file.
func Infof(format string, args ...any) { write("INFO", format, args...) }

// Warnf logs a recoverable problem to stderr and the debug file.
func Warnf(format string, args ...any) { write("WARN", format, args...) }

// Errorf logs a failure to stderr and the debug file.
func Errorf(format string, args ...any) { write("ERROR", format, args...) }

// Debugf logs to the debug file only.
func Debugf(format string, args ...any) { write("DEBUG", format, args...) }
