package tiktok

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
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

func TestRoomAlive(t *testing.T) {
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
			got, err := c.RoomAlive(t.Context(), "7123")
			if err != nil {
				t.Fatalf("RoomAlive: %v", err)
			}
			if got != tc.want {
				t.Errorf("RoomAlive = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRoomAliveEmptyRoomID(t *testing.T) {
	c := New("", nil)
	if _, err := c.RoomAlive(t.Context(), ""); !errors.Is(err, ErrNotLive) {
		t.Fatalf("err = %v, want ErrNotLive", err)
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

func TestLiveURLsRestrictedScrapeFallbacks(t *testing.T) {
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
				if r.URL.Path == "/webcast/room/info/" {
					writeBody(w, infoRestricted)
					return
				}
				writeBody(w, tc.page)
			}))
			if _, err := c.LiveURLs(t.Context(), "7123", tc.user); !errors.Is(err, ErrPrivateRoom) {
				t.Fatalf("err = %v, want ErrPrivateRoom", err)
			}
		})
	}
}

func TestLiveURLsRestrictedScrapeHLS(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/webcast/room/info/" {
			writeBody(w, infoRestricted)
			return
		}
		writeBody(w, `<script>src="https://cdn.example/live.m3u8?sign=1"</script>`)
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

func signedMux(roomID string) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/api-live/room", func(w http.ResponseWriter, r *http.Request) {
		writeBody(w, `{"data":{"user":{"roomId":"`+roomID+`"}}}`)
	})
	return mux
}

func TestRoomIDForUserSignedURL(t *testing.T) {
	mux := signedMux("701")
	mux.HandleFunc("/tiktok/room/api/sign", func(w http.ResponseWriter, r *http.Request) {
		writeBody(w, `{"signed_url":"http://`+r.Host+`/api-live/room?sig=1","signed_path":"/api-live/room"}`)
	})
	c := newTestClient(t, mux)
	got, err := c.RoomIDForUser(t.Context(), "alice")
	if err != nil {
		t.Fatalf("RoomIDForUser: %v", err)
	}
	if got != "701" {
		t.Fatalf("roomID = %q, want 701", got)
	}
}

func TestRoomIDForUserSignedPath(t *testing.T) {
	mux := signedMux("702")
	mux.HandleFunc("/tiktok/room/api/sign", func(w http.ResponseWriter, r *http.Request) {
		writeBody(w, `{"signed_path":"/api-live/room"}`)
	})
	c := newTestClient(t, mux)
	got, err := c.RoomIDForUser(t.Context(), "alice")
	if err != nil {
		t.Fatalf("RoomIDForUser: %v", err)
	}
	if got != "702" {
		t.Fatalf("roomID = %q, want 702", got)
	}
}

func TestRoomIDForUserEulerFallback(t *testing.T) {
	var apiKeySent bool
	mux := http.NewServeMux()
	mux.HandleFunc("/tiktok/room/api/sign", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	mux.HandleFunc("/webcast/room_info", func(w http.ResponseWriter, r *http.Request) {
		apiKeySent = len(r.Header.Values("X-Api-Key")) > 0
		if r.URL.Query().Get("uniqueId") != "alice" {
			t.Errorf("uniqueId = %q, want alice", r.URL.Query().Get("uniqueId"))
		}
		writeBody(w, `{"data":{"room_info":{"id":"703"}}}`)
	})
	c := newTestClient(t, mux)
	got, err := c.RoomIDForUser(t.Context(), "alice")
	if err != nil {
		t.Fatalf("RoomIDForUser: %v", err)
	}
	if got != "703" {
		t.Fatalf("roomID = %q, want 703", got)
	}
	if !apiKeySent {
		t.Error("euler fallback must send the x-api-key header")
	}
}

func TestRoomIDForUserErrors(t *testing.T) {
	cases := []struct {
		name  string
		sign  string
		body  string
		euler string
		want  error
	}{
		{"waf page", `{"signed_path":"/api-live/room"}`, "Please wait", "", ErrWAF},
		{"empty body", `{"signed_path":"/api-live/room"}`, "", "", ErrWAF},
		{"no room id, euler empty", `{"signed_path":"/api-live/room"}`, `{"data":{"user":{"roomId":""}}}`, `{"data":{}}`, ErrRoomID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/tiktok/room/api/sign", func(w http.ResponseWriter, r *http.Request) {
				writeBody(w, tc.sign)
			})
			mux.HandleFunc("/api-live/room", func(w http.ResponseWriter, r *http.Request) {
				writeBody(w, tc.body)
			})
			mux.HandleFunc("/webcast/room_info", func(w http.ResponseWriter, r *http.Request) {
				writeBody(w, tc.euler)
			})
			c := newTestClient(t, mux)
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
	mux := signedMux("704")
	mux.HandleFunc("/tiktok/room/api/sign", func(w http.ResponseWriter, r *http.Request) {
		writeBody(w, `{"signed_path":"/api-live/room"}`)
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
			case "/tiktok/room/api/sign":
				body = `{"signed_path":"/api-live/room"}`
			case "/api-live/room":
				body = `{"data":{"user":{"roomId":"704"}}}`
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

func TestSecUID(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"present", `{"user":{"secUid":"MS4wLjABAAAA==","userId":"1"}`, "MS4wLjABAAAA=="},
		{"absent", `<html>login</html>`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/foryou" {
					t.Errorf("unexpected request %s", r.URL.Path)
				}
				writeBody(w, tc.body)
			}))
			got, err := c.SecUID(t.Context())
			if err != nil {
				t.Fatalf("SecUID: %v", err)
			}
			if got != tc.want {
				t.Fatalf("secUid = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFollowers(t *testing.T) {
	var queries []string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/user/list/", func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		if len(queries) == 1 {
			http.SetCookie(w, &http.Cookie{Name: "msToken", Value: "tok123", Path: "/"})
			writeBody(w, `{"userList":[],"hasMore":false,"minCursor":0}`)
			return
		}
		writeBody(w, `{"userList":[{"user":{"uniqueId":"alice"}},{"user":{"uniqueId":""}},{"user":{"uniqueId":"bob"}}],"hasMore":false,"minCursor":7}`)
	})
	c := newTestClient(t, mux)

	got, err := c.Followers(t.Context(), "SECUID123")
	if err != nil {
		t.Fatalf("Followers: %v", err)
	}
	if len(got) != 2 || got[0] != "alice" || got[1] != "bob" {
		t.Fatalf("followers = %v, want [alice bob]", got)
	}
	if len(queries) != 2 {
		t.Fatalf("made %d list requests, want 2 (prime + one page)", len(queries))
	}
	second := queries[1]
	for _, want := range []string{"msToken=tok123", "secUid=SECUID123", "count=5", "maxCursor=0", "minCursor=0"} {
		if !strings.Contains(second, want) {
			t.Errorf("followers query missing %q: %s", want, second)
		}
	}
}

func TestFollowersStopsWhenCursorRepeats(t *testing.T) {
	var calls int
	mux := http.NewServeMux()
	mux.HandleFunc("/api/user/list/", func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch calls {
		case 1:
			http.SetCookie(w, &http.Cookie{Name: "msToken", Value: "tok", Path: "/"})
			writeBody(w, `{"userList":[],"hasMore":false,"minCursor":0}`)
		case 2:
			writeBody(w, `{"userList":[{"user":{"uniqueId":"alice"}}],"hasMore":true,"minCursor":5}`)
		default:
			writeBody(w, `{"userList":[{"user":{"uniqueId":"bob"}}],"hasMore":true,"minCursor":5}`)
		}
	})
	c := newTestClient(t, mux)
	got, err := c.Followers(t.Context(), "SEC")
	if err != nil {
		t.Fatalf("Followers: %v", err)
	}
	if len(got) != 2 || got[0] != "alice" || got[1] != "bob" {
		t.Fatalf("followers = %v, want [alice bob]", got)
	}
	if calls != 3 {
		t.Fatalf("made %d requests, want 3 (prime + two pages before the cursor repeats)", calls)
	}
}

func TestFollowersErrors(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		wantSub string
	}{
		{"non 200", http.StatusForbidden, "{}", "failed to retrieve followers list"},
		{"empty body", http.StatusOK, "", "empty response"},
		{"no users", http.StatusOK, `{"userList":[],"hasMore":false,"minCursor":0}`, "followers"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				writeBody(w, tc.body)
			}))
			_, err := c.Followers(t.Context(), "SEC")
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("err = %v, want one mentioning %q", err, tc.wantSub)
			}
		})
	}
}

func TestContextCancellationIsHonoured(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeBody(w, infoLive)
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.RoomAlive(ctx, "7123"); err == nil {
		t.Fatal("cancelled context should surface an error")
	}
}

// TestRestrictedLiveIsCapturable walks the exact chain the recorder takes for a
// login-restricted room: TikTok answers 4003110 for the room API, the live page
// still leaks a CDN url, and that url serves media. A restricted live is only
// usable if all three steps line up, so assert them together.
func TestRestrictedLiveIsCapturable(t *testing.T) {
	stream := strings.Repeat("flv-frame-", 2048)
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/webcast/room/check_alive/":
			writeBody(w, `{"data":[{"alive":true}]}`)
		case "/webcast/room/info/":
			writeBody(w, infoRestricted)
		case "/@creator/live":
			// Point the scraped url back at this server: a restricted room's page
			// advertises an absolute CDN host, and the test must stay offline.
			cdn := "http://" + r.Host + "/live_or4.flv?sig=abc&amp;t=1"
			writeBody(w, `<script>var p="`+cdn+`";</script>`)
		case "/live_or4.flv":
			writeBody(w, stream)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))

	ctx := t.Context()
	alive, err := c.RoomAlive(ctx, "7123")
	if err != nil {
		t.Fatalf("RoomAlive: %v", err)
	}
	if !alive {
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
