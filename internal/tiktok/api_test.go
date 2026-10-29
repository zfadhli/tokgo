package tiktok

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	infoLive       = `{"status_code":0,"data":{"status":2,"stream_url":{"flv_pull_url":{"FULL_HD1":"https://cdn.example/full.flv"}}}}`
	infoEnded      = `{"status_code":0,"data":{"status":4,"stream_url":{"flv_pull_url":{"FULL_HD1":"https://cdn.example/full.flv"}}}}`
	infoRestricted = `{"status_code":4003110,"message":"restricted"}`
	infoOtherCode  = `{"status_code":4003100,"data":{"status":2,"stream_url":{"flv_pull_url":{"FULL_HD1":"https://cdn.example/full.flv"}}}}`
)

func writeBody(w http.ResponseWriter, body string) {
	_, _ = io.WriteString(w, body)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func serveRoomInfo(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/webcast/room/info/":
			writeBody(w, body)
		case "/webcast/room/check_alive/":
			writeBody(w, `{"data":[{"alive":true}]}`)
		default:
			http.NotFound(w, r)
		}
	})
}

func TestRoomStatus(t *testing.T) {
	cases := []struct {
		name  string
		alive string
		info  string
		want  bool
	}{
		{"streaming", `{"data":[{"alive":true}]}`, infoLive, true},
		{"ended status", `{"data":[{"alive":true}]}`, infoEnded, false},
		{"restricted 4003110", `{"data":[{"alive":true}]}`, infoRestricted, true},
		{"other status code", `{"data":[{"alive":true}]}`, infoOtherCode, false},
		{"not alive", `{"data":[{"alive":false}]}`, "", false},
		{"empty data list", `{"data":[]}`, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/webcast/room/check_alive/":
					writeBody(w, tc.alive)
				case "/webcast/room/info/":
					if tc.info == "" {
						t.Error("room/info requested for a room that is not alive")
					}
					writeBody(w, tc.info)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
				}
			}))
			got, err := c.RoomStatus(t.Context(), "7123")
			if err != nil {
				t.Fatalf("RoomStatus: %v", err)
			}
			if got.Live != tc.want {
				t.Errorf("RoomStatus.Live = %v, want %v", got.Live, tc.want)
			}
		})
	}
}

func TestRoomStatusEmptyRoomID(t *testing.T) {
	c := New("", nil)
	if _, err := c.RoomStatus(t.Context(), ""); !errors.Is(err, ErrNotLive) {
		t.Fatalf("err = %v, want ErrNotLive", err)
	}
}

func TestRoomStatusTitleAndUptime(t *testing.T) {
	info := func(createTime string) string {
		return `{"status_code":0,"data":{"status":2,"title":"Your budget questions answered","create_time":` +
			createTime + `,"stream_url":{"flv_pull_url":{"FULL_HD1":"https://cdn.example/full.flv"}}}}`
	}
	cases := []struct {
		name       string
		info       string
		wantUptime bool
	}{
		{"fresh create_time", info(strconv.FormatInt(time.Now().Add(-90*time.Second).Unix(), 10)), true},
		{"missing create_time", info("0"), false},
		{"stale create_time", info(strconv.FormatInt(time.Now().Add(-48*time.Hour).Unix(), 10)), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/webcast/room/check_alive/":
					writeBody(w, `{"data":[{"alive":true}]}`)
				case "/webcast/room/info/":
					writeBody(w, tc.info)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
				}
			}))
			status, err := c.RoomStatus(t.Context(), "7702058376456391427")
			if err != nil {
				t.Fatalf("RoomStatus: %v", err)
			}
			if !status.Live {
				t.Fatal("a room with a stream must report live")
			}
			if want := "Your budget questions answered"; status.Title != want {
				t.Errorf("Title = %q, want %q", status.Title, want)
			}
			if !tc.wantUptime {
				if !status.StartedAt.IsZero() {
					t.Errorf("StartedAt = %v, want zero", status.StartedAt)
				}
				return
			}
			if status.StartedAt.IsZero() {
				t.Fatal("a fresh create_time must yield a start time")
			}
			if drift := status.StartedAt.Sub(time.Now().Add(-90 * time.Second)); drift > 5*time.Second || drift < -5*time.Second {
				t.Errorf("StartedAt is %v off the 90s probe", drift)
			}
		})
	}
}

func TestLiveURLsQualityPassthrough(t *testing.T) {
	body := `{"status_code":0,"data":{"status":2,"stream_url":{"live_core_sdk_data":{"pull_data":{` +
		`"stream_data":"{\"data\":{\"hd\":{\"main\":{\"flv\":\"flv-hd\"}},\"origin\":{\"main\":{\"flv\":\"flv-origin\",\"hls\":\"hls-origin\"}}}}",` +
		`"options":{"qualities":[{"sdk_key":"hd","level":3},{"sdk_key":"origin","level":10}]}}}}}}`
	c := newTestClient(t, serveRoomInfo(body))
	got, err := c.LiveURLs(t.Context(), "7123", "")
	if err != nil {
		t.Fatalf("LiveURLs: %v", err)
	}
	want := []string{"flv-origin", "hls-origin", "flv-hd"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("url %d = %q, want %q (full %v)", i, got[i], want[i], got)
		}
	}
}

func TestLiveURLsRestrictedScrape(t *testing.T) {
	page := `<script>var u=["https://cdn.example/a.flv?x=1&amp;y=2","https://cdn.example/b_or4.flv?z=3&amp;w=4\"];</script>`
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/webcast/room/info/":
			writeBody(w, infoRestricted)
		case "/tiktok/room/api/sign":
			w.WriteHeader(http.StatusServiceUnavailable)
		case "/@creator/live":
			writeBody(w, page)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
	got, err := c.LiveURLs(t.Context(), "7123", "creator")
	if err != nil {
		t.Fatalf("LiveURLs: %v", err)
	}
	want := []string{"https://cdn.example/b_or4.flv?z=3&w=4"}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestLiveURLsRestrictedWithoutFallback(t *testing.T) {
	cases := []struct {
		name string
		user string
		page string
	}{
		{"no user to scrape", "", ""},
		{"page without flv or m3u8", "creator", `<html>nothing here</html>`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/webcast/room/info/":
					writeBody(w, infoRestricted)
				default:
					writeBody(w, tc.page)
				}
			}))
			if _, err := c.LiveURLs(t.Context(), "7123", tc.user); !errors.Is(err, ErrPrivateRoom) {
				t.Fatalf("err = %v, want ErrPrivateRoom", err)
			}
		})
	}
}

func TestLiveURLsRestrictedScrapeHLS(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/webcast/room/info/":
			writeBody(w, infoRestricted)
		case "/tiktok/room/api/sign":
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			writeBody(w, `<script>src="https://cdn.example/live.m3u8?sign=1"</script>`)
		}
	}))
	got, err := c.LiveURLs(t.Context(), "7123", "creator")
	if err != nil {
		t.Fatalf("LiveURLs: %v", err)
	}
	if len(got) != 1 || got[0] != "https://cdn.example/live.m3u8?sign=1" {
		t.Fatalf("got %v", got)
	}
}

func TestLiveURLsProseMarkers(t *testing.T) {
	cases := []struct {
		name string
		body string
		want error
	}{
		{"private account", `{"status_code":0,"message":"This account is private"}`, ErrPrivateAccount},
		{"follow required", `{"status_code":0,"message":"Follow the creator to watch their LIVE"}`, ErrPrivateFollow},
		{"ended", infoEnded, ErrNotLive},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, serveRoomInfo(tc.body))
			if _, err := c.LiveURLs(t.Context(), "7123", ""); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestLiveURLsNoCandidatesIsEmptyNotError(t *testing.T) {
	c := newTestClient(t, serveRoomInfo(`{"status_code":0,"data":{"status":2}}`))
	got, err := c.LiveURLs(t.Context(), "7123", "")
	if err != nil {
		t.Fatalf("LiveURLs: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("got %v, want an empty non-nil slice", got)
	}
}

func liveMux(t *testing.T, roomID string) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/api-live/user/room/", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("uniqueId"); got != "alice" {
			t.Errorf("uniqueId = %q, want alice", got)
		}
		writeBody(w, `{"statusCode":0,"data":{"user":{"roomId":`+roomID+`}}}`)
	})
	return mux
}

func TestRoomIDForUser(t *testing.T) {
	c := newTestClient(t, liveMux(t, `"701"`))
	got, err := c.RoomIDForUser(t.Context(), "alice")
	if err != nil {
		t.Fatalf("RoomIDForUser: %v", err)
	}
	if got != "701" {
		t.Fatalf("roomID = %q, want 701", got)
	}
}

func TestRoomIDForUserNumericRoomID(t *testing.T) {
	c := newTestClient(t, liveMux(t, `702`))
	got, err := c.RoomIDForUser(t.Context(), "alice")
	if err != nil {
		t.Fatalf("RoomIDForUser: %v", err)
	}
	if got != "702" {
		t.Fatalf("roomID = %q, want 702", got)
	}
}

func TestRoomIDForUserErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"waf page", http.StatusOK, "Please wait", ErrWAF},
		{"waf stub", http.StatusOK, "<html>SlardarWAF</html>", ErrWAF},
		{"forbidden", http.StatusForbidden, "", ErrWAF},
		{"empty body", http.StatusOK, "", ErrWAF},
		{"no room id", http.StatusOK, `{"statusCode":19881007,"msg":null,"data":{}}`, ErrRoomID},
		{"unparsable", http.StatusOK, "<html>nope</html>", ErrRoomID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				writeBody(w, tc.body)
			}))
			if _, err := c.RoomIDForUser(t.Context(), "alice"); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestUserForRoomID(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
		err  error
	}{
		{"owner", `{"data":{"owner":{"display_id":"alice","unique_id":"alice_u"}}}`, "alice", nil},
		{"follow marker", `{"status_code":0,"message":"Follow the creator to watch their LIVE"}`, "", ErrPrivateFollow},
		{"private account", `{"status_code":0,"message":"This account is private"}`, "", ErrPrivateAccount},
		{"missing owner", `{"data":{"owner":{}}}`, "", ErrUserUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, serveRoomInfo(tc.body))
			got, err := c.UserForRoomID(t.Context(), "7123")
			if tc.err != nil {
				if !errors.Is(err, tc.err) {
					t.Fatalf("err = %v, want %v", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("UserForRoomID: %v", err)
			}
			if got != tc.want {
				t.Fatalf("user = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestUserAndRoomFromURL(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api-live/user/room/", func(w http.ResponseWriter, r *http.Request) {
		writeBody(w, `{"statusCode":0,"data":{"user":{"roomId":"704"}}}`)
	})
	mux.HandleFunc("/@alice/live", func(w http.ResponseWriter, r *http.Request) {
		writeBody(w, "<html>live page</html>")
	})
	mux.HandleFunc("/@blocked/live", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/live")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/short", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://www.tiktok.com/@bob/live")
		w.WriteHeader(http.StatusMovedPermanently)
		writeBody(w, `<a href="https://www.tiktok.com/@bob/live?enter_from=share">watch</a>`)
	})
	mux.HandleFunc("/dud", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMovedPermanently)
		writeBody(w, "<html>no link</html>")
	})
	c := newTestClient(t, mux)

	t.Run("country blocked", func(t *testing.T) {
		if _, _, err := c.UserAndRoomFromURL(t.Context(), c.base+"/@blocked/live"); !errors.Is(err, ErrCountryBlocked) {
			t.Fatalf("err = %v, want ErrCountryBlocked", err)
		}
	})

	t.Run("live url", func(t *testing.T) {
		stub := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			body := ""
			switch r.URL.Path {
			case "/@alice/live":
				if r.URL.Host != "www.tiktok.com" {
					t.Errorf("requested host %q, want www.tiktok.com", r.URL.Host)
				}
				body = "<html>live page</html>"
			case "/api-live/user/room/":
				body = `{"statusCode":0,"data":{"user":{"roomId":"704"}}}`
			default:
				t.Errorf("unexpected request %s", r.URL)
			}
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		scoped := New("", nil)
		scoped.hc.Transport = stub
		user, roomID, err := scoped.UserAndRoomFromURL(t.Context(), "https://www.tiktok.com/@alice/live?is_from_webapp=1")
		if err != nil {
			t.Fatalf("UserAndRoomFromURL: %v", err)
		}
		if user != "alice" || roomID != "704" {
			t.Fatalf("got user %q room %q, want alice/704", user, roomID)
		}
	})

	t.Run("mobile shortlink", func(t *testing.T) {
		user, roomID, err := c.UserAndRoomFromURL(t.Context(), c.base+"/short")
		if err != nil {
			t.Fatalf("UserAndRoomFromURL: %v", err)
		}
		if user != "bob" || roomID != "704" {
			t.Fatalf("got user %q room %q, want bob/704", user, roomID)
		}
	})

	t.Run("shortlink without a user", func(t *testing.T) {
		if _, _, err := c.UserAndRoomFromURL(t.Context(), c.base+"/dud"); !errors.Is(err, ErrInvalidURL) {
			t.Fatalf("err = %v, want ErrInvalidURL", err)
		}
	})
}

func TestCountryBlocked(t *testing.T) {
	redirecting := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/live" {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		w.Header().Set("Location", "https://www.tiktok.com/live/blocked")
		w.WriteHeader(http.StatusFound)
	}))
	blocked, err := redirecting.CountryBlocked(t.Context())
	if err != nil {
		t.Fatalf("CountryBlocked: %v", err)
	}
	if !blocked {
		t.Error("a 302 from /live must count as blocked")
	}

	open := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeBody(w, "<html>live</html>")
	}))
	blocked, err = open.CountryBlocked(t.Context())
	if err != nil {
		t.Fatalf("CountryBlocked: %v", err)
	}
	if blocked {
		t.Error("a 200 from /live must not count as blocked")
	}
}

func TestContextCancellationIsHonoured(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeBody(w, infoLive)
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.RoomStatus(ctx, "7123"); err == nil {
		t.Fatal("cancelled context should surface an error")
	}
}

// TestRestrictedLiveIsCapturable walks the chain the recorder takes for a
// login-restricted room: the webcast room API answers 4003110, the public live
// page still carries a stream url, and that url serves media.
func TestRestrictedLiveIsCapturable(t *testing.T) {
	stream := strings.Repeat("flv-frame-", 2048)
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/webcast/room/check_alive/":
			writeBody(w, `{"data":[{"alive":true}]}`)
		case "/webcast/room/info/":
			writeBody(w, infoRestricted)
		case "/@creator/live":
			// HTML carries no JSON escaping; the scrape unescapes the entity.
			cdn := "http://" + r.Host + "/live_or4.flv?sig=abc&amp;t=1"
			writeBody(w, `<script>var p="`+cdn+`";</script>`)
		case "/live_or4.flv":
			writeBody(w, stream)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))

	ctx := t.Context()
	status, err := c.RoomStatus(ctx, "7123")
	if err != nil {
		t.Fatalf("RoomStatus: %v", err)
	}
	if !status.Live {
		t.Fatal("a restricted room must report alive so the caller can try the fallback")
	}

	urls, err := c.LiveURLs(ctx, "7123", "creator")
	if err != nil {
		t.Fatalf("LiveURLs: %v", err)
	}
	if len(urls) != 1 || !strings.HasSuffix(urls[0], "/live_or4.flv?sig=abc&t=1") {
		t.Fatalf("scraped urls = %v", urls)
	}

	body, err := c.OpenStream(ctx, urls[0])
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	defer body.Close()
	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if string(got) != stream {
		t.Fatalf("captured %d bytes, want %d", len(got), len(stream))
	}
}

// TestManagedCookiesNeverSent pins the rule: hints TikTok computes per session
// are not forwarded, whatever cookies.json or the jar says, because a stale
// tt-target-idc makes the room API answer 4003110 for serveable rooms.
func TestManagedCookiesNeverSent(t *testing.T) {
	var seen string
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Cookie")
		writeBody(w, `{}`)
	}))
	c.hc.Jar.SetCookies(mustURL(t, c.base+"/"), []*http.Cookie{
		{Name: "sessionid_ss", Value: "keep-me", Path: "/"},
		{Name: idcCookie, Value: "useast2a", Path: "/"},
	})

	resp, err := c.get(t.Context(), c.base+"/probe", false)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()

	if !strings.Contains(seen, "sessionid_ss=keep-me") {
		t.Errorf("session cookie was dropped from the request: %q", seen)
	}
	if strings.Contains(seen, idcCookie) {
		t.Errorf("%s leaked onto the wire: %q", idcCookie, seen)
	}
}

// TestManagedCookieNotLoaded covers the config path: a configured hint is
// ignored rather than seeded into the jar.
func TestManagedCookieNotLoaded(t *testing.T) {
	c := New("", map[string]string{idcCookie: "useast2a", "sessionid_ss": "abc"})
	for _, cookie := range c.hc.Jar.Cookies(mustURL(t, "https://webcast.tiktok.com/")) {
		if cookie.Name == idcCookie {
			t.Fatalf("%s was seeded into the jar", idcCookie)
		}
	}
	if got := len(c.hc.Jar.Cookies(mustURL(t, "https://webcast.tiktok.com/"))); got != 1 {
		t.Fatalf("jar holds %d cookies, want only the session cookie", got)
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %s: %v", raw, err)
	}
	return parsed
}
