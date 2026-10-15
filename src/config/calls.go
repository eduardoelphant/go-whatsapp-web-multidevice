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
	WhatsappCallsRTCPort                = 3478   // stage 2b: one UDP port for every browser leg (ICE UDP mux); publish it in mode: host
	WhatsappCallsRTCPublicIP            = ""     // stage 2b: the host's public IPv4, put in the SDP's host candidate (1:1 NAT)
	WhatsappCallsRTCGraceSeconds        = 10     // stage 2b: hang up the WhatsApp leg after the browser leg is down this long
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
