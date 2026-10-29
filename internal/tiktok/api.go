package tiktok

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"tokgo/internal/logx"
)

var (
	flvPattern    = regexp.MustCompile(`https?://[^\s"'<>]+\.flv[^\s"'<>]*`)
	hlsPattern    = regexp.MustCompile(`https?://[^\s"'<>]+\.m3u8[^\s"'<>]*`)
	secUIDPattern = regexp.MustCompile(`"secUid":"(.*?)",`)
	shortlinkUser = regexp.MustCompile(`com/@(.*?)/live`)
	liveURLUser   = regexp.MustCompile(`^https?://(?:www\.)?tiktok\.com/@([^/]+)/live`)
)

const (
	followMarker  = "Follow the creator to watch their LIVE"
	privateMarker = "This account is private"
)

// RoomAlive reports whether roomID is streaming right now. A restricted room
// counts as alive so the caller surfaces the login/region error instead of a
// misleading "not live".
func (c *Client) RoomAlive(ctx context.Context, roomID string) (bool, error) {
	if roomID == "" {
		return false, ErrNotLive
	}
	resp, err := c.get(ctx, c.webcast+"/webcast/room/check_alive/?aid=1988&region=CH&room_ids="+url.QueryEscape(roomID)+"&user_is_login=true", false)
	if err != nil {
		return false, err
	}
	var alive struct {
		Data []struct {
			Alive bool `json:"alive"`
		} `json:"data"`
	}
	if err := decodeInto(resp, &alive); err != nil {
		return false, err
	}
	if len(alive.Data) == 0 || !alive.Data[0].Alive {
		return false, nil
	}

	info, err := c.roomInfo(ctx, roomID)
	if err != nil {
		return false, err
	}
	switch {
	case info.restricted():
		return true, nil
	case info.StatusCode != 0:
		return false, nil
	case info.ended():
		return false, nil
	}
	return info.hasStream(), nil
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

// RoomIDForUser resolves a username to a room id, preferring the tikrec signed
// lookup and falling back to euler when that service is down.
func (c *Client) RoomIDForUser(ctx context.Context, user string) (string, error) {
	signed, signErr := c.signedRoomURL(ctx, user)
	if signed == "" {
		id, err := c.eulerRoomID(ctx, user)
		if err != nil {
			if signErr != nil {
				return "", fmt.Errorf("%w (sign service unavailable: %v)", ErrRoomID, signErr)
			}
			return "", err
		}
		return id, nil
	}

	resp, err := c.get(ctx, signed, false)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(string(body))
	if text == "" || strings.Contains(text, "Please wait") {
		return "", ErrWAF
	}
	var payload struct {
		Data struct {
			User struct {
				RoomID FlexString `json:"roomId"`
			} `json:"user"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		logx.Warnf("unparsable room id response: %v", err)
		return "", ErrRoomID
	}
	if payload.Data.User.RoomID == "" {
		return "", ErrRoomID
	}
	return string(payload.Data.User.RoomID), nil
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

// SecUID returns the session's secUid, or "" when the page carries none.
func (c *Client) SecUID(ctx context.Context) (string, error) {
	resp, err := c.get(ctx, c.base+"/foryou", false)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	match := secUIDPattern.FindStringSubmatch(string(body))
	if match == nil {
		return "", nil
	}
	return match[1], nil
}

// followersQuery is the reference tool's user list query. Placeholders stand in
// for the values that change per request; the rest is kept byte-identical.
const followersQuery = "WebIdLastTime=1747672102&aid=1988&app_language=it-IT&app_name=tiktok_web&browser_language=it-IT&browser_name=Mozilla&browser_online=true&browser_platform=Linux%20x86_64&browser_version=5.0%20%28X11%3B%20Linux%20x86_64%29%20AppleWebKit%2F537.36%20%28KHTML%2C%20like%20Gecko%29%20Chrome%2F140.0.0.0%20Safari%2F537.36&channel=tiktok_web&cookie_enabled=true&count=5&data_collection_enabled=true&device_id=7506194516308166166&device_platform=web_pc&focus_state=true&from_page=user&history_len=3&is_fullscreen=false&is_page_visible=true&maxCursor={cursor}&minCursor={cursor}&odinId=7246312836442604570&os=linux&priority_region=IT&referer=&region=IT&scene=21&screen_height=1080&screen_width=1920&tz_name=Europe%2FRome&user_is_login=true&secUid={secUid}&verifyFp=verify_mh4yf0uq_rdjp1Xwt_OoTk_4Jrf_AS8H_sp31opbnJFre&webcast_language=it-IT&msToken={msToken}&X-Bogus=&X-Gnarly="

// Followers lists every follower's uniqueId. The first request only exists to
// collect the msToken cookie the paged endpoint requires.
func (c *Client) Followers(ctx context.Context, secUID string) ([]string, error) {
	prime := strings.NewReplacer("{cursor}", "0", "{secUid}", secUID, "{msToken}", "").Replace(followersQuery)
	if resp, err := c.get(ctx, c.base+"/api/user/list/?"+prime, false); err == nil {
		resp.Body.Close()
	}
	token := c.msToken()

	cursor := "0"
	var users []string
	for {
		page := strings.NewReplacer("{cursor}", cursor, "{secUid}", secUID, "{msToken}", token).Replace(followersQuery)

		resp, err := c.get(ctx, c.base+"/api/user/list/?"+page, false)
		if err != nil {
			return nil, err
		}
		status := resp.StatusCode
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, errors.New("failed to retrieve followers list")
		}
		if len(strings.TrimSpace(string(body))) == 0 {
			return nil, errors.New("empty response from the TikTok followers API")
		}
		var payload struct {
			UserList []struct {
				User struct {
					UniqueID string `json:"uniqueId"`
				} `json:"user"`
			} `json:"userList"`
			HasMore   bool       `json:"hasMore"`
			MinCursor FlexString `json:"minCursor"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, err
		}
		for _, entry := range payload.UserList {
			if entry.User.UniqueID != "" {
				users = append(users, entry.User.UniqueID)
			}
		}

		next := string(payload.MinCursor)
		if !payload.HasMore || next == "" || next == cursor {
			break
		}
		cursor = next
	}
	if len(users) == 0 {
		return nil, errors.New("no followers found")
	}
	return users, nil
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

// roomInfo fetches the webcast room payload. A restricted answer triggers one
// retry without the tt-target-idc cookie: that cookie is a routing hint, and a
// value that does not match the session's region makes TikTok answer 4003110
// for rooms it would otherwise serve. The reference tool ships a hardcoded
// idc value in its default cookies.json, so this trap is common.
func (c *Client) roomInfo(ctx context.Context, roomID string) (*RoomInfo, error) {
	info, err := c.fetchRoomInfo(ctx, roomID)
	if err != nil {
		return nil, err
	}
	if info.restricted() && c.dropCookie(idcCookie) {
		logx.Infof("room api answered 4003110 with the configured %s cookie present, retrying without it", idcCookie)
		if retried, retryErr := c.fetchRoomInfo(ctx, roomID); retryErr == nil {
			if retried.restricted() {
				logx.Warnf("still restricted after dropping %s: the room itself needs a login", idcCookie)
			}
			return retried, nil
		}
	}
	return info, nil
}

func (c *Client) fetchRoomInfo(ctx context.Context, roomID string) (*RoomInfo, error) {
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

func (c *Client) signedRoomURL(ctx context.Context, user string) (string, error) {
	resp, err := c.get(ctx, c.tikrec+"/tiktok/room/api/sign?unique_id="+url.QueryEscape(user), false)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var signed struct {
		SignedURL  string `json:"signed_url"`
		SignedPath string `json:"signed_path"`
	}
	if err := json.Unmarshal(body, &signed); err != nil {
		return "", fmt.Errorf("unparsable sign response: %w", err)
	}
	if signed.SignedURL != "" {
		return signed.SignedURL, nil
	}
	if signed.SignedPath != "" {
		return c.base + signed.SignedPath, nil
	}
	return "", errors.New("no signed url in the response")
}

func (c *Client) eulerRoomID(ctx context.Context, user string) (string, error) {
	req, err := c.request(ctx, c.euler+"/webcast/room_info?uniqueId="+url.QueryEscape(user)+"&giftInfo=false")
	if err != nil {
		return "", ErrRoomID
	}
	req.Header.Set("x-api-key", "")
	resp, err := c.do(req, false)
	if err != nil {
		return "", ErrRoomID
	}
	defer resp.Body.Close()
	var payload struct {
		Data struct {
			RoomInfo struct {
				ID FlexString `json:"id"`
			} `json:"room_info"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", ErrRoomID
	}
	if payload.Data.RoomInfo.ID == "" {
		return "", ErrRoomID
	}
	return string(payload.Data.RoomInfo.ID), nil
}

func (c *Client) msToken() string {
	site, err := url.Parse(c.base + "/")
	if err != nil {
		return ""
	}
	for _, cookie := range c.hc.Jar.Cookies(site) {
		if cookie.Name == "msToken" {
			return cookie.Value
		}
	}
	return ""
}

func decodeInto(resp *http.Response, v any) error {
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(v)
}

// restrictedURLs handles a room the webcast API refuses with 4003110. The web
// UI endpoint reached through the signing service carries the same stream
// manifest and is not subject to that refusal, so it is tried first. The public
// live page is the second attempt: TikTok increasingly answers it with a
// SlardarWAF stub, so it can no longer be relied on alone.
func (c *Client) restrictedURLs(ctx context.Context, user string) ([]string, error) {
	if user != "" {
		if urls, err := c.signedLiveURLs(ctx, user); err == nil && len(urls) > 0 {
			logx.Infof("room api refused the room, resolved %d stream urls from the signed web endpoint", len(urls))
			return urls, nil
		}
		if streamURL := c.scrapeLiveURL(ctx, user); streamURL != "" {
			logx.Infof("room api refused the room, resolved a stream url from the live page")
			return []string{streamURL}, nil
		}
	}
	return nil, ErrPrivateRoom
}

// signedLiveURLs reads the live room payload of the signed web UI endpoint and
// reuses the same quality ordering as the webcast response.
func (c *Client) signedLiveURLs(ctx context.Context, user string) ([]string, error) {
	signed, err := c.signedRoomURL(ctx, user)
	if err != nil {
		return nil, err
	}
	resp, err := c.get(ctx, signed, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("signed live room: %s", resp.Status)
	}

	var payload struct {
		Data struct {
			LiveRoom *struct {
				Status     FlexString `json:"status"`
				StreamData struct {
					PullData struct {
						StreamData string  `json:"stream_data"`
						Options    Options `json:"options"`
					} `json:"pull_data"`
				} `json:"streamData"`
			} `json:"liveRoom"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	if payload.Data.LiveRoom == nil {
		return nil, ErrNotLive
	}
	info := &RoomInfo{Data: RoomData{
		Status: payload.Data.LiveRoom.Status,
		StreamURL: StreamURL{LiveCoreSDKData: LiveCoreSDKData{PullData: PullData{
			StreamData: payload.Data.LiveRoom.StreamData.PullData.StreamData,
			Options:    payload.Data.LiveRoom.StreamData.PullData.Options,
		}}},
	}}
	return info.Candidates(), nil
}
