package rest

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"testing"
	"time"

	domainMessage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/message"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
)

type skippedMessageServiceStub struct {
	domainMessage.IMessageUsecase
	res domainMessage.SkippedMessageResponse
	err error
	id  string
}

func (stub *skippedMessageServiceStub) SkippedMessage(_ context.Context, messageID string) (domainMessage.SkippedMessageResponse, error) {
	stub.id = messageID
	return stub.res, stub.err
}

func TestSkippedMessageRouteReturnsTheReason(t *testing.T) {
	app := fiber.New()
	service := &skippedMessageServiceStub{res: domainMessage.SkippedMessageResponse{
		MessageID: "FE599AAD8E1162D719", ChatJID: "5548999990000@s.whatsapp.net", Reason: "protocol:EPHEMERAL_SETTING",
		Fields: []string{"protocolMessage"}, Timestamp: time.Date(2026, 10, 27, 17, 57, 41, 0, time.UTC),
	}}
	InitRestMessage(app, service, nil)

	response, err := app.Test(httptest.NewRequest("GET", "/message/FE599AAD8E1162D719/skipped", nil))
	require.NoError(t, err)
	require.Equal(t, fiber.StatusOK, response.StatusCode)
	require.Equal(t, "FE599AAD8E1162D719", service.id)

	body, _ := io.ReadAll(response.Body)
	var decoded struct {
		Code    string                               `json:"code"`
		Results domainMessage.SkippedMessageResponse `json:"results"`
	}
	require.NoError(t, json.Unmarshal(body, &decoded))
	require.Equal(t, "SUCCESS", decoded.Code)
	require.Equal(t, "protocol:EPHEMERAL_SETTING", decoded.Results.Reason)
	require.Equal(t, []string{"protocolMessage"}, decoded.Results.Fields)
}

func TestSkippedMessageRouteNotFound(t *testing.T) {
	app := fiber.New()
	InitRestMessage(app, &skippedMessageServiceStub{err: domainMessage.ErrSkippedMessageNotFound}, nil)

	response, err := app.Test(httptest.NewRequest("GET", "/message/UNKNOWN/skipped", nil))
	require.NoError(t, err)
	require.Equal(t, fiber.StatusNotFound, response.StatusCode)
	body, _ := io.ReadAll(response.Body)
	require.Contains(t, string(body), "SKIPPED_MESSAGE_NOT_FOUND")
}
