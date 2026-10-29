package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"tokgo/internal/logx"
)

// configPath is a file in the config dir, next to the target list. An empty
// string means persistence is off, which is what an unset HOME gives.
func configPath(name string) string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "tokgo", name)
}

func targetFile() string { return configPath("targets.txt") }

func settingsFile() string { return configPath("settings.json") }

// storedSettings is the on-disk shape of the input-line knobs. Zero values are
// the defaults, so they stay out of the file.
type storedSettings struct {
	Once     bool   `json:"once,omitempty"`
	Duration int    `json:"duration,omitempty"`
	Interval int    `json:"interval,omitempty"`
	Output   string `json:"output,omitempty"`
	Proxy    string `json:"proxy,omitempty"`
	Telegram bool   `json:"telegram,omitempty"`
}

// readSettings reads the saved knobs. A missing file is the defaults, not an
// error.
func readSettings(path string) (storedSettings, error) {
	var out storedSettings
	if path == "" {
		return out, nil
	}
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return out, err
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return storedSettings{}, err
	}
	return out, nil
}

// writeSettings saves the knobs so the next launch opens on them.
func writeSettings(path string, s storedSettings) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	body, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(body, '\n'), 0o644)
}

// saved is one entry of the target list: a username, and whether opening the
// screen should start it on its own. New names are manual until /autostart.
type saved struct {
	user string
	auto bool
}

// loadTargets reads one username per line, with a leading * marking the entry
// as part of the autostart group. Blank lines, duplicates and a leading @ are
// tolerated; a missing file is an empty list, not an error.
func loadTargets(path string) []saved {
	if path == "" {
		return nil
	}
	body, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			logx.Warnf("cannot read %s: %v", path, err)
		}
		return nil
	}
	var out []saved
	seen := make(map[string]bool)
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		auto := strings.HasPrefix(line, "*")
		name := strings.TrimPrefix(strings.TrimPrefix(line, "*"), "@")
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, saved{user: name, auto: auto})
	}
	return out
}

// saveTargets writes the list back, creating the directory it lives in. The
// autostart group keeps its * so the file still reads as a plain list.
func saveTargets(path string, list []saved) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	lines := make([]string, 0, len(list))
	for _, s := range list {
		prefix := ""
		if s.auto {
			prefix = "*"
		}
		lines = append(lines, prefix+s.user)
	}
	body := strings.Join(lines, "\n")
	if body != "" {
		body += "\n"
	}
	return os.WriteFile(path, []byte(body), 0o644)
}
