package telegram

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()

	missing, err := Load(filepath.Join(dir, "absent.json"))
	if err != nil || missing != (Config{}) {
		t.Fatalf("missing file = %+v, %v; want zero config and no error", missing, err)
	}

	blank := filepath.Join(dir, "blank.json")
	if err := os.WriteFile(blank, []byte(`{"bot_token":"  ","chat_id":"42"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if cfg, err := Load(blank); err != nil || cfg != (Config{}) {
		t.Fatalf("blank token = %+v, %v; want not configured", cfg, err)
	}

	full := filepath.Join(dir, "telegram.json")
	body := `{"bot_token":"abc","chat_id":"42","caption":"<b>live</b>"}`
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(full)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg != (Config{BotToken: "abc", ChatID: "42", Caption: "<b>live</b>"}) {
		t.Fatalf("Load = %+v", cfg)
	}

	broken := filepath.Join(dir, "broken.json")
	if err := os.WriteFile(broken, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(broken); err == nil {
		t.Fatal("want an error for malformed json")
	}
}

func TestUploadSendsMultipartForm(t *testing.T) {
	var (
		gotChatID, gotCaption, gotParseMode string
		gotName, gotContent                 string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/botTESTTOKEN/sendDocument" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("multipart parse: %v", err)
			return
		}
		gotChatID = r.FormValue("chat_id")
		gotCaption = r.FormValue("caption")
		gotParseMode = r.FormValue("parse_mode")
		file, header, err := r.FormFile("document")
		if err != nil {
			t.Errorf("document part: %v", err)
			return
		}
		defer file.Close()
		data, _ := io.ReadAll(file)
		gotName, gotContent = header.Filename, string(data)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()
	useBase(t, srv.URL)

	file := filepath.Join(t.TempDir(), "clip.mp4")
	if err := os.WriteFile(file, []byte("video-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Config{BotToken: "TESTTOKEN", ChatID: "42", Caption: "<b>live</b>"}
	if err := Upload(context.Background(), cfg, file); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	if gotChatID != "42" || gotCaption != "<b>live</b>" || gotParseMode != "html" {
		t.Fatalf("fields chat_id=%q caption=%q parse_mode=%q", gotChatID, gotCaption, gotParseMode)
	}
	if gotName != "clip.mp4" || gotContent != "video-bytes" {
		t.Fatalf("document %q = %q", gotName, gotContent)
	}
}

func TestUploadReportsAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Unauthorized: bad token", http.StatusUnauthorized)
	}))
	defer srv.Close()
	useBase(t, srv.URL)

	file := filepath.Join(t.TempDir(), "clip.mp4")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Upload(context.Background(), Config{BotToken: "TESTTOKEN", ChatID: "42"}, file)
	if err == nil || !strings.Contains(err.Error(), "Unauthorized: bad token") {
		t.Fatalf("error = %v, want the response body", err)
	}
}

func TestUploadRefusesOversizeAndUnconfigured(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("no upload may be attempted")
	}))
	defer srv.Close()
	useBase(t, srv.URL)

	big := filepath.Join(t.TempDir(), "huge.mp4")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxFileBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()

	err = Upload(context.Background(), Config{BotToken: "TESTTOKEN", ChatID: "42"}, big)
	if err == nil || !strings.Contains(err.Error(), "bot API upload limit") {
		t.Fatalf("oversize error = %v", err)
	}

	err = Upload(context.Background(), Config{}, filepath.Join(t.TempDir(), "nothing.mp4"))
	if err == nil || !strings.Contains(err.Error(), "telegram not configured: set bot_token in telegram.json") {
		t.Fatalf("unconfigured error = %v", err)
	}
}

// useBase points the package at a test server for the duration of the test.
func useBase(t *testing.T, base string) {
	t.Helper()
	prev := apiBase
	apiBase = base
	t.Cleanup(func() { apiBase = prev })
}
