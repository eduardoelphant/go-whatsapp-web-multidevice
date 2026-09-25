package whatsapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/webhookoutbox"
)

// useTestOutbox switches to durable mode with an outbox that is never started,
// so queued rows stay pending for the test to inspect.
func useTestOutbox(t *testing.T) *webhookoutbox.Outbox {
	t.Helper()
	store, err := webhookoutbox.OpenStore("file:" + filepath.Join(t.TempDir(), "outbox.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	previousOutbox, previousSubmit := durableOutbox, submitWebhookFn
	outbox := webhookoutbox.New(store, resolveWebhookSecret, webhookoutbox.DefaultPolicy)
	durableOutbox, submitWebhookFn = outbox, submitWebhookDurable
	t.Cleanup(func() {
		durableOutbox, submitWebhookFn = previousOutbox, previousSubmit
		store.Close()
	})
	return outbox
}

// useGlobalWebhooks sets the global webhook URLs and stubs device lookups.
func useGlobalWebhooks(t *testing.T, urls ...string) {
	t.Helper()
	prevURLs, prevEvents := config.WhatsappWebhook, config.WhatsappWebhookEvents
	prevMerge, prevChatwoot := config.WhatsappWebhookDeviceMergeGlobal, config.ChatwootEnabled
	prevStorage, prevSession := webhookStorageForTest, sessionIDForJIDFn
	config.WhatsappWebhook, config.WhatsappWebhookEvents = urls, nil
	config.WhatsappWebhookDeviceMergeGlobal, config.ChatwootEnabled = false, false
	webhookStorageForTest = func(string) (*domainChatStorage.DeviceRecord, error) { return nil, nil }
	sessionIDForJIDFn = func(string) string { return "" }
	t.Cleanup(func() {
		config.WhatsappWebhook, config.WhatsappWebhookEvents = prevURLs, prevEvents
		config.WhatsappWebhookDeviceMergeGlobal, config.ChatwootEnabled = prevMerge, prevChatwoot
		webhookStorageForTest, sessionIDForJIDFn = prevStorage, prevSession
	})
}

// pendingRows lists the pending rows oldest first.
func pendingRows(t *testing.T, outbox *webhookoutbox.Outbox) []webhookoutbox.Row {
	t.Helper()
	rows, err := outbox.Store().List(context.Background(), webhookoutbox.StatusPending, 500, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	slices.Reverse(rows)
	return rows
}

func TestDurableSubmitQueuesOneRowPerURL(t *testing.T) {
	outbox := useTestOutbox(t)
	useGlobalWebhooks(t, "http://a.test/hook", "http://b.test/hook")
	payload := map[string]any{"event": "message", "device_id": "5511999999999@s.whatsapp.net", "payload": map[string]any{"id": "m1"}}

	if err := forwardPayloadToConfiguredWebhooks(context.Background(), payload, "message"); err != nil {
		t.Fatal(err)
	}
	rows := pendingRows(t, outbox)
	if len(rows) != 2 || rows[0].TargetURL != "http://a.test/hook" || rows[1].TargetURL != "http://b.test/hook" {
		t.Fatalf("rows %+v", rows)
	}
	if rows[0].EventID == rows[1].EventID {
		t.Fatal("two URLs share an event_id")
	}
	for _, row := range rows {
		if row.ConfigRef != "global" || row.EventName != "message" {
			t.Fatalf("row %+v", row)
		}
	}
	if _, ok := payload["event_id"]; ok {
		t.Fatal("caller payload gained event_id")
	}
}

func TestDurableSubmitRefersToTheDeviceWebhookConfig(t *testing.T) {
	outbox := useTestOutbox(t)
	useGlobalWebhooks(t)
	deviceURL := "http://device.test/hook"
	webhookStorageForTest = func(string) (*domainChatStorage.DeviceRecord, error) {
		return &domainChatStorage.DeviceRecord{WebhookURL: &deviceURL}, nil
	}
	body := func() map[string]any {
		return map[string]any{"event": "message", "device_id": "5511999999999@s.whatsapp.net"}
	}

	if err := forwardPayloadToConfiguredWebhooks(context.Background(), body(), "message"); err != nil {
		t.Fatal(err)
	}
	sessionIDForJIDFn = func(string) string { return "org_2" }
	if err := forwardPayloadToConfiguredWebhooks(context.Background(), body(), "message"); err != nil {
		t.Fatal(err)
	}

	rows := pendingRows(t, outbox)
	if len(rows) != 2 || rows[0].ConfigRef != "jid:5511999999999@s.whatsapp.net" || rows[1].ConfigRef != "device:org_2" {
		t.Fatalf("config refs %+v", rows)
	}
	if rows[0].TargetURL != deviceURL {
		t.Fatalf("device URL not used: %+v", rows[0])
	}
}

func TestDurableSubmitFailureMarksTheHandlerFailed(t *testing.T) {
	outbox := useTestOutbox(t)
	useGlobalWebhooks(t, "http://a.test/hook")
	outbox.Store().Close() // the queue is unavailable

	ctx, failed := withHandlerFailureFlag(context.Background())
	err := forwardPayloadToConfiguredWebhooks(ctx, map[string]any{"event": "message"}, "message")
	if err == nil || !failed.Load() {
		t.Fatalf("err = %v, failed = %v; want an error and the flag set", err, failed.Load())
	}
}

func TestChatPresenceBypassesTheOutbox(t *testing.T) {
	outbox := useTestOutbox(t)
	got := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		got <- body
	}))
	t.Cleanup(srv.Close)
	useGlobalWebhooks(t, srv.URL)

	payload := map[string]any{"event": "chat_presence", "payload": map[string]any{"state": "composing"}}
	if err := forwardPayloadToConfiguredWebhooks(context.Background(), payload, "chat_presence"); err != nil {
		t.Fatal(err)
	}
	select {
	case body := <-got:
		if _, ok := body["event_id"]; ok {
			t.Fatalf("chat_presence went through the outbox: %v", body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("chat_presence was not sent directly")
	}
	if rows := pendingRows(t, outbox); len(rows) != 0 {
		t.Fatalf("chat_presence queued: %+v", rows)
	}
}

func TestDeviceAndGlobalLegsQueueConcurrently(t *testing.T) {
	outbox := useTestOutbox(t)
	useGlobalWebhooks(t, "http://global.test/hook")
	config.WhatsappWebhookDeviceMergeGlobal = true
	deviceURL := "http://device.test/hook"
	webhookStorageForTest = func(string) (*domainChatStorage.DeviceRecord, error) {
		return &domainChatStorage.DeviceRecord{WebhookURL: &deviceURL}, nil
	}

	for i := range 20 {
		payload := map[string]any{"event": "message", "device_id": "5511999999999@s.whatsapp.net",
			"payload": map[string]any{"id": i, "nested": map[string]any{"text": "hi"}}}
		if err := forwardPayloadToConfiguredWebhooks(context.Background(), payload, "message"); err != nil {
			t.Fatal(err)
		}
	}

	rows := pendingRows(t, outbox)
	perURL := map[string]int{}
	for _, row := range rows {
		perURL[row.TargetURL]++
		full, err := outbox.Store().Get(context.Background(), row.EventID)
		if err != nil || full == nil {
			t.Fatalf("Get: %v", err)
		}
		var body map[string]any
		if err := json.Unmarshal(full.Body, &body); err != nil || body["event_id"] != row.EventID {
			t.Fatalf("body %s", full.Body)
		}
	}
	if perURL[deviceURL] != 20 || perURL["http://global.test/hook"] != 20 {
		t.Fatalf("rows per URL %v", perURL)
	}
}

func TestResolveWebhookSecret(t *testing.T) {
	prevSecret, prevInsecure, prevStorage := config.WhatsappWebhookSecret, config.WhatsappWebhookInsecureSkipVerify, webhookStorageForTest
	t.Cleanup(func() {
		config.WhatsappWebhookSecret, config.WhatsappWebhookInsecureSkipVerify, webhookStorageForTest = prevSecret, prevInsecure, prevStorage
	})
	config.WhatsappWebhookSecret, config.WhatsappWebhookInsecureSkipVerify = "global-secret", false
	ctx := context.Background()
	jid := "5511999999999@s.whatsapp.net"
	deviceURL := "http://device.test/hook"

	check := func(ref, wantSecret string, wantInsecure bool) {
		t.Helper()
		secret, insecure, err := resolveWebhookSecret(ctx, ref)
		if err != nil || secret != wantSecret || insecure != wantInsecure {
			t.Fatalf("resolve(%s) = %q, %v, %v; want %q, %v", ref, secret, insecure, err, wantSecret, wantInsecure)
		}
	}

	check("global", "global-secret", false)

	webhookStorageForTest = func(got string) (*domainChatStorage.DeviceRecord, error) {
		if got != jid {
			return nil, errors.New("unexpected jid " + got)
		}
		return &domainChatStorage.DeviceRecord{WebhookURL: &deviceURL, WebhookSecret: "device-secret", WebhookInsecureSkipVerify: true}, nil
	}
	check("jid:"+jid, "device-secret", true)

	webhookStorageForTest = func(string) (*domainChatStorage.DeviceRecord, error) {
		return &domainChatStorage.DeviceRecord{WebhookURL: &deviceURL}, nil
	}
	check("jid:"+jid, "global-secret", false) // device webhook without its own secret

	webhookStorageForTest = func(string) (*domainChatStorage.DeviceRecord, error) { return nil, nil }
	check("jid:"+jid, "global-secret", false) // device webhook removed since queueing

	webhookStorageForTest = func(string) (*domainChatStorage.DeviceRecord, error) { return nil, errors.New("db locked") }
	if _, _, err := resolveWebhookSecret(ctx, "jid:"+jid); err == nil {
		t.Fatal("lookup error must be returned so the attempt is retried")
	}
	if _, _, err := resolveWebhookSecret(ctx, "bogus"); err == nil {
		t.Fatal("unknown ref accepted")
	}
}

func TestStartDurableWebhooksModes(t *testing.T) {
	previousOutbox, previousSubmit := durableOutbox, submitWebhookFn
	t.Cleanup(func() {
		if durableOutbox != nil && durableOutbox != previousOutbox {
			durableOutbox.Store().Close()
		}
		durableOutbox, submitWebhookFn = previousOutbox, previousSubmit
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	for _, mode := range []string{"", "direct", " Direct "} {
		t.Setenv(webhookDeliveryEnv, mode)
		if err := StartDurableWebhooks(ctx); err != nil || durableWebhooksEnabled() {
			t.Fatalf("mode %q: err %v, enabled %v", mode, err, durableWebhooksEnabled())
		}
	}

	t.Setenv(webhookDeliveryEnv, "queue")
	if err := StartDurableWebhooks(ctx); err == nil || durableWebhooksEnabled() {
		t.Fatalf("invalid mode accepted: %v", err)
	}

	t.Setenv(webhookDeliveryEnv, "durable")
	t.Setenv(webhookOutboxDBEnv, "file:"+filepath.Join(t.TempDir(), "outbox.db"))
	if err := StartDurableWebhooks(ctx); err != nil || !durableWebhooksEnabled() || DurableWebhookOutbox() == nil {
		t.Fatalf("durable mode: err %v, enabled %v", err, durableWebhooksEnabled())
	}
}
