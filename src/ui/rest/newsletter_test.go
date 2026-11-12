package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"testing"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainNewsletter "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/newsletter"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/ui/rest/middleware"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type newsletterServiceStub struct {
	domainNewsletter.INewsletterUsecase
	request       *domainNewsletter.DownloadMediaRequest
	contextDevice *whatsapp.DeviceInstance
}

func (stub *newsletterServiceStub) DownloadMedia(ctx context.Context, request domainNewsletter.DownloadMediaRequest) (domainNewsletter.DownloadMediaResponse, error) {
	stub.request = &request
	stub.contextDevice, _ = whatsapp.DeviceFromContext(ctx)
	return domainNewsletter.DownloadMediaResponse{
		ServerID:  request.ServerID,
		MessageID: "ACBD7F85529AAD63FAF376F622351D14",
		Status:    "Media downloaded successfully",
		MediaType: "image",
		MimeType:  "image/jpeg",
		Filename:  "result.jpg",
		FilePath:  "statics/media/120363123456789/2026-08-28/result.jpg",
		FileSize:  1234,
	}, nil
}

func TestDownloadNewsletterMediaRoute(t *testing.T) {
	oldBasePath := config.AppBasePath
	config.AppBasePath = "/api"
	defer func() {
		config.AppBasePath = oldBasePath
	}()

	device := whatsapp.NewDeviceInstance("device-a", nil, nil)
	service := &newsletterServiceStub{}
	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Locals("device", device)
		return c.Next()
	})
	InitRestNewsletter(app, service)

	request := httptest.NewRequest(
		"GET",
		"http://example.com/newsletter/messages/8872/download?newsletter_id=120363123456789%40newsletter",
		nil,
	)
	response, err := app.Test(request)

	require.NoError(t, err)
	require.Equal(t, fiber.StatusOK, response.StatusCode)
	require.NotNil(t, service.request)
	assert.Equal(t, 8872, service.request.ServerID)
	assert.Equal(t, "120363123456789@newsletter", service.request.NewsletterID)
	assert.Same(t, device, service.contextDevice)

	var body struct {
		Results domainNewsletter.DownloadMediaResponse `json:"results"`
	}
	require.NoError(t, json.NewDecoder(response.Body).Decode(&body))
	assert.Equal(t, "http://example.com/api/statics/media/120363123456789/2026-08-28/result.jpg", body.Results.FileURL)
}

type newsletterDataStub struct {
	domainNewsletter.INewsletterUsecase
	err     error
	created domainNewsletter.CreateRequest
}

func (s *newsletterDataStub) Info(_ context.Context, request domainNewsletter.InfoRequest) (domainNewsletter.NewsletterInfoResponse, error) {
	return domainNewsletter.NewsletterInfoResponse{NewsletterID: request.NewsletterID, Name: "Canal"}, s.err
}

func (s *newsletterDataStub) Create(_ context.Context, request domainNewsletter.CreateRequest) (domainNewsletter.NewsletterInfoResponse, error) {
	s.created = request
	return domainNewsletter.NewsletterInfoResponse{NewsletterID: "1@newsletter", Name: request.Name}, s.err
}

func newNewsletterDataApp(service domainNewsletter.INewsletterUsecase) *fiber.App {
	app := fiber.New()
	app.Use(middleware.Recovery())
	InitRestNewsletter(app, service)
	return app
}

func TestNewsletterInfoRoute(t *testing.T) {
	app := newNewsletterDataApp(&newsletterDataStub{})
	resp, err := app.Test(httptest.NewRequest("GET", "/newsletter/info?newsletter_id=1@newsletter", nil))
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)
	var out struct {
		Code    string                                  `json:"code"`
		Results domainNewsletter.NewsletterInfoResponse `json:"results"`
	}
	raw, _ := io.ReadAll(resp.Body)
	require.NoError(t, json.Unmarshal(raw, &out))
	assert.Equal(t, "SUCCESS", out.Code)
	assert.Equal(t, "1@newsletter", out.Results.NewsletterID)
}

func TestNewsletterInfoNotFoundIs404(t *testing.T) {
	app := newNewsletterDataApp(&newsletterDataStub{err: pkgError.ErrNewsletterNotFound})
	resp, err := app.Test(httptest.NewRequest("GET", "/newsletter/info?newsletter_id=1@newsletter", nil))
	require.NoError(t, err)
	assert.Equal(t, 404, resp.StatusCode)
	var out struct {
		Code string `json:"code"`
	}
	raw, _ := io.ReadAll(resp.Body)
	require.NoError(t, json.Unmarshal(raw, &out))
	assert.Equal(t, "NOT_FOUND", out.Code)
}

func TestNewsletterCreateRouteMultipart(t *testing.T) {
	service := &newsletterDataStub{}
	app := newNewsletterDataApp(service)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("name", "Canal"))
	require.NoError(t, writer.WriteField("description", "Descrição"))
	part, err := writer.CreateFormFile("picture", "p.jpg")
	require.NoError(t, err)
	_, err = part.Write([]byte{0xFF, 0xD8, 0xFF, 0xE0, 1, 2, 3})
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	req := httptest.NewRequest("POST", "/newsletter/create", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := app.Test(req)
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, "Canal", service.created.Name)
	assert.Equal(t, "Descrição", service.created.Description)
	assert.Equal(t, []byte{0xFF, 0xD8, 0xFF, 0xE0, 1, 2, 3}, service.created.Picture)
}

func TestNewsletterCreateRouteWithoutPicture(t *testing.T) {
	service := &newsletterDataStub{}
	app := newNewsletterDataApp(service)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("name", "Canal"))
	require.NoError(t, writer.Close())

	req := httptest.NewRequest("POST", "/newsletter/create", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := app.Test(req)
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)
	assert.Empty(t, service.created.Picture)
}
