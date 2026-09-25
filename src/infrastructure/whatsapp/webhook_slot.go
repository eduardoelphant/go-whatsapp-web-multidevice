package whatsapp

import (
	"fmt"

	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
)

// Fork (elphant): events emitted before pairing (QR, passkey) have no JID, so the
// JID-based device webhook lookup finds nothing. They carry the slot id in
// session_id instead, and this resolves the device webhook config from it.

// webhookSlotStorageForTest replaces the slot record lookup in tests.
var webhookSlotStorageForTest func(slotID string) (*domainChatStorage.DeviceRecord, error)

// getWebhookConfigForSlot returns the device webhook config for the payload's
// session_id, or nil when there is no slot id or the slot has no webhook URL.
func getWebhookConfigForSlot(payload map[string]any) (*domainChatStorage.DeviceWebhookConfig, error) {
	slotID, _ := payload["session_id"].(string)
	if slotID == "" {
		return nil, nil
	}
	record, err := getDeviceRecordBySlot(slotID)
	if err != nil {
		return nil, fmt.Errorf("failed to get device record for slot %s: %w", slotID, err)
	}
	if record == nil || record.WebhookURL == nil || *record.WebhookURL == "" {
		return nil, nil
	}
	return &domainChatStorage.DeviceWebhookConfig{
		WebhookURL:                record.WebhookURL,
		WebhookSecret:             record.WebhookSecret,
		WebhookEvents:             record.WebhookEvents,
		WebhookInsecureSkipVerify: record.WebhookInsecureSkipVerify,
	}, nil
}

func getDeviceRecordBySlot(slotID string) (*domainChatStorage.DeviceRecord, error) {
	if webhookSlotStorageForTest != nil {
		return webhookSlotStorageForTest(slotID)
	}
	dm := GetDeviceManager()
	if dm != nil && dm.storage != nil {
		return dm.storage.GetDeviceRecord(slotID)
	}
	return nil, nil
}
