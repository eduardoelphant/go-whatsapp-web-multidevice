package rest

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	domainCall "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/call"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/ui/rest/middleware"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *callServiceStub) SetDeviceCalls(_ context.Context, deviceID string, enabled bool, maxCalls *int) (domainCall.DeviceCallsResponse, error) {
	if s.err != nil {
		return domainCall.DeviceCallsResponse{}, s.err
	}
	s.switchDevice, s.switchValue, s.switchMax = deviceID, enabled, maxCalls
	limit := 1
	if maxCalls != nil {
		limit = *maxCalls
	}
	return domainCall.DeviceCallsResponse{Enabled: enabled, MaxCalls: limit, MaxCallsCeiling: 3, MaxCallsTotal: 6}, nil
}

func (s *callServiceStub) GetDeviceCalls(_ context.Context, deviceID string) (domainCall.DeviceCallsResponse, error) {
	s.switchDevice = deviceID
	return domainCall.DeviceCallsResponse{Enabled: s.switchValue, MaxCalls: 2, MaxCallsCeiling: 3, MaxCallsTotal: 6}, s.err
}

func TestDeviceCallsRoutes(t *testing.T) {
	service := &callServiceStub{}
	app := fiber.New()
	app.Use(middleware.Recovery())
	InitRestDeviceCalls(app, service)

	put := func(body string) int {
		req := httptest.NewRequest("PUT", "/devices/slot-a/calls", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		require.NoError(t, err)
		return resp.StatusCode
	}

	assert.Equal(t, 200, put(`{"enabled":true}`))
	assert.Equal(t, "slot-a", service.switchDevice)
	assert.True(t, service.switchValue)
	assert.Equal(t, 400, put(`{}`), "enabled is required")

	resp, err := app.Test(httptest.NewRequest("GET", "/devices/slot-a/calls", nil))
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	var out struct {
		Results domainCall.DeviceCallsResponse `json:"results"`
	}
	require.NoError(t, json.Unmarshal(body, &out))
	assert.True(t, out.Results.Enabled)

	service.err = whatsapp.ErrCallsMasterOff
	assert.Equal(t, 400, put(`{"enabled":true}`))
}

func TestDeviceCallsRoutesCarryMaxCalls(t *testing.T) {
	service := &callServiceStub{}
	app := fiber.New()
	app.Use(middleware.Recovery())
	InitRestDeviceCalls(app, service)

	put := func(body string) (int, map[string]any) {
		req := httptest.NewRequest("PUT", "/devices/slot-a/calls", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		require.NoError(t, err)
		raw, _ := io.ReadAll(resp.Body)
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		return resp.StatusCode, out
	}

	code, out := put(`{"enabled":true,"max_calls":3}`)
	assert.Equal(t, 200, code)
	require.NotNil(t, service.switchMax)
	assert.Equal(t, 3, *service.switchMax)
	assert.Equal(t, map[string]any{"enabled": true, "max_calls": float64(3), "max_calls_ceiling": float64(3), "max_calls_total": float64(6)}, out["results"])

	code, _ = put(`{"enabled":true}`)
	assert.Equal(t, 200, code)
	assert.Nil(t, service.switchMax, "omitted max_calls is passed as nil (unchanged)")
	code, _ = put(`{"enabled":true,"max_calls":null}`)
	assert.Equal(t, 200, code)
	assert.Nil(t, service.switchMax)

	service.err = pkgError.ValidationError("max_calls must be between 1 and 3")
	code, out = put(`{"enabled":true,"max_calls":4}`)
	assert.Equal(t, 400, code)
	assert.Equal(t, "max_calls must be between 1 and 3", out["message"])
	service.err = nil

	resp, err := app.Test(httptest.NewRequest("GET", "/devices/slot-a/calls", nil))
	require.NoError(t, err)
	raw, _ := io.ReadAll(resp.Body)
	var got struct {
		Results domainCall.DeviceCallsResponse `json:"results"`
	}
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Equal(t, domainCall.DeviceCallsResponse{Enabled: true, MaxCalls: 2, MaxCallsCeiling: 3, MaxCallsTotal: 6}, got.Results)
}
