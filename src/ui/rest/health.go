package rest

import (
	"fmt"
	"sort"
	"strings"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/safego"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"github.com/gofiber/fiber/v3"
)

// HealthProvider is what the health routes read; the reconnect watchdog implements it.
type HealthProvider interface {
	Snapshot() []whatsapp.DeviceHealth
	Counters() (attempts, successes int64)
}

// Health serves GET /health/devices and GET /metrics (fork: elphant, spec 2026-09-30-gateway-g9).
// Both sit behind Basic Auth: /health/devices lists device ids. The public /health is unchanged.
type Health struct {
	Provider HealthProvider
}

func InitRestHealth(app fiber.Router, provider HealthProvider) Health {
	rest := Health{Provider: provider}
	app.Get("/health/devices", rest.Devices)
	app.Get("/metrics", rest.Metrics)
	return rest
}

func (h Health) snapshot() []whatsapp.DeviceHealth {
	if h.Provider == nil {
		return []whatsapp.DeviceHealth{}
	}
	devices := h.Provider.Snapshot()
	if devices == nil {
		return []whatsapp.DeviceHealth{}
	}
	return devices
}

func (h Health) counters() (int64, int64) {
	if h.Provider == nil {
		return 0, 0
	}
	return h.Provider.Counters()
}

func (h Health) Devices(c fiber.Ctx) error {
	return c.JSON(utils.ResponseData{
		Status:  200,
		Code:    "SUCCESS",
		Message: "Device health",
		Results: map[string]any{
			"devices":          h.snapshot(),
			"panics_recovered": safego.Panics(),
		},
	})
}

// Metrics writes the Prometheus text format by hand, so there is no new dependency.
func (h Health) Metrics(c fiber.Ctx) error {
	attempts, successes := h.counters()
	states := map[string]int{}
	for _, d := range h.snapshot() {
		states[d.State]++
	}
	names := make([]string, 0, len(states))
	for name := range states {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	metric := func(name, kind, help string, value int64) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n%s %d\n", name, help, name, kind, name, value)
	}
	metric("gowa_goroutine_panics_total", "counter", "Panics recovered in background goroutines.", safego.Panics())
	metric("gowa_reconnect_attempts_total", "counter", "Reconnect attempts made by the watchdog.", attempts)
	metric("gowa_reconnect_successes_total", "counter", "Reconnect attempts that connected.", successes)
	b.WriteString("# HELP gowa_devices Devices by state.\n# TYPE gowa_devices gauge\n")
	for _, name := range names {
		fmt.Fprintf(&b, "gowa_devices{state=%q} %d\n", name, states[name])
	}

	c.Set(fiber.HeaderContentType, "text/plain; version=0.0.4; charset=utf-8")
	return c.SendString(b.String())
}
