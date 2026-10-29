package cli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

func TestNormalize(t *testing.T) {
	fs := pflag.NewFlagSet("tokgo", pflag.ContinueOnError)
	bind(fs)
	tests := []struct {
		args, want []string
	}{
		{[]string{"-user", "x"}, []string{"--user", "x"}},
		{[]string{"-user=@x"}, []string{"--user=@x"}},
		{[]string{"--user", "x"}, []string{"--user", "x"}},
		{[]string{"-duration", "-1"}, []string{"--duration", "-1"}},
		{[]string{"-telegram"}, []string{"--telegram"}},
		{[]string{"-h"}, []string{"-h"}},
		{[]string{"-help"}, []string{"--help"}},
		{[]string{"-bogus"}, []string{"--bogus"}},
		{[]string{"-1"}, []string{"-1"}},
		{[]string{"x", "y"}, []string{"x", "y"}},
		{[]string{"-user", "x", "--", "-user"}, []string{"--user", "x", "--", "-user"}},
	}
	for _, tc := range tests {
		if got := normalize(tc.args, fs); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("normalize(%q) = %q, want %q", tc.args, got, tc.want)
		}
	}
}

func TestHelpListsEveryFlag(t *testing.T) {
	cmd := New(Options{Version: "0.1.0"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("--help error = %v", err)
	}

	help := out.String()
	for _, name := range []string{
		"url", "user", "room_id", "now", "automatic_interval", "proxy",
		"output", "duration", "telegram", "ffmpeg-path", "no-update-check",
	} {
		if !strings.Contains(help, "--"+name) {
			t.Errorf("help missing --%s:\n%s", name, help)
		}
	}
	for _, phrase := range []string{"comma-separated", "seconds", "minutes", "tui"} {
		if !strings.Contains(help, phrase) {
			t.Errorf("help missing %q:\n%s", phrase, help)
		}
	}
}

// capture swaps the process streams for the duration of fn and returns what
// was written to each.
func capture(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	defer func() { os.Stdout, os.Stderr = oldOut, oldErr }()

	fn()

	os.Stdout, os.Stderr = oldOut, oldErr
	outW.Close()
	errW.Close()
	stdoutBytes, _ := io.ReadAll(outR)
	stderrBytes, _ := io.ReadAll(errR)
	return string(stdoutBytes), string(stderrBytes)
}

func TestExecuteStatus(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		tui     func() error
		want    int
		wantOut string
		wantErr string
	}{
		{
			name:    "unknown flag",
			args:    []string{"-bogus"},
			want:    2,
			wantErr: "Error: unknown flag: --bogus",
		},
		{
			name:    "mode flag is gone",
			args:    []string{"-mode", "bogus", "-user", "x"},
			want:    2,
			wantErr: "unknown flag: --mode",
		},
		{
			name:    "no target",
			args:    nil,
			want:    2,
			wantOut: "v0.1.0",
			wantErr: "missing -url, -user or -room_id",
		},
		{
			name:    "unknown command",
			args:    []string{"bogus"},
			want:    2,
			wantErr: "unknown command",
		},
		{
			name: "tui dispatched",
			args: []string{"tui"},
			tui:  func() error { return nil },
			want: 0,
		},
		{
			name:    "tui failure",
			args:    []string{"tui"},
			tui:     func() error { return errors.New("no tty") },
			want:    1,
			wantErr: "no tty",
		},
		{
			name:    "tui missing",
			args:    []string{"tui"},
			want:    1,
			wantErr: "tui is not available in this build",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := Options{Version: "0.1.0", TUI: tc.tui}
			var got int
			stdout, stderr := capture(t, func() { got = Execute(tc.args, opts) })

			if got != tc.want {
				t.Errorf("Execute(%q) = %d, want %d (stderr %q)", tc.args, got, tc.want, stderr)
			}
			if !strings.Contains(stdout, tc.wantOut) {
				t.Errorf("Execute(%q) stdout = %q, want containing %q", tc.args, stdout, tc.wantOut)
			}
			if !strings.Contains(stderr, tc.wantErr) {
				t.Errorf("Execute(%q) stderr = %q, want containing %q", tc.args, stderr, tc.wantErr)
			}
		})
	}
}

func TestExecuteHelpExitsZero(t *testing.T) {
	var got int
	stdout, _ := capture(t, func() { got = Execute([]string{"-h"}, Options{Version: "0.1.0"}) })
	if got != 0 {
		t.Fatalf("Execute(-h) = %d, want 0", got)
	}
	if !strings.Contains(stdout, "--automatic_interval") {
		t.Errorf("-h output missing flags:\n%s", stdout)
	}
}
