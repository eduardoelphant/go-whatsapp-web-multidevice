package cmd

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/staticurl"
	"github.com/gofiber/fiber/v3"
)

func staticsApp(t *testing.T, basePath string, accounts map[string]string, secret ...[]byte) *fiber.App {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "qrcode"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "qrcode", "scan-qr-slot.png"), []byte("png-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "qrcode", "with space.png"), []byte("png-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	app := fiber.New()
	var key []byte
	if len(secret) > 0 {
		key = secret[0]
	}
	mountStatics(app, basePath, dir, accounts, key)
	app.Get("/api", func(c fiber.Ctx) error { return c.SendString("api") })
	return app
}

func statusOf(t *testing.T, app *fiber.App, path, user, pass string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode
}

func TestStaticsWithoutAuthSettingIsPublicAsBefore(t *testing.T) {
	app := staticsApp(t, "", nil)

	if got := statusOf(t, app, "/statics/qrcode/scan-qr-slot.png", "", ""); got != http.StatusOK {
		t.Fatalf("status = %d, want 200 (no credentials configured: nothing to protect)", got)
	}
}

func TestStaticsBehindAuthNeedsCredentials(t *testing.T) {
	app := staticsApp(t, "", map[string]string{"admin": "secret"})

	if got := statusOf(t, app, "/statics/qrcode/scan-qr-slot.png", "", ""); got != http.StatusUnauthorized {
		t.Fatalf("no credentials: status = %d, want 401", got)
	}
	if got := statusOf(t, app, "/statics/qrcode/scan-qr-slot.png", "admin", "wrong"); got != http.StatusUnauthorized {
		t.Fatalf("wrong password: status = %d, want 401", got)
	}
	if got := statusOf(t, app, "/statics/qrcode/scan-qr-slot.png", "admin", "secret"); got != http.StatusOK {
		t.Fatalf("right credentials: status = %d, want 200", got)
	}
}

func TestStaticsBehindAuthHonorsTheBasePath(t *testing.T) {
	app := staticsApp(t, "/gowa", map[string]string{"admin": "secret"})

	if got := statusOf(t, app, "/gowa/statics/qrcode/scan-qr-slot.png", "", ""); got != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 under the base path", got)
	}
	if got := statusOf(t, app, "/gowa/statics/qrcode/scan-qr-slot.png", "admin", "secret"); got != http.StatusOK {
		t.Fatalf("status = %d, want 200 with credentials", got)
	}
}

func TestMountingStaticsDoesNotProtectOtherRoutes(t *testing.T) {
	app := staticsApp(t, "", map[string]string{"admin": "secret"})

	if got := statusOf(t, app, "/api", "", ""); got != http.StatusOK {
		t.Fatalf("status = %d: the statics auth must stay scoped to /statics", got)
	}
}

// D-13: a signed, unexpired URL opens one file without credentials; Basic Auth still works.
var staticsSecret = []byte("a-long-random-statics-secret")

func signedPath(rel string, expires time.Time) string {
	return "/statics/" + rel + "?" + staticurl.Sign(staticsSecret, rel, expires)
}

func TestSignedURLOpensTheFileWithoutCredentials(t *testing.T) {
	app := staticsApp(t, "", map[string]string{"admin": "secret"}, staticsSecret)

	if got := statusOf(t, app, signedPath("qrcode/scan-qr-slot.png", time.Now().Add(time.Minute)), "", ""); got != http.StatusOK {
		t.Fatalf("signed URL: status = %d, want 200", got)
	}
	if got := statusOf(t, app, "/statics/qrcode/scan-qr-slot.png", "admin", "secret"); got != http.StatusOK {
		t.Fatalf("Basic Auth must keep working: status = %d", got)
	}
	if got := statusOf(t, app, "/statics/qrcode/scan-qr-slot.png", "", ""); got != http.StatusUnauthorized {
		t.Fatalf("no signature and no credentials: status = %d, want 401", got)
	}
}

func TestSignedURLThatExpiredOrWasTamperedIsRefused(t *testing.T) {
	app := staticsApp(t, "", map[string]string{"admin": "secret"}, staticsSecret)

	if got := statusOf(t, app, signedPath("qrcode/scan-qr-slot.png", time.Now().Add(-time.Second)), "", ""); got != http.StatusUnauthorized {
		t.Fatalf("expired: status = %d, want 401", got)
	}
	// A signature for another file does not open this one.
	other := "/statics/qrcode/scan-qr-slot.png?" + staticurl.Sign(staticsSecret, "qrcode/other.png", time.Now().Add(time.Minute))
	if got := statusOf(t, app, other, "", ""); got != http.StatusUnauthorized {
		t.Fatalf("another file's signature: status = %d, want 401", got)
	}
	wrongSecret := "/statics/qrcode/scan-qr-slot.png?" + staticurl.Sign([]byte("another-secret"), "qrcode/scan-qr-slot.png", time.Now().Add(time.Minute))
	if got := statusOf(t, app, wrongSecret, "", ""); got != http.StatusUnauthorized {
		t.Fatalf("wrong secret: status = %d, want 401", got)
	}
}

func TestSignedURLHonorsTheBasePath(t *testing.T) {
	app := staticsApp(t, "/gowa", map[string]string{"admin": "secret"}, staticsSecret)

	rel := "qrcode/scan-qr-slot.png"
	if got := statusOf(t, app, "/gowa"+signedPath(rel, time.Now().Add(time.Minute)), "", ""); got != http.StatusOK {
		t.Fatalf("status = %d, want 200 under the base path", got)
	}
}

func TestWithoutASecretASignatureIsIgnored(t *testing.T) {
	app := staticsApp(t, "", map[string]string{"admin": "secret"})

	if got := statusOf(t, app, signedPath("qrcode/scan-qr-slot.png", time.Now().Add(time.Minute)), "", ""); got != http.StatusUnauthorized {
		t.Fatalf("no secret configured: status = %d, want 401", got)
	}
}

func TestASignatureNeverOpensAnythingWhenStaticsIsPublic(t *testing.T) {
	app := staticsApp(t, "", nil, staticsSecret)

	if got := statusOf(t, app, "/statics/qrcode/scan-qr-slot.png", "", ""); got != http.StatusOK {
		t.Fatalf("public statics: status = %d, want 200", got)
	}
}

func TestSignedURLForAFileNameThatNeedsEscaping(t *testing.T) {
	app := staticsApp(t, "", map[string]string{"admin": "secret"}, staticsSecret)

	// The issuer signs the decoded path; the request carries it percent-encoded.
	path := "/statics/qrcode/with%20space.png?" + staticurl.Sign(staticsSecret, "qrcode/with space.png", time.Now().Add(time.Minute))
	if got := statusOf(t, app, path, "", ""); got != http.StatusOK {
		t.Fatalf("status = %d, want 200 for an escaped file name", got)
	}
}
