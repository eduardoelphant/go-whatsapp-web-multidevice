package validations

import (
	"context"
	"strings"

	domainLID "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/lid"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
)

const (
	LookupMaxEntries = 500
	ListDefaultLimit = 100
	ListMaxLimit     = 1000
	lidMaxDigits     = 20
)

// NormalizeLID reduces a LID to its digits, with or without the @lid suffix.
func NormalizeLID(raw string) (string, bool) {
	value := strings.TrimSpace(raw)
	value = strings.TrimSuffix(value, "@lid")
	if value == "" || len(value) > lidMaxDigits {
		return "", false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	return value, true
}

// ValidateLookup checks the size of a batch lookup; entries are normalized one by one later.
func ValidateLookup(_ context.Context, request domainLID.LookupRequest) error {
	total := len(request.PNs) + len(request.LIDs)
	if total == 0 {
		return pkgError.ValidationError("pns or lids is required")
	}
	if total > LookupMaxEntries {
		return pkgError.ValidationError("at most 500 entries per lookup")
	}
	return nil
}

// ClampListLimit applies the default and the maximum page size.
func ClampListLimit(limit int) int {
	if limit <= 0 {
		return ListDefaultLimit
	}
	if limit > ListMaxLimit {
		return ListMaxLimit
	}
	return limit
}
