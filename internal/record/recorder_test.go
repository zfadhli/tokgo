package record

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"tokgo/internal/logx"
	"tokgo/internal/tiktok"
)

// fakeAPI stands in for the network so recordings run off canned streams.
type fakeAPI struct {
	urls   []string
	urlErr error
	alive  func(call int) (bool, error)
	open   func(ctx context.Context, url string) (io.ReadCloser, error)

	mu     sync.Mutex
	calls  int
	opened []string
}

func (f *fakeAPI) RoomAlive(context.Context, string) (bool, error) {
	f.mu.Lock()
	f.calls++
	call := f.calls
	f.mu.Unlock()
	if f.alive == nil {
		return true, nil
	}
	return f.alive(call)
}

func (f *fakeAPI) LiveURLs(context.Context, string, string) ([]string, error) {
	return f.urls, f.urlErr
}

func (f *fakeAPI) OpenStream(ctx context.Context, url string) (io.ReadCloser, error) {
	f.mu.Lock()
	f.opened = append(f.opened, url)
	f.mu.Unlock()
	return f.open(ctx, url)
}

func (f *fakeAPI) openedURLs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.opened...)
}

// stalledBody hands out data once, then blocks until Close, so a recording only
// ends through a duration cap or a cancelled context.
type stalledBody struct {
	data      []byte
	sent      bool
	done      chan struct{}
	closeOnce sync.Once
	blocked   chan struct{}
}

func newStalledBody(data []byte) *stalledBody {
	return &stalledBody{data: data, done: make(chan struct{}), blocked: make(chan struct{}, 1)}
}

func (b *stalledBody) Read(p []byte) (int, error) {
	if !b.sent {
		b.sent = true
		return copy(p, b.data), nil
	}
	select {
	case b.blocked <- struct{}{}:
	default:
	}
	<-b.done
	return 0, io.EOF
}

func (b *stalledBody) Close() error {
	b.closeOnce.Do(func() { close(b.done) })
	return nil
}

func streamBytes() []byte { return bytes.Repeat([]byte("tokgo-live-chunk"), 512) }

// runsOnce answers the liveness check for a single connection, then reports the
// room dead so the capture stops without a deadline or a cancel.
func runsOnce(call int) (bool, error) { return call <= 1, nil }

func TestRecordCapturesUntilDurationCap(t *testing.T) {
	payload := streamBytes()
	api := &fakeAPI{
		urls: []string{"url-a"},
		open: func(context.Context, string) (io.ReadCloser, error) {
			return newStalledBody(payload), nil
		},
	}
	stub, _ := ffmpegStub(t, "")
	dir := t.TempDir()
	rec := New(api, Options{OutputDir: dir, Duration: 1, FFmpegPath: stub})

	start := time.Now()
	path, err := collect(t, rec, context.Background())
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond || elapsed > 5*time.Second {
		t.Fatalf("recording stopped after %v, want about 1s (duration cap)", elapsed)
	}
	if !strings.HasSuffix(path, ".mp4") {
		t.Fatalf("Record returned %q, want the converted path", path)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("file holds %d bytes, want %d", len(got), len(payload))
	}
	assertOnlyRecording(t, dir, path)
}

func TestRecordFallsThroughEmptyCandidate(t *testing.T) {
	payload := streamBytes()
	api := &fakeAPI{
		urls:  []string{"url-empty", "url-good"},
		alive: func(call int) (bool, error) { return call <= 2, nil },
		open: func(_ context.Context, url string) (io.ReadCloser, error) {
			if url == "url-empty" {
				return io.NopCloser(bytes.NewReader(nil)), nil
			}
			return io.NopCloser(bytes.NewReader(payload)), nil
		},
	}
	stub, _ := ffmpegStub(t, "")
	dir := t.TempDir()
	rec := New(api, Options{OutputDir: dir, FFmpegPath: stub})

	path, err := collect(t, rec, context.Background())
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if opened := api.openedURLs(); len(opened) != 2 || opened[0] != "url-empty" || opened[1] != "url-good" {
		t.Fatalf("opened %v, want both candidates in order", opened)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("file holds %d bytes, want the second stream's %d", len(got), len(payload))
	}
	assertOnlyRecording(t, dir, path)
}

// A candidate under minStreamBytes is thrown away whole. Its segment must leave
// the concat list too: a truncated segment makes the remux fail and costs the
// whole recording.
func TestRecordDiscardsSegmentsFromASubThresholdCandidate(t *testing.T) {
	short := bytes.Repeat([]byte("junk"), 25)
	payload := streamBytes()
	bodies := map[string]*bytes.Reader{
		"url-short": bytes.NewReader(short),
		"url-good":  bytes.NewReader(payload),
	}
	api := &fakeAPI{
		urls: []string{"url-short", "url-good"},
		open: func(_ context.Context, url string) (io.ReadCloser, error) {
			return io.NopCloser(bodies[url]), nil
		},
	}
	stub, _ := ffmpegStub(t, "")
	dir := t.TempDir()
	rec := New(api, Options{OutputDir: dir, FFmpegPath: stub})

	path, err := collect(t, rec, context.Background())
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if !strings.HasSuffix(path, ".mp4") {
		t.Fatalf("Record returned %q, want the converted path", path)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("file holds %d bytes, want only the accepted stream's %d", len(got), len(payload))
	}
	if junk, err := filepath.Glob(filepath.Join(dir, "TK_*.000.*")); err != nil || len(junk) != 0 {
		t.Fatalf("left %v behind, %v, want the discarded segment removed", junk, err)
	}
	assertOnlyRecording(t, dir, path)
}

func TestRecordAllCandidatesEmpty(t *testing.T) {
	dir := t.TempDir()
	api := &fakeAPI{
		urls: []string{"url-a", "url-b"},
		open: func(context.Context, string) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(nil)), nil
		},
	}
	stub, _ := ffmpegStub(t, "")
	rec := New(api, Options{OutputDir: dir, FFmpegPath: stub})

	path, err := collect(t, rec, context.Background())
	if !errors.Is(err, tiktok.ErrLiveURL) {
		t.Fatalf("Record error = %v, want tiktok.ErrLiveURL", err)
	}
	if path != "" {
		t.Fatalf("Record returned path %q for a failed capture", path)
	}
	assertOnlyRecording(t, dir, "")
}

func TestRecordCancellationKeepsCapture(t *testing.T) {
	payload := streamBytes()
	body := newStalledBody(payload)
	api := &fakeAPI{
		urls: []string{"url-a"},
		open: func(context.Context, string) (io.ReadCloser, error) { return body, nil },
	}
	stub, _ := ffmpegStub(t, "")
	dir := t.TempDir()
	rec := New(api, Options{OutputDir: dir, FFmpegPath: stub})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-body.blocked
		cancel()
	}()

	path, err := collect(t, rec, ctx)
	if err != nil {
		t.Fatalf("cancellation must not fail the recording: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("file holds %d bytes, want the buffered %d", len(got), len(payload))
	}
	assertOnlyRecording(t, dir, path)
}

// A reconnect repeats the stream timestamps, so its bytes get their own segment
// and the remux joins the two. Appending into one file loses that video.
func TestRecordReconnectWritesOneSegmentPerConnection(t *testing.T) {
	payload := streamBytes()
	api := &fakeAPI{
		urls:  []string{"url-a"},
		alive: func(call int) (bool, error) { return call <= 2, nil },
		open: func(context.Context, string) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(payload)), nil
		},
	}
	stub, calls := ffmpegStub(t, "")
	dir := t.TempDir()
	rec := New(api, Options{OutputDir: dir, FFmpegPath: stub})

	path, err := collect(t, rec, context.Background())
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := append(append([]byte(nil), payload...), payload...); !bytes.Equal(got, want) {
		t.Fatalf("file holds %d bytes, want both connections' %d", len(got), len(want))
	}
	invocations := stubCalls(t, calls)
	if len(invocations) != 3 {
		t.Fatalf("ffmpeg calls = %q, want two muxers and one convert", invocations)
	}
	if !strings.Contains(invocations[0], "pipe:0") || !strings.Contains(invocations[0], ".000.ts") {
		t.Fatalf("first call = %q, want the first segment", invocations[0])
	}
	if !strings.Contains(invocations[1], "pipe:0") || !strings.Contains(invocations[1], ".001.ts") {
		t.Fatalf("second call = %q, want a second segment", invocations[1])
	}
	if !strings.Contains(invocations[2], "-f concat") {
		t.Fatalf("third call = %q, want the concat remux", invocations[2])
	}
	assertOnlyRecording(t, dir, path)
}

// A broken ffmpeg must not cost a single byte: the capture goes straight to a
// .flv segment and the run keeps it when the remux cannot run either.
func TestRecordFallsBackToRawCaptureWhenMuxerFails(t *testing.T) {
	payload := streamBytes()
	api := &fakeAPI{
		urls:  []string{"url-a"},
		alive: runsOnce,
		open: func(context.Context, string) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(payload)), nil
		},
	}
	logs := captureLogs(t)
	dir := t.TempDir()
	rec := New(api, Options{OutputDir: dir, FFmpegPath: filepath.Join(t.TempDir(), "no-such-ffmpeg")})

	path, err := collect(t, rec, context.Background())
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if !strings.HasSuffix(path, ".flv") {
		t.Fatalf("Record returned %q, want the raw .flv capture", path)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("file holds %d bytes, want every byte the CDN sent (%d)", len(got), len(payload))
	}
	assertOnlyRecording(t, dir, path)
	if warned := countLogs(logs(), "muxer unavailable"); warned != 1 {
		t.Fatalf("warned %d times about the muxer, want exactly one: %q", warned, logs())
	}
}

// ffmpeg exits non-zero when stdin is cut mid-tag. The segment it wrote is still
// good, so the recording is remuxed and kept.
func TestRecordKeepsSegmentFromMuxerThatExitsBadly(t *testing.T) {
	payload := streamBytes()
	api := &fakeAPI{
		urls:  []string{"url-a"},
		alive: runsOnce,
		open: func(context.Context, string) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(payload)), nil
		},
	}
	stub, calls := ffmpegStub(t, `if [ "$in" = "pipe:0" ]; then
	cat > /dev/null
	printf 'partial-ts' > "$last"
	exit 1
fi
cp "$in" "$last"`)
	dir := t.TempDir()
	rec := New(api, Options{OutputDir: dir, FFmpegPath: stub})

	path, err := collect(t, rec, context.Background())
	if err != nil {
		t.Fatalf("a non-zero muxer exit must not fail the recording: %v", err)
	}
	if !strings.HasSuffix(path, ".mp4") {
		t.Fatalf("Record returned %q, want the converted path", path)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "partial-ts" {
		t.Fatalf("file holds %q, want the partial segment's bytes", got)
	}
	if invocations := stubCalls(t, calls); len(invocations) != 2 {
		t.Fatalf("ffmpeg calls = %q, want one mux and exactly one convert", invocations)
	}
	assertOnlyRecording(t, dir, path)
}

// The same non-zero exit on a capture we stopped on purpose is expected noise:
// no failure, no error log, and the mp4 still comes out.
func TestRecordQuietAboutMuxerExitOnDeliberateStop(t *testing.T) {
	payload := streamBytes()
	body := newStalledBody(payload)
	api := &fakeAPI{
		urls: []string{"url-a"},
		open: func(context.Context, string) (io.ReadCloser, error) { return body, nil },
	}
	stub, _ := ffmpegStub(t, `if [ "$in" = "pipe:0" ]; then
	cat > /dev/null
	printf 'partial-ts' > "$last"
	exit 1
fi
cp "$in" "$last"`)
	dir := t.TempDir()
	logs := captureLogs(t)
	rec := New(api, Options{OutputDir: dir, FFmpegPath: stub})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-body.blocked
		cancel()
	}()

	path, err := collect(t, rec, ctx)
	if err != nil {
		t.Fatalf("cancellation must not fail the recording: %v", err)
	}
	if !strings.HasSuffix(path, ".mp4") {
		t.Fatalf("Record returned %q, want the converted path", path)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "partial-ts" {
		t.Fatalf("file = %q, %v, want the partial segment's bytes", got, err)
	}
	for _, line := range logs() {
		if strings.HasPrefix(line, "ERROR") {
			t.Fatalf("a deliberate stop logged an error: %q", line)
		}
		if strings.HasPrefix(line, "WARN") && strings.Contains(line, "muxer") {
			t.Fatalf("a deliberate stop warned about the muxer: %q", line)
		}
	}
}

// assertOnlyRecording fails when the output directory holds anything but the
// returned recording, so no abandoned segment or concat list can hide there.
func assertOnlyRecording(t *testing.T, dir, path string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := 0
	if path != "" {
		want = 1
	}
	if len(entries) != want {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("output directory holds %v, want %d file(s)", names, want)
	}
	if path != "" && entries[0].Name() != filepath.Base(path) {
		t.Fatalf("output directory holds %q, want %q", entries[0].Name(), filepath.Base(path))
	}
}

// captureLogs diverts the process logger into a slice for the test's duration.
func captureLogs(t *testing.T) func() []string {
	t.Helper()
	var (
		mu    sync.Mutex
		lines []string
	)
	logx.SetSink(func(level, text string) {
		mu.Lock()
		lines = append(lines, level+" "+text)
		mu.Unlock()
	})
	t.Cleanup(func() { logx.SetSink(nil) })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), lines...)
	}
}

func countLogs(lines []string, substr string) int {
	n := 0
	for _, line := range lines {
		if strings.Contains(line, substr) {
			n++
		}
	}
	return n
}

// collect runs Record with a watchdog so a broken stop condition fails instead
// of hanging the suite.
func collect(t *testing.T, rec *Recorder, ctx context.Context) (string, error) {
	t.Helper()
	type outcome struct {
		path string
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		path, err := rec.Record(ctx, "user", "room")
		done <- outcome{path, err}
	}()
	select {
	case got := <-done:
		return got.path, got.err
	case <-time.After(15 * time.Second):
		t.Fatal("Record did not return within 15s")
		return "", nil
	}
}

// sinkStub is a capture sink whose Write can be made to fail.
type sinkStub struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	err   error
	stops int
}

func (s *sinkStub) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return 0, s.err
	}
	return s.buf.Write(p)
}

func (s *sinkStub) Interrupt() {
	s.mu.Lock()
	s.stops++
	s.mu.Unlock()
}

func (s *sinkStub) Finish() error { return nil }

func (s *sinkStub) bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.buf.Bytes()...)
}

// errorBody hands out its payload once, then fails instead of reaching EOF.
type errorBody struct {
	data []byte
	err  error
}

func (b *errorBody) Read(p []byte) (int, error) {
	if b.data == nil {
		return 0, b.err
	}
	n := copy(p, b.data)
	b.data = nil
	return n, nil
}

func (b *errorBody) Close() error { return nil }

// A write failure is not a CDN hiccup: it comes back as its own error so the
// caller can keep the bytes instead of reconnecting.
func TestCopyStreamReportsWriteErrorsSeparately(t *testing.T) {
	sink := &sinkStub{err: syscall.ENOSPC}
	n, err := copyStream(context.Background(), sink, io.NopCloser(bytes.NewReader(streamBytes())), time.Time{})
	var writeErr *writeError
	if !errors.As(err, &writeErr) {
		t.Fatalf("copyStream error = %v, want a write failure", err)
	}
	if !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("copyStream error hides the cause: %v", err)
	}
	if n != 0 {
		t.Fatalf("copyStream counted %d bytes, want 0", n)
	}
}

// The same failing write is our own deadline stopping the stream, not a defect.
func TestCopyStreamIgnoresTheInterruptedWrite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sink := &sinkStub{err: syscall.ENOSPC}
	if _, err := copyStream(ctx, sink, io.NopCloser(bytes.NewReader(streamBytes())), time.Time{}); err != nil {
		t.Fatalf("a deliberate stop was reported as a failure: %v", err)
	}
}

// A read failure stays what it always was: log it and let the caller reconnect.
func TestCopyStreamReconnectsAfterReadErrors(t *testing.T) {
	payload := streamBytes()
	sink := &sinkStub{}
	body := &errorBody{data: payload, err: errors.New("unexpected EOF")}
	n, err := copyStream(context.Background(), sink, body, time.Time{})
	if err != nil {
		t.Fatalf("a read error must not be returned: %v", err)
	}
	if n != int64(len(payload)) {
		t.Fatalf("copyStream counted %d bytes, want %d", n, len(payload))
	}
	if !bytes.Equal(sink.bytes(), payload) {
		t.Fatalf("sink holds %d bytes, want %d", len(sink.bytes()), len(payload))
	}
}
