package validations

import (
	"context"
	"fmt"
	domainUser "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/user"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestValidateUserAvatar(t *testing.T) {
	type args struct {
		request domainUser.AvatarRequest
	}
	tests := []struct {
		name string
		args args
		err  any
	}{
		{
			name: "should success",
			args: args{request: domainUser.AvatarRequest{
				Phone:       "1728937129312@s.whatsapp.net",
				IsPreview:   false,
				IsCommunity: false,
			}},
			err: nil,
		},
		{
			name: "should error with empty phone",
			args: args{request: domainUser.AvatarRequest{
				Phone:       "",
				IsPreview:   false,
				IsCommunity: false,
			}},
			err: pkgError.ValidationError("phone: cannot be blank."),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateUserAvatar(context.Background(), tt.args.request)
			assert.Equal(t, tt.err, err)
		})
	}
}

func TestValidateUserInfo(t *testing.T) {
	type args struct {
		request domainUser.InfoRequest
	}
	tests := []struct {
		name string
		args args
		err  any
	}{
		{
			name: "should success",
			args: args{request: domainUser.InfoRequest{
				Phone: "1728937129312@s.whatsapp.net",
			}},
			err: nil,
		},
		{
			name: "should error with empty phone",
			args: args{request: domainUser.InfoRequest{
				Phone: "",
			}},
			err: pkgError.ValidationError("phone: cannot be blank."),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateUserInfo(context.Background(), tt.args.request)
			assert.Equal(t, tt.err, err)
		})
	}
}

func TestValidateBusinessProfile(t *testing.T) {
	type args struct {
		request domainUser.BusinessProfileRequest
	}
	tests := []struct {
		name string
		args args
		err  any
	}{
		{
			name: "should success with valid phone",
			args: args{request: domainUser.BusinessProfileRequest{
				Phone: "1728937129312@s.whatsapp.net",
			}},
			err: nil,
		},
		{
			name: "should error with empty phone",
			args: args{request: domainUser.BusinessProfileRequest{
				Phone: "",
			}},
			err: pkgError.ValidationError("phone: cannot be blank."),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateBusinessProfile(context.Background(), tt.args.request)
			assert.Equal(t, tt.err, err)
		})
	}
}

func TestValidateCheckBatch(t *testing.T) {
	ctx := context.Background()
	many := make([]string, CheckBatchMaxPhones+1)
	for i := range many {
		many[i] = fmt.Sprintf("55119%08d", i)
	}
	assert.NoError(t, ValidateCheckBatch(ctx, domainUser.CheckBatchRequest{Phones: []string{"5511999999999"}}))
	assert.NoError(t, ValidateCheckBatch(ctx, domainUser.CheckBatchRequest{Phones: many[:CheckBatchMaxPhones]}))
	assert.Error(t, ValidateCheckBatch(ctx, domainUser.CheckBatchRequest{}))
	assert.Error(t, ValidateCheckBatch(ctx, domainUser.CheckBatchRequest{Phones: []string{}}))
	assert.Error(t, ValidateCheckBatch(ctx, domainUser.CheckBatchRequest{Phones: many}))
}

func TestNormalizeBatchPhone(t *testing.T) {
	tests := []struct {
		raw    string
		digits string
		ok     bool
	}{
		{"5511999999999", "5511999999999", true},
		{"+55 11 98888-7777", "5511988887777", true},
		{"(55) 11 97777.6666", "5511977776666", true},
		{"5511977776666@s.whatsapp.net", "5511977776666", true},
		{"  5511977776666  ", "5511977776666", true},
		{"123456", "", false},
		{"1234567", "1234567", true},
		{"123456789012345", "123456789012345", true},
		{"1234567890123456", "", false},
		{"123456789@lid", "", false},
		{"120363000000000000@g.us", "", false},
		{"5511999999999:12@s.whatsapp.net", "", false},
		{"55119999abc99", "", false},
		{"05511988887777", "", false},                           // leading 0: E.164 never starts with 0
		{"5511977776666@S.WHATSAPP.NET", "5511977776666", true}, // the server part is case-insensitive
		{"", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			digits, ok := NormalizeBatchPhone(tt.raw)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.digits, digits)
		})
	}
}
