package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	domainUser "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/user"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/ui/rest/middleware"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
)

type checkBatchStub struct {
	domainUser.IUserUsecase
	got       domainUser.CheckBatchRequest
	gotDevice *whatsapp.DeviceInstance
}

func (s *checkBatchStub) IsOnWhatsAppBatch(ctx context.Context, request domainUser.CheckBatchRequest) (domainUser.CheckBatchResponse, error) {
	s.got = request
	s.gotDevice, _ = whatsapp.DeviceFromContext(ctx)
	code := "upstream"
	return domainUser.CheckBatchResponse{{Query: "5511988887777", Status: "error", Error: &code}}, nil
}

func newCheckBatchApp(stub *checkBatchStub, device *whatsapp.DeviceInstance) *fiber.App {
	app := fiber.New()
	app.Use(middleware.Recovery())
	app.Use(func(c fiber.Ctx) error {
		c.Locals("device", device)
		return c.Next()
	})
	controller := User{Service: stub}
	app.Post("/user/check", controller.UserCheckBatch)
	return app
}

func TestUserCheckBatchReturnsItemsInTheEnvelope(t *testing.T) {
	stub := &checkBatchStub{}
	app := newCheckBatchApp(stub, nil)

	req := httptest.NewRequest(http.MethodPost, "/user/check", strings.NewReader(`{"phones":["5511988887777"]}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var body struct {
		Code    string                   `json:"code"`
		Results []map[string]interface{} `json:"results"`
	}
	assert.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "SUCCESS", body.Code)
	assert.Len(t, body.Results, 1)
	assert.Equal(t, "error", body.Results[0]["status"])
	assert.Nil(t, body.Results[0]["pn"]) // null, key present
	assert.Contains(t, body.Results[0], "lid")
	assert.Equal(t, []string{"5511988887777"}, stub.got.Phones)
}

func TestUserCheckBatchScopesTheRequestDevice(t *testing.T) {
	stub := &checkBatchStub{}
	device := &whatsapp.DeviceInstance{}
	app := newCheckBatchApp(stub, device)

	req := httptest.NewRequest(http.MethodPost, "/user/check", strings.NewReader(`{"phones":["5511988887777"]}`))
	req.Header.Set("Content-Type", "application/json")
	_, err := app.Test(req)
	assert.NoError(t, err)
	assert.Same(t, device, stub.gotDevice)
}

func TestUserCheckBatchRejectsMalformedBody(t *testing.T) {
	stub := &checkBatchStub{}
	app := newCheckBatchApp(stub, nil)

	req := httptest.NewRequest(http.MethodPost, "/user/check", strings.NewReader(`{"phones":`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, resp.StatusCode, 400)
	assert.Less(t, resp.StatusCode, 500)
}
