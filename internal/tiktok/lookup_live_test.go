//go:build live

package tiktok

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"
)

// TestLookupYieldsPlayableStream is the real-service check for the unsigned
// path: the live-room endpoint must resolve a username to a room id, and the
// manifest reached with that id must serve media.
func TestLookupYieldsPlayableStream(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	c := New("", nil)
	roomID, err := c.RoomIDForUser(ctx, "aljazeeraenglish")
	if err != nil {
		t.Skipf("room lookup unavailable: %v", err)
	}
	urls, err := c.LiveURLs(ctx, roomID, "aljazeeraenglish")
	if err != nil {
		t.Skipf("user offline or lookup failed for room %s: %v", roomID, err)
	}
	if len(urls) == 0 {
		t.Skip("user is not live right now, no manifest to check")
	}
	t.Logf("room %s returned %d candidate urls, best=%s", roomID, len(urls), trim(urls[0]))

	body, err := c.OpenStream(ctx, urls[0])
	if err != nil {
		t.Fatalf("the manifest url does not serve media: %v", err)
	}
	defer body.Close()

	head := make([]byte, 1<<14)
	n, err := io.ReadFull(body, head)
	if err != nil && err != io.ErrUnexpectedEOF {
		t.Fatalf("reading the stream: %v", err)
	}
	head = head[:n]
	switch {
	case bytes.HasPrefix(head, []byte("FLV")):
		t.Logf("first %d bytes are an FLV container", n)
	case bytes.Contains(head, []byte("#EXTM3U")):
		t.Logf("first %d bytes are an HLS playlist", n)
	default:
		t.Fatalf("unexpected stream head: %q", head[:min(64, len(head))])
	}
}

func trim(s string) string {
	if len(s) > 90 {
		return s[:90]
	}
	return s
}
