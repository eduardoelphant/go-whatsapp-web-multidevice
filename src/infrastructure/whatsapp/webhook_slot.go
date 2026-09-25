package whatsapp

import (
	"fmt"

	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
)

// Fork (elphant): session.status bodies carry the slot id in session_id before
// routing (other events only get it afterwards). Routing by it covers events with
// no JID yet (QR, passkey) and a logged_out whose record lost its JID to the
// keep-slot cleanup while the event was being delivered. When the slot has no
// webhook, the JID lookup result stands.

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
