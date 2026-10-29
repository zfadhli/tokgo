// Package config holds the resolved run configuration shared by every mode.
package config

// Mode selects how the recorder decides what to capture.
type Mode int

const (
	// Manual records now, once, and exits.
	Manual Mode = iota
	// Automatic polls the user until they are live, records, then polls again.
	Automatic
	// Followers polls every followed account and records whoever is live.
	Followers
)

func (m Mode) String() string {
	switch m {
	case Automatic:
		return "automatic"
	case Followers:
		return "followers"
	default:
		return "manual"
	}
}

// ParseMode maps a CLI mode name to a Mode.
func ParseMode(s string) (Mode, bool) {
	switch s {
	case "manual":
		return Manual, true
	case "automatic":
		return Automatic, true
	case "followers":
		return Followers, true
	}
	return Manual, false
}

// Config is the full set of knobs one recorder run needs.
type Config struct {
	URL               string
	User              string
	RoomID            string
	Mode              Mode
	AutomaticInterval int // minutes between liveness polls
	Duration          int // seconds; 0 means no cap
	OutputDir         string
	Bitrate           string
	FFmpegPath        string
	Proxy             string
	Cookies           map[string]string
	UploadTelegram    bool
}
