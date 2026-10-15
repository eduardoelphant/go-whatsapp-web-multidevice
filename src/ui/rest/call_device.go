package rest

import (
	domainCall "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/call"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"github.com/gofiber/fiber/v3"
)

// Fork (elphant): the per-device voice calls switch (stage 2b, B7). The routes sit with the
// other /devices/:device_id routes, before the header-scoped device group.

func InitRestDeviceCalls(app fiber.Router, service domainCall.ICallUsecase) {
	handler := Call{Service: service}
	app.Put("/devices/:device_id/calls", handler.SetDeviceCalls)
	app.Get("/devices/:device_id/calls", handler.GetDeviceCalls)
}

func (controller *Call) SetDeviceCalls(c fiber.Ctx) error {
	var request domainCall.DeviceCallsRequest
	utils.PanicIfNeeded(c.Bind().Body(&request))
	if request.Enabled == nil {
		utils.PanicIfNeeded(pkgError.ValidationError("enabled is required"))
	}
	res, err := controller.Service.SetDeviceCalls(c.Context(), pathDeviceID(c), *request.Enabled)
	utils.PanicIfNeeded(err)
	return c.JSON(utils.ResponseData{Status: 200, Code: "SUCCESS", Message: "Calls switch updated", Results: res})
}

func (controller *Call) GetDeviceCalls(c fiber.Ctx) error {
	res, err := controller.Service.GetDeviceCalls(c.Context(), pathDeviceID(c))
	utils.PanicIfNeeded(err)
	return c.JSON(utils.ResponseData{Status: 200, Code: "SUCCESS", Message: "Calls switch", Results: res})
}
