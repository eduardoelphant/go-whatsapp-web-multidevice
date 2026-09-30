package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	domainLID "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/lid"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/ui/rest/middleware"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
)

type lidStub struct {
	domainLID.ILIDUsecase
	calls  []string
	device *whatsapp.DeviceInstance
	after  string
	limit  int
}

func (s *lidStub) PNToLID(ctx context.Context, phone string) (domainLID.PNItem, error) {
	s.calls = append(s.calls, "pn:"+phone)
	s.device, _ = whatsapp.DeviceFromContext(ctx)
	return domainLID.PNItem{PN: phone + "@s.whatsapp.net"}, nil
}

func (s *lidStub) LIDToPN(ctx context.Context, lid string) (domainLID.LIDItem, error) {
	s.calls = append(s.calls, "lid:"+lid)
	return domainLID.LIDItem{LID: lid + "@lid"}, nil
}

func (s *lidStub) Lookup(_ context.Context, _ domainLID.LookupRequest) (domainLID.LookupResponse, error) {
	s.calls = append(s.calls, "lookup")
	return domainLID.LookupResponse{PNs: []domainLID.PNItem{}, LIDs: []domainLID.LIDItem{}}, nil
}

func (s *lidStub) List(_ context.Context, request domainLID.ListRequest) (domainLID.ListResponse, error) {
	s.calls = append(s.calls, "list")
	s.after, s.limit = request.After, request.Limit
	return domainLID.ListResponse{Items: []domainLID.ListItem{}}, nil
}

func newLIDApp(stub *lidStub, device *whatsapp.DeviceInstance) *fiber.App {
	app := fiber.New()
	app.Use(middleware.Recovery())
	app.Use(func(c fiber.Ctx) error {
		c.Locals("device", device)
		return c.Next()
	})
	InitRestLIDList(app, stub)
	InitRestLID(app, stub)
	return app
}

func do(t *testing.T, app *fiber.App, method, path, body string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	assert.NoError(t, err)
	return resp
}

func TestLIDRoutesReachTheRightMethod(t *testing.T) {
	stub := &lidStub{}
	app := newLIDApp(stub, nil)

	assert.Equal(t, http.StatusOK, do(t, app, http.MethodGet, "/lids/pn/5511988887777", "").StatusCode)
	assert.Equal(t, http.StatusOK, do(t, app, http.MethodGet, "/lids/100000000000001@lid", "").StatusCode)
	assert.Equal(t, http.StatusOK, do(t, app, http.MethodPost, "/lids/lookup", `{"pns":["5511988887777"]}`).StatusCode)
	assert.Equal(t, http.StatusOK, do(t, app, http.MethodGet, "/lids?limit=2&after=101", "").StatusCode)

	// /lids/pn/x is a phone lookup and /lids/lookup a batch, never read as a LID.
	assert.Equal(t, []string{"pn:5511988887777", "lid:100000000000001@lid", "lookup", "list"}, stub.calls)
	assert.Equal(t, "101", stub.after)
	assert.Equal(t, 2, stub.limit)
}

func TestLIDEnvelope(t *testing.T) {
	app := newLIDApp(&lidStub{}, nil)

	resp := do(t, app, http.MethodGet, "/lids/pn/5511988887777", "")

	var body struct {
		Code    string                 `json:"code"`
		Results map[string]interface{} `json:"results"`
	}
	assert.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "SUCCESS", body.Code)
	assert.Contains(t, body.Results, "pn")
	assert.Contains(t, body.Results, "lid") // null, key present
}

func TestLIDMalformedLookupBodyIs400(t *testing.T) {
	app := newLIDApp(&lidStub{}, nil)

	resp := do(t, app, http.MethodPost, "/lids/lookup", `{"pns":`)

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestLIDLookupsScopeTheRequestDevice(t *testing.T) {
	stub := &lidStub{}
	device := &whatsapp.DeviceInstance{}
	app := newLIDApp(stub, device)

	do(t, app, http.MethodGet, "/lids/pn/5511988887777", "")

	assert.Same(t, device, stub.device)
}

func TestLIDPathParamsAreURLDecoded(t *testing.T) {
	stub := &lidStub{}
	app := newLIDApp(stub, nil)

	do(t, app, http.MethodGet, "/lids/pn/%2B55%2011%2098888-7777", "")
	do(t, app, http.MethodGet, "/lids/pn/5511988887777%40s.whatsapp.net", "")
	do(t, app, http.MethodGet, "/lids/100000000000001%40lid", "")

	assert.Equal(t, []string{"pn:+55 11 98888-7777", "pn:5511988887777@s.whatsapp.net", "lid:100000000000001@lid"}, stub.calls)
}

// The list is global to the gateway: it is registered outside the device group, so it works
// without X-Device-Id however many devices exist, while the lookups still need a device.
func TestLIDListDoesNotNeedADevice(t *testing.T) {
	stub := &lidStub{}
	app := fiber.New()
	app.Use(middleware.Recovery())
	InitRestLIDList(app, stub)
	group := app.Group("", func(c fiber.Ctx) error {
		return c.Status(http.StatusBadRequest).SendString("device required")
	})
	InitRestLID(group, stub)

	assert.Equal(t, http.StatusOK, do(t, app, http.MethodGet, "/lids?limit=2", "").StatusCode)
	assert.Equal(t, http.StatusBadRequest, do(t, app, http.MethodGet, "/lids/pn/5511988887777", "").StatusCode)
}
