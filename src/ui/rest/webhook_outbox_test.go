package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/webhookoutbox"
	"github.com/gofiber/fiber/v3"
)

func newOutboxTestApp(outbox *webhookoutbox.Outbox) *fiber.App {
	app := fiber.New()
	InitRestWebhookOutbox(app, func() *webhookoutbox.Outbox { return outbox })
	return app
}

func callOutbox(t *testing.T, app *fiber.App, method, target string) (int, map[string]any) {
	t.Helper()
	resp, err := app.Test(httptest.NewRequest(method, target, nil))
	if err != nil {
		t.Fatalf("%s %s: %v", method, target, err)
	}
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body
}

func TestWebhookOutboxRoutesAre404InDirectMode(t *testing.T) {
	app := newOutboxTestApp(nil)
	for _, route := range [][2]string{
		{http.MethodGet, "/webhooks/stats"},
		{http.MethodGet, "/webhooks/deliveries"},
		{http.MethodGet, "/webhooks/deliveries/X"},
		{http.MethodPost, "/webhooks/deliveries/X/redeliver"},
		{http.MethodPost, "/webhooks/replay?since=2026-09-25T00:00:00Z"},
	} {
		if status, _ := callOutbox(t, app, route[0], route[1]); status != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", route[0], route[1], status)
		}
	}
}

func TestWebhookOutboxRoutes(t *testing.T) {
	ctx := context.Background()
	store, err := webhookoutbox.OpenStore("file:" + filepath.Join(t.TempDir(), "outbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	resolve := func(context.Context, string) (string, bool, error) { return "", false, nil }
	outbox := webhookoutbox.New(store, resolve, webhookoutbox.DefaultPolicy) // not started: nothing is sent
	first, _ := outbox.Enqueue(ctx, "http://a.test/hook", "global", "message", map[string]any{"event": "message"})
	second, _ := outbox.Enqueue(ctx, "http://a.test/hook", "global", "message.ack", map[string]any{"event": "message.ack"})
	if err := store.MarkDead(ctx, first.ID, 1, "HTTP 400", 400); err != nil {
		t.Fatal(err)
	}
	app := newOutboxTestApp(outbox)

	status, body := callOutbox(t, app, http.MethodGet, "/webhooks/stats")
	stats, _ := body["results"].(map[string]any)
	if status != 200 || stats["pending"] != 1.0 || stats["dead"] != 1.0 {
		t.Fatalf("stats %d %v", status, body)
	}

	status, body = callOutbox(t, app, http.MethodGet, "/webhooks/deliveries?status=dead")
	list, _ := body["results"].([]any)
	if status != 200 || len(list) != 1 {
		t.Fatalf("dead list %d %v", status, body)
	}
	item := list[0].(map[string]any)
	if _, hasBody := item["body"]; hasBody || item["event_id"] != first.EventID || item["last_error"] != "HTTP 400" {
		t.Fatalf("list item %v", item)
	}

	status, body = callOutbox(t, app, http.MethodGet, "/webhooks/deliveries/"+second.EventID)
	row, _ := body["results"].(map[string]any)
	stored, _ := row["body"].(map[string]any)
	if status != 200 || stored["event_id"] != second.EventID {
		t.Fatalf("get %d %v", status, body)
	}
	if status, _ = callOutbox(t, app, http.MethodGet, "/webhooks/deliveries/NOPE"); status != 404 {
		t.Fatalf("unknown get = %d", status)
	}

	status, body = callOutbox(t, app, http.MethodPost, "/webhooks/deliveries/"+first.EventID+"/redeliver")
	row, _ = body["results"].(map[string]any)
	if status != 200 || row["status"] != "pending" || row["replay"] != true {
		t.Fatalf("redeliver %d %v", status, body)
	}
	if status, _ = callOutbox(t, app, http.MethodPost, "/webhooks/deliveries/NOPE/redeliver"); status != 404 {
		t.Fatalf("unknown redeliver = %d", status)
	}

	for _, bad := range []string{
		"/webhooks/deliveries?status=lost",
		"/webhooks/deliveries?limit=0",
		"/webhooks/deliveries?limit=501",
		"/webhooks/deliveries?before_id=abc",
	} {
		if status, _ := callOutbox(t, app, http.MethodGet, bad); status != 400 {
			t.Errorf("GET %s = %d, want 400", bad, status)
		}
	}
	for _, bad := range []string{"/webhooks/replay", "/webhooks/replay?since=yesterday"} {
		if status, _ := callOutbox(t, app, http.MethodPost, bad); status != 400 {
			t.Errorf("POST %s = %d, want 400", bad, status)
		}
	}

	if err := store.MarkDelivered(ctx, second.ID, 1, 200); err != nil {
		t.Fatal(err)
	}
	status, body = callOutbox(t, app, http.MethodPost, "/webhooks/replay?since=2000-01-01T00:00:00Z")
	result, _ := body["results"].(map[string]any)
	if status != 200 || result["requeued"] != 1.0 {
		t.Fatalf("replay %d %v", status, body)
	}
}
