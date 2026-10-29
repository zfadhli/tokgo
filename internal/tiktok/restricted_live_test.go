//go:build live

package tiktok

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"
)

// TestSignedEndpointYieldsPlayableStream is the real-service check for the
// restricted-room fallback: the payload reached through the signing service must
// carry stream urls, and the first one must serve actual media. A 4003110 room
// is not reproducible on demand, but this is the exact source used for it.
func TestSignedEndpointYieldsPlayableStream(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	c := New("", nil)
	urls, err := c.signedLiveURLs(ctx, "aljazeeraenglish")
	if err != nil {
		t.Skipf("signed endpoint unavailable (signing service down or user offline): %v", err)
	}
	if len(urls) == 0 {
		t.Skip("user is not live right now, no manifest to check")
	}
	t.Logf("signed endpoint returned %d candidate urls, best=%s", len(urls), trim(urls[0]))

	body, err := c.OpenStream(ctx, urls[0])
	if err != nil {
		t.Fatalf("the fallback url does not serve media: %v", err)
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
