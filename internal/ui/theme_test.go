package ui

import (
	"strings"
	"testing"
	"time"
)

func TestTagPlainWhenQuiet(t *testing.T) {
	SetQuiet(true)
	defer SetQuiet(false)

	tests := []struct{ kind, text, want string }{
		{"INFO", "hello", "[INFO]  hello"},
		{"START", "alice=20261028_172928", "[START] alice=20261028_172928"},
		{"STOP", "stream ended", "[STOP]  stream ended"},
		{"DONE", "alice=20261028_172928.mp4", "[DONE]  alice=20261028_172928.mp4"},
		{"WARN", "careful", "[WARN]  careful"},
		{"ERROR", "failed", "[ERROR] failed"},
		{"NOTE", "unknown kind", "[NOTE]  unknown kind"},
		{"VERYLONGKIND", "wide", "[VERYLONGKIND] wide"},
	}
	for _, tc := range tests {
		got := Tag(tc.kind, tc.text)
		if got != tc.want {
			t.Errorf("Tag(%q, %q) = %q, want %q", tc.kind, tc.text, got, tc.want)
		}
		if strings.Contains(got, "\x1b") {
			t.Errorf("Tag(%q, %q) carries an escape while quiet: %q", tc.kind, tc.text, got)
		}
	}
}

func TestLabelPadsToColumn(t *testing.T) {
	SetQuiet(true)
	defer SetQuiet(false)

	if got, want := Label("target"), "target  "; got != want {
		t.Errorf("Label(target) = %q, want %q", got, want)
	}
	if got, want := Label("cookies"), "cookies "; got != want {
		t.Errorf("Label(cookies) = %q, want %q", got, want)
	}
	if got, want := Label("ffmpeg-path"), "ffmpeg-path"; got != want {
		t.Errorf("Label(ffmpeg-path) = %q, want %q", got, want)
	}
}

func TestEnabledOffWhileQuiet(t *testing.T) {
	SetQuiet(true)
	defer SetQuiet(false)
	if Enabled() {
		t.Fatal("Enabled() reported true while quiet")
	}
}

func TestDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{41 * time.Second, "41s"},
		{59 * time.Second, "59s"},
		{4 * time.Minute, "4min"},
		{4*time.Minute + 12*time.Second, "4min 12s"},
		{time.Hour + 2*time.Minute + 5*time.Second, "1h 02min"},
	}
	for _, tc := range tests {
		if got := Duration(tc.d); got != tc.want {
			t.Errorf("Duration(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestBytes(t *testing.T) {
	tests := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1500, "1.5 kB"},
		{912_000_000, "869.8 MB"},
		{182_400_000_000, "169.9 GB"},
		{909_320_069_120, "846.9 GB"},
	}
	for _, tc := range tests {
		if got := Bytes(tc.n); got != tc.want {
			t.Errorf("Bytes(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

func TestClock(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{-5 * time.Second, "00:00"},
		{0, "00:00"},
		{41 * time.Second, "00:41"},
		{13 * time.Minute, "13:00"},
		{59*time.Minute + 59*time.Second, "59:59"},
		{time.Hour + 2*time.Minute + 5*time.Second, "1:02:05"},
		{10 * time.Hour, "10:00:00"},
	}
	for _, tc := range tests {
		if got := Clock(tc.d); got != tc.want {
			t.Errorf("Clock(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}
