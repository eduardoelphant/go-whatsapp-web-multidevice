package rest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	domainUser "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/user"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/ui/rest/middleware"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
)

type checkSingleStub struct {
	domainUser.IUserUsecase
	gotDevice *whatsapp.DeviceInstance
}

func (s *checkSingleStub) IsOnWhatsApp(ctx context.Context, _ domainUser.CheckRequest) (domainUser.CheckResponse, error) {
	s.gotDevice, _ = whatsapp.DeviceFromContext(ctx)
	return domainUser.CheckResponse{IsOnWhatsApp: true}, nil
}

// GET /user/check must use the device of the request (X-Device-Id), not the default client.
func TestUserCheckScopesTheRequestDevice(t *testing.T) {
	stub := &checkSingleStub{}
	device := &whatsapp.DeviceInstance{}
	app := fiber.New()
	app.Use(middleware.Recovery())
	app.Use(func(c fiber.Ctx) error {
		c.Locals("device", device)
		return c.Next()
	})
	controller := User{Service: stub}
	app.Get("/user/check", controller.UserCheck)

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/user/check?phone=5511988887777", nil))
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Same(t, device, stub.gotDevice)
}
