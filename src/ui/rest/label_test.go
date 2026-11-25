package rest

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

func (s *labelServiceStub) CreateLabel(context.Context, domainLabel.CreateLabelRequest) (domainLabel.CreateLabelResponse, error) {
	return domainLabel.CreateLabelResponse{LabelID: "18"}, s.err
}

func (s *labelServiceStub) UpdateLabel(context.Context, domainLabel.UpdateLabelRequest) error {
	return s.err
}

func (s *labelServiceStub) DeleteLabel(context.Context, domainLabel.DeleteLabelRequest) error {
	return s.err
}

func (s *labelServiceStub) LabelChat(context.Context, domainLabel.LabelChatRequest) error {
	return s.err
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

type conflictErr struct{}

func (conflictErr) Error() string   { return "label sync already running for this device" }
func (conflictErr) ErrCode() string { return "LABEL_SYNC_RUNNING" }
func (conflictErr) StatusCode() int { return http.StatusConflict }

func TestSyncLabelsReturns409WhenAlreadyRunning(t *testing.T) {
	app := newLabelApp(&labelServiceStub{err: conflictErr{}})
	resp, err := app.Test(httptest.NewRequest("POST", "/labels/sync", nil))
	require.NoError(t, err)
	assert.Equal(t, 409, resp.StatusCode)
}

func TestCreateLabelReturnsLabelID(t *testing.T) {
	app := newLabelApp(&labelServiceStub{})
	req := httptest.NewRequest(http.MethodPost, "/labels", strings.NewReader(`{"name":"VIP","color":3}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	raw, _ := io.ReadAll(resp.Body)
	var body struct {
		Results struct {
			LabelID string `json:"label_id"`
		} `json:"results"`
	}
	require.NoError(t, json.Unmarshal(raw, &body))
	assert.Equal(t, "18", body.Results.LabelID)
}

func TestLabelWriteRoutesExist(t *testing.T) {
	app := newLabelApp(&labelServiceStub{})
	cases := []struct{ method, path, body string }{
		{http.MethodPatch, "/labels/18", `{"name":"VIP","color":3}`},
		{http.MethodDelete, "/labels/18", `{"name":"VIP"}`},
		{http.MethodPost, "/labels/18/chats", `{"chat_jid":"5511999999999@s.whatsapp.net","labeled":true}`},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode, tc.method+" "+tc.path)
	}
}
