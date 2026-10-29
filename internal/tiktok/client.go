package tiktok

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/url"
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
	base    string
	webcast string
	euler   string
	tikrec  string
}

// New builds a client seeded with cookies (name to value) and, when proxy is
// non-empty, routed through it. It never fails; an unparsable proxy is ignored.
func New(proxy string, cookies map[string]string) *Client {
	jar, _ := cookiejar.New(nil)
	if len(cookies) > 0 {
		origin := &url.URL{Scheme: "https", Host: "www.tiktok.com", Path: "/"}
		batch := make([]*http.Cookie, 0, len(cookies))
		for name, value := range cookies {
			batch = append(batch, &http.Cookie{Name: name, Value: value})
		}
		jar.SetCookies(origin, batch)
	}
	return &Client{
		hc:      &http.Client{Jar: jar, Transport: newTransport(proxy)},
		base:    "https://www.tiktok.com",
		webcast: "https://webcast.tiktok.com",
		euler:   "https://tiktok.eulerstream.com",
		tikrec:  "https://tikrec.com",
	}
}

// Browser copies the client without the proxy so stream downloads go direct
// while still sharing the login cookies.
func (c *Client) Browser() *Client {
	b := *c
	b.hc = &http.Client{Jar: c.hc.Jar, Transport: newTransport("")}
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
