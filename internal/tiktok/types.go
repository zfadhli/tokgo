package tiktok

import (
	"strings"
)

// FlexString accepts a JSON number, string, or null and keeps its literal text.
// TikTok returns room status as a number in some regions and a string in others.
type FlexString string

func (f *FlexString) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" || s == "" {
		*f = ""
		return nil
	}
	*f = FlexString(strings.Trim(s, `"`))
	return nil
}

// RoomInfo is the subset of /webcast/room/info the recorder actually uses.
type RoomInfo struct {
	StatusCode int      `json:"status_code"`
	Data       RoomData `json:"data"`
	// Raw keeps the body so callers can spot TikTok's prose-only error markers.
	Raw string `json:"-"`
}

// RoomData carries liveness plus the stream endpoints.
type RoomData struct {
	Status    FlexString `json:"status"`
	Owner     Owner      `json:"owner"`
	StreamURL StreamURL  `json:"stream_url"`
}

// Owner identifies who is broadcasting.
type Owner struct {
	DisplayID string     `json:"display_id"`
	UniqueID  string     `json:"unique_id"`
	RoomID    FlexString `json:"room_id"`
}

// StreamURL is the legacy pull-url envelope.
type StreamURL struct {
	LiveCoreSDKData LiveCoreSDKData   `json:"live_core_sdk_data"`
	FLVPullURL      map[string]string `json:"flv_pull_url"`
	HLSPullURL      string            `json:"hls_pull_url"`
	RTMPPullURL     string            `json:"rtmp_pull_url"`
}

// LiveCoreSDKData wraps the per-quality stream manifest.
type LiveCoreSDKData struct {
	PullData PullData `json:"pull_data"`
}

// PullData holds the quality manifest as a JSON-encoded string plus its levels.
type PullData struct {
	StreamData string  `json:"stream_data"`
	Options    Options `json:"options"`
}

// Options lists every selectable quality with its numeric level.
type Options struct {
	Qualities []Quality `json:"qualities"`
}

// Quality maps an SDK key ("origin", "hd", ...) to a ranking level.
type Quality struct {
	SDKKey string `json:"sdk_key"`
	Level  int    `json:"level"`
}

// sdkManifest is the decoded form of PullData.StreamData.
type sdkManifest struct {
	Data map[string]struct {
		Main struct {
			FLV  string `json:"flv"`
			HLS  string `json:"hls"`
			M3U8 string `json:"m3u8"`
		} `json:"main"`
	} `json:"data"`
}

// ended reports TikTok's terminal room state (4 = ended).
func (r *RoomInfo) ended() bool {
	return r.Data.Status != "" && string(r.Data.Status) != "2"
}

// restricted reports the WAF / region access-restriction status code.
func (r *RoomInfo) restricted() bool { return r.StatusCode == 4003110 }

// hasStream reports whether any pull endpoint is present at all.
func (r *RoomInfo) hasStream() bool { return len(r.Candidates()) > 0 }
