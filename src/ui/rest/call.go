package rest

import (
	domainCall "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/call"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"github.com/gofiber/fiber/v3"
)

type Call struct {
	Service domainCall.ICallUsecase
}

func InitRestCall(app fiber.Router, service domainCall.ICallUsecase) Call {
	rest := Call{Service: service}
	app.Post("/call/reject", rest.RejectCall)
	// Fork (elphant): voice calls.
	app.Post("/call/answer", rest.AnswerCall)
	app.Post("/call/start", rest.StartCall)
	app.Post("/call/hangup", rest.HangupCall)
	app.Get("/calls/active", rest.ActiveCalls)
	return rest
}

func (controller *Call) RejectCall(c fiber.Ctx) error {
	var request domainCall.RejectCallRequest
	err := c.Bind().Body(&request)
	utils.PanicIfNeeded(err)

	err = controller.Service.RejectCall(
		whatsapp.ContextWithDevice(c.Context(), getDeviceFromCtx(c)),
		request.CallerJID,
		request.CallID,
	)
	utils.PanicIfNeeded(err)

	return c.JSON(utils.ResponseData{
		Status:  200,
		Code:    "SUCCESS",
		Message: "Call rejected successfully",
		Results: nil,
	})
}

// Fork (elphant): voice calls.

func (controller *Call) AnswerCall(c fiber.Ctx) error {
	var request domainCall.CallIDRequest
	utils.PanicIfNeeded(c.Bind().Body(&request))
	utils.PanicIfNeeded(controller.Service.AnswerCall(whatsapp.ContextWithDevice(c.Context(), getDeviceFromCtx(c)), request.CallID))
	return c.JSON(utils.ResponseData{Status: 200, Code: "SUCCESS", Message: "Call answered"})
}

func (controller *Call) StartCall(c fiber.Ctx) error {
	var request domainCall.StartCallRequest
	utils.PanicIfNeeded(c.Bind().Body(&request))
	info, err := controller.Service.StartCall(whatsapp.ContextWithDevice(c.Context(), getDeviceFromCtx(c)), request.Phone)
	utils.PanicIfNeeded(err)
	return c.JSON(utils.ResponseData{Status: 200, Code: "SUCCESS", Message: "Call placed", Results: info})
}

func (controller *Call) HangupCall(c fiber.Ctx) error {
	var request domainCall.CallIDRequest
	utils.PanicIfNeeded(c.Bind().Body(&request))
	utils.PanicIfNeeded(controller.Service.HangupCall(whatsapp.ContextWithDevice(c.Context(), getDeviceFromCtx(c)), request.CallID))
	return c.JSON(utils.ResponseData{Status: 200, Code: "SUCCESS", Message: "Call ended"})
}

func (controller *Call) ActiveCalls(c fiber.Ctx) error {
	calls, err := controller.Service.ActiveCalls(whatsapp.ContextWithDevice(c.Context(), getDeviceFromCtx(c)))
	utils.PanicIfNeeded(err)
	return c.JSON(utils.ResponseData{Status: 200, Code: "SUCCESS", Message: "Active calls", Results: calls})
}
