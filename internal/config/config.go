// Package config holds the resolved run configuration shared by every mode.
package config

// Config is the full set of knobs one recorder run needs.
type Config struct {
	URL               string
	User              string
	RoomID            string
	Now               bool // record at once instead of waiting for the account to go live
	AutomaticInterval int  // minutes between liveness polls
	Duration          int  // seconds; 0 means no cap
	OutputDir         string
	FFmpegPath        string
	Proxy             string
	Cookies           map[string]string
	UploadTelegram    bool
}
