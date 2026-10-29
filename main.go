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
	"os/signal"
	"syscall"
	"time"

	"tokgo/internal/app"
	"tokgo/internal/boot"
	"tokgo/internal/cli"
	"tokgo/internal/logx"
	"tokgo/internal/telegram"
	"tokgo/internal/tui"
)

const version = "0.1.0"

// Builds that are distributed through GitHub releases can set this; a plain
// source build has nobody to ask, so the update check stays offline.
const updateRepo = ""

func main() {
	logx.Init(logx.DefaultPath)
	defer logx.Close()

	// `tokgo tui` is the only subcommand: everything else is flags, matching the
	// reference CLI.
	if len(os.Args) > 1 && os.Args[1] == "tui" {
		if err := tui.Run(version); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

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
	cfg.Cookies = boot.ReadCookies(boot.CookieFile)
	if cfg.OutputDir != "" {
		if err := os.MkdirAll(cfg.OutputDir, 0o755); err != nil {
			logx.Errorf("cannot create output directory %s: %v", cfg.OutputDir, err)
			os.Exit(1)
		}
	}

	if err := boot.CheckFFmpeg(cfg.FFmpegPath); err != nil {
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
		if errors.Is(err, app.ErrReported) {
			os.Exit(1)
		}
		logx.Errorf("%v", err)
		os.Exit(1)
	}
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
      ___           ___           ___           ___           ___
     /\  \         /\  \         /\__\         /\  \         /\  \
     \:\  \       /::\  \       /:/  /        /::\  \       /::\  \
      \:\  \     /:/\:\  \     /:/__/        /:/\:\  \     /:/\:\  \
      /::\  \   /:/  \:\  \   /::\__\____   /:/  \:\  \   /:/  \:\  \
     /:/\:\__\ /:/__/ \:\__\ /:/\:::::\__\ /:/__/_\:\__\ /:/__/ \:\__\
    /:/  \/__/ \:\  \ /:/  / \/_|:|~~|~    \:\  /\ \/__/ \:\  \ /:/  /
   /:/  /       \:\  /:/  /     |:|  |      \:\ \:\__\    \:\  /:/  /
   \/__/         \:\/:/  /      |:|  |       \:\/:/  /     \:\/:/  /
                  \::/  /       |:|  |        \::/  /       \::/  /
                   \/__/         \|__|         \/__/         \/__/
`
