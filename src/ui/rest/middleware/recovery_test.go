package middleware

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"

	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/gofiber/fiber/v3"
	"github.com/sirupsen/logrus"
)

func recoveryLogFor(t *testing.T, panicValue any) (int, string) {
	t.Helper()
	var buf bytes.Buffer
	originalOut, originalLevel := logrus.StandardLogger().Out, logrus.GetLevel()
	logrus.SetOutput(&buf)
	logrus.SetLevel(logrus.InfoLevel)
	defer func() {
		logrus.SetOutput(originalOut)
		logrus.SetLevel(originalLevel)
	}()

	app := fiber.New()
	app.Use(Recovery())
	app.Get("/", func(fiber.Ctx) error { panic(panicValue) })
	resp, err := app.Test(httptest.NewRequest("GET", "/", nil))
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, buf.String()
}

func TestRecoveryLogsClientErrorsBelowErrorLevel(t *testing.T) {
	status, logged := recoveryLogFor(t, pkgError.ValidationError("phone: cannot be blank"))
	if status != 400 {
		t.Fatalf("expected status 400, got %d", status)
	}
	if strings.Contains(logged, "level=error") {
		t.Fatalf("expected a client error not to be logged at error level, got %q", logged)
	}
	if !strings.Contains(logged, "phone: cannot be blank") {
		t.Fatalf("expected the client error to stay in the log, got %q", logged)
	}
}

func TestRecoveryLogsServerErrorsAtErrorLevel(t *testing.T) {
	status, logged := recoveryLogFor(t, "boom")
	if status != 500 {
		t.Fatalf("expected status 500, got %d", status)
	}
	if !strings.Contains(logged, "level=error") {
		t.Fatalf("expected an unexpected panic to be logged at error level, got %q", logged)
	}
}
