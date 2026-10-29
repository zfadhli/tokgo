package tiktok

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sync"

	"tokgo/internal/logx"
)

var chromeHeaders = map[string]string{
	"User-Agent":                "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
	"Accept-Language":           "en-US",
	"Accept":                    "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,application/json,text/plain,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7",
	"Referer":                   "https://www.tiktok.com/",
	"Origin":                    "https://www.tiktok.com",
	"Sec-Ch-Ua":                 `"Not_A Brand";v="8", "Chromium";v="120", "Google Chrome";v="120"`,
	"Sec-Ch-Ua-Mobile":          "?0",
	"Sec-Ch-Ua-Platform":        `"Windows"`,
	"Upgrade-Insecure-Requests": "1",
	"Sec-Fetch-Site":            "none",
	"Sec-Fetch-Mode":            "navigate",
	"Sec-Fetch-User":            "?1",
	"Sec-Fetch-Dest":            "document",
}

// Client talks to TikTok as a logged-in Chrome 126 session. The host fields are
// separate so tests can point every endpoint at one httptest server.
type Client struct {
	hc      *http.Client
	filter  *cookieFilter
	base    string
	webcast string
}

// cookieHosts are the TikTok hosts that receive the configured cookies. The jar
// scopes host-only cookies to the host they were set for, so seeding just
// www.tiktok.com leaves the webcast API unauthenticated, which is exactly the
// endpoint a login-gated room needs. No other host sees the session.
var cookieHosts = []string{"www.tiktok.com", "webcast.tiktok.com"}

// New builds a client seeded with cookies (name to value) and, when proxy is
// non-empty, routed through it. It never fails; an unparsable proxy is ignored.
func New(proxy string, cookies map[string]string) *Client {
	jar, _ := cookiejar.New(nil)
	if len(cookies) > 0 {
		warnManagedCookies(cookies)
		batch := make([]*http.Cookie, 0, len(cookies))
		for name, value := range cookies {
			if isManagedCookie(name) {
				continue
			}
			batch = append(batch, &http.Cookie{Name: name, Value: value})
		}
		for _, host := range cookieHosts {
			jar.SetCookies(&url.URL{Scheme: "https", Host: host, Path: "/"}, batch)
		}
	}
	filter := &cookieFilter{next: newTransport(proxy), omitted: newOmitSet()}
	return &Client{
		hc:      &http.Client{Jar: jar, Transport: filter},
		filter:  filter,
		base:    "https://www.tiktok.com",
		webcast: "https://webcast.tiktok.com",
	}
}

// Browser copies the client without the proxy so stream downloads go direct
// while still sharing the login cookies.
func (c *Client) Browser() *Client {
	// The omit set is shared so a cookie dropped by one client is not re-learnt
	// by the other: both talk to the same session, so the same names must go.
	filter := &cookieFilter{next: newTransport(""), omitted: c.filter.omitted}
	b := *c
	b.filter = filter
	b.hc = &http.Client{Jar: c.hc.Jar, Transport: filter}
	return &b
}

func (c *Client) get(ctx context.Context, rawURL string, noRedirect bool) (*http.Response, error) {
	req, err := c.request(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	return c.do(req, noRedirect)
}

func (c *Client) request(ctx context.Context, rawURL string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	for name, value := range chromeHeaders {
		req.Header.Set(name, value)
	}
	return req, nil
}

func (c *Client) do(req *http.Request, noRedirect bool) (*http.Response, error) {
	if !noRedirect {
		return c.hc.Do(req)
	}
	client := *c.hc
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return client.Do(req)
}

// idcCookie is the routing hint TikTok reads to decide which datacentre serves a
// request. A stale value makes the webcast API answer 4003110.
const idcCookie = "tt-target-idc"

// managedCookies are routing/region hints TikTok computes per session. Sent by
// hand they can only be wrong: measured against a login-gated room, tt-target-idc
// = useast2a makes room/info answer 4003110 while every other value, and no
// value at all, returns the stream manifest. They are therefore never sent,
// whatever cookies.json says.
var managedCookies = []string{"tt-target-idc", "tt-target-idc-sign"}

func isManagedCookie(name string) bool {
	for _, managed := range managedCookies {
		if managed == name {
			return true
		}
	}
	return false
}

// omitSet is the list of cookie names withheld from outgoing requests.
type omitSet struct {
	mu    sync.RWMutex
	names map[string]bool
}

// newOmitSet starts with every server-managed cookie already withheld.
func newOmitSet() *omitSet {
	set := &omitSet{names: make(map[string]bool, len(managedCookies))}
	for _, name := range managedCookies {
		set.names[name] = true
	}
	return set
}

func (s *omitSet) has(name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.names[name]
}

func (s *omitSet) size() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.names)
}

// cookieFilter strips managed cookie names from outgoing requests, whether they
// came from cookies.json or from the jar.
type cookieFilter struct {
	next    http.RoundTripper
	omitted *omitSet
}

func (f *cookieFilter) RoundTrip(r *http.Request) (*http.Response, error) {
	if f.omitted.size() == 0 || len(r.Cookies()) == 0 {
		return f.next.RoundTrip(r)
	}

	kept := make([]*http.Cookie, 0, len(r.Cookies()))
	for _, cookie := range r.Cookies() {
		if !f.omitted.has(cookie.Name) {
			kept = append(kept, cookie)
		}
	}
	clone := r.Clone(r.Context())
	clone.Header.Del("Cookie")
	for _, cookie := range kept {
		clone.AddCookie(cookie)
	}
	return f.next.RoundTrip(clone)
}

// warnManagedCookies tells the operator that a configured hint is being ignored,
// because the failure it causes looks like a private room.
func warnManagedCookies(cookies map[string]string) {
	for _, name := range managedCookies {
		if value, ok := cookies[name]; ok && value != "" {
			logx.Warnf("ignoring %s=%s from cookies.json: TikTok computes that hint per session, and a stale value makes the room API answer 4003110", name, value)
		}
	}
}
