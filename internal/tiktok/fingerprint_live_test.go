//go:build live

package tiktok

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// chromeJA4 is Chrome's JA4 cipher+extension hash: 15 ciphers, 16 extensions.
// It is what a WAF matches on, and it is the value crypto/tls can never produce.
const chromeJA4 = "t13d1516h1_8daaf6152771"

type fingerprint struct {
	TLS struct {
		JA3 string `json:"ja3_hash"`
		JA4 string `json:"ja4"`
	} `json:"tls"`
	HTTP string `json:"http_version"`
	UA   string `json:"user_agent"`
}

func TestTLSFingerprintIsChrome(t *testing.T) {
	plain, err := probeFingerprint(&http.Transport{ForceAttemptHTTP2: false})
	if err != nil {
		t.Skipf("network or fingerprint service unavailable: %v", err)
	}
	chrome, err := probeChromeFingerprint()
	if err != nil {
		t.Fatalf("the impersonating transport failed its own probe: %v", err)
	}

	t.Logf("crypto/tls  ja4=%s ja3=%s http=%s ua=%s", plain.TLS.JA4, plain.TLS.JA3, plain.HTTP, plain.UA)
	t.Logf("utls chrome ja4=%s ja3=%s http=%s ua=%s", chrome.TLS.JA4, chrome.TLS.JA3, chrome.HTTP, chrome.UA)

	if !hasPrefix(chrome.TLS.JA4, chromeJA4) {
		t.Errorf("utls JA4 = %q, want the Chrome prefix %q", chrome.TLS.JA4, chromeJA4)
	}
	if hasPrefix(plain.TLS.JA4, chromeJA4) {
		t.Errorf("crypto/tls JA4 = %q already matches Chrome, so this check proves nothing", plain.TLS.JA4)
	}
	if !speaksHTTP1(chrome.HTTP) {
		t.Errorf("utls spoke %q, want HTTP/1.1 to match the reference tool's forced version", chrome.HTTP)
	}
	if !strings.Contains(chrome.UA, "Chrome/120") {
		t.Errorf("user agent seen by the server = %q, want the Chrome header set", chrome.UA)
	}
	if plain.TLS.JA3 == chrome.TLS.JA3 {
		t.Error("both transports report the same JA3, the impersonation is not taking effect")
	}
}

func TestChromeHandshakeAgainstTikTok(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	raw, err := (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, "tcp", "www.tiktok.com:443")
	if err != nil {
		t.Skipf("no network: %v", err)
	}
	conn, err := newChromeConn(raw, testTLSConfig("www.tiktok.com", nil))
	if err != nil {
		t.Fatalf("newChromeConn: %v", err)
	}
	defer conn.Close()
	if err := conn.HandshakeContext(ctx); err != nil {
		t.Fatalf("handshake with TikTok failed: %v", err)
	}
	state := conn.ConnectionState()
	t.Logf("tiktok: proto=%s cipher=%s", state.NegotiatedProtocol, tls.CipherSuiteName(state.CipherSuite))
	if state.NegotiatedProtocol != "http/1.1" {
		t.Errorf("negotiated %q, want http/1.1", state.NegotiatedProtocol)
	}
}

func probeFingerprint(tr http.RoundTripper) (fingerprint, error) {
	client := &http.Client{Transport: tr, Timeout: 25 * time.Second}
	resp, err := client.Get("https://tls.peet.ws/api/all")
	if err != nil {
		return fingerprint{}, err
	}
	defer resp.Body.Close()
	var payload fingerprint
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return fingerprint{}, fmt.Errorf("decode fingerprint: %w", err)
	}
	return payload, nil
}

// probeChromeFingerprint goes through the real client, so the report covers the
// headers on the wire as well as the TLS handshake.
func probeChromeFingerprint() (fingerprint, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	client := New("", nil)
	resp, err := client.get(ctx, "https://tls.peet.ws/api/all", false)
	if err != nil {
		return fingerprint{}, err
	}
	defer resp.Body.Close()
	var payload fingerprint
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return fingerprint{}, fmt.Errorf("decode fingerprint: %w", err)
	}
	return payload, nil
}

func speaksHTTP1(version string) bool {
	v := strings.ToLower(version)
	return v == "h1" || strings.HasPrefix(v, "http/1.1")
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// TestImpersonationVersusPlainTLS requests the same signed TikTok URL with Go's
// TLS stack and with the Chrome one. TikTok's WAF fingerprints the handshake, so
// this is the measurement that justifies the dependency.
func TestImpersonationVersusPlainTLS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	chrome := New("", nil)
	signed, err := chrome.signedRoomURL(ctx, "tiktok")
	if err != nil {
		t.Skipf("signing service unavailable: %v", err)
	}

	plainStatus := statusOf(t, plainClient(), signed)
	chromeStatus := statusOf(t, chrome, signed)
	t.Logf("signed room url: crypto/tls=%d utls-chrome=%d", plainStatus, chromeStatus)

	if chromeStatus != http.StatusOK {
		t.Errorf("the impersonating client got %d, want 200", chromeStatus)
	}
	if plainStatus == http.StatusForbidden && chromeStatus == http.StatusOK {
		t.Logf("impersonation unblocked a 403: the Chrome hello passes where Go's is refused")
	}
}

func plainClient() *Client {
	c := New("", nil)
	c.hc = &http.Client{Transport: &http.Transport{ForceAttemptHTTP2: false}}
	return c
}

func statusOf(t *testing.T, c *Client, rawURL string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resp, err := c.get(ctx, rawURL, false)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	return resp.StatusCode
}

// TestProxyTunnelThroughChromeTLS exercises the hand-rolled CONNECT tunnel
// against a real TLS endpoint: the request must arrive through the proxy and the
// handshake must still be Chrome's. Skipped when the network is unavailable.
func TestProxyTunnelThroughChromeTLS(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	connects := make(chan string, 4)
	go func() {
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			go proxyRequest(client, connects)
		}
	}()

	client := New("http://"+listener.Addr().String(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := client.get(ctx, "https://www.tiktok.com/", false)
	if err != nil {
		t.Skipf("network or proxy path unavailable: %v", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 500 {
		t.Errorf("status through the tunnel = %d", resp.StatusCode)
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	resp.Body.Close()

	select {
	case target := <-connects:
		if target != "www.tiktok.com:443" {
			t.Errorf("proxied CONNECT target = %q", target)
		}
	case <-time.After(5 * time.Second):
		t.Error("the proxy never saw a CONNECT")
	}
}

func proxyRequest(client net.Conn, connects chan<- string) {
	defer client.Close()
	reader := bufio.NewReader(client)
	line, err := reader.ReadString('\n')
	if err != nil {
		return
	}
	fields := strings.Fields(line)
	if len(fields) < 2 || fields[0] != http.MethodConnect {
		return
	}
	for {
		header, err := reader.ReadString('\n')
		if err != nil || header == "\r\n" {
			break
		}
	}
	upstream, err := net.DialTimeout("tcp", fields[1], 15*time.Second)
	if err != nil {
		io.WriteString(client, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
		return
	}
	defer upstream.Close()
	io.WriteString(client, "HTTP/1.1 200 Connection established\r\n\r\n")
	connects <- fields[1]

	done := make(chan struct{}, 2)
	go func() { io.Copy(upstream, reader); done <- struct{}{} }()
	go func() { io.Copy(client, upstream); done <- struct{}{} }()
	<-done
}
