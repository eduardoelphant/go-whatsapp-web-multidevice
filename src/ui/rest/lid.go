package rest

import (
	domainLID "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/lid"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"github.com/gofiber/fiber/v3"
)

// LID serves the LID and phone mapping endpoints (fork: elphant, docs/reference/elphant-fork.md).
type LID struct {
	Service domainLID.ILIDUsecase
}

// InitRestLID registers the routes. `/lids/pn/:phone` and the POST lookup never collide with
// `/lids/:lid`, which is registered last.
func InitRestLID(app fiber.Router, service domainLID.ILIDUsecase) LID {
	rest := LID{Service: service}
	app.Get("/lids/pn/:phone", rest.PNToLID)
	app.Post("/lids/lookup", rest.Lookup)
	app.Get("/lids", rest.List)
	app.Get("/lids/:lid", rest.LIDToPN)
	return rest
}

func (controller *LID) PNToLID(c fiber.Ctx) error {
	ctx := whatsapp.ContextWithDevice(c.Context(), getDeviceFromCtx(c))
	response, err := controller.Service.PNToLID(ctx, c.Params("phone"))
	utils.PanicIfNeeded(err)
	return c.JSON(utils.ResponseData{Status: 200, Code: "SUCCESS", Message: "Success lookup lid", Results: response})
}

func (controller *LID) LIDToPN(c fiber.Ctx) error {
	ctx := whatsapp.ContextWithDevice(c.Context(), getDeviceFromCtx(c))
	response, err := controller.Service.LIDToPN(ctx, c.Params("lid"))
	utils.PanicIfNeeded(err)
	return c.JSON(utils.ResponseData{Status: 200, Code: "SUCCESS", Message: "Success lookup phone", Results: response})
}

func (controller *LID) Lookup(c fiber.Ctx) error {
	var request domainLID.LookupRequest
	if err := c.Bind().Body(&request); err != nil {
		return c.Status(400).JSON(utils.ResponseData{Status: 400, Code: "BAD_REQUEST", Message: "Invalid request body"})
	}
	ctx := whatsapp.ContextWithDevice(c.Context(), getDeviceFromCtx(c))
	response, err := controller.Service.Lookup(ctx, request)
	utils.PanicIfNeeded(err)
	return c.JSON(utils.ResponseData{Status: 200, Code: "SUCCESS", Message: "Success lookup lids", Results: response})
}

// List is global to the gateway: whatsmeow's LID map has no device column.
func (controller *LID) List(c fiber.Ctx) error {
	var request domainLID.ListRequest
	if err := c.Bind().Query(&request); err != nil {
		return c.Status(400).JSON(utils.ResponseData{Status: 400, Code: "BAD_REQUEST", Message: "Invalid query"})
	}
	response, err := controller.Service.List(c.Context(), request)
	utils.PanicIfNeeded(err)
	return c.JSON(utils.ResponseData{Status: 200, Code: "SUCCESS", Message: "Success list lids", Results: response})
}
