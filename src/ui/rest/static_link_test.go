package rest

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/staticurl"
	"github.com/gofiber/fiber/v3"
)

func withStaticsConfig(t *testing.T, auth bool, secret string, credentials []string) {
	t.Helper()
	prevAuth, prevSecret, prevCreds, prevTTL := config.AppStaticsAuth, config.AppStaticsSecret, config.AppBasicAuthCredential, config.AppStaticsURLTTLMinutes
	config.AppStaticsAuth, config.AppStaticsSecret, config.AppBasicAuthCredential, config.AppStaticsURLTTLMinutes = auth, secret, credentials, 15
	t.Cleanup(func() {
		config.AppStaticsAuth, config.AppStaticsSecret, config.AppBasicAuthCredential, config.AppStaticsURLTTLMinutes = prevAuth, prevSecret, prevCreds, prevTTL
	})
}

func linkFor(t *testing.T, build func(fiber.Ctx) string) string {
	t.Helper()
	app := fiber.New()
	var got string
	app.Get("/x", func(c fiber.Ctx) error {
		got = build(c)
		return c.SendStatus(200)
	})
	if _, err := app.Test(mustRequest(t, "/x")); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestQRLinkIsUnchangedWhileSigningIsOff(t *testing.T) {
	withStaticsConfig(t, false, "", nil)

	got := linkFor(t, func(c fiber.Ctx) string { return staticLink(c, "statics/qrcode/scan-qr-dev1.png") })

	if strings.Contains(got, "?") || !strings.HasSuffix(got, "/statics/qrcode/scan-qr-dev1.png") {
		t.Fatalf("link = %q, want the plain path", got)
	}
}

func TestQRLinkIsSignedWhenStaticsAuthHasASecret(t *testing.T) {
	withStaticsConfig(t, true, "a-long-random-secret", []string{"admin:secret"})

	got := linkFor(t, func(c fiber.Ctx) string { return staticLink(c, "statics/qrcode/scan-qr-dev1.png") })

	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	q := parsed.Query()
	if !staticurl.Verify([]byte("a-long-random-secret"), "qrcode/scan-qr-dev1.png", q.Get("exp"), q.Get("sig"), time.Now()) {
		t.Fatalf("link %q does not carry a valid signature for qrcode/scan-qr-dev1.png", got)
	}
	if staticurl.Verify([]byte("a-long-random-secret"), "qrcode/scan-qr-dev1.png", q.Get("exp"), q.Get("sig"), time.Now().Add(16*time.Minute)) {
		t.Fatal("the signature must expire with the configured 15 minutes")
	}
}

func TestMediaURLIsSignedOverTheDecodedPath(t *testing.T) {
	withStaticsConfig(t, true, "a-long-random-secret", []string{"admin:secret"})

	got := linkFor(t, func(c fiber.Ctx) string { return publicStaticFileURL(c, "statics/media/a b.jpeg") })

	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(parsed.EscapedPath(), "a%20b.jpeg") {
		t.Fatalf("path = %q, want the escaped file name", parsed.EscapedPath())
	}
	q := parsed.Query()
	if !staticurl.Verify([]byte("a-long-random-secret"), "media/a b.jpeg", q.Get("exp"), q.Get("sig"), time.Now()) {
		t.Fatalf("link %q does not carry a signature over the decoded path", got)
	}
}

func TestNoSignatureWithoutASecretOrWithoutStaticsAuthOrWithoutBasicAuth(t *testing.T) {
	for name, set := range map[string]func(*testing.T){
		"no secret":      func(t *testing.T) { withStaticsConfig(t, true, "", []string{"admin:secret"}) },
		"statics public": func(t *testing.T) { withStaticsConfig(t, false, "a-long-random-secret", []string{"admin:secret"}) },
		"no basic auth":  func(t *testing.T) { withStaticsConfig(t, true, "a-long-random-secret", nil) },
	} {
		set(t)
		got := linkFor(t, func(c fiber.Ctx) string { return publicStaticFileURL(c, "statics/media/a.jpeg") })
		if strings.Contains(got, "?") {
			t.Errorf("%s: link %q must not be signed", name, got)
		}
	}
}

func mustRequest(t *testing.T, path string) *http.Request {
	t.Helper()
	return httptest.NewRequest(http.MethodGet, path, nil)
}
