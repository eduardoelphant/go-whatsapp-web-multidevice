package rest

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/ui/rest/middleware"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
)

type healthStub struct {
	devices             []whatsapp.DeviceHealth
	attempts, successes int64
}

func (h healthStub) Snapshot() []whatsapp.DeviceHealth { return h.devices }
func (h healthStub) Counters() (int64, int64)          { return h.attempts, h.successes }

func newHealthApp(provider HealthProvider) *fiber.App {
	app := fiber.New()
	app.Use(middleware.Recovery())
	InitRestHealth(app, provider)
	return app
}

func getBody(t *testing.T, app *fiber.App, path string) (*http.Response, string) {
	t.Helper()
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, path, nil))
	assert.NoError(t, err)
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestHealthDevicesShape(t *testing.T) {
	ends := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	app := newHealthApp(healthStub{devices: []whatsapp.DeviceHealth{
		{ID: "slot-1", State: "logged_in", Connected: true, LoggedIn: true, LastConnectedAt: &ends},
		{ID: "slot-2", State: "disconnected", ReconnectAttempts: 2, NextAttemptAt: &ends},
	}})

	resp, body := getBody(t, app, "/health/devices")

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var parsed struct {
		Code    string `json:"code"`
		Results struct {
			Devices []map[string]interface{} `json:"devices"`
			Panics  float64                  `json:"panics_recovered"`
		} `json:"results"`
	}
	assert.NoError(t, json.Unmarshal([]byte(body), &parsed))
	assert.Equal(t, "SUCCESS", parsed.Code)
	assert.Len(t, parsed.Results.Devices, 2)
	first := parsed.Results.Devices[0]
	assert.Equal(t, "slot-1", first["id"])
	assert.Equal(t, true, first["connected"])
	assert.Equal(t, "2026-09-30T12:00:00Z", first["last_connected_at"])
	assert.Nil(t, first["next_attempt_at"]) // null, key present
	assert.Contains(t, first, "next_attempt_at")
	assert.Equal(t, float64(2), parsed.Results.Devices[1]["reconnect_attempts"])
}

func TestHealthDevicesWithANilProviderIsEmpty(t *testing.T) {
	app := newHealthApp(nil)

	resp, body := getBody(t, app, "/health/devices")

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, body, `"devices":[]`)
}

func TestMetricsTextFormat(t *testing.T) {
	app := newHealthApp(healthStub{
		devices: []whatsapp.DeviceHealth{
			{ID: "slot-secret-1", State: "logged_in"},
			{ID: "slot-secret-2", State: "logged_in"},
			{ID: "slot-secret-3", State: "disconnected"},
		},
		attempts: 7, successes: 5,
	})

	resp, body := getBody(t, app, "/metrics")

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/plain; version=0.0.4; charset=utf-8", resp.Header.Get("Content-Type"))
	for _, want := range []string{
		"# HELP gowa_goroutine_panics_total",
		"# TYPE gowa_goroutine_panics_total counter",
		"gowa_goroutine_panics_total ",
		"# TYPE gowa_reconnect_attempts_total counter",
		"gowa_reconnect_attempts_total 7\n",
		"gowa_reconnect_successes_total 5\n",
		"# TYPE gowa_devices gauge",
		`gowa_devices{state="logged_in"} 2` + "\n",
		`gowa_devices{state="disconnected"} 1` + "\n",
	} {
		assert.Contains(t, body, want)
	}
	assert.False(t, strings.Contains(body, "slot-secret"), "device ids must never appear in the metrics")
}

func TestMetricsWithANilProviderReportsZeros(t *testing.T) {
	app := newHealthApp(nil)

	_, body := getBody(t, app, "/metrics")

	assert.Contains(t, body, "gowa_reconnect_attempts_total 0\n")
}
