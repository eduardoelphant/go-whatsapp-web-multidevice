package config

import "strings"

// Fork (elphant): voice calls through meowcaller. See docs/specs/2026-10-14-gateway-voice-calls-design.md.
// Kept out of settings.go so upstream merges stay clean.
var (
	WhatsappCallsEnabled      = false  // answer and place calls on allowed devices
	WhatsappCallsDevices      []string // device ids, phone numbers or JIDs allowed; empty = all. Read at start (changing it needs a restart); a device paired after start gets calls without one
	WhatsappCallsCodec        = "opus" // "opus" or "mlow"
	WhatsappCallsMaxPerDevice = 1      // simultaneous calls per device
	WhatsappCallsTestAudio    = ""     // file played to the peer (stage 2a); empty = silence
)

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
