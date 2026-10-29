package record

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOutputPathFromSegment(t *testing.T) {
	cases := []struct{ segment, want string }{
		{"TK_u_2025.01.01_00-00-00.000.ts", "TK_u_2025.01.01_00-00-00.mp4"},
		{"TK_u_2025.01.01_00-00-00.007.ts", "TK_u_2025.01.01_00-00-00.mp4"},
		{"TK_u_2025.01.01_00-00-00.000.flv", "TK_u_2025.01.01_00-00-00.mp4"},
	}
	for _, tc := range cases {
		if got := outputPath(tc.segment); got != tc.want {
			t.Fatalf("outputPath(%q) = %q, want %q", tc.segment, got, tc.want)
		}
	}
}

func TestFFmpegArgs(t *testing.T) {
	cases := []struct {
		name    string
		bitrate string
		concat  bool
		want    []string
	}{
		{
			name:    "stream copy without bitrate",
			bitrate: "",
			want:    []string{"-y", "-i", "TK_a.000.ts", "-c", "copy", "TK_a.mp4"},
		},
		{
			name:    "re-encode video with bitrate",
			bitrate: "1500k",
			want: []string{
				"-y", "-i", "TK_a.000.ts",
				"-c:v", "libx264", "-b:v", "1500k", "-c:a", "copy",
				"TK_a.mp4",
			},
		},
		{
			name:   "concat list",
			concat: true,
			want: []string{
				"-y", "-f", "concat", "-safe", "0", "-i", "list.txt",
				"-c", "copy", "TK_a.mp4",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ffmpegArgs("TK_a.000.ts", "TK_a.mp4", tc.bitrate, tc.concat)
			if tc.concat {
				got = ffmpegArgs("list.txt", "TK_a.mp4", tc.bitrate, tc.concat)
			}
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

// A single segment remuxes straight to the mp4, whichever container the
// capture wrote.
func TestConvertRemuxesOneSegment(t *testing.T) {
	for _, ext := range []string{".ts", ".flv"} {
		t.Run(ext, func(t *testing.T) {
			dir := t.TempDir()
			in := filepath.Join(dir, "TK_a_2025.01.01_00-00-00.000"+ext)
			if err := os.WriteFile(in, []byte("captured"), 0o644); err != nil {
				t.Fatal(err)
			}

			stub, _ := ffmpegStub(t, "")
			got, err := Convert(context.Background(), []string{in}, "", stub)
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
				t.Fatalf("segment still present after conversion: %v", err)
			}
		})
	}
}

// A reconnect leaves several segments; the remux joins them in order and the
// scratch concat list does not survive.
func TestConvertConcatenatesSegments(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "TK_a_2025.01.01_00-00-00.000.ts")
	second := filepath.Join(dir, "TK_a_2025.01.01_00-00-00.001.ts")
	for path, body := range map[string]string{first: "first ", second: "second"} {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	stub, calls := ffmpegStub(t, "")
	got, err := Convert(context.Background(), []string{first, second}, "", stub)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	want := filepath.Join(dir, "TK_a_2025.01.01_00-00-00.mp4")
	if got != want {
		t.Fatalf("Convert returned %q, want %q", got, want)
	}
	if data, err := os.ReadFile(want); err != nil || string(data) != "first second" {
		t.Fatalf("output file = %q, %v", data, err)
	}
	for _, seg := range []string{first, second} {
		if _, err := os.Stat(seg); !os.IsNotExist(err) {
			t.Fatalf("segment %s still present after conversion: %v", seg, err)
		}
	}
	if lists, err := filepath.Glob(filepath.Join(dir, "tokgo-concat-*")); err != nil || len(lists) != 0 {
		t.Fatalf("concat list left behind: %v, %v", lists, err)
	}
	if invocations := stubCalls(t, calls); len(invocations) != 1 || !strings.Contains(invocations[0], "-f concat") {
		t.Fatalf("ffmpeg calls = %q, want one concat invocation", invocations)
	}
}

func TestConvertReportsFFmpegOutput(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "TK_b_2025.01.01_00-00-00.000.ts")
	last := filepath.Join(dir, "TK_b_2025.01.01_00-00-00.001.ts")
	for _, path := range []string{first, last} {
		if err := os.WriteFile(path, []byte("garbage"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	stub, _ := ffmpegStub(t, "echo 'Invalid data found' >&2\nexit 1")
	got, err := Convert(context.Background(), []string{first, last}, "", stub)
	if err == nil {
		t.Fatal("want an error when ffmpeg exits non-zero")
	}
	if !strings.Contains(err.Error(), "Invalid data found") {
		t.Fatalf("error does not carry ffmpeg output: %v", err)
	}
	if got != last {
		t.Fatalf("Convert returned %q, want the last segment %q", got, last)
	}
	for _, path := range []string{first, last} {
		if _, statErr := os.Stat(path); statErr != nil {
			t.Fatalf("segment must survive a failed conversion: %v", statErr)
		}
	}
}

// stubArgs leaves $in set to ffmpeg's -i argument, $last to the output path and
// $concat to 1 when the concat demuxer is in play.
const stubArgs = `in=""
concat=0
while [ $# -gt 1 ]; do
	case "$1" in
	-f) [ "$2" = "concat" ] && concat=1; shift 2 ;;
	-i) in="$2"; shift 2 ;;
	*) shift ;;
	esac
done
last="$1"
`

// defaultStubBody stands in for a stream-copy: pipe input becomes the output,
// a concat list is joined in order, and anything else is copied.
const defaultStubBody = `if [ "$in" = "pipe:0" ]; then cat > "$last"
elif [ "$concat" = "1" ]; then sed -e "s/^file '//" -e "s/'\$//" "$in" | while IFS= read -r f; do cat "$f"; done > "$last"
else cp "$in" "$last"
fi`

// ffmpegStub writes a fake ffmpeg that logs every invocation and then runs body.
// An empty body means the default copy behaviour.
func ffmpegStub(t *testing.T, body string) (path, calls string) {
	t.Helper()
	dir := t.TempDir()
	calls = filepath.Join(dir, "calls")
	path = filepath.Join(dir, "ffmpeg")
	if body == "" {
		body = defaultStubBody
	}
	script := "#!/bin/sh\necho \"$*\" >> '" + calls + "'\n" + stubArgs + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path, calls
}

// stubCalls returns one line of arguments per stub invocation.
func stubCalls(t *testing.T, calls string) []string {
	t.Helper()
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// A failed cleanup must not cost the recording: the mp4 is already good, so its
// path comes back with the error.
func TestConvertKeepsTheOutputPathWhenCleanupFails(t *testing.T) {
	dir := t.TempDir()
	segment := filepath.Join(dir, "TK_a_2025.01.01_00-00-00.000.ts")
	if err := os.WriteFile(segment, []byte("captured"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The same segment twice: the first remove succeeds, the second cannot.
	stub, _ := ffmpegStub(t, "")
	got, err := Convert(context.Background(), []string{segment, segment}, "", stub)
	if err == nil {
		t.Fatal("want an error when a segment cannot be removed")
	}
	want := filepath.Join(dir, "TK_a_2025.01.01_00-00-00.mp4")
	if got != want {
		t.Fatalf("Convert returned %q, want the finished recording %q", got, want)
	}
	if data, err := os.ReadFile(want); err != nil || string(data) != "capturedcaptured" {
		t.Fatalf("output file = %q, %v", data, err)
	}
}
