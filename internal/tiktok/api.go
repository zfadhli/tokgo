package tiktok

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"tokgo/internal/logx"
)

var (
	flvPattern    = regexp.MustCompile(`https?://[^\s"'<>]+\.flv[^\s"'<>]*`)
	hlsPattern    = regexp.MustCompile(`https?://[^\s"'<>]+\.m3u8[^\s"'<>]*`)
	shortlinkUser = regexp.MustCompile(`com/@(.*?)/live`)
	liveURLUser   = regexp.MustCompile(`^https?://(?:www\.)?tiktok\.com/@([^/]+)/live`)
)

const (
	followMarker  = "Follow the creator to watch their LIVE"
	privateMarker = "This account is private"
	roomFresh     = 24 * time.Hour
)

// RoomStatus reports whether roomID is streaming right now, with the title and
// (when it can be trusted) how long the room has been live. A restricted room
// counts as alive so the caller surfaces the login/region error instead of a
// misleading "not live".
func (c *Client) RoomStatus(ctx context.Context, roomID string) (RoomStatus, error) {
	if roomID == "" {
		return RoomStatus{}, ErrNotLive
	}
	resp, err := c.get(ctx, c.webcast+"/webcast/room/check_alive/?aid=1988&region=CH&room_ids="+url.QueryEscape(roomID)+"&user_is_login=true", false)
	if err != nil {
		return RoomStatus{}, err
	}
	var alive struct {
		Data []struct {
			Alive bool `json:"alive"`
		} `json:"data"`
	}
	if err := decodeInto(resp, &alive); err != nil {
		return RoomStatus{}, err
	}
	if len(alive.Data) == 0 || !alive.Data[0].Alive {
		return RoomStatus{}, nil
	}

	info, err := c.roomInfo(ctx, roomID)
	if err != nil {
		return RoomStatus{}, err
	}
	status := RoomStatus{Title: info.Data.Title}
	switch {
	case info.restricted():
		status.Live = true
	case info.StatusCode != 0:
		return RoomStatus{}, nil
	case info.ended():
		return RoomStatus{}, nil
	default:
		status.Live = info.hasStream()
	}

	// TikTok reuses a room object across broadcasts and live payloads carry
	// start_time: 0, so an old create_time is the previous stream's start and
	// would print nonsense uptime. Omit it rather than guess.
	if status.Live {
		t, _ := strconv.ParseInt(string(info.Data.CreateTime), 10, 64)
		if t > 0 {
			started := time.Unix(t, 0)
			if time.Since(started) <= roomFresh {
				status.StartedAt = started
			}
		}
	}
	return status, nil
}

// LiveURLs returns every CDN url for the room, best quality first.
func (c *Client) LiveURLs(ctx context.Context, roomID, user string) ([]string, error) {
	info, err := c.roomInfo(ctx, roomID)
	if err != nil {
		return nil, err
	}
	if strings.Contains(info.Raw, privateMarker) {
		return nil, ErrPrivateAccount
	}
	if info.restricted() {
		return c.restrictedURLs(ctx, user)
	}
	if strings.Contains(info.Raw, followMarker) {
		return nil, ErrPrivateFollow
	}
	if info.ended() {
		return nil, ErrNotLive
	}
	return info.Candidates(), nil
}

// RoomIDForUser resolves a username to a room id through TikTok's own live
// endpoint. It needs no signature, no cookie and no third party: the plain
// endpoint answers a Chrome-shaped request directly.
func (c *Client) RoomIDForUser(ctx context.Context, user string) (string, error) {
	resp, err := c.get(ctx, c.base+"/api-live/user/room/?aid=1988&sourceType=54&uniqueId="+url.QueryEscape(user), false)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode == http.StatusForbidden {
		return "", ErrWAF
	}
	text := strings.TrimSpace(string(body))
	if text == "" || strings.Contains(text, "Please wait") || strings.Contains(text, "SlardarWAF") {
		return "", ErrWAF
	}
	var payload struct {
		StatusCode int    `json:"statusCode"`
		Msg        string `json:"msg"`
		Data       struct {
			User struct {
				RoomID FlexString `json:"roomId"`
			} `json:"user"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		logx.Warnf("unparsable room id response: %v", err)
		return "", ErrRoomID
	}
	if payload.Data.User.RoomID != "" {
		return string(payload.Data.User.RoomID), nil
	}
	if payload.Msg != "" {
		return "", fmt.Errorf("%w (status %d: %s)", ErrRoomID, payload.StatusCode, payload.Msg)
	}
	return "", fmt.Errorf("%w (status %d)", ErrRoomID, payload.StatusCode)
}

// UserForRoomID resolves a room id to the broadcaster's display id.
func (c *Client) UserForRoomID(ctx context.Context, roomID string) (string, error) {
	info, err := c.roomInfo(ctx, roomID)
	if err != nil {
		return "", err
	}
	switch {
	case strings.Contains(info.Raw, followMarker):
		return "", ErrPrivateFollow
	case strings.Contains(info.Raw, privateMarker):
		return "", ErrPrivateAccount
	case info.Data.Owner.DisplayID == "":
		return "", ErrUserUnknown
	}
	return info.Data.Owner.DisplayID, nil
}

// UserAndRoomFromURL resolves a pasted live url to the username and room id.
func (c *Client) UserAndRoomFromURL(ctx context.Context, liveURL string) (string, string, error) {
	resp, err := c.get(ctx, liveURL, true)
	if err != nil {
		return "", "", err
	}
	body, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		return "", "", readErr
	}
	if resp.StatusCode == http.StatusFound {
		return "", "", ErrCountryBlocked
	}

	user := ""
	if resp.StatusCode == http.StatusMovedPermanently {
		match := shortlinkUser.FindStringSubmatch(string(body))
		if match == nil {
			return "", "", ErrInvalidURL
		}
		user = match[1]
	}
	if match := liveURLUser.FindStringSubmatch(liveURL); match != nil {
		user = match[1]
	}
	if user == "" {
		return "", "", ErrInvalidURL
	}

	roomID, err := c.RoomIDForUser(ctx, user)
	if err != nil {
		return "", "", err
	}
	return user, roomID, nil
}

// CountryBlocked reports whether TikTok answers the live page with a redirect.
func (c *Client) CountryBlocked(ctx context.Context) (bool, error) {
	resp, err := c.get(ctx, c.base+"/live", true)
	if err != nil {
		return false, err
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusFound, nil
}

// OpenStream hands back the response body of a stream url, unbuffered, so the
// recorder can copy it straight to disk.
func (c *Client) OpenStream(ctx context.Context, streamURL string) (io.ReadCloser, error) {
	resp, err := c.get(ctx, streamURL, false)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		resp.Body.Close()
		return nil, fmt.Errorf("stream request failed: %s", resp.Status)
	}
	return resp.Body, nil
}

func (c *Client) roomInfo(ctx context.Context, roomID string) (*RoomInfo, error) {
	resp, err := c.get(ctx, c.webcast+"/webcast/room/info/?aid=1988&room_id="+url.QueryEscape(roomID), false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var info RoomInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, err
	}
	info.Raw = string(body)
	return &info, nil
}

// scrapeLiveURL digs a stream url out of the room page TikTok serves when the
// API answers 4003110 for a login-restricted room.
func (c *Client) scrapeLiveURL(ctx context.Context, user string) string {
	resp, err := c.get(ctx, c.base+"/@"+user+"/live", false)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ""
	}
	page := string(body)

	picked := ""
	flvURLs := flvPattern.FindAllString(page, -1)
	for _, candidate := range flvURLs {
		if strings.Contains(candidate, "_or4") || strings.Contains(candidate, "_sd") {
			picked = candidate
			break
		}
	}
	if picked == "" && len(flvURLs) > 0 {
		picked = flvURLs[0]
	}
	if picked == "" {
		picked = hlsPattern.FindString(page)
	}
	return strings.TrimRight(html.UnescapeString(picked), `\`)
}

func decodeInto(resp *http.Response, v any) error {
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(v)
}

// restrictedURLs handles a room the webcast API refuses with 4003110. The public
// live page is the only remaining source: TikTok usually answers it with a
// SlardarWAF stub, so a login-gated room normally needs cookies.
func (c *Client) restrictedURLs(ctx context.Context, user string) ([]string, error) {
	if user != "" {
		if streamURL := c.scrapeLiveURL(ctx, user); streamURL != "" {
			logx.Infof("room api refused the room, resolved a stream url from the live page")
			return []string{streamURL}, nil
		}
	}
	return nil, ErrPrivateRoom
}
