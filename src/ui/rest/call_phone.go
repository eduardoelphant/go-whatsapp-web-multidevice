package rest

import (
	_ "embed"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/gofiber/fiber/v3"
)

// Fork (elphant): a bare test softphone for voice calls stage 2b, to try the browser leg without
// the CRM. It makes the same RTCPeerConnection calls as the CRM's useWebRtc.js.

//go:embed call_phone.html
var callPhoneHTML []byte

func InitRestCallPhone(app fiber.Router) {
	app.Get("/calls/phone", func(c fiber.Ctx) error {
		if !config.WhatsappCallsEnabled {
			return c.SendStatus(fiber.StatusNotFound)
		}
		c.Set(fiber.HeaderCacheControl, "no-store")
		c.Type("html")
		return c.Send(callPhoneHTML)
	})
}
