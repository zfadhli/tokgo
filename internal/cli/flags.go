// Package cli turns the command line into a validated run configuration and
// builds the cobra command tree around it.
package cli

import (
	"errors"
	"io"
	"regexp"
	"strings"

	"github.com/spf13/pflag"

	"tokgo/internal/config"
)

// Parsed is a validated command line. Users holds the usernames after cleaning,
// which Config.User cannot because it carries a single string.
type Parsed struct {
	config.Config
	Users       []string
	UpdateCheck bool
}

var tikTokURL = regexp.MustCompile(`^.*www\.tiktok\.com.*$|^.*vm\.tiktok\.com.*$`)

// flags collects the reference CLI's knobs. Every one is a single-dash long
// name there, which normalize rewrites into the double-dash form pflag parses.
type flags struct {
	url, user, roomID, modeName, proxy, output, bitrate, ffmpeg string
	interval, duration                                          int
	telegram, noUpdateCheck                                     bool
}

// bind registers the flags on fs and returns the struct that collects them.
func bind(fs *pflag.FlagSet) *flags {
	f := &flags{}
	fs.StringVar(&f.url, "url", "", "TikTok live room url")
	fs.StringVar(&f.user, "user", "", "username, or a comma-separated list of usernames")
	fs.StringVar(&f.roomID, "room_id", "", "TikTok room id")
	fs.StringVar(&f.modeName, "mode", "manual", "manual, automatic or followers")
	fs.IntVar(&f.interval, "automatic_interval", 5, "minutes between liveness checks")
	fs.StringVar(&f.proxy, "proxy", "", "proxy url for requests")
	fs.StringVar(&f.output, "output", "", "directory for recordings")
	fs.IntVar(&f.duration, "duration", 0, "recording length cap in seconds, 0 means no cap")
	fs.BoolVar(&f.telegram, "telegram", false, "upload recordings to Telegram")
	fs.StringVar(&f.bitrate, "bitrate", "", "ffmpeg bitrate for the recording")
	fs.StringVar(&f.ffmpeg, "ffmpeg-path", "", "path to the ffmpeg binary")
	fs.BoolVar(&f.noUpdateCheck, "no-update-check", false, "skip the update check at startup")
	return f
}

// parsed validates the collected flags.
func (f *flags) parsed() (Parsed, error) {
	mode, ok := config.ParseMode(f.modeName)
	if !ok {
		return Parsed{}, errors.New("missing or invalid -mode: choose manual, automatic or followers")
	}

	users := splitUsers(f.user)
	if mode != config.Followers && f.url == "" && len(users) == 0 && f.roomID == "" {
		return Parsed{}, errors.New("missing -url, -user or -room_id")
	}
	if len(users) > 1 && (f.roomID != "" || f.url != "") {
		return Parsed{}, errors.New("when passing multiple usernames, do not pass -room_id or -url")
	}
	if f.url != "" && !tikTokURL.MatchString(f.url) {
		return Parsed{}, errors.New("the provided url does not look like a TikTok live url")
	}
	given := 0
	for _, set := range []bool{len(users) > 0, f.roomID != "", f.url != ""} {
		if set {
			given++
		}
	}
	if given > 1 {
		return Parsed{}, errors.New("pass only one of -user, -room_id, -url")
	}
	if f.interval < 1 {
		return Parsed{}, errors.New("-automatic_interval must be at least 1 minute")
	}
	if f.duration < 0 {
		return Parsed{}, errors.New("-duration cannot be negative")
	}

	return Parsed{
		Config: config.Config{
			URL:               f.url,
			User:              f.user,
			RoomID:            f.roomID,
			Mode:              mode,
			AutomaticInterval: f.interval,
			Duration:          f.duration,
			OutputDir:         f.output,
			Bitrate:           f.bitrate,
			FFmpegPath:        f.ffmpeg,
			Proxy:             f.proxy,
			UploadTelegram:    f.telegram,
		},
		Users:       users,
		UpdateCheck: !f.noUpdateCheck,
	}, nil
}

// Parse parses argv (without the program name) and validates it. The TUI builds
// argv and sends it through here, so the form cannot accept a combination the
// flags reject or reject one they allow.
func Parse(args []string) (Parsed, error) {
	fs := pflag.NewFlagSet("tokgo", pflag.ContinueOnError)
	fs.SetOutput(io.Discard)
	f := bind(fs)
	if err := fs.Parse(normalize(args, fs)); err != nil {
		return Parsed{}, err
	}
	return f.parsed()
}

// normalize rewrites the reference CLI's single-dash long flags (-user) into
// the double-dash form pflag parses (--user). Shorthands such as -h, values
// such as a negative number, and anything after a bare -- are left alone.
func normalize(args []string, fs *pflag.FlagSet) []string {
	out := make([]string, 0, len(args))
	for i, arg := range args {
		if arg == "--" {
			return append(out, args[i:]...)
		}
		if !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "--") || len(arg) < 3 {
			out = append(out, arg)
			continue
		}
		name, value, hasValue := strings.Cut(arg[1:], "=")
		// A leading digit is a value, not a flag, so -duration -1 survives; a
		// registered shorthand is already the form pflag wants.
		if name[0] < 'a' || name[0] > 'z' || (fs.Lookup(name) == nil && fs.ShorthandLookup(name[:1]) != nil) {
			out = append(out, arg)
			continue
		}
		if hasValue {
			out = append(out, "--"+name+"="+value)
		} else {
			out = append(out, "--"+name)
		}
	}
	return out
}

func splitUsers(s string) []string {
	var users []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(part), "@"))
		if part != "" {
			users = append(users, part)
		}
	}
	return users
}
