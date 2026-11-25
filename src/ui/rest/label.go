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
	app.Post("/labels", rest.CreateLabel)
	app.Patch("/labels/:label_id", rest.UpdateLabel)
	app.Delete("/labels/:label_id", rest.DeleteLabel)
	app.Post("/labels/:label_id/chats", rest.LabelChat)

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

func labelBadBody(c fiber.Ctx) error {
	return c.Status(fiber.StatusBadRequest).JSON(utils.ResponseData{
		Status:  fiber.StatusBadRequest,
		Code:    "BAD_REQUEST",
		Message: "Invalid request body",
	})
}

func (controller *Label) CreateLabel(c fiber.Ctx) error {
	var request domainLabel.CreateLabelRequest
	if err := c.Bind().Body(&request); err != nil {
		return labelBadBody(c)
	}

	response, err := controller.Service.CreateLabel(whatsapp.ContextWithDevice(c.Context(), getDeviceFromCtx(c)), request)
	utils.PanicIfNeeded(err)

	return c.JSON(utils.ResponseData{
		Status:  fiber.StatusOK,
		Code:    "SUCCESS",
		Message: "Label created",
		Results: response,
	})
}

func (controller *Label) UpdateLabel(c fiber.Ctx) error {
	var request domainLabel.UpdateLabelRequest
	if err := c.Bind().Body(&request); err != nil {
		return labelBadBody(c)
	}
	request.LabelID = c.Params("label_id")

	err := controller.Service.UpdateLabel(whatsapp.ContextWithDevice(c.Context(), getDeviceFromCtx(c)), request)
	utils.PanicIfNeeded(err)

	return c.JSON(utils.ResponseData{Status: fiber.StatusOK, Code: "SUCCESS", Message: "Label updated"})
}

func (controller *Label) DeleteLabel(c fiber.Ctx) error {
	var request domainLabel.DeleteLabelRequest
	if err := c.Bind().Body(&request); err != nil {
		return labelBadBody(c)
	}
	request.LabelID = c.Params("label_id")

	err := controller.Service.DeleteLabel(whatsapp.ContextWithDevice(c.Context(), getDeviceFromCtx(c)), request)
	utils.PanicIfNeeded(err)

	return c.JSON(utils.ResponseData{Status: fiber.StatusOK, Code: "SUCCESS", Message: "Label deleted"})
}

func (controller *Label) LabelChat(c fiber.Ctx) error {
	var request domainLabel.LabelChatRequest
	if err := c.Bind().Body(&request); err != nil {
		return labelBadBody(c)
	}
	request.LabelID = c.Params("label_id")

	err := controller.Service.LabelChat(whatsapp.ContextWithDevice(c.Context(), getDeviceFromCtx(c)), request)
	utils.PanicIfNeeded(err)

	return c.JSON(utils.ResponseData{Status: fiber.StatusOK, Code: "SUCCESS", Message: "Label chat updated"})
}
