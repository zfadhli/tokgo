package record

import (
	"io"
	"os"
)

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
