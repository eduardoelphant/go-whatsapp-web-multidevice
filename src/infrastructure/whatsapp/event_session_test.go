package whatsapp

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	"go.mau.fi/whatsmeow/types/events"
)

var sessionTestNow = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

func init() {
	// Every test in this package that calls handler would otherwise leave
	// session.status deliveries running while later tests swap config and
	// submitWebhookFn. Tests that check delivery opt in with trackSessionDispatch.
	sessionStatusDispatch = func(func()) {}
}

// trackSessionDispatch delivers session.status asynchronously, as in
// production, and waits for every delivery when the test ends. Call it after
// registering cleanups that restore globals, so the wait runs before them.
func trackSessionDispatch(t *testing.T) {
	t.Helper()
	previous := sessionStatusDispatch
	var inFlight sync.WaitGroup
	sessionStatusDispatch = func(deliver func()) {
		inFlight.Add(1)
		go func() {
			defer inFlight.Done()
			deliver()
		}()
	}
	t.Cleanup(func() {
		inFlight.Wait()
		sessionStatusDispatch = previous
	})
}

func TestSessionStatusFromEvent(t *testing.T) {
	cases := []struct {
		name string
		evt  any
		want SessionStatus
	}{
		{"connected", &events.Connected{}, SessionStatus{Status: SessionStatusConnected}},
		{"disconnected", &events.Disconnected{}, SessionStatus{Status: SessionStatusDisconnected}},
		{"logged out", &events.LoggedOut{Reason: events.ConnectFailureLoggedOut},
			SessionStatus{Status: SessionStatusLoggedOut, Code: sessionPtr(401), Reason: sessionPtr("401: logged out from another device")}},
		{"stream replaced", &events.StreamReplaced{}, SessionStatus{Status: SessionStatusStreamReplaced}},
		{"temporary ban", &events.TemporaryBan{Code: events.TempBanSentToTooManyPeople, Expire: 24 * time.Hour},
			SessionStatus{
				Status:    SessionStatusTemporaryBan,
				Code:      sessionPtr(101),
				Reason:    sessionPtr("101: you sent too many messages to people who don't have you in their address books"),
				ExpiresAt: sessionPtr(sessionTestNow.Add(24 * time.Hour)),
			}},
		{"temporary ban without expiry", &events.TemporaryBan{Code: events.TempBanBlockedByUsers},
			SessionStatus{Status: SessionStatusTemporaryBan, Code: sessionPtr(102), Reason: sessionPtr("102: too many people blocked you")}},
		{"connect failure with message", &events.ConnectFailure{Reason: events.ConnectFailureGeneric, Message: "bad request"},
			SessionStatus{Status: SessionStatusConnectFailure, Code: sessionPtr(400), Reason: sessionPtr("bad request")}},
		{"connect failure without message", &events.ConnectFailure{Reason: events.ConnectFailureServiceUnavailable},
			SessionStatus{Status: SessionStatusConnectFailure, Code: sessionPtr(503), Reason: sessionPtr("503: unknown error")}},
		{"first keepalive timeout", &events.KeepAliveTimeout{ErrorCount: 1}, SessionStatus{Status: SessionStatusKeepAliveTimeout}},
		{"keepalive restored", &events.KeepAliveRestored{}, SessionStatus{Status: SessionStatusKeepAliveRestored}},
		{"client outdated", &events.ClientOutdated{}, SessionStatus{Status: SessionStatusClientOutdated, Code: sessionPtr(405)}},
		{"pair success", &events.PairSuccess{}, SessionStatus{Status: SessionStatusPairSuccess}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := sessionStatusFromEvent(tc.evt, sessionTestNow)
			if !ok {
				t.Fatalf("sessionStatusFromEvent(%T) returned ok=false", tc.evt)
			}
			if !reflect.DeepEqual(got.Payload(), tc.want.Payload()) {
				t.Fatalf("payload = %#v, want %#v", got.Payload(), tc.want.Payload())
			}
		})
	}
}

func TestSessionStatusFromEventSkipsOtherEvents(t *testing.T) {
	for _, evt := range []any{
		&events.KeepAliveTimeout{ErrorCount: 2},
		&events.KeepAliveTimeout{ErrorCount: 7},
		&events.StreamError{Code: "500"},
		&events.CATRefreshError{},
		&events.Message{},
	} {
		if status, ok := sessionStatusFromEvent(evt, sessionTestNow); ok {
			t.Errorf("%T %+v produced status %q, want none", evt, evt, status.Status)
		}
	}
}

func TestSessionStatusPayloadAlwaysHasFiveKeys(t *testing.T) {
	raw, err := json.Marshal(SessionStatus{Status: SessionStatusConnected}.Payload())
	if err != nil {
		t.Fatal(err)
	}
	want := `{"code":null,"expires_at":null,"qr_code":null,"reason":null,"status":"connected"}`
	if string(raw) != want {
		t.Fatalf("payload JSON = %s, want %s", raw, want)
	}

	banned := SessionStatus{Status: SessionStatusTemporaryBan, ExpiresAt: sessionPtr(sessionTestNow.Add(time.Hour))}
	if got := banned.Payload()["expires_at"]; got != "2026-09-24T13:00:00Z" {
		t.Fatalf("expires_at = %v, want 2026-09-24T13:00:00Z", got)
	}
}

func TestBuildSessionStatusBody(t *testing.T) {
	instance := &DeviceInstance{id: "org_2", jid: "5511999999999@s.whatsapp.net", createdAt: time.Now()}
	body := buildSessionStatusBody(instance, SessionStatus{Status: SessionStatusConnected})

	if body["event"] != SessionStatusEvent {
		t.Errorf("event = %v, want %s", body["event"], SessionStatusEvent)
	}
	if body["device_id"] != "5511999999999@s.whatsapp.net" {
		t.Errorf("device_id = %v", body["device_id"])
	}
	if body["session_id"] != "org_2" {
		t.Errorf("session_id = %v, want org_2", body["session_id"])
	}
	if _, err := time.Parse(time.RFC3339, body["timestamp"].(string)); err != nil {
		t.Errorf("timestamp not RFC3339: %v", err)
	}
	payload, _ := body["payload"].(map[string]any)
	if payload["status"] != SessionStatusConnected {
		t.Errorf("payload status = %v", payload["status"])
	}

	unpaired := buildSessionStatusBody(NewDeviceInstance("fresh-slot", nil, nil), SessionStatus{Status: SessionStatusQRTimeout})
	if unpaired["device_id"] != "" || unpaired["session_id"] != "fresh-slot" {
		t.Errorf("unpaired body device_id=%v session_id=%v, want empty and fresh-slot", unpaired["device_id"], unpaired["session_id"])
	}
}

// captureSessionWebhooks points the global webhook at a stub and returns the
// bodies it receives.
func captureSessionWebhooks(t *testing.T) <-chan map[string]any {
	t.Helper()
	originalWebhooks := config.WhatsappWebhook
	originalEvents := config.WhatsappWebhookEvents
	originalSubmit := submitWebhookFn
	originalStorage := webhookStorageForTest
	config.WhatsappWebhook = []string{"https://session-status.test"}
	config.WhatsappWebhookEvents = nil
	webhookStorageForTest = func(string) (*chatstorage.DeviceRecord, error) { return nil, nil }

	got := make(chan map[string]any, 32)
	submitWebhookFn = func(_ context.Context, payload map[string]any, _ string, _ *chatstorage.DeviceWebhookConfig) error {
		got <- payload
		return nil
	}
	t.Cleanup(func() {
		config.WhatsappWebhook = originalWebhooks
		config.WhatsappWebhookEvents = originalEvents
		submitWebhookFn = originalSubmit
		webhookStorageForTest = originalStorage
	})
	trackSessionDispatch(t)
	return got
}

// waitSessionWebhook returns the first captured body for sessionID, skipping
// bodies left over from other tests' goroutines.
func waitSessionWebhook(t *testing.T, got <-chan map[string]any, sessionID string) map[string]any {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case body := <-got:
			if body["event"] == SessionStatusEvent && body["session_id"] == sessionID {
				return body
			}
		case <-deadline:
			t.Fatalf("no %s webhook for session %s", SessionStatusEvent, sessionID)
			return nil
		}
	}
}

func TestHandlerEmitsSessionStatusWebhook(t *testing.T) {
	got := captureSessionWebhooks(t)
	instance := NewDeviceInstance("session-e2e", nil, nil)

	handler(context.Background(), instance, &events.KeepAliveRestored{})

	body := waitSessionWebhook(t, got, "session-e2e")
	if body["device_id"] != "" {
		t.Errorf("device_id = %v, want empty for an unpaired slot", body["device_id"])
	}
	payload, _ := body["payload"].(map[string]any)
	if payload["status"] != SessionStatusKeepAliveRestored {
		t.Errorf("status = %v, want %s", payload["status"], SessionStatusKeepAliveRestored)
	}
}

func TestHandlerLoggedOutWebhookKeepsJID(t *testing.T) {
	got := captureSessionWebhooks(t)
	instance := &DeviceInstance{id: "session-logout", jid: "5511999999999@s.whatsapp.net", createdAt: time.Now()}
	// Mirrors the manager's keep-slot callback, which clears the JID.
	instance.SetOnLoggedOut(func(string) { instance.ResetClient() })

	go handler(context.Background(), instance, &events.LoggedOut{Reason: events.ConnectFailureLoggedOut})
	if msg := recvBroadcast(t); msg.Code != "DEVICE_LOGGED_OUT" {
		t.Fatalf("broadcast code = %s, want DEVICE_LOGGED_OUT", msg.Code)
	}

	body := waitSessionWebhook(t, got, "session-logout")
	if body["device_id"] != "5511999999999@s.whatsapp.net" {
		t.Errorf("device_id = %v, want the JID from before the logout", body["device_id"])
	}
	payload, _ := body["payload"].(map[string]any)
	if payload["status"] != SessionStatusLoggedOut || payload["code"] != 401 {
		t.Errorf("payload = %#v, want logged_out with code 401", payload)
	}
	if instance.JID() != "" {
		t.Error("handleLoggedOut should still clear the JID")
	}
}

func TestHandlerDoesNotBlockOnSlowSessionWebhook(t *testing.T) {
	originalWebhooks := config.WhatsappWebhook
	originalSubmit := submitWebhookFn
	originalStorage := webhookStorageForTest
	config.WhatsappWebhook = []string{"https://slow.test"}
	webhookStorageForTest = func(string) (*chatstorage.DeviceRecord, error) { return nil, nil }
	release := make(chan struct{})
	submitWebhookFn = func(context.Context, map[string]any, string, *chatstorage.DeviceWebhookConfig) error {
		<-release
		return nil
	}
	// Cleanups run last-registered first: release the stuck delivery, wait for
	// it, then restore the globals it reads.
	t.Cleanup(func() {
		config.WhatsappWebhook = originalWebhooks
		submitWebhookFn = originalSubmit
		webhookStorageForTest = originalStorage
	})
	trackSessionDispatch(t)
	t.Cleanup(func() { close(release) })

	done := make(chan struct{})
	go func() {
		handler(context.Background(), NewDeviceInstance("session-slow", nil, nil), &events.KeepAliveRestored{})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("handler blocked on a slow session.status webhook")
	}
}

func TestSessionStatusRespectsEventWhitelist(t *testing.T) {
	originalWebhooks := config.WhatsappWebhook
	originalEvents := config.WhatsappWebhookEvents
	originalSubmit := submitWebhookFn
	config.WhatsappWebhook = []string{"https://session-status.test"}
	calls := 0
	submitWebhookFn = func(_ context.Context, payload map[string]any, _ string, _ *chatstorage.DeviceWebhookConfig) error {
		// Count only this test's body: session.status goroutines started by
		// earlier handler tests may still be delivering.
		if payload["session_id"] == "session-filter" {
			calls++
		}
		return nil
	}
	t.Cleanup(func() {
		config.WhatsappWebhook = originalWebhooks
		config.WhatsappWebhookEvents = originalEvents
		submitWebhookFn = originalSubmit
	})
	instance := NewDeviceInstance("session-filter", nil, nil)

	config.WhatsappWebhookEvents = []string{"message"}
	if err := forwardPayloadToConfiguredWebhooks(context.Background(), buildSessionStatusBody(instance, SessionStatus{Status: SessionStatusConnected}), SessionStatusEvent); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("session.status delivered %d times with a whitelist that omits it", calls)
	}

	config.WhatsappWebhookEvents = []string{"message", SessionStatusEvent}
	if err := forwardPayloadToConfiguredWebhooks(context.Background(), buildSessionStatusBody(instance, SessionStatus{Status: SessionStatusConnected}), SessionStatusEvent); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("session.status delivered %d times with a whitelist that includes it, want 1", calls)
	}
}

func TestSessionStatusNotForwardedToChatwoot(t *testing.T) {
	if shouldForwardEventToChatwoot(SessionStatusEvent) {
		t.Fatal("session.status must not be forwarded to Chatwoot")
	}
}

func TestSessionStatusFromPairingEvents(t *testing.T) {
	cases := []struct {
		name string
		evt  any
		want SessionStatus
	}{
		{"passkey request", &events.PairPasskeyRequest{}, SessionStatus{Status: SessionStatusPasskeyRequired}},
		{"passkey confirmation", &events.PairPasskeyConfirmation{Code: "ABCD-EFGH"}, SessionStatus{Status: SessionStatusPasskeyConfirmation}},
		{"pair error", &events.PairError{Error: errors.New("bad signature")},
			SessionStatus{Status: SessionStatusPairError, Reason: sessionPtr("bad signature")}},
		{"passkey error", &events.PairPasskeyError{Error: errors.New("assertion rejected")},
			SessionStatus{Status: SessionStatusPairError, Reason: sessionPtr("assertion rejected")}},
		{"pair error without error value", &events.PairError{}, SessionStatus{Status: SessionStatusPairError}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := sessionStatusFromEvent(tc.evt, sessionTestNow)
			if !ok {
				t.Fatalf("sessionStatusFromEvent(%T) returned ok=false", tc.evt)
			}
			if !reflect.DeepEqual(got.Payload(), tc.want.Payload()) {
				t.Fatalf("payload = %#v, want %#v", got.Payload(), tc.want.Payload())
			}
		})
	}
}

func TestNewQRSessionStatus(t *testing.T) {
	payload := NewQRSessionStatus("2@abc,def", 60*time.Second, sessionTestNow).Payload()
	want := map[string]any{
		"status":     SessionStatusQR,
		"reason":     nil,
		"code":       nil,
		"expires_at": "2026-09-24T12:01:00Z",
		"qr_code":    "2@abc,def",
	}
	if !reflect.DeepEqual(payload, want) {
		t.Fatalf("payload = %#v, want %#v", payload, want)
	}
}

func TestHandlerPasskeyRequestEmitsSessionStatus(t *testing.T) {
	got := captureSessionWebhooks(t)
	instance := NewDeviceInstance("session-passkey", nil, nil)

	go handler(context.Background(), instance, &events.PairPasskeyRequest{})
	if msg := recvBroadcast(t); msg.Code != "PASSKEY_REQUEST" {
		t.Fatalf("broadcast code = %s, want PASSKEY_REQUEST", msg.Code)
	}

	body := waitSessionWebhook(t, got, "session-passkey")
	payload, _ := body["payload"].(map[string]any)
	if payload["status"] != SessionStatusPasskeyRequired {
		t.Fatalf("status = %v, want %s", payload["status"], SessionStatusPasskeyRequired)
	}
}
