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

type callServiceStub struct {
	domainCall.ICallUsecase
	err           error
	answered      string
	started       string
	contextDevice *whatsapp.DeviceInstance
	switchDevice  string
	switchValue   bool
}

func (s *callServiceStub) AnswerCall(ctx context.Context, id string) error {
	s.answered = id
	s.contextDevice, _ = whatsapp.DeviceFromContext(ctx)
	return s.err
}
func (s *callServiceStub) StartCall(ctx context.Context, phone string) (domainCall.CallInfo, error) {
	s.started = phone
	return domainCall.CallInfo{CallID: "OUT-1", Direction: "BUSINESS_INITIATED", State: "ringing"}, s.err
}
func (s *callServiceStub) HangupCall(ctx context.Context, id string) error { return s.err }
func (s *callServiceStub) ActiveCalls(ctx context.Context) ([]domainCall.CallInfo, error) {
	return []domainCall.CallInfo{{CallID: "IN-1", State: "ringing"}}, s.err
}

func newCallApp(service domainCall.ICallUsecase, device *whatsapp.DeviceInstance) *fiber.App {
	app := fiber.New()
	app.Use(middleware.Recovery()) // handlers panic through utils.PanicIfNeeded, as in production
	app.Use(func(c fiber.Ctx) error { c.Locals("device", device); return c.Next() })
	InitRestCall(app, service)
	return app
}

func TestCallRoutesHappyPath(t *testing.T) {
	device := whatsapp.NewDeviceInstance("device-a", nil, nil)
	service := &callServiceStub{}
	app := newCallApp(service, device)

	req := httptest.NewRequest("POST", "/call/answer", strings.NewReader(`{"call_id":"IN-1"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, "IN-1", service.answered)
	assert.Same(t, device, service.contextDevice)

	req = httptest.NewRequest("POST", "/call/start", strings.NewReader(`{"phone":"+5511900000000"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err = app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, "+5511900000000", service.started)

	resp, err = app.Test(httptest.NewRequest("GET", "/calls/active", nil))
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	var out struct {
		Results []domainCall.CallInfo `json:"results"`
	}
	require.NoError(t, json.Unmarshal(body, &out))
	require.Len(t, out.Results, 1)
	assert.Equal(t, "IN-1", out.Results[0].CallID)
}

func TestCallRoutesDisabledDevice(t *testing.T) {
	device := whatsapp.NewDeviceInstance("device-a", nil, nil)
	app := newCallApp(&callServiceStub{err: whatsapp.ErrCallsOff}, device)
	for _, r := range []struct{ method, path, body string }{
		{"POST", "/call/answer", `{"call_id":"IN-1"}`},
		{"POST", "/call/start", `{"phone":"+5511900000000"}`},
		{"POST", "/call/hangup", `{"call_id":"IN-1"}`},
		{"GET", "/calls/active", ""},
	} {
		req := httptest.NewRequest(r.method, r.path, strings.NewReader(r.body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		require.NoError(t, err)
		assert.Equal(t, 400, resp.StatusCode, r.path)
	}
}
