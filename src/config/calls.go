package config

import "strings"

// CallsAllowed reports whether calls are enabled for a device, matching the allow
// list against the slot id, the phone number (JID user) or the full JID string.
func CallsAllowed(slotID, jidUser, jidString string) bool {
	if !WhatsappCallsEnabled {
		return false
	}
	if len(WhatsappCallsDevices) == 0 {
		return true
	}
	for _, entry := range WhatsappCallsDevices {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if entry == slotID || (jidUser != "" && entry == jidUser) || (jidString != "" && entry == jidString) {
			return true
		}
	}
	return false
}
