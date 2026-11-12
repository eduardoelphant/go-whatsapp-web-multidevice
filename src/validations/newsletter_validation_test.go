package validations

import (
	"context"
	"math"
	"strings"
	"testing"

	domainNewsletter "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/newsletter"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/stretchr/testify/assert"
)

func TestValidateUnfollowNewsletter(t *testing.T) {
	type args struct {
		request domainNewsletter.UnfollowRequest
	}
	tests := []struct {
		name string
		args args
		err  any
	}{
		{
			name: "should success with valid newsletter id",
			args: args{request: domainNewsletter.UnfollowRequest{
				NewsletterID: "120363123456789@newsletter",
			}},
			err: nil,
		},
		{
			name: "should success with different newsletter id format",
			args: args{request: domainNewsletter.UnfollowRequest{
				NewsletterID: "newsletter-abc123xyz",
			}},
			err: nil,
		},
		{
			name: "should error with empty newsletter id",
			args: args{request: domainNewsletter.UnfollowRequest{
				NewsletterID: "",
			}},
			err: pkgError.ValidationError("newsletter_id: cannot be blank."),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateUnfollowNewsletter(context.Background(), tt.args.request)
			assert.Equal(t, tt.err, err)
		})
	}
}

func TestValidateGetNewsletterMessages(t *testing.T) {
	tests := []struct {
		name       string
		request    domainNewsletter.GetMessagesRequest
		err        any
		wantCount  int
		wantBefore int
	}{
		{
			name:      "should success with valid newsletter id and default count",
			request:   domainNewsletter.GetMessagesRequest{NewsletterID: "120363123456789@newsletter"},
			err:       nil,
			wantCount: 50,
		},
		{
			name:       "should success with explicit count and before",
			request:    domainNewsletter.GetMessagesRequest{NewsletterID: "120363123456789@newsletter", Count: 10, Before: 100},
			err:        nil,
			wantCount:  10,
			wantBefore: 100,
		},
		{
			name:    "should error with empty newsletter id",
			request: domainNewsletter.GetMessagesRequest{},
			err:     pkgError.ValidationError("newsletter_id: cannot be blank."),
		},
		{
			name:      "should error when count exceeds max",
			request:   domainNewsletter.GetMessagesRequest{NewsletterID: "120363123456789@newsletter", Count: 101},
			err:       pkgError.ValidationError("count: must be no greater than 100."),
			wantCount: 101,
		},
		{
			name:      "should error with negative before",
			request:   domainNewsletter.GetMessagesRequest{NewsletterID: "120363123456789@newsletter", Before: -1},
			err:       pkgError.ValidationError("before: must be no less than 0."),
			wantCount: 50,
		},
		{
			name:      "should error with a non-newsletter jid (phone)",
			request:   domainNewsletter.GetMessagesRequest{NewsletterID: "6289685028129@s.whatsapp.net"},
			err:       pkgError.ValidationError("newsletter_id: must end with @newsletter."),
			wantCount: 50,
		},
		{
			name:      "should error with a non-newsletter jid (group)",
			request:   domainNewsletter.GetMessagesRequest{NewsletterID: "120363123456789@g.us"},
			err:       pkgError.ValidationError("newsletter_id: must end with @newsletter."),
			wantCount: 50,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := tt.request
			err := ValidateGetNewsletterMessages(context.Background(), &req)
			assert.Equal(t, tt.err, err)
			if tt.wantCount != 0 {
				assert.Equal(t, tt.wantCount, req.Count)
			}
			if tt.wantBefore != 0 {
				assert.Equal(t, tt.wantBefore, req.Before)
			}
		})
	}
}

func TestValidateDownloadNewsletterMedia(t *testing.T) {
	tests := []struct {
		name    string
		request domainNewsletter.DownloadMediaRequest
		err     any
	}{
		{
			name: "should accept valid request",
			request: domainNewsletter.DownloadMediaRequest{
				NewsletterID: "120363123456789@newsletter",
				ServerID:     8872,
			},
		},
		{
			name:    "should reject empty newsletter id",
			request: domainNewsletter.DownloadMediaRequest{ServerID: 8872},
			err:     pkgError.ValidationError("newsletter_id: cannot be blank."),
		},
		{
			name: "should reject non-newsletter jid",
			request: domainNewsletter.DownloadMediaRequest{
				NewsletterID: "120363123456789@g.us",
				ServerID:     8872,
			},
			err: pkgError.ValidationError("newsletter_id: must end with @newsletter."),
		},
		{
			name: "should reject zero server id",
			request: domainNewsletter.DownloadMediaRequest{
				NewsletterID: "120363123456789@newsletter",
			},
			err: pkgError.ValidationError("server_id: must be no less than 1."),
		},
		{
			name: "should reject negative server id",
			request: domainNewsletter.DownloadMediaRequest{
				NewsletterID: "120363123456789@newsletter",
				ServerID:     -1,
			},
			err: pkgError.ValidationError("server_id: must be no less than 1."),
		},
		{
			name: "should reject server id that would overflow lookup boundary",
			request: domainNewsletter.DownloadMediaRequest{
				NewsletterID: "120363123456789@newsletter",
				ServerID:     math.MaxInt,
			},
			err: pkgError.ValidationError("server_id: must be less than the maximum integer value."),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateDownloadNewsletterMedia(context.Background(), tt.request)
			assert.Equal(t, tt.err, err)
		})
	}
}

func TestValidateNewsletterInfo(t *testing.T) {
	tests := []struct {
		name string
		id   string
		ok   bool
	}{
		{"valid", "120363123456789@newsletter", true},
		{"empty", "", false},
		{"group jid", "120363123456789@g.us", false},
		{"phone jid", "5511999999999@s.whatsapp.net", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateNewsletterInfo(context.Background(), domainNewsletter.InfoRequest{NewsletterID: tt.id})
			if tt.ok {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

func TestValidateCreateNewsletter(t *testing.T) {
	jpeg := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, make([]byte, 32)...)
	png := append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, make([]byte, 32)...)
	tests := []struct {
		name    string
		request domainNewsletter.CreateRequest
		ok      bool
	}{
		{"name only", domainNewsletter.CreateRequest{Name: "Canal"}, true},
		{"name 100 runes", domainNewsletter.CreateRequest{Name: strings.Repeat("é", 100)}, true},
		{"name 101 runes", domainNewsletter.CreateRequest{Name: strings.Repeat("a", 101)}, false},
		{"name empty", domainNewsletter.CreateRequest{}, false},
		{"description 1024", domainNewsletter.CreateRequest{Name: "C", Description: strings.Repeat("a", 1024)}, true},
		{"description 1025", domainNewsletter.CreateRequest{Name: "C", Description: strings.Repeat("a", 1025)}, false},
		{"jpeg picture", domainNewsletter.CreateRequest{Name: "C", Picture: jpeg}, true},
		{"png picture", domainNewsletter.CreateRequest{Name: "C", Picture: png}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCreateNewsletter(context.Background(), tt.request)
			if tt.ok {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}
