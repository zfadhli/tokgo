package record

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"
)

// A muxer that stops reading leaves the writer parked in a full pipe. Only the
// interrupt can release it, so this test would hang on the old body-only close.
func TestMuxerInterruptUnblocksAStalledWrite(t *testing.T) {
	out := filepath.Join(t.TempDir(), "TK_a_2025.01.01_00-00-00.000.ts")
	stub, _ := ffmpegStub(t, "sleep 1") // never reads stdin
	m, err := startMuxer(stub, out)
	if err != nil {
		t.Fatalf("startMuxer: %v", err)
	}
	payload := bytes.Repeat([]byte("live-chunk"), 26<<10) // 260 KiB, well past the pipe buffer

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := copyStream(ctx, m, io.NopCloser(bytes.NewReader(payload)), time.Time{}); err != nil {
			t.Errorf("a cancelled copy reported a failure: %v", err)
		}
	}()

	select {
	case <-done:
		t.Fatal("copyStream returned without a cancel, the pipe never stalled")
	case <-time.After(250 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not unblock the write into the stalled muxer")
	}

	m.Interrupt()
	m.Interrupt()
	if err := m.Finish(); err != nil {
		t.Fatalf("muxer exit after a deliberate stop: %v", err)
	}
}
