// Package cli turns the command line into a validated run configuration.
package cli

import (
	"bytes"
	"errors"
	"flag"
	"regexp"
	"strings"

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

// Usage is the help text, printed with -h and on a parse error.
func Usage() string {
	return `Usage: tokgo [options]
       tokgo tui

Flags:
  -url string                TikTok live room url
  -user string               username, or a comma-separated list of usernames
  -room_id string            TikTok room id
  -mode string               manual, automatic or followers (default "manual")
  -automatic_interval int    minutes between liveness checks (default 5)
  -proxy string              proxy url for requests
  -output string             directory for recordings
  -duration int              recording length cap in seconds, 0 means no cap
  -telegram                  upload recordings to Telegram
  -bitrate string            ffmpeg bitrate for the recording
  -ffmpeg-path string        path to the ffmpeg binary
  -no-update-check           skip the update check at startup
`
}

// Parse parses argv (without the program name) and validates it.
func Parse(args []string) (Parsed, error) {
	fs := flag.NewFlagSet("tokgo", flag.ContinueOnError)
	var sink bytes.Buffer
	fs.SetOutput(&sink)

	var (
		url      = fs.String("url", "", "")
		user     = fs.String("user", "", "")
		roomID   = fs.String("room_id", "", "")
		modeName = fs.String("mode", "manual", "")
		interval = fs.Int("automatic_interval", 5, "")
		proxy    = fs.String("proxy", "", "")
		output   = fs.String("output", "", "")
		duration = fs.Int("duration", 0, "")
		telegram = fs.Bool("telegram", false, "")
		bitrate  = fs.String("bitrate", "", "")
		ffmpeg   = fs.String("ffmpeg-path", "", "")
		noChecks = fs.Bool("no-update-check", false, "")
	)
	if err := fs.Parse(args); err != nil {
		return Parsed{}, err
	}

	mode, ok := config.ParseMode(*modeName)
	if !ok {
		return Parsed{}, errors.New("missing or invalid -mode: choose manual, automatic or followers")
	}

	users := splitUsers(*user)
	if mode != config.Followers && *url == "" && len(users) == 0 && *roomID == "" {
		return Parsed{}, errors.New("missing -url, -user or -room_id")
	}
	if len(users) > 1 && (*roomID != "" || *url != "") {
		return Parsed{}, errors.New("when passing multiple usernames, do not pass -room_id or -url")
	}
	if *url != "" && !tikTokURL.MatchString(*url) {
		return Parsed{}, errors.New("the provided url does not look like a TikTok live url")
	}
	given := 0
	for _, set := range []bool{len(users) > 0, *roomID != "", *url != ""} {
		if set {
			given++
		}
	}
	if given > 1 {
		return Parsed{}, errors.New("pass only one of -user, -room_id, -url")
	}
	if *interval < 1 {
		return Parsed{}, errors.New("-automatic_interval must be at least 1 minute")
	}
	if *duration < 0 {
		return Parsed{}, errors.New("-duration cannot be negative")
	}

	return Parsed{
		Config: config.Config{
			URL:               *url,
			User:              *user,
			RoomID:            *roomID,
			Mode:              mode,
			AutomaticInterval: *interval,
			Duration:          *duration,
			OutputDir:         *output,
			Bitrate:           *bitrate,
			FFmpegPath:        *ffmpeg,
			Proxy:             *proxy,
			UploadTelegram:    *telegram,
		},
		Users:       users,
		UpdateCheck: !*noChecks,
	}, nil
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
