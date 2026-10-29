package cli

import (
	"strings"
	"testing"

	"tokgo/internal/config"
)

func TestHeaderDescribesTheRun(t *testing.T) {
	cfg := config.Config{
		User:              "alice",
		Once:              true,
		AutomaticInterval: 3,
		Duration:          4,
		OutputDir:         t.TempDir(),
		Cookies:           map[string]string{"sessionid_ss": "0123456789b9c2"},
	}

	stdout, _ := capture(t, func() { header(cfg, []string{"alice"}) })

	for _, want := range []string{
		"  target  @alice · record once, cap 4min\n",
		"  ffmpeg  ",
		"  output  " + cfg.OutputDir + " · ",
		"  cookies sessionid_ss ••••b9c2\n",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("header missing %q:\n%s", want, stdout)
		}
	}
}

func TestHeaderAnonymousWithoutCookies(t *testing.T) {
	cfg := config.Config{RoomID: "7625123627591093022", AutomaticInterval: 3, OutputDir: t.TempDir()}

	stdout, _ := capture(t, func() { header(cfg, nil) })

	for _, want := range []string{
		"  target  room 7625123627591093022 · wait for live, poll 3min\n",
		"  cookies none · anonymous\n",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("header missing %q:\n%s", want, stdout)
		}
	}
}
