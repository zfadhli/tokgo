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
	"testing"
	"time"

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

func TestRecordCapturesUntilDurationCap(t *testing.T) {
	payload := streamBytes()
	api := &fakeAPI{
		urls: []string{"url-a"},
		open: func(context.Context, string) (io.ReadCloser, error) {
			return newStalledBody(payload), nil
		},
	}
	rec := New(api, Options{OutputDir: t.TempDir(), Duration: 1, FFmpegPath: copyStub(t, "cp \"$3\" \"$last\"")})

	start := time.Now()
	path, err := collect(t, rec, context.Background())
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond || elapsed > 5*time.Second {
		t.Fatalf("recording stopped after %v, want about 1s (duration cap)", elapsed)
	}
	if !strings.HasSuffix(path, ".mp4") || strings.HasSuffix(path, "_flv.mp4") {
		t.Fatalf("Record returned %q, want the converted path", path)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("file holds %d bytes, want %d", len(got), len(payload))
	}
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
	rec := New(api, Options{OutputDir: t.TempDir(), FFmpegPath: copyStub(t, "cp \"$3\" \"$last\"")})

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
}

func TestRecordAllCandidatesEmpty(t *testing.T) {
	dir := t.TempDir()
	api := &fakeAPI{
		urls: []string{"url-a", "url-b"},
		open: func(context.Context, string) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(nil)), nil
		},
	}
	rec := New(api, Options{OutputDir: dir, FFmpegPath: copyStub(t, "cp \"$3\" \"$last\"")})

	path, err := collect(t, rec, context.Background())
	if !errors.Is(err, tiktok.ErrLiveURL) {
		t.Fatalf("Record error = %v, want tiktok.ErrLiveURL", err)
	}
	if path != "" {
		t.Fatalf("Record returned path %q for a failed capture", path)
	}
	leftovers, err := filepath.Glob(filepath.Join(dir, "TK_*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("failed capture left %v behind", leftovers)
	}
}

func TestRecordCancellationKeepsCapture(t *testing.T) {
	payload := streamBytes()
	body := newStalledBody(payload)
	api := &fakeAPI{
		urls: []string{"url-a"},
		open: func(context.Context, string) (io.ReadCloser, error) { return body, nil },
	}
	rec := New(api, Options{OutputDir: t.TempDir(), FFmpegPath: copyStub(t, "cp \"$3\" \"$last\"")})

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
