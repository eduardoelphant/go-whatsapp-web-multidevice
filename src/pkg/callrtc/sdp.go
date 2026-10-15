package callrtc

import (
	"fmt"
	"strings"

	"github.com/pion/sdp/v3"
)

// NormalizeSDP gives every line a CRLF ending, as Meta's and the browsers' SDP has; some
// clients hand over LF-only text, and the CRM normalises the same way.
func NormalizeSDP(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return ""
	}
	return strings.ReplaceAll(s, "\n", "\r\n") + "\r\n"
}

// requireOpusAudio refuses an offer that has no m=audio section with an Opus 48 kHz rtpmap.
func requireOpusAudio(s string) error {
	var desc sdp.SessionDescription
	if err := desc.UnmarshalString(s); err != nil {
		return fmt.Errorf("callrtc: browser offer: %w", err)
	}
	for _, m := range desc.MediaDescriptions {
		if m.MediaName.Media != "audio" {
			continue
		}
		for _, a := range m.Attributes {
			if a.Key == "rtpmap" && strings.Contains(strings.ToLower(a.Value), " opus/48000") {
				return nil
			}
		}
	}
	return errNoOpus
}
