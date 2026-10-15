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
	answeredSDP   string
	started       string
	contextDevice *whatsapp.DeviceInstance
	switchDevice  string
	switchValue   bool
}

func (s *callServiceStub) AnswerCall(ctx context.Context, request domainCall.AnswerCallRequest) (domainCall.AnswerCallResponse, error) {
	s.answered, s.answeredSDP = request.CallID, request.SDP
	s.contextDevice, _ = whatsapp.DeviceFromContext(ctx)
	return domainCall.AnswerCallResponse{CallID: request.CallID}, s.err
}
func (s *callServiceStub) StartCall(ctx context.Context, request domainCall.StartCallRequest) (domainCall.StartCallResponse, error) {
	s.started = request.Phone
	return domainCall.StartCallResponse{CallID: "OUT-1", SDPAnswer: "v=0 gowa-answer"}, s.err
}
func (s *callServiceStub) GetCall(ctx context.Context, callID string) (domainCall.CallDetail, error) {
	return domainCall.CallDetail{CallID: callID, State: "ringing", Direction: "USER_INITIATED", OfferSDP: "v=0 gowa-offer"}, s.err
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

	post := func(path, body string) map[string]any {
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		require.NoError(t, err)
		require.Equal(t, 200, resp.StatusCode, path)
		var out struct {
			Results map[string]any `json:"results"`
		}
		raw, _ := io.ReadAll(resp.Body)
		require.NoError(t, json.Unmarshal(raw, &out))
		return out.Results
	}

	res := post("/call/answer", `{"call_id":"IN-1","sdp":"v=0 browser-answer"}`)
	assert.Equal(t, "IN-1", res["call_id"])
	assert.Equal(t, "v=0 browser-answer", service.answeredSDP)
	assert.Same(t, device, service.contextDevice)

	res = post("/call/start", `{"phone":"+5511900000000","sdp":"v=0 browser-offer"}`)
	assert.Equal(t, "OUT-1", res["call_id"])
	assert.Equal(t, "v=0 gowa-answer", res["sdp_answer"])
	assert.Equal(t, "+5511900000000", service.started)

	resp, err := app.Test(httptest.NewRequest("GET", "/call/IN-1", nil))
	require.NoError(t, err)
	raw, _ := io.ReadAll(resp.Body)
	var detail struct {
		Results domainCall.CallDetail `json:"results"`
	}
	require.NoError(t, json.Unmarshal(raw, &detail))
	assert.Equal(t, "v=0 gowa-offer", detail.Results.OfferSDP)

	resp, err = app.Test(httptest.NewRequest("GET", "/calls/active", nil))
	require.NoError(t, err)
	raw, _ = io.ReadAll(resp.Body)
	var active struct {
		Results []domainCall.CallInfo `json:"results"`
	}
	require.NoError(t, json.Unmarshal(raw, &active))
	require.Len(t, active.Results, 1)
	assert.Equal(t, "IN-1", active.Results[0].CallID)
}

func TestAnswerTwiceIs409(t *testing.T) {
	app := newCallApp(&callServiceStub{err: whatsapp.ErrCallAnswered}, whatsapp.NewDeviceInstance("device-a", nil, nil))
	req := httptest.NewRequest("POST", "/call/answer", strings.NewReader(`{"call_id":"IN-1","sdp":"v=0"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 409, resp.StatusCode)
}

func TestCallRoutesDisabledDevice(t *testing.T) {
	device := whatsapp.NewDeviceInstance("device-a", nil, nil)
	app := newCallApp(&callServiceStub{err: whatsapp.ErrCallsOff}, device)
	for _, r := range []struct{ method, path, body string }{
		{"POST", "/call/answer", `{"call_id":"IN-1","sdp":"v=0"}`},
		{"POST", "/call/start", `{"phone":"+5511900000000","sdp":"v=0"}`},
		{"POST", "/call/hangup", `{"call_id":"IN-1"}`},
		{"GET", "/calls/active", ""},
		{"GET", "/call/IN-1", ""},
	} {
		req := httptest.NewRequest(r.method, r.path, strings.NewReader(r.body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		require.NoError(t, err)
		assert.Equal(t, 400, resp.StatusCode, r.path)
	}
}
