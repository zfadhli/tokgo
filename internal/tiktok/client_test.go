package tiktok

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func newTestClient(t *testing.T, h http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New("", nil)
	c.base, c.webcast, c.euler, c.tikrec = srv.URL, srv.URL, srv.URL, srv.URL
	return c
}

func seedCookies(c *Client, cookies map[string]string) {
	u, err := url.Parse(c.base + "/")
	if err != nil {
		panic(err)
	}
	batch := make([]*http.Cookie, 0, len(cookies))
	for name, value := range cookies {
		batch = append(batch, &http.Cookie{Name: name, Value: value})
	}
	c.hc.Jar.SetCookies(u, batch)
}

func cookieValue(jar http.CookieJar, u *url.URL, name string) string {
	for _, ck := range jar.Cookies(u) {
		if ck.Name == name {
			return ck.Value
		}
	}
	return ""
}

func TestNewSeedsCookieJarAndHosts(t *testing.T) {
	c := New("", map[string]string{"sessionid": "abc123"})
	if c == nil {
		t.Fatal("New returned nil")
	}
	if c.hc.Jar == nil {
		t.Fatal("client has no cookie jar")
	}
	u, err := url.Parse("https://www.tiktok.com/")
	if err != nil {
		t.Fatal(err)
	}
	if got := cookieValue(c.hc.Jar, u, "sessionid"); got != "abc123" {
		t.Errorf("seeded cookie sessionid = %q, want abc123", got)
	}
	want := map[string]string{
		"base":    "https://www.tiktok.com",
		"webcast": "https://webcast.tiktok.com",
		"euler":   "https://tiktok.eulerstream.com",
		"tikrec":  "https://tikrec.com",
	}
	got := map[string]string{"base": c.base, "webcast": c.webcast, "euler": c.euler, "tikrec": c.tikrec}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s host = %q, want %q", k, got[k], v)
		}
	}
	if New("", nil) == nil {
		t.Error("New with no cookies returned nil")
	}
}

func TestTransportTimeouts(t *testing.T) {
	c := New("http://127.0.0.1:9", nil)
	tr, ok := c.hc.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T, want *http.Transport", c.hc.Transport)
	}
	if tr.Proxy == nil {
		t.Error("proxied client has no Proxy func")
	}
	if tr.TLSHandshakeTimeout != 15*time.Second {
		t.Errorf("TLSHandshakeTimeout = %v, want 15s", tr.TLSHandshakeTimeout)
	}
	if tr.ResponseHeaderTimeout != 30*time.Second {
		t.Errorf("ResponseHeaderTimeout = %v, want 30s", tr.ResponseHeaderTimeout)
	}
	if !tr.ForceAttemptHTTP2 {
		t.Error("ForceAttemptHTTP2 should be set")
	}
	if tr.DialContext == nil {
		t.Error("DialContext should be set")
	}
}

func TestBrowserDropsProxyAndSharesJar(t *testing.T) {
	c := New("http://127.0.0.1:9", map[string]string{"a": "b"})
	b := c.Browser()
	if b == c {
		t.Fatal("Browser returned the same client")
	}
	tr, ok := b.hc.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("browser transport is %T, want *http.Transport", b.hc.Transport)
	}
	if tr.Proxy != nil {
		t.Error("Browser client must not use the proxy")
	}
	if b.hc.Jar != c.hc.Jar {
		t.Error("Browser client must share the cookie jar")
	}
}

func TestRequestCarriesChromeHeaders(t *testing.T) {
	var seen http.Header
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
	}))
	resp, err := c.get(context.Background(), c.base+"/probe", false)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	want := map[string]string{
		"User-Agent":                "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.6478.127 Safari/537.36",
		"Accept-Language":           "en-US",
		"Accept":                    "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,application/json,text/plain,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7",
		"Referer":                   "https://www.tiktok.com/",
		"Origin":                    "https://www.tiktok.com",
		"Sec-Ch-Ua":                 `"Not/A)Brand";v="8", "Chromium";v="126"`,
		"Sec-Ch-Ua-Mobile":          "?0",
		"Sec-Ch-Ua-Platform":        `"Windows"`,
		"Upgrade-Insecure-Requests": "1",
		"Sec-Fetch-Site":            "none",
		"Sec-Fetch-Mode":            "navigate",
		"Sec-Fetch-User":            "?1",
		"Sec-Fetch-Dest":            "document",
	}
	for name, value := range want {
		if got := seen.Get(name); got != value {
			t.Errorf("header %s = %q, want %q", name, got, value)
		}
	}
}

func TestSeededCookieIsSent(t *testing.T) {
	var cookie string
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie = r.Header.Get("Cookie")
	}))
	seedCookies(c, map[string]string{"sessionid": "sentinel"})
	resp, err := c.get(context.Background(), c.base+"/probe", false)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !strings.Contains(cookie, "sessionid=sentinel") {
		t.Errorf("request cookie header = %q, want the seeded sessionid", cookie)
	}
}

func TestGetRedirectControl(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/from" {
			http.Redirect(w, r, "/to", http.StatusFound)
			return
		}
		io.WriteString(w, "landed")
	}))

	resp, err := c.get(context.Background(), c.base+"/from", false)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "landed" {
		t.Errorf("followed request got status %d body %q", resp.StatusCode, body)
	}

	resp, err = c.get(context.Background(), c.base+"/from", true)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Errorf("noRedirect request got status %d, want 302", resp.StatusCode)
	}
}

func TestOpenStream(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stream" {
			io.WriteString(w, "FLVDATA")
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))

	rc, err := c.OpenStream(context.Background(), c.base+"/stream")
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != "FLVDATA" {
		t.Errorf("stream body = %q, want FLVDATA", got)
	}

	if _, err := c.OpenStream(context.Background(), c.base+"/missing"); err == nil {
		t.Fatal("OpenStream on 503 returned no error")
	} else if !strings.Contains(err.Error(), "503") {
		t.Errorf("error %q does not mention the status", err)
	}
}
