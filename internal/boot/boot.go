// Package boot holds the process-level setup shared by the CLI and the TUI:
// reading the cookie jar and checking that ffmpeg exists.
package boot

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"tokgo/internal/logx"
)

// CookieFile is where cookies live, matching the reference layout.
const CookieFile = "cookies.json"

// ReadCookies tolerates a missing or empty file: anonymous lookups still work
// for public rooms, and every path that truly needs a login reports it itself.
func ReadCookies(path string) map[string]string {
	body, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		if !os.IsNotExist(err) {
			logx.Warnf("cannot read %s: %v", path, err)
		}
		return nil
	}
	cookies := map[string]string{}
	if err := json.Unmarshal(body, &cookies); err != nil {
		logx.Warnf("%s is not a flat JSON object of cookies: %v", path, err)
		return nil
	}
	for name, value := range cookies {
		if value == "" {
			delete(cookies, name)
		}
	}
	return cookies
}

// CheckFFmpeg reports whether the ffmpeg binary the run will use is reachable.
func CheckFFmpeg(path string) error {
	if path == "" {
		path = "ffmpeg"
	}
	if _, err := exec.LookPath(path); err != nil {
		return fmt.Errorf("ffmpeg not found (%s) - install it or pass -ffmpeg-path: %w", path, err)
	}
	return nil
}
