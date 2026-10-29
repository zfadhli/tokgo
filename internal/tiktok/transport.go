package tiktok

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	utls "github.com/refraction-networking/utls"
	"tokgo/internal/logx"
)

const (
	dialTimeout  = 15 * time.Second
	tunnelBudget = 15 * time.Second
)

// chromeHello is pinned rather than HelloChrome_Auto: Auto tracks the newest
// profile, which sends post-quantum key shares that some middleboxes drop, and
// it would drift away from the User-Agent below without anyone noticing.
var chromeHello = utls.HelloChrome_120

// newTransport builds an HTTP/1.1 transport that performs every TLS handshake
// with Chrome's ClientHello instead of Go's. Vendored proxies are tunnelled
// here rather than by http.Transport, because a custom dialer and Proxy cannot
// be combined.
func newTransport(proxy string) *http.Transport {
	tr := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: dialTimeout}).DialContext,
		DialTLSContext:        dialTLS(parseProxy(proxy)),
		TLSHandshakeTimeout:   dialTimeout,
		ResponseHeaderTimeout: 30 * time.Second,
		MaxIdleConnsPerHost:   4,
	}
	return tr
}

// parseProxy keeps a blank or unusable proxy configuration out of the dialer.
// url.Parse("") returns a non-nil empty URL, which would otherwise make every
// request dial an empty address.
func parseProxy(proxy string) *url.URL {
	if strings.TrimSpace(proxy) == "" {
		return nil
	}
	parsed, err := url.Parse(proxy)
	if err != nil || parsed.Host == "" {
		logx.Warnf("ignoring unusable proxy %q: %v", proxy, err)
		return nil
	}
	return parsed
}

// dialTLS returns the transport's TLS dialer: CONNECT through the proxy when
// one is configured, then a Chrome handshake over the resulting conn.
func dialTLS(proxyURL *url.URL) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialer := &net.Dialer{Timeout: dialTimeout}
		raw, err := dialer.DialContext(ctx, network, targetOf(proxyURL, addr))
		if err != nil {
			return nil, err
		}
		if proxyURL != nil {
			tunnelled, err := connect(raw, addr, proxyURL)
			if err != nil {
				raw.Close()
				return nil, err
			}
			raw = tunnelled
		}
		conn, err := chromeHandshake(ctx, raw, addr)
		if err != nil {
			raw.Close()
			return nil, err
		}
		return conn, nil
	}
}

// chromeHandshake runs a Chrome ClientHello directly, so the fingerprint a WAF
// sees is the browser's, not crypto/tls's. http.Transport only treats a conn
// from DialTLSContext as TLS if it is a crypto/tls conn, which is why the
// result is a utls conn and HTTP/2 stays off.
func chromeHandshake(ctx context.Context, raw net.Conn, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	cfg := &utls.Config{
		ServerName: host,
		NextProtos: []string{"http/1.1"},
		MinVersion: tls.VersionTLS12,
	}
	conn, err := newChromeConn(raw, cfg)
	if err != nil {
		return nil, err
	}
	if err := conn.HandshakeContext(ctx); err != nil {
		var alert *utls.AlertError
		if errors.As(err, &alert) {
			return nil, fmt.Errorf("tls alert from %s: %w", host, err)
		}
		return nil, err
	}
	return conn, nil
}

// newChromeConn is the injectable seam: tests supply their own trust store.
func newChromeConn(raw net.Conn, cfg *utls.Config) (*utls.UConn, error) {
	spec, err := chromeClientHello()
	if err != nil {
		return nil, err
	}
	conn := utls.UClient(raw, cfg, utls.HelloCustom)
	if err := conn.ApplyPreset(spec); err != nil {
		return nil, err
	}
	return conn, nil
}

// chromeClientHello loads the pinned Chrome profile and narrows ALPN to
// HTTP/1.1. utls presets carry their own ALPN list (h2,http/1.1) and ignore
// Config.NextProtos, which would leave http.Transport speaking h1 to a server
// that agreed on h2. The reference tool pins HTTP/1.1 for the same reason.
func chromeClientHello() (*utls.ClientHelloSpec, error) {
	spec, err := utls.UTLSIdToSpec(chromeHello)
	if err != nil {
		return nil, fmt.Errorf("building the Chrome client hello: %w", err)
	}
	for _, ext := range spec.Extensions {
		if alpn, ok := ext.(*utls.ALPNExtension); ok {
			alpn.AlpnProtocols = []string{"http/1.1"}
		}
	}
	return &spec, nil
}

func targetOf(proxyURL *url.URL, addr string) string {
	if proxyURL == nil {
		return addr
	}
	return proxyURL.Host
}

// connect performs the HTTP CONNECT handshake for an HTTPS tunnel and returns
// a conn positioned exactly at the start of the TLS stream.
func connect(raw net.Conn, target string, proxyURL *url.URL) (net.Conn, error) {
	if err := raw.SetDeadline(time.Now().Add(tunnelBudget)); err != nil {
		return nil, err
	}

	req := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Opaque: target},
		Host:   target,
		Header: http.Header{},
	}
	if proxyURL.User != nil {
		password, _ := proxyURL.User.Password()
		credentials := proxyURL.User.Username() + ":" + password
		req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(credentials)))
	}
	if err := req.Write(raw); err != nil {
		return nil, fmt.Errorf("proxy connect write: %w", err)
	}

	// ReadResponse may buffer the first bytes of the tunnel, so the reader has to
	// survive into the TLS handshake.
	reader := bufio.NewReader(raw)
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		return nil, fmt.Errorf("proxy connect read: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("proxy refused CONNECT to %s: %s", target, resp.Status)
	}
	if err := raw.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	tunnelled := net.Conn(raw)
	if reader.Buffered() > 0 {
		tunnelled = &bufferedConn{Conn: raw, r: reader}
	}
	return tunnelled, nil
}

// bufferedConn keeps bytes ReadResponse already pulled off the socket.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }
