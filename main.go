// Command tokgo records TikTok live streams to mp4.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"tokgo/internal/app"
	"tokgo/internal/cli"
	"tokgo/internal/logx"
	"tokgo/internal/telegram"
)

const version = "0.1.0"

// Builds that are distributed through GitHub releases can set this; a plain
// source build has nobody to ask, so the update check stays offline.
const updateRepo = ""

func main() {
	logx.Init(logx.DefaultPath)
	defer logx.Close()

	banner()

	parsed, err := cli.Parse(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(os.Stdout, cli.Usage())
			return
		}
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprint(os.Stderr, cli.Usage())
		os.Exit(2)
	}
	cfg := parsed.Config
	cfg.Cookies = readCookies(cookieFile)
	if cfg.OutputDir != "" {
		if err := os.MkdirAll(cfg.OutputDir, 0o755); err != nil {
			logx.Errorf("cannot create output directory %s: %v", cfg.OutputDir, err)
			os.Exit(1)
		}
	}

	if err := checkFFmpeg(cfg.FFmpegPath); err != nil {
		logx.Errorf("%v", err)
		os.Exit(1)
	}
	if parsed.UpdateCheck && updateRepo != "" {
		checkUpdates()
	} else {
		logx.Infof("skipped update check")
	}

	uploads, err := telegram.Load("telegram.json")
	if err != nil {
		logx.Warnf("telegram.json: %v", err)
	}

	ctx, stop := signalContext()
	defer stop()

	if err := app.New(cfg, uploads).Run(ctx, parsed.Users); err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		logx.Errorf("%v", err)
		os.Exit(1)
	}
}

// cookieFile is where cookies live, matching the reference layout.
const cookieFile = "cookies.json"

// readCookies tolerates a missing or empty file: anonymous lookups still work
// for public rooms, and every path that truly needs a login reports it itself.
func readCookies(path string) map[string]string {
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

func checkFFmpeg(path string) error {
	if path == "" {
		path = "ffmpeg"
	}
	if _, err := exec.LookPath(path); err != nil {
		return fmt.Errorf("ffmpeg not found (%s) - install it or pass -ffmpeg-path: %w", path, err)
	}
	return nil
}

// signalContext stops the recording on the first Ctrl-C so the capture is
// finalised and converted, and aborts immediately on the second.
func signalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
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

func checkUpdates() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	url := "https://api.github.com/repos/" + updateRepo + "/releases/latest"
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
		logx.Infof("current %s, latest release %s: https://github.com/%s", version, latest.TagName, updateRepo)
	}
}

func banner() {
	fmt.Printf("%sv%s\n\n", bannerArt, version)
}

const bannerArt = `
  _        _         ____
 | |_ ___ | | ___ __/ ___|__ _
 | __/ _ \| |/ / _ \ |  _/ _ \
 | || (_) |   < (_) | |_| (_) |
  \__\___/|_|\_\___/|____\___/
`
