package tiktok

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sync"
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
	euler   string
	tikrec  string
}

// cookieHosts are the TikTok hosts that receive the configured cookies. The jar
// scopes host-only cookies to the host they were set for, so seeding just
// www.tiktok.com leaves the webcast API unauthenticated, which is exactly the
// endpoint a login-gated room needs. Third-party hosts (the signing service, the
// euler API) must not see the session.
var cookieHosts = []string{"www.tiktok.com", "webcast.tiktok.com"}

// New builds a client seeded with cookies (name to value) and, when proxy is
// non-empty, routed through it. It never fails; an unparsable proxy is ignored.
func New(proxy string, cookies map[string]string) *Client {
	jar, _ := cookiejar.New(nil)
	if len(cookies) > 0 {
		batch := make([]*http.Cookie, 0, len(cookies))
		for name, value := range cookies {
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
		euler:   "https://tiktok.eulerstream.com",
		tikrec:  "https://tikrec.com",
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

// dropCookie reports whether name was configured and stops it from being sent
// from now on. Removing it from the jar is unreliable: a host-only cookie has no
// domain to match a delete against, so the name is filtered out of each
// request's Cookie header instead.
func (c *Client) dropCookie(name string) bool {
	origin, err := url.Parse(c.webcast + "/")
	if err != nil {
		return false
	}
	for _, cookie := range c.hc.Jar.Cookies(origin) {
		if cookie.Name == name && !c.filter.omitted.has(name) {
			c.filter.omitted.add(name)
			return true
		}
	}
	return false
}

// omitSet is the shared list of cookie names withheld from outgoing requests.
type omitSet struct {
	mu    sync.RWMutex
	names map[string]bool
}

func newOmitSet() *omitSet { return &omitSet{names: map[string]bool{}} }

func (s *omitSet) add(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.names[name] = true
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

// cookieFilter strips omitted cookie names from outgoing requests, which is the
// only reliable way to drop a cookie the jar keeps re-attaching.
type cookieFilter struct {
	next    http.RoundTripper
	omitted *omitSet
}

func (f *cookieFilter) RoundTrip(r *http.Request) (*http.Response, error) {
	if f.omitted.size() == 0 {
		return f.next.RoundTrip(r)
	}

	kept := r.Cookies()[:0]
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
