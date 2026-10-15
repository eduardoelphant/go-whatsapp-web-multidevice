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
	"github.com/aldinokemal/go-whatsapp-web-multidevice/ui/rest/middleware"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *callServiceStub) SetDeviceCalls(_ context.Context, deviceID string, enabled bool) (domainCall.DeviceCallsResponse, error) {
	if s.err != nil {
		return domainCall.DeviceCallsResponse{}, s.err
	}
	s.switchDevice, s.switchValue = deviceID, enabled
	return domainCall.DeviceCallsResponse{Enabled: enabled}, nil
}

func (s *callServiceStub) GetDeviceCalls(_ context.Context, deviceID string) (domainCall.DeviceCallsResponse, error) {
	s.switchDevice = deviceID
	return domainCall.DeviceCallsResponse{Enabled: s.switchValue}, s.err
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
