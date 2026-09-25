package whatsapp

import (
	"context"
	"reflect"
	"testing"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
)

func TestForwardPayloadResolvesSlotWebhookBeforePairing(t *testing.T) {
	const globalURL = "https://global-webhook.test"
	deviceURL := "https://slot-webhook.test"

	cases := []struct {
		name         string
		payload      map[string]any
		slotRecord   *chatstorage.DeviceRecord
		wantURLs     []string
		wantLookedUp []string
	}{
		{
			name:         "before pairing uses slot config",
			payload:      map[string]any{"event": SessionStatusEvent, "device_id": "", "session_id": "slot-a"},
			slotRecord:   &chatstorage.DeviceRecord{DeviceID: "slot-a", WebhookURL: &deviceURL},
			wantURLs:     []string{deviceURL},
			wantLookedUp: []string{"slot-a"},
		},
		{
			name:         "slot event filter drops event",
			payload:      map[string]any{"event": SessionStatusEvent, "device_id": "", "session_id": "slot-a"},
			slotRecord:   &chatstorage.DeviceRecord{DeviceID: "slot-a", WebhookURL: &deviceURL, WebhookEvents: "message"},
			wantURLs:     nil,
			wantLookedUp: []string{"slot-a"},
		},
		{
			name:         "slot without webhook falls back to global",
			payload:      map[string]any{"event": SessionStatusEvent, "device_id": "", "session_id": "slot-a"},
			slotRecord:   &chatstorage.DeviceRecord{DeviceID: "slot-a"},
			wantURLs:     []string{globalURL},
			wantLookedUp: []string{"slot-a"},
		},
		{
			name:         "no session id falls back to global",
			payload:      map[string]any{"event": SessionStatusEvent, "device_id": ""},
			slotRecord:   &chatstorage.DeviceRecord{DeviceID: "slot-a", WebhookURL: &deviceURL},
			wantURLs:     []string{globalURL},
			wantLookedUp: nil,
		},
		{
			name:         "paired device keeps JID lookup",
			payload:      map[string]any{"event": SessionStatusEvent, "device_id": "5511999999999@s.whatsapp.net", "session_id": "slot-a"},
			slotRecord:   &chatstorage.DeviceRecord{DeviceID: "slot-a", WebhookURL: &deviceURL},
			wantURLs:     []string{globalURL},
			wantLookedUp: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			originalWebhooks := config.WhatsappWebhook
			originalEvents := config.WhatsappWebhookEvents
			originalSubmit := submitWebhookFn
			originalStorage := webhookStorageForTest
			originalSlot := webhookSlotStorageForTest
			t.Cleanup(func() {
				config.WhatsappWebhook = originalWebhooks
				config.WhatsappWebhookEvents = originalEvents
				submitWebhookFn = originalSubmit
				webhookStorageForTest = originalStorage
				webhookSlotStorageForTest = originalSlot
			})

			config.WhatsappWebhook = []string{globalURL}
			config.WhatsappWebhookEvents = nil
			webhookStorageForTest = func(string) (*chatstorage.DeviceRecord, error) { return nil, nil }
			var lookedUp []string
			webhookSlotStorageForTest = func(slotID string) (*chatstorage.DeviceRecord, error) {
				lookedUp = append(lookedUp, slotID)
				return tc.slotRecord, nil
			}
			var calledURLs []string
			submitWebhookFn = func(_ context.Context, payload map[string]any, url string, _ *chatstorage.DeviceWebhookConfig) error {
				// Ignore session.status deliveries still running from earlier handler tests.
				if sessionID, ok := payload["session_id"]; ok && sessionID != "slot-a" {
					return nil
				}
				calledURLs = append(calledURLs, url)
				return nil
			}

			if err := forwardPayloadToConfiguredWebhooks(context.Background(), tc.payload, SessionStatusEvent); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(calledURLs, tc.wantURLs) {
				t.Errorf("delivered to %v, want %v", calledURLs, tc.wantURLs)
			}
			if !reflect.DeepEqual(lookedUp, tc.wantLookedUp) {
				t.Errorf("slot lookups %v, want %v", lookedUp, tc.wantLookedUp)
			}
		})
	}
}
