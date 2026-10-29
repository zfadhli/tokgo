package tui

import (
	"os"
	"path/filepath"
	"strings"

	"tokgo/internal/logx"
)

// targetFile is where the saved target list lives between sessions. An empty
// string means persistence is off, which is what an unset HOME gives.
func targetFile() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "tokgo", "targets.txt")
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
