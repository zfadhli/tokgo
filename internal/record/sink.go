package record

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// muxerWait bounds how long a stopped muxer may take to exit. Past it the
// process is killed, so a wedged ffmpeg cannot hang a recording.
const muxerWait = 10 * time.Second

// captureSink is where one connection's bytes land: the live muxer's stdin when
// it is running, the capture file when it is not. Interrupt releases a pending
// Write and must be safe to call more than once.
type captureSink interface {
	io.Writer
	Interrupt()
	Finish() error
}

// writeError reports a failed write into the sink. Whatever already reached the
// disk is a valid partial recording, so the caller remuxes it rather than
// treating the session as lost.
type writeError struct{ err error }

func (e *writeError) Error() string { return "capture write failed: " + e.err.Error() }

func (e *writeError) Unwrap() error { return e.err }

// rawFile is the direct-to-disk sink, used when no muxer is running.
type rawFile struct{ f *os.File }

func (r *rawFile) Write(p []byte) (int, error) { return r.f.Write(p) }

// Interrupt does nothing: a disk write returns on its own, and the file has to
// stay open for the reconnect loop.
func (r *rawFile) Interrupt() {}

func (r *rawFile) Finish() error { return r.f.Close() }

// muxer feeds capture bytes into a live ffmpeg that writes an mpegts segment.
// -c copy means the capture never re-encodes, so a killed segment is still
// playable and a dead muxer costs only the bytes after its last flush.
type muxer struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stderr *bytes.Buffer
	once   sync.Once
}

// startMuxer runs the live mpegts muxer writing out. A failure here is a broken
// ffmpeg path, not a broken network: the caller falls back to raw capture.
func startMuxer(ffmpegPath, out string) (*muxer, error) {
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}
	cmd := exec.Command(ffmpegPath,
		"-y", "-nostdin", "-loglevel", "error",
		"-i", "pipe:0", "-c", "copy", "-f", "mpegts", out)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		stdin.Close()
		return nil, err
	}
	return &muxer{cmd: cmd, stdin: stdin, stderr: stderr}, nil
}

func (m *muxer) Write(p []byte) (int, error) { return m.stdin.Write(p) }

// Interrupt cuts ffmpeg's stdin: that releases a Write parked in a full pipe and
// tells the muxer to flush and exit. Idempotent.
func (m *muxer) Interrupt() { m.once.Do(func() { m.stdin.Close() }) }

// Finish closes stdin and waits for ffmpeg under its own bound, so a wedged
// muxer cannot hang the process. A non-zero exit is expected when stdin was cut
// mid-tag: ffmpeg reports the truncated packet, the segment itself is valid.
func (m *muxer) Finish() error {
	m.Interrupt()
	done := make(chan error, 1)
	go func() { done <- m.cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			return nil
		}
		return fmt.Errorf("muxer: %w: %s", err, tail(m.stderr.Bytes(), stderrTail))
	case <-time.After(muxerWait):
		m.cmd.Process.Kill()
		return fmt.Errorf("muxer did not exit within %s, killed", muxerWait)
	}
}
