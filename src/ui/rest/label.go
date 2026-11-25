package rest

import (
	domainLabel "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/label"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"github.com/gofiber/fiber/v3"
)

type Label struct {
	Service domainLabel.ILabelUsecase
}

func InitRestLabel(app fiber.Router, service domainLabel.ILabelUsecase) Label {
	rest := Label{Service: service}
	app.Post("/labels/sync", rest.SyncLabels)

	return rest
}

func (controller *Label) SyncLabels(c fiber.Ctx) error {
	response, err := controller.Service.SyncLabels(whatsapp.ContextWithDevice(c.Context(), getDeviceFromCtx(c)))
	utils.PanicIfNeeded(err)

	return c.Status(fiber.StatusAccepted).JSON(utils.ResponseData{
		Status:  fiber.StatusAccepted,
		Code:    "SUCCESS",
		Message: "Label sync started",
		Results: response,
	})
}
