package tiktok

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	utls "github.com/refraction-networking/utls"
)

// testTLSConfig is the utls equivalent of the config a client would build.
func testTLSConfig(host string, roots *x509.CertPool) *utls.Config {
	return &utls.Config{
		ServerName: host,
		NextProtos: []string{"http/1.1"},
		RootCAs:    roots,
		MinVersion: tls.VersionTLS12,
	}
}

// TestConnectTunnelPreservesBufferedBytes pins the subtle part of the CONNECT
// handshake: bytes the proxy sends in the same packet as its 200 must reach the
// TLS layer, not vanish into a discarded bufio.Reader.
func TestConnectTunnelPreservesBufferedBytes(t *testing.T) {
	const (
		target   = "example.test:443"
		trailing = "TLSHELLO"
	)
	proxyment := make(chan string, 1)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	go func() {
		server, err := listener.Accept()
		if err != nil {
			return
		}
		defer server.Close()
		server.SetDeadline(time.Now().Add(5 * time.Second))
		reader := bufio.NewReader(server)
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		var headers []string
		for {
			h, err := reader.ReadString('\n')
			if err != nil || h == "\r\n" {
				break
			}
			headers = append(headers, strings.TrimSpace(h))
		}
		proxyment <- line + strings.Join(headers, "|")
		io.WriteString(server, "HTTP/1.1 200 Connection established\r\n\r\n"+trailing)
		time.Sleep(2 * time.Second)
	}()

	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	proxyURL, err := url.Parse("http://user:secret@proxy.test:3128")
	if err != nil {
		t.Fatal(err)
	}
	tunnel, err := connect(conn, target, proxyURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	got := make([]byte, len(trailing))
	tunnel.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(tunnel, got); err != nil {
		t.Fatalf("reading tunnel: %v", err)
	}
	if string(got) != trailing {
		t.Fatalf("tunnel read %q, want %q (buffered bytes lost)", got, trailing)
	}

	seen := <-proxyment
	if !strings.HasPrefix(seen, "CONNECT "+target+" HTTP/1.1") {
		t.Errorf("CONNECT line = %q", seen)
	}
	// Basic dXNlcjpzZWNyZXQ= is user:secret.
	if !strings.Contains(seen, "Proxy-Authorization: Basic dXNlcjpzZWNyZXQ=") {
		t.Errorf("missing proxy credentials: %q", seen)
	}
}

// TestConnectTunnelRejectionIsAnError proves a proxy refusal surfaces instead of
// being treated as an open tunnel.
func TestConnectTunnelRejectionIsAnError(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		server, err := listener.Accept()
		if err != nil {
			return
		}
		defer server.Close()
		reader := bufio.NewReader(server)
		for {
			line, err := reader.ReadString('\n')
			if err != nil || line == "\r\n" {
				break
			}
		}
		io.WriteString(server, "HTTP/1.1 407 Proxy Authentication Required\r\nContent-Length: 0\r\n\r\n")
	}()

	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if _, err := connect(conn, "example.test:443", &url.URL{Scheme: "http", Host: "proxy.test"}); err == nil {
		t.Fatal("a 407 response must not be accepted as a tunnel")
	}
}

// TestChromeHandshakeInterop runs the real utls ClientHello against a Go TLS
// server and speaks HTTP/1.1 over the result, which is what the recorder does
// against TikTok.
func TestChromeHandshakeInterop(t *testing.T) {
	var gotProto string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotProto = r.Proto
		io.WriteString(w, "ok")
	}))
	defer srv.Close()

	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	addr := srv.Listener.Addr().String()
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}

	raw, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := newChromeConn(raw, testTLSConfig(host, roots))
	if err != nil {
		t.Fatalf("newChromeConn: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := conn.HandshakeContext(ctx); err != nil {
		raw.Close()
		t.Fatalf("handshake: %v", err)
	}
	if proto := conn.ConnectionState().NegotiatedProtocol; proto != "http/1.1" {
		t.Errorf("negotiated %q, want http/1.1", proto)
	}

	req, err := http.NewRequest("GET", "/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	if err := req.Write(conn); err != nil {
		t.Fatalf("write request: %v", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("got %d %q, want 200 ok", resp.StatusCode, body)
	}
	if gotProto != "HTTP/1.1" {
		t.Errorf("server saw %q, want HTTP/1.1", gotProto)
	}
}

// TestTransportDialsItsOwnTunnel guards the invariant that made the custom
// dialer necessary: http.Transport ignores DialTLSContext for proxied requests,
// so the proxy must be handled inside the dialer and Transport.Proxy stay nil.
func TestTransportDialsItsOwnTunnel(t *testing.T) {
	for _, proxy := range []string{"", "http://127.0.0.1:3128", "socks5://127.0.0.1:1080"} {
		tr := newTransport(proxy)
		if tr.Proxy != nil {
			t.Errorf("proxy %q: Transport.Proxy must stay nil, the dialer tunnels", proxy)
		}
		if tr.DialTLSContext == nil {
			t.Errorf("proxy %q: DialTLSContext missing, TLS would fall back to crypto/tls", proxy)
		}
		if tr.ForceAttemptHTTP2 {
			t.Errorf("proxy %q: HTTP/2 must stay off to keep the HTTP/1.1 ALPN", proxy)
		}
	}
}
