package boot

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSaveCookiesWritesTheJar proves a value typed in the TUI survives the next
// launch, and that an existing loose file is tightened to 0600.
func TestSaveCookiesWritesTheJar(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cookies.json")
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cookies := map[string]string{"sessionid_ss": "a-session-value", "ttwid": "1"}
	if err := SaveCookies(path, cookies); err != nil {
		t.Fatalf("SaveCookies: %v", err)
	}
	if got := ReadCookies(path); !maps.Equal(got, cookies) {
		t.Fatalf("read back = %v, want %v", got, cookies)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.HasSuffix(string(body), "\n") {
		t.Fatalf("the file must end with a newline:\n%s", body)
	}
	if !strings.Contains(string(body), "\n  \"ttwid\": \"1\"") {
		t.Fatalf("the map must be indented:\n%s", body)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o, want 600", got)
	}
}
