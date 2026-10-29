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

// loadTargets reads one username per line. Blank lines, duplicates and a
// leading @ are tolerated; a missing file is an empty list, not an error.
func loadTargets(path string) []string {
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
	var users []string
	seen := make(map[string]bool)
	for _, line := range strings.Split(string(body), "\n") {
		name := strings.TrimPrefix(strings.TrimSpace(line), "@")
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		users = append(users, name)
	}
	return users
}

// saveTargets writes the list back, creating the directory it lives in.
func saveTargets(path string, users []string) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	body := strings.Join(users, "\n")
	if body != "" {
		body += "\n"
	}
	return os.WriteFile(path, []byte(body), 0o644)
}
