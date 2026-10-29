package cli

import (
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"tokgo/internal/config"
)

func want(cfg config.Config, users []string, updateCheck bool) *Parsed {
	return &Parsed{Config: cfg, Users: users, UpdateCheck: updateCheck}
}

func TestParse(t *testing.T) {
	const liveURL = "https://www.tiktok.com/@x/live"

	tests := []struct {
		name    string
		args    []string
		want    *Parsed
		wantErr string
	}{
		{
			name: "minimal user strips at",
			args: []string{"-user", "@name"},
			want: want(config.Config{User: "@name", AutomaticInterval: 3}, []string{"name"}, true),
		},
		{
			name: "double dash long flag accepted",
			args: []string{"--user", "@name"},
			want: want(config.Config{User: "@name", AutomaticInterval: 3}, []string{"name"}, true),
		},
		{
			name: "single dash long flag with value accepted",
			args: []string{"-user=@name"},
			want: want(config.Config{User: "@name", AutomaticInterval: 3}, []string{"name"}, true),
		},
		{
			name: "shorthand user",
			args: []string{"-u", "@name"},
			want: want(config.Config{User: "@name", AutomaticInterval: 3}, []string{"name"}, true),
		},
		{
			name: "shorthand output",
			args: []string{"-u", "name", "-o", "/tmp/out"},
			want: want(config.Config{User: "name", AutomaticInterval: 3, OutputDir: "/tmp/out"}, []string{"name"}, true),
		},
		{
			name: "multiple users split and cleaned",
			args: []string{"-user", "a,b , @c"},
			want: want(config.Config{User: "a,b , @c", AutomaticInterval: 3}, []string{"a", "b", "c"}, true),
		},
		{
			name:    "multiple users with url rejected",
			args:    []string{"-user", "a,b", "-url", liveURL},
			wantErr: "when passing multiple usernames, do not pass -room_id or -url",
		},
		{
			name:    "multiple users with room id rejected",
			args:    []string{"-user", "a,b", "-room_id", "123"},
			wantErr: "when passing multiple usernames, do not pass -room_id or -url",
		},
		{
			name:    "no target",
			args:    nil,
			wantErr: "missing -url, -user or -room_id",
		},
		{
			name:    "mode flag is gone",
			args:    []string{"-mode", "automatic", "-user", "x"},
			wantErr: "unknown flag: --mode",
		},
		{
			name:    "bitrate flag is gone",
			args:    []string{"-user", "x", "-bitrate", "1M"},
			wantErr: "unknown flag: --bitrate",
		},
		{
			name: "www url accepted",
			args: []string{"-url", liveURL},
			want: want(config.Config{URL: liveURL, AutomaticInterval: 3}, nil, true),
		},
		{
			name: "vm url accepted",
			args: []string{"-url", "https://vm.tiktok.com/abc"},
			want: want(config.Config{URL: "https://vm.tiktok.com/abc", AutomaticInterval: 3}, nil, true),
		},
		{
			name:    "non tiktok url rejected",
			args:    []string{"-url", "https://example.com/live"},
			wantErr: "the provided url does not look like a TikTok live url",
		},
		{
			name:    "user and url rejected",
			args:    []string{"-user", "a", "-url", liveURL},
			wantErr: "pass only one of -user, -room_id, -url",
		},
		{
			name:    "user and room id rejected",
			args:    []string{"-user", "a", "-room_id", "1"},
			wantErr: "pass only one of -user, -room_id, -url",
		},
		{
			name: "room id accepted",
			args: []string{"-room_id", "123"},
			want: want(config.Config{RoomID: "123", AutomaticInterval: 3}, nil, true),
		},
		{
			name:    "automatic interval zero rejected",
			args:    []string{"-user", "x", "-automatic_interval", "0"},
			wantErr: "-automatic_interval must be at least 1 minute",
		},
		{
			name: "duration accepted",
			args: []string{"-user", "x", "-duration", "30"},
			want: want(config.Config{User: "x", AutomaticInterval: 3, Duration: 30}, []string{"x"}, true),
		},
		{
			name:    "negative duration rejected",
			args:    []string{"-user", "x", "-duration", "-1"},
			wantErr: "-duration cannot be negative",
		},
		{
			name: "no update check accepted",
			args: []string{"-user", "x", "-no-update-check"},
			want: want(config.Config{User: "x", AutomaticInterval: 3}, []string{"x"}, false),
		},
		{
			name: "record once",
			args: []string{"-user", "x", "-once", "-automatic_interval", "3"},
			want: want(config.Config{User: "x", Once: true, AutomaticInterval: 3}, []string{"x"}, true),
		},
		{
			name: "passthrough flags",
			args: []string{"-user", "x", "-telegram", "-ffmpeg-path", "/usr/bin/ffmpeg", "-proxy", "http://p:1", "-output", "/tmp/out"},
			want: want(config.Config{
				User: "x", AutomaticInterval: 3, OutputDir: "/tmp/out",
				FFmpegPath: "/usr/bin/ffmpeg", Proxy: "http://p:1", UploadTelegram: true,
			}, []string{"x"}, true),
		},
		{
			name:    "unknown flag",
			args:    []string{"-bogus"},
			wantErr: "unknown flag: --bogus",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse(tc.args)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("Parse(%q) error = nil, want containing %q", tc.args, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Parse(%q) error = %q, want containing %q", tc.args, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%q) error = %v", tc.args, err)
			}
			if !reflect.DeepEqual(got, *tc.want) {
				t.Fatalf("Parse(%q) =\n%#v\nwant\n%#v", tc.args, got, *tc.want)
			}
		})
	}
}

func TestParseWritesNothing(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = w, w

	_, parseErr := Parse([]string{"-bogus"})

	os.Stdout, os.Stderr = oldOut, oldErr
	w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if parseErr == nil {
		t.Error("Parse(-bogus) error = nil, want flag error")
	}
	if len(out) != 0 {
		t.Errorf("Parse wrote to stdout/stderr: %q", out)
	}
}
