package validations

import (
	"context"
	"fmt"
	"testing"

	domainLID "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/lid"
	"github.com/stretchr/testify/assert"
)

func TestNormalizeLID(t *testing.T) {
	tests := []struct {
		raw    string
		digits string
		ok     bool
	}{
		{"100000000000001", "100000000000001", true},
		{"100000000000001@lid", "100000000000001", true},
		{"  100000000000001@lid ", "100000000000001", true},
		{"5511999999999@s.whatsapp.net", "", false},
		{"abc", "", false},
		{"", "", false},
		{"@lid", "", false},
		{"123456789012345678901", "", false}, // 21 digits
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			digits, ok := NormalizeLID(tt.raw)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.digits, digits)
		})
	}
}

func TestValidateLookup(t *testing.T) {
	ctx := context.Background()
	many := make([]string, LookupMaxEntries+1)
	for i := range many {
		many[i] = fmt.Sprintf("55119%08d", i)
	}
	assert.NoError(t, ValidateLookup(ctx, domainLID.LookupRequest{PNs: []string{"5511999999999"}}))
	assert.NoError(t, ValidateLookup(ctx, domainLID.LookupRequest{LIDs: []string{"100000000000001"}}))
	assert.NoError(t, ValidateLookup(ctx, domainLID.LookupRequest{PNs: many[:300], LIDs: many[:200]}))
	assert.Error(t, ValidateLookup(ctx, domainLID.LookupRequest{}))
	assert.Error(t, ValidateLookup(ctx, domainLID.LookupRequest{PNs: many[:300], LIDs: many[:201]}))
	assert.Error(t, ValidateLookup(ctx, domainLID.LookupRequest{PNs: many}))
}

func TestClampListLimit(t *testing.T) {
	assert.Equal(t, ListDefaultLimit, ClampListLimit(0))
	assert.Equal(t, ListDefaultLimit, ClampListLimit(-5))
	assert.Equal(t, 50, ClampListLimit(50))
	assert.Equal(t, ListMaxLimit, ClampListLimit(ListMaxLimit))
	assert.Equal(t, ListMaxLimit, ClampListLimit(ListMaxLimit+1))
}
