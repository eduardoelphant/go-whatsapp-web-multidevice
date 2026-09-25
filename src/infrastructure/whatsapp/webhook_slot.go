package whatsapp

import (
	"fmt"

	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
)

// Fork (elphant): events emitted before pairing (QR, passkey) have no JID, so the
// JID-based device webhook lookup finds nothing. They carry the slot id in
// session_id instead, and this resolves the device webhook config from it.

// getWebhookConfigForSlot returns the device webhook config for the payload's
// session_id, or nil when there is no slot id or the slot has no webhook URL.
// It reads GetDeviceWebhookConfig: GetDeviceRecord does not load webhook columns.
func getWebhookConfigForSlot(payload map[string]any) (*domainChatStorage.DeviceWebhookConfig, error) {
	slotID, _ := payload["session_id"].(string)
	if slotID == "" {
		return nil, nil
	}
	dm := GetDeviceManager()
	if dm == nil || dm.storage == nil {
		return nil, nil
	}
	webhookConfig, err := dm.storage.GetDeviceWebhookConfig(slotID)
	if err != nil {
		return nil, fmt.Errorf("failed to get webhook config for slot %s: %w", slotID, err)
	}
	if webhookConfig == nil || webhookConfig.WebhookURL == nil || *webhookConfig.WebhookURL == "" {
		return nil, nil
	}
	return webhookConfig, nil
}
