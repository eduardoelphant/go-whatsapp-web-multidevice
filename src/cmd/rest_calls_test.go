package cmd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/callrec"
	"github.com/gofiber/fiber/v3"
)

func recordingsApp(t *testing.T, accounts map[string]string, secret []byte) *fiber.App {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ABC123.ogg"), []byte("OggS-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "OTHER.ogg"), []byte("other"), 0o644); err != nil {
		t.Fatal(err)
	}
	app := fiber.New()
	mountCallRecordings(app, "", dir, accounts, secret)
	return app
}

func TestRecordingSignedURLOpensWithoutCredentials(t *testing.T) {
	secret := []byte("s3cret")
	app := recordingsApp(t, map[string]string{"admin": "pw"}, secret)
	link := callrec.URLPath("", "ABC123", secret, time.Now().Add(time.Hour))
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, link, nil))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.Header.Get("Cache-Control") != "private, no-store" || resp.Header.Get("Content-Disposition") != `attachment; filename="ABC123.ogg"` {
		t.Fatalf("headers: %q / %q", resp.Header.Get("Cache-Control"), resp.Header.Get("Content-Disposition"))
	}
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "audio/ogg" || string(body) != "OggS-bytes" {
		t.Fatalf("status %d, type %q, body %q", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
}

func TestRecordingRouteRejectsTamperedExpiredAndTraversal(t *testing.T) {
	secret := []byte("s3cret")
	app := recordingsApp(t, map[string]string{"admin": "pw"}, secret)
	now := time.Now()

	expired := callrec.URLPath("", "ABC123", secret, now.Add(-time.Minute))
	if got := statusOf(t, app, expired, "", ""); got != http.StatusUnauthorized {
		t.Fatalf("expired link: %d, want 401", got)
	}
	tampered := callrec.URLPath("", "ABC123", secret, now.Add(time.Hour)) + "0"
	if got := statusOf(t, app, tampered, "", ""); got != http.StatusUnauthorized {
		t.Fatalf("tampered link: %d, want 401", got)
	}
	other := callrec.URLPath("", "OTHER", secret, now.Add(time.Hour))
	borrowed := "/call/ABC123/recording" + other[len("/call/OTHER/recording"):]
	if got := statusOf(t, app, borrowed, "", ""); got != http.StatusUnauthorized {
		t.Fatalf("another call's signature: %d, want 401", got)
	}
	for _, path := range []string{"/call/..%2F..%2Fetc%2Fpasswd/recording", "/call/a.b/recording", "/call/%2E%2E/recording"} {
		if got := statusOf(t, app, path, "admin", "pw"); got == http.StatusOK {
			t.Fatalf("%s served a file", path)
		}
	}
	if got := statusOf(t, app, "/call/ABC123/recording", "admin", "pw"); got != http.StatusOK {
		t.Fatalf("Basic Auth: %d, want 200", got)
	}
	if got := statusOf(t, app, "/call/GONE/recording", "admin", "pw"); got != http.StatusGone {
		t.Fatalf("missing recording: %d, want 410", got)
	}
}
