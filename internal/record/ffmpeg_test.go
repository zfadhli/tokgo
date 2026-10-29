package record

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFFmpegArgs(t *testing.T) {
	cases := []struct {
		name    string
		bitrate string
		want    []string
	}{
		{
			name:    "stream copy without bitrate",
			bitrate: "",
			want:    []string{"-y", "-i", "TK_a_flv.mp4", "-c", "copy", "TK_a.mp4"},
		},
		{
			name:    "re-encode video with bitrate",
			bitrate: "1500k",
			want: []string{
				"-y", "-i", "TK_a_flv.mp4",
				"-c:v", "libx264", "-b:v", "1500k", "-c:a", "copy",
				"TK_a.mp4",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ffmpegArgs("TK_a_flv.mp4", "TK_a.mp4", tc.bitrate)
			if len(got) != len(tc.want) {
				t.Fatalf("args = %q, want %q", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("args = %q, want %q", got, tc.want)
				}
			}
		})
	}
}

func TestConvertCopiesCapturedFile(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "TK_a_2025.01.01_00-00-00_flv.mp4")
	if err := os.WriteFile(in, []byte("captured"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Convert(context.Background(), in, "", copyStub(t, "cp \"$3\" \"$last\""))
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	want := filepath.Join(dir, "TK_a_2025.01.01_00-00-00.mp4")
	if got != want {
		t.Fatalf("Convert returned %q, want %q", got, want)
	}
	if data, err := os.ReadFile(want); err != nil || string(data) != "captured" {
		t.Fatalf("output file = %q, %v", data, err)
	}
	if _, err := os.Stat(in); !os.IsNotExist(err) {
		t.Fatalf("input still present after conversion: %v", err)
	}
}

func TestConvertReportsFFmpegOutput(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "TK_b_2025.01.01_00-00-00_flv.mp4")
	if err := os.WriteFile(in, []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Convert(context.Background(), in, "", copyStub(t, "echo 'Invalid data found' >&2\nexit 1"))
	if err == nil {
		t.Fatal("want an error when ffmpeg exits non-zero")
	}
	if !strings.Contains(err.Error(), "Invalid data found") {
		t.Fatalf("error does not carry ffmpeg output: %v", err)
	}
	if _, statErr := os.Stat(in); statErr != nil {
		t.Fatalf("capture file must survive a failed conversion: %v", statErr)
	}
}

// copyStub writes an executable that receives ffmpeg's arguments and runs body.
func copyStub(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffmpeg")
	script := "#!/bin/sh\nlast=\"\"\nfor a in \"$@\"; do last=\"$a\"; done\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
