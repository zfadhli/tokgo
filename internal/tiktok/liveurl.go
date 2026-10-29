package tiktok

import (
	"encoding/json"
	"sort"
)

// legacyFLVKeys are tried in this order when the SDK manifest is unavailable.
var legacyFLVKeys = []string{"FULL_HD1", "HD1", "SD2", "SD1"}

// Candidates returns every usable CDN url, best quality first, deduplicated.
//
// Ordering rules: SDK qualities sorted by level descending (unknown level
// ranks last), each contributing its flv then hls endpoint; then the legacy
// flv_pull_url tiers, the HLS pull url, and finally RTMP.
func (r *RoomInfo) Candidates() []string {
	out := make([]string, 0, 8)
	add := func(url string) {
		if url == "" {
			return
		}
		for _, seen := range out {
			if seen == url {
				return
			}
		}
		out = append(out, url)
	}

	sdk := r.Data.StreamURL.LiveCoreSDKData.PullData
	if sdk.StreamData != "" {
		var manifest sdkManifest
		if err := json.Unmarshal([]byte(sdk.StreamData), &manifest); err == nil {
			level := make(map[string]int, len(sdk.Options.Qualities))
			for _, q := range sdk.Options.Qualities {
				level[q.SDKKey] = q.Level
			}

			keys := make([]string, 0, len(manifest.Data))
			for key := range manifest.Data {
				keys = append(keys, key)
			}
			sort.SliceStable(keys, func(i, j int) bool {
				return rank(level, keys[i]) > rank(level, keys[j])
			})

			for _, key := range keys {
				main := manifest.Data[key].Main
				add(main.FLV)
				if main.HLS != "" {
					add(main.HLS)
				} else {
					add(main.M3U8)
				}
			}
		}
	}

	stream := r.Data.StreamURL
	for _, key := range legacyFLVKeys {
		add(stream.FLVPullURL[key])
	}
	add(stream.HLSPullURL)
	add(stream.RTMPPullURL)

	return out
}

// rank returns the manifest quality level, or -1 when unranked so that
// unknown keys sort after every known quality.
func rank(levels map[string]int, key string) int {
	if l, ok := levels[key]; ok {
		return l
	}
	return -1
}
