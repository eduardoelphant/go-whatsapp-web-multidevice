package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
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

func TestNewsletterInfoRouteRejectsIDWithoutSuffix(t *testing.T) {
	app := newNewsletterDataApp(&newsletterDataStub{err: pkgError.ValidationError("newsletter_id: must end with @newsletter")})
	resp, err := app.Test(httptest.NewRequest("GET", "/newsletter/info?newsletter_id=123", nil))
	require.NoError(t, err)
	assert.Equal(t, 400, resp.StatusCode)
}

type newsletterSendStub struct {
	domainNewsletter.INewsletterUsecase
	err     error
	request domainNewsletter.SendRequest
}

func (s *newsletterSendStub) Send(_ context.Context, request domainNewsletter.SendRequest) (domainNewsletter.SendResponse, error) {
	s.request = request
	return domainNewsletter.SendResponse{MessageID: "3EB0X", ServerID: 55, Timestamp: "2026-11-12T10:00:00Z"}, s.err
}

func TestNewsletterSendRouteMultipart(t *testing.T) {
	service := &newsletterSendStub{}
	app := newNewsletterDataApp(service)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range map[string]string{
		"newsletter_id": "1@newsletter", "message_id": "3EB0X", "type": "poll", "poll_question": "Qual?",
		"poll_max_answer": "2", "link_preview": "true", "filename": "a.pdf",
	} {
		require.NoError(t, writer.WriteField(key, value))
	}
	require.NoError(t, writer.WriteField("poll_options", "A"))
	require.NoError(t, writer.WriteField("poll_options", "B"))
	require.NoError(t, writer.WriteField("poll_options", "A"))
	part, err := writer.CreateFormFile("file", "f.bin")
	require.NoError(t, err)
	_, err = part.Write([]byte{1, 2, 3})
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	req := httptest.NewRequest("POST", "/newsletter/send", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := app.Test(req)
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)

	assert.Equal(t, "1@newsletter", service.request.NewsletterID)
	assert.Equal(t, "3EB0X", service.request.MessageID)
	assert.Equal(t, "poll", service.request.Type)
	assert.Equal(t, 2, service.request.PollMaxAnswer)
	assert.True(t, service.request.LinkPreview)
	assert.Equal(t, "a.pdf", service.request.Filename)
	assert.Equal(t, []string{"A", "B", "A"}, service.request.PollOptions)
	assert.Equal(t, []byte{1, 2, 3}, service.request.File)
	assert.Equal(t, "f.bin", service.request.FileName)

	var out struct {
		Code    string                        `json:"code"`
		Results domainNewsletter.SendResponse `json:"results"`
	}
	raw, _ := io.ReadAll(resp.Body)
	require.NoError(t, json.Unmarshal(raw, &out))
	assert.Equal(t, "SUCCESS", out.Code)
	assert.Equal(t, 55, out.Results.ServerID)
}

func TestNewsletterSendRouteErrorCodes(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"media rejected", pkgError.MediaRejected("bad media"), 400, "MEDIA_REJECTED"},
		{"not authorized", pkgError.NotAuthorized("not-authorized"), 403, "NOT_AUTHORIZED"},
		{"not found", pkgError.ErrNewsletterNotFound, 404, "NOT_FOUND"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := newNewsletterDataApp(&newsletterSendStub{err: tc.err})
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			require.NoError(t, writer.WriteField("newsletter_id", "1@newsletter"))
			require.NoError(t, writer.WriteField("type", "text"))
			require.NoError(t, writer.WriteField("text", "x"))
			require.NoError(t, writer.Close())
			req := httptest.NewRequest("POST", "/newsletter/send", &body)
			req.Header.Set("Content-Type", writer.FormDataContentType())
			resp, err := app.Test(req)
			require.NoError(t, err)
			assert.Equal(t, tc.wantStatus, resp.StatusCode)
			var out struct {
				Code string `json:"code"`
			}
			raw, _ := io.ReadAll(resp.Body)
			require.NoError(t, json.Unmarshal(raw, &out))
			assert.Equal(t, tc.wantCode, out.Code)
		})
	}
}

type newsletterEditStub struct {
	domainNewsletter.INewsletterUsecase
	edited  domainNewsletter.EditRequest
	revoked domainNewsletter.RevokeRequest
	err     error
}

func (s *newsletterEditStub) Edit(_ context.Context, request domainNewsletter.EditRequest) (domainNewsletter.EditResponse, error) {
	s.edited = request
	return domainNewsletter.EditResponse{MessageID: request.MessageID}, s.err
}

func (s *newsletterEditStub) Revoke(_ context.Context, request domainNewsletter.RevokeRequest) (domainNewsletter.EditResponse, error) {
	s.revoked = request
	return domainNewsletter.EditResponse{MessageID: request.MessageID}, s.err
}

func (s *newsletterEditStub) DownloadMedia(_ context.Context, request domainNewsletter.DownloadMediaRequest) (domainNewsletter.DownloadMediaResponse, error) {
	return domainNewsletter.DownloadMediaResponse{ServerID: request.ServerID, FilePath: "x"}, nil
}

func postNewsletterJSON(t *testing.T, app *fiber.App, path, body string) *http.Response {
	t.Helper()
	request := httptest.NewRequest("POST", path, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(request)
	require.NoError(t, err)
	return resp
}

func TestNewsletterEditAndRevokeRoutes(t *testing.T) {
	stub := &newsletterEditStub{}
	app := newNewsletterDataApp(stub)

	resp := postNewsletterJSON(t, app, "/newsletter/messages/3EB0AB/edit", `{"newsletter_id":"1@newsletter","text":"novo"}`)
	require.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, domainNewsletter.EditRequest{NewsletterID: "1@newsletter", MessageID: "3EB0AB", Text: "novo"}, stub.edited)
	var out struct {
		Results domainNewsletter.EditResponse `json:"results"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	assert.Equal(t, "3EB0AB", out.Results.MessageID)

	resp = postNewsletterJSON(t, app, "/newsletter/messages/3EB0AB/revoke", `{"newsletter_id":"1@newsletter"}`)
	require.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, domainNewsletter.RevokeRequest{NewsletterID: "1@newsletter", MessageID: "3EB0AB"}, stub.revoked)
}

func TestNewsletterEditRefusedAnswers422(t *testing.T) {
	app := newNewsletterDataApp(&newsletterEditStub{err: pkgError.EditRefused("bad-request")})
	resp := postNewsletterJSON(t, app, "/newsletter/messages/3EB0AB/edit", `{"newsletter_id":"1@newsletter","text":"novo"}`)
	require.Equal(t, 422, resp.StatusCode)
	var out struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	assert.Equal(t, "EDIT_REFUSED", out.Code)
	assert.Equal(t, "bad-request", out.Message)
}

func TestNewsletterDownloadAndEditRoutesDoNotShadowEachOther(t *testing.T) {
	stub := &newsletterEditStub{}
	app := newNewsletterDataApp(stub)

	resp, err := app.Test(httptest.NewRequest("GET", "/newsletter/messages/8872/download?newsletter_id=1@newsletter", nil))
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	resp = postNewsletterJSON(t, app, "/newsletter/messages/8872/edit", `{"newsletter_id":"1@newsletter","text":"x"}`)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, "8872", stub.edited.MessageID)
}

type newsletterUpdateStub struct {
	domainNewsletter.INewsletterUsecase
	updated domainNewsletter.UpdateRequest
	err     error
}

func (s *newsletterUpdateStub) Update(_ context.Context, request domainNewsletter.UpdateRequest) (domainNewsletter.NewsletterInfoResponse, error) {
	s.updated = request
	return domainNewsletter.NewsletterInfoResponse{NewsletterID: request.NewsletterID, Name: "Novo"}, s.err
}

func postNewsletterUpdate(t *testing.T, app *fiber.App, fields map[string]string, picture []byte) *http.Response {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range fields {
		require.NoError(t, writer.WriteField(key, value))
	}
	if picture != nil {
		part, err := writer.CreateFormFile("picture", "p.jpg")
		require.NoError(t, err)
		_, err = part.Write(picture)
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	req := httptest.NewRequest("POST", "/newsletter/update", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := app.Test(req)
	require.NoError(t, err)
	return resp
}

func TestNewsletterUpdateRoutePassesOnlyTheFieldsSent(t *testing.T) {
	stub := &newsletterUpdateStub{}
	app := newNewsletterDataApp(stub)

	resp := postNewsletterUpdate(t, app, map[string]string{"newsletter_id": "1@newsletter", "description": ""}, nil)
	require.Equal(t, 200, resp.StatusCode)
	assert.Nil(t, stub.updated.Name)
	require.NotNil(t, stub.updated.Description)
	assert.Equal(t, "", *stub.updated.Description)
	assert.Empty(t, stub.updated.Picture)
	assert.False(t, stub.updated.RemovePicture)

	resp = postNewsletterUpdate(t, app, map[string]string{"newsletter_id": "1@newsletter", "name": "Novo", "remove_picture": "true"}, nil)
	require.Equal(t, 200, resp.StatusCode)
	require.NotNil(t, stub.updated.Name)
	assert.Equal(t, "Novo", *stub.updated.Name)
	assert.True(t, stub.updated.RemovePicture)

	resp = postNewsletterUpdate(t, app, map[string]string{"newsletter_id": "1@newsletter"}, []byte{0xFF, 0xD8, 0xFF, 0xE0, 9})
	require.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, []byte{0xFF, 0xD8, 0xFF, 0xE0, 9}, stub.updated.Picture)
}

func TestNewsletterUpdateRouteErrorCodes(t *testing.T) {
	cases := map[string]struct {
		err    error
		status int
		code   string
	}{
		"sem papel":     {pkgError.NotAuthorized("forbidden"), 403, "NOT_AUTHORIZED"},
		"canal ausente": {pkgError.ErrNewsletterNotFound, 404, "NOT_FOUND"},
		"recusa":        {pkgError.UpdateRefused("refused"), 422, "UPDATE_REFUSED"},
	}
	for label, tc := range cases {
		t.Run(label, func(t *testing.T) {
			app := newNewsletterDataApp(&newsletterUpdateStub{err: tc.err})
			resp := postNewsletterUpdate(t, app, map[string]string{"newsletter_id": "1@newsletter", "name": "x"}, nil)
			require.Equal(t, tc.status, resp.StatusCode)
			var out struct {
				Code string `json:"code"`
			}
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
			assert.Equal(t, tc.code, out.Code)
		})
	}
}
