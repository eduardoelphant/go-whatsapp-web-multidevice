package whatsapp

import (
	"context"
	"reflect"
	"testing"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
)

// slotWebhookStubStorage mirrors how SQLiteRepository stores device webhooks:
// GetDeviceRecord does not load the webhook columns, GetDeviceWebhookConfig does.
type slotWebhookStubStorage struct {
	chatstorage.IChatStorageRepository
	configs  map[string]*chatstorage.DeviceWebhookConfig
	lookedUp []string
}

func (s *slotWebhookStubStorage) GetDeviceRecord(deviceID string) (*chatstorage.DeviceRecord, error) {
	if _, ok := s.configs[deviceID]; !ok {
		return nil, nil
	}
	return &chatstorage.DeviceRecord{DeviceID: deviceID}, nil
}

func (s *slotWebhookStubStorage) GetDeviceWebhookConfig(deviceID string) (*chatstorage.DeviceWebhookConfig, error) {
	s.lookedUp = append(s.lookedUp, deviceID)
	return s.configs[deviceID], nil
}

func TestForwardPayloadResolvesSlotWebhookBeforePairing(t *testing.T) {
	const globalURL = "https://global-webhook.test"
	deviceURL := "https://slot-webhook.test"

	cases := []struct {
		name         string
		payload      map[string]any
		slotConfig   *chatstorage.DeviceWebhookConfig
		wantURLs     []string
		wantLookedUp []string
	}{
		{
			name:         "before pairing uses slot config",
			payload:      map[string]any{"event": SessionStatusEvent, "device_id": "", "session_id": "slot-a"},
			slotConfig:   &chatstorage.DeviceWebhookConfig{WebhookURL: &deviceURL},
			wantURLs:     []string{deviceURL},
			wantLookedUp: []string{"slot-a"},
		},
		{
			name:         "slot event filter drops event",
			payload:      map[string]any{"event": SessionStatusEvent, "device_id": "", "session_id": "slot-a"},
			slotConfig:   &chatstorage.DeviceWebhookConfig{WebhookURL: &deviceURL, WebhookEvents: "message"},
			wantURLs:     nil,
			wantLookedUp: []string{"slot-a"},
		},
		{
			name:         "slot without webhook falls back to global",
			payload:      map[string]any{"event": SessionStatusEvent, "device_id": "", "session_id": "slot-a"},
			slotConfig:   &chatstorage.DeviceWebhookConfig{},
			wantURLs:     []string{globalURL},
			wantLookedUp: []string{"slot-a"},
		},
		{
			name:         "no session id falls back to global",
			payload:      map[string]any{"event": SessionStatusEvent, "device_id": ""},
			slotConfig:   &chatstorage.DeviceWebhookConfig{WebhookURL: &deviceURL},
			wantURLs:     []string{globalURL},
			wantLookedUp: nil,
		},
		{
			name:         "paired device keeps JID lookup",
			payload:      map[string]any{"event": SessionStatusEvent, "device_id": "5511999999999@s.whatsapp.net", "session_id": "slot-a"},
			slotConfig:   &chatstorage.DeviceWebhookConfig{WebhookURL: &deviceURL},
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
			t.Cleanup(func() {
				config.WhatsappWebhook = originalWebhooks
				config.WhatsappWebhookEvents = originalEvents
				submitWebhookFn = originalSubmit
				webhookStorageForTest = originalStorage
			})

			config.WhatsappWebhook = []string{globalURL}
			config.WhatsappWebhookEvents = nil
			webhookStorageForTest = func(string) (*chatstorage.DeviceRecord, error) { return nil, nil }
			storage := &slotWebhookStubStorage{configs: map[string]*chatstorage.DeviceWebhookConfig{"slot-a": tc.slotConfig}}
			withDeviceManager(t, NewDeviceManager(nil, nil, storage))
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
			if !reflect.DeepEqual(storage.lookedUp, tc.wantLookedUp) {
				t.Errorf("slot lookups %v, want %v", storage.lookedUp, tc.wantLookedUp)
			}
		})
	}
}
