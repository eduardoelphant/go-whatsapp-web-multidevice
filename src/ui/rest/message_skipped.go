package rest

import (
	"errors"
	"net/http"

	domainMessage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/message"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"github.com/gofiber/fiber/v3"
)

// Fork (elphant): GET /message/:message_id/skipped says what a message chat
// storage did not keep (no text, no media) was, e.g. protocol:EPHEMERAL_SETTING.
// 404 SKIPPED_MESSAGE_NOT_FOUND: the message was stored, never arrived, or the
// record expired (30 days).
func (controller *Message) SkippedMessage(c fiber.Ctx) error {
	res, err := controller.Service.SkippedMessage(whatsapp.ContextWithDevice(c.Context(), getDeviceFromCtx(c)), c.Params("message_id"))
	switch {
	case errors.Is(err, domainMessage.ErrSkippedMessageNotFound):
		return c.Status(http.StatusNotFound).JSON(utils.ResponseData{Status: http.StatusNotFound, Code: "SKIPPED_MESSAGE_NOT_FOUND", Message: err.Error()})
	case err != nil:
		utils.PanicIfNeeded(err)
	}
	return c.JSON(utils.ResponseData{Status: http.StatusOK, Code: "SUCCESS", Message: "Skipped message", Results: res})
}
