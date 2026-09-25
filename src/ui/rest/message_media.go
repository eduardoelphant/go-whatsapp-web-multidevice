package rest

import (
	"errors"
	"fmt"
	"net/http"

	domainMessage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/message"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"github.com/gofiber/fiber/v3"
)

// Fork (elphant): GET /message/:message_id/media streams the media file and
// never writes it to /statics.
func (controller *Message) StreamMedia(c fiber.Ctx) error {
	stream, err := controller.Service.StreamMedia(whatsapp.ContextWithDevice(c.Context(), getDeviceFromCtx(c)), c.Params("message_id"))
	switch {
	case errors.Is(err, domainMessage.ErrMediaNotFound):
		return c.Status(http.StatusNotFound).JSON(utils.ResponseData{Status: http.StatusNotFound, Code: "MEDIA_NOT_FOUND", Message: err.Error()})
	case errors.Is(err, domainMessage.ErrMediaGone):
		return c.Status(http.StatusGone).JSON(utils.ResponseData{Status: http.StatusGone, Code: "MEDIA_GONE", Message: err.Error()})
	case err != nil:
		utils.PanicIfNeeded(err)
	}
	c.Set(fiber.HeaderContentType, stream.Mime)
	if stream.Filename != "" {
		c.Set(fiber.HeaderContentDisposition, fmt.Sprintf("attachment; filename=%q", stream.Filename))
	}
	return c.SendStream(stream.File, int(stream.Size))
}
