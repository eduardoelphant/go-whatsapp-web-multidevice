package rest

import (
	"io"
	"net/http/httptest"
	"testing"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCallPhonePage(t *testing.T) {
	orig := config.WhatsappCallsEnabled
	t.Cleanup(func() { config.WhatsappCallsEnabled = orig })
	app := fiber.New()
	InitRestCallPhone(app)

	config.WhatsappCallsEnabled = false
	resp, err := app.Test(httptest.NewRequest("GET", "/calls/phone", nil))
	require.NoError(t, err)
	assert.Equal(t, 404, resp.StatusCode)

	config.WhatsappCallsEnabled = true
	resp, err = app.Test(httptest.NewRequest("GET", "/calls/phone", nil))
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "text/html")
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{"new RTCPeerConnection", "waitIceComplete", `replace(/\r?\n/g, '\r\n')`,
		"/call/answer", "/call/start", "/call/hangup", "offer_sdp", "sdp_answer", "X-Device-Id", "if (busy || pc)", "busy = false"} {
		assert.Contains(t, string(body), want)
	}
}
