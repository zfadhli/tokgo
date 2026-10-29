package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"tokgo/internal/app"
	"tokgo/internal/boot"
	"tokgo/internal/logx"
	"tokgo/internal/telegram"
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
	if parsed.UpdateCheck && opts.UpdateRepo != "" {
		checkUpdates(opts.UpdateRepo, opts.Version)
	} else {
		logx.Infof("skipped update check")
	}

	uploads, err := telegram.Load("telegram.json")
	if err != nil {
		logx.Warnf("telegram.json: %v", err)
	}

	ctx, stop := signalContext(ctx)
	defer stop()

	return app.New(cfg, uploads).Run(ctx, parsed.Users)
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
