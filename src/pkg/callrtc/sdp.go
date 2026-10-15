package callrtc

import "strings"

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
