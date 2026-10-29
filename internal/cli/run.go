package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"tokgo/internal/app"
	"tokgo/internal/boot"
	"tokgo/internal/config"
	"tokgo/internal/logx"
	"tokgo/internal/telegram"
	"tokgo/internal/ui"
)

// run performs the process-level setup a recording needs and then records.
func run(ctx context.Context, parsed Parsed, opts Options) error {
	cfg := parsed.Config
	cfg.Cookies = boot.ReadCookies(boot.CookieFile)
	if cfg.OutputDir != "" {
		if err := os.MkdirAll(cfg.OutputDir, 0o755); err != nil {
			return fmt.Errorf("cannot create output directory %s: %w", cfg.OutputDir, err)
		}
	}

	if err := boot.CheckFFmpeg(cfg.FFmpegPath); err != nil {
		return err
	}
	header(cfg, parsed.Users)
	if parsed.UpdateCheck && opts.UpdateRepo != "" {
		checkUpdates(opts.UpdateRepo, opts.Version)
	} else {
		logx.Debugf("skipped update check")
	}

	uploads, err := telegram.Load("telegram.json")
	if err != nil {
		logx.Warnf("telegram.json: %v", err)
	}

	ctx, stop := signalContext(ctx)
	defer stop()

	return app.New(cfg, uploads).Run(ctx, parsed.Users)
}

// header prints the run's target and wiring below the banner.
func header(cfg config.Config, users []string) {
	fmt.Println("  " + ui.Label("target") + ui.Value(targetLine(cfg, users)))

	ffmpegPath := cfg.FFmpegPath
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}
	resolved, err := exec.LookPath(ffmpegPath)
	if err != nil {
		resolved = ffmpegPath
	}
	ffmpegLine := resolved
	if version := boot.FFmpegVersion(resolved); version != "" {
		ffmpegLine = version + " · " + resolved
	}
	fmt.Println("  " + ui.Label("ffmpeg") + ui.Value(ffmpegLine))

	out, err := filepath.Abs(cfg.OutputDir)
	if err != nil {
		out = cfg.OutputDir
	}
	line := ui.Path(out)
	if free, ok := boot.FreeSpace(out); ok {
		line += ui.Dim(" · " + ui.Bytes(free) + " free")
	}
	fmt.Println("  " + ui.Label("output") + line)

	fmt.Println("  " + ui.Label("cookies") + ui.Value(cookieLine(cfg.Cookies)))
	fmt.Println()
}

func targetLine(cfg config.Config, users []string) string {
	target := "room " + cfg.RoomID
	switch {
	case cfg.URL != "":
		target = cfg.URL
	case len(users) > 0:
		target = "@" + strings.Join(users, ",")
	}
	mode := fmt.Sprintf("wait for live, poll %dmin", cfg.AutomaticInterval)
	if cfg.Once {
		mode = "record once"
	}
	if cfg.Duration > 0 {
		mode += fmt.Sprintf(", cap %dmin", cfg.Duration)
	}
	return target + " · " + mode
}

// cookieLine names the login cookie without printing it.
func cookieLine(cookies map[string]string) string {
	for _, name := range [...]string{"sessionid_ss", "sessionid"} {
		value := cookies[name]
		if value == "" {
			continue
		}
		if len(value) > 4 {
			value = value[len(value)-4:]
		}
		return name + " ••••" + value
	}
	return "none · anonymous"
}

// signalContext stops the recording on the first Ctrl-C so the capture is
// finalised and converted, and aborts immediately on the second.
func signalContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigs
		logx.Infof("Ctrl-C detected, finishing the recording (press again to abort)")
		cancel()
		<-sigs
		logx.Warnf("aborting immediately")
		logx.Close()
		os.Exit(130)
	}()
	return ctx, cancel
}

type release struct {
	TagName string `json:"tag_name"`
}

func checkUpdates(repo, version string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	url := "https://api.github.com/repos/" + repo + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		logx.Debugf("update check failed: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	var latest release
	if err := json.NewDecoder(resp.Body).Decode(&latest); err != nil || latest.TagName == "" {
		return
	}
	if latest.TagName != version {
		logx.Infof("current %s, latest release %s: https://github.com/%s", version, latest.TagName, repo)
	}
}
