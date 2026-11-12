package rest

import (
	"io"
	"strconv"

	domainNewsletter "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/newsletter"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"github.com/gofiber/fiber/v3"
)

type Newsletter struct {
	Service domainNewsletter.INewsletterUsecase
}

func InitRestNewsletter(app fiber.Router, service domainNewsletter.INewsletterUsecase) Newsletter {
	rest := Newsletter{Service: service}
	app.Post("/newsletter/unfollow", rest.Unfollow)
	app.Get("/newsletter/info", rest.Info)
	app.Post("/newsletter/create", rest.Create)
	app.Post("/newsletter/send", rest.Send)
	app.Get("/newsletter/messages", rest.GetMessages)
	app.Get("/newsletter/messages/:server_id/download", rest.DownloadMedia)
	return rest
}

func (controller *Newsletter) Unfollow(c fiber.Ctx) error {
	var request domainNewsletter.UnfollowRequest
	err := c.Bind().Body(&request)
	utils.PanicIfNeeded(err)

	err = controller.Service.Unfollow(whatsapp.ContextWithDevice(c.Context(), getDeviceFromCtx(c)), request)
	utils.PanicIfNeeded(err)

	return c.JSON(utils.ResponseData{
		Status:  200,
		Code:    "SUCCESS",
		Message: "Success unfollow newsletter",
	})
}

func (controller *Newsletter) GetMessages(c fiber.Ctx) error {
	var request domainNewsletter.GetMessagesRequest
	err := c.Bind().Query(&request)
	utils.PanicIfNeeded(err)

	response, err := controller.Service.GetMessages(whatsapp.ContextWithDevice(c.Context(), getDeviceFromCtx(c)), request)
	utils.PanicIfNeeded(err)

	return c.JSON(utils.ResponseData{
		Status:  200,
		Code:    "SUCCESS",
		Message: "Success get newsletter messages",
		Results: response,
	})
}

func (controller *Newsletter) DownloadMedia(c fiber.Ctx) error {
	var request domainNewsletter.DownloadMediaRequest
	err := c.Bind().Query(&request)
	utils.PanicIfNeeded(err)

	request.ServerID, err = strconv.Atoi(c.Params("server_id"))
	if err != nil {
		utils.PanicIfNeeded(pkgError.ValidationError("server_id: must be a valid integer."))
	}

	response, err := controller.Service.DownloadMedia(whatsapp.ContextWithDevice(c.Context(), getDeviceFromCtx(c)), request)
	utils.PanicIfNeeded(err)
	if response.FileURL == "" {
		response.FileURL = publicStaticFileURL(c, response.FilePath)
	}

	return c.JSON(utils.ResponseData{
		Status:  200,
		Code:    "SUCCESS",
		Message: response.Status,
		Results: response,
	})
}

func (controller *Newsletter) Info(c fiber.Ctx) error {
	var request domainNewsletter.InfoRequest
	err := c.Bind().Query(&request)
	utils.PanicIfNeeded(err)

	response, err := controller.Service.Info(whatsapp.ContextWithDevice(c.Context(), getDeviceFromCtx(c)), request)
	utils.PanicIfNeeded(err)

	return c.JSON(utils.ResponseData{
		Status:  200,
		Code:    "SUCCESS",
		Message: "Success get newsletter info",
		Results: response,
	})
}

func (controller *Newsletter) Create(c fiber.Ctx) error {
	var request domainNewsletter.CreateRequest
	err := c.Bind().Body(&request)
	utils.PanicIfNeeded(err)

	request.Picture, err = readNewsletterPicture(c)
	utils.PanicIfNeeded(err)

	response, err := controller.Service.Create(whatsapp.ContextWithDevice(c.Context(), getDeviceFromCtx(c)), request)
	utils.PanicIfNeeded(err)

	return c.JSON(utils.ResponseData{
		Status:  200,
		Code:    "SUCCESS",
		Message: "Success create newsletter",
		Results: response,
	})
}

// Send publishes one post. The multipart "file" field is read as bytes, "poll_options"
// is a repeated field.
func (controller *Newsletter) Send(c fiber.Ctx) error {
	var request domainNewsletter.SendRequest
	err := c.Bind().Body(&request)
	utils.PanicIfNeeded(err)

	if form, formErr := c.MultipartForm(); formErr == nil && form != nil {
		request.PollOptions = form.Value["poll_options"]
	}
	if len(request.PollOptions) == 0 {
		request.PollOptions = nil
	}

	if file, fileErr := c.FormFile("file"); fileErr == nil {
		handle, openErr := file.Open()
		if openErr != nil {
			utils.PanicIfNeeded(pkgError.ValidationError("file: unreadable file."))
		}
		defer handle.Close()
		request.File, err = io.ReadAll(handle)
		utils.PanicIfNeeded(err)
		request.FileName = file.Filename
	}

	response, err := controller.Service.Send(whatsapp.ContextWithDevice(c.Context(), getDeviceFromCtx(c)), request)
	utils.PanicIfNeeded(err)

	return c.JSON(utils.ResponseData{
		Status:  200,
		Code:    "SUCCESS",
		Message: "Success send newsletter post",
		Results: response,
	})
}

// readNewsletterPicture returns the raw bytes of the optional multipart "picture"
// field; the usecase validation checks they are a JPEG.
func readNewsletterPicture(c fiber.Ctx) ([]byte, error) {
	file, err := c.FormFile("picture")
	if err != nil {
		return nil, nil
	}
	handle, err := file.Open()
	if err != nil {
		return nil, pkgError.ValidationError("picture: unreadable file.")
	}
	defer handle.Close()

	return io.ReadAll(handle)
}
