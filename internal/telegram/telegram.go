// Package telegram delivers finished recordings to a chat. The reference tool
// speaks MTProto; a full MTProto client is out of scope for this rewrite, so
// uploads go through the HTTP Bot API instead.
package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"tokgo/internal/logx"
)

// apiBase is a variable so tests can point uploads at an httptest server.
var apiBase = "https://api.telegram.org"

const (
	maxFileBytes = 2 << 30
	maxErrorBody = 1 << 10
)

// Config is the telegram side of the run configuration.
type Config struct {
	BotToken string `json:"bot_token"`
	ChatID   string `json:"chat_id"`
	Caption  string `json:"caption"`
}

// Load reads telegram.json. A missing file or a blank token means telegram is
// not configured, which is not an error.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Config{}, nil
		}
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if strings.TrimSpace(cfg.BotToken) == "" {
		return Config{}, nil
	}
	return cfg, nil
}

// Upload posts filePath as a document to the configured chat.
func Upload(ctx context.Context, cfg Config, filePath string) error {
	if strings.TrimSpace(cfg.BotToken) == "" {
		return errors.New("telegram not configured: set bot_token in telegram.json")
	}
	info, err := os.Stat(filePath)
	if err != nil {
		return err
	}
	if info.Size() > maxFileBytes {
		return fmt.Errorf("%s is %d bytes, over the telegram bot API upload limit of 2 GiB", filePath, info.Size())
	}
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}

	pr, pw := io.Pipe()
	form := multipart.NewWriter(pw)
	go func() {
		defer f.Close()
		if err := writeForm(form, cfg, filepath.Base(filePath), f); err != nil {
			pw.CloseWithError(err)
			return
		}
		pw.Close()
	}()

	url := fmt.Sprintf("%s/bot%s/sendDocument", apiBase, cfg.BotToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, pr)
	if err != nil {
		pr.CloseWithError(err)
		return err
	}
	req.Header.Set("Content-Type", form.FormDataContentType())

	logx.Infof("uploading %s to telegram", filePath)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("telegram upload failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	logx.Infof("telegram upload finished: %s", filePath)
	return nil
}

func writeForm(form *multipart.Writer, cfg Config, name string, f *os.File) error {
	for _, field := range [][2]string{
		{"chat_id", cfg.ChatID},
		{"parse_mode", "html"},
		{"caption", cfg.Caption},
	} {
		if field[1] == "" {
			continue
		}
		if err := form.WriteField(field[0], field[1]); err != nil {
			return err
		}
	}
	part, err := form.CreateFormFile("document", name)
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, f); err != nil {
		return err
	}
	return form.Close()
}
