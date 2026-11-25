package rest

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"testing"

	domainLabel "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/label"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/ui/rest/middleware"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type labelServiceStub struct {
	err error
}

func (s *labelServiceStub) SyncLabels(context.Context) (domainLabel.SyncLabelsResponse, error) {
	return domainLabel.SyncLabelsResponse{SyncID: "abc123"}, s.err
}

func newLabelApp(service domainLabel.ILabelUsecase) *fiber.App {
	app := fiber.New()
	app.Use(middleware.Recovery())
	app.Use(func(c fiber.Ctx) error {
		c.Locals("device", whatsapp.NewDeviceInstance("device-a", nil, nil))
		return c.Next()
	})
	InitRestLabel(app, service)

	return app
}

func TestSyncLabelsReturns202WithSyncID(t *testing.T) {
	app := newLabelApp(&labelServiceStub{})
	resp, err := app.Test(httptest.NewRequest("POST", "/labels/sync", nil))
	require.NoError(t, err)
	assert.Equal(t, 202, resp.StatusCode)

	var out struct {
		Results domainLabel.SyncLabelsResponse `json:"results"`
	}
	raw, _ := io.ReadAll(resp.Body)
	require.NoError(t, json.Unmarshal(raw, &out))
	assert.Equal(t, "abc123", out.Results.SyncID)
}
