package rest

import (
	"errors"
	"mime"
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
	c.Set(fiber.HeaderContentDisposition, attachmentDisposition(stream.Filename))
	return c.SendStream(stream.File, int(stream.Size))
}

// attachmentDisposition encodes the filename per RFC 2231/6266 (non-ASCII as
// filename*=utf-8”…); an empty or unencodable name gives a bare "attachment".
func attachmentDisposition(filename string) string {
	if filename == "" {
		return "attachment"
	}
	if v := mime.FormatMediaType("attachment", map[string]string{"filename": filename}); v != "" {
		return v
	}
	return "attachment"
}
