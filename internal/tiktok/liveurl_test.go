package tiktok

import "testing"

func TestCandidatesQualityOrder(t *testing.T) {
	info := &RoomInfo{Data: RoomData{StreamURL: StreamURL{
		LiveCoreSDKData: LiveCoreSDKData{PullData: PullData{
			StreamData: `{"data":{"sd":{"main":{"flv":"flv-sd"}},"origin":{"main":{"flv":"flv-origin","hls":"hls-origin"}},"hd":{"main":{"flv":"flv-hd"}},"weird":{"main":{"flv":"flv-weird"}}}}`,
			Options: Options{Qualities: []Quality{
				{SDKKey: "origin", Level: 10},
				{SDKKey: "hd", Level: 3},
				{SDKKey: "sd", Level: 2},
			}},
		}},
		FLVPullURL:  map[string]string{"FULL_HD1": "flv-full", "SD1": "flv-sd1"},
		HLSPullURL:  "hls-legacy",
		RTMPPullURL: "rtmp-legacy",
	}}}

	want := []string{
		"flv-origin", "hls-origin", // level 10
		"flv-hd",     // level 3
		"flv-sd",     // level 2
		"flv-weird",  // unranked, sorts last
		"flv-full",   // legacy FULL_HD1
		"flv-sd1",    // legacy SD1
		"hls-legacy", // legacy hls
		"rtmp-legacy",
	}

	got := info.Candidates()
	if len(got) != len(want) {
		t.Fatalf("got %d candidates %v, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("candidate %d = %q, want %q (full order %v)", i, got[i], want[i], got)
		}
	}
}

func TestCandidatesLegacyOnlyAndDedupe(t *testing.T) {
	info := &RoomInfo{Data: RoomData{StreamURL: StreamURL{
		FLVPullURL: map[string]string{"HD1": "same-flv", "SD2": "same-flv"},
		HLSPullURL: "same-flv",
	}}}
	got := info.Candidates()
	if len(got) != 1 || got[0] != "same-flv" {
		t.Fatalf("dedupe failed: %v", got)
	}
}

func TestCandidatesEmpty(t *testing.T) {
	if got := (&RoomInfo{}).Candidates(); len(got) != 0 {
		t.Fatalf("want no candidates, got %v", got)
	}
}

func TestEndedAndRestricted(t *testing.T) {
	if !(&RoomInfo{Data: RoomData{Status: "4"}}).ended() {
		t.Error(`status "4" should count as ended`)
	}
	if (&RoomInfo{Data: RoomData{Status: "2"}}).ended() {
		t.Error(`status "2" should count as live`)
	}
	if (&RoomInfo{}).ended() {
		t.Error("missing status should not count as ended")
	}
	if !(&RoomInfo{StatusCode: 4003110}).restricted() {
		t.Error("4003110 should count as restricted")
	}
}
