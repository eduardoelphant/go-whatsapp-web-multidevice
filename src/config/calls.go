package config

import "strings"

// Fork (elphant): voice calls through meowcaller. See docs/specs/2026-10-14-gateway-voice-calls-design.md.
// Kept out of settings.go so upstream merges stay clean.
var (
	WhatsappCallsEnabled                = false  // answer and place calls on allowed devices
	WhatsappCallsDevices                []string // seeds the per-device switch of a device that has none yet (stage 2b); later changes go through PUT /devices/:device_id/calls
	WhatsappCallsCodec                  = "opus" // "opus" or "mlow"
	WhatsappCallsMaxPerDevice           = 1      // simultaneous calls per device
	WhatsappCallsLogLevel               = "info" // call engine log level: debug, info, warn or error
	WhatsappCallsEarlyMedia             = "off"  // inbound ringing media: off, listen or full (full shows the call as answered to the caller; do not use)
	WhatsappCallsMaxDurationMinutes     = 120    // stage 2b: hang up a call after this many minutes and report it FAILED (0: no limit)
	WhatsappCallsRecordingURLTTLMinutes = 1440   // stage 2b: validity of the signed recording link in call.ended
	WhatsappCallsRecordingRetentionDays = 7      // stage 2b: recordings older than this are deleted (0: keep)
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
