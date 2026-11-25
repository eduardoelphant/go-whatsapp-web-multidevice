package validations

import (
	"context"
	"strings"
	"testing"

	domainLabel "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/label"
	"github.com/stretchr/testify/assert"
)

func TestValidateCreateLabel(t *testing.T) {
	tests := []struct {
		name    string
		req     domainLabel.CreateLabelRequest
		wantErr bool
	}{
		{"valid", domainLabel.CreateLabelRequest{Name: "VIP", Color: 3}, false},
		{"color zero ok", domainLabel.CreateLabelRequest{Name: "VIP", Color: 0}, false},
		{"color 19 ok", domainLabel.CreateLabelRequest{Name: "VIP", Color: 19}, false},
		{"empty name", domainLabel.CreateLabelRequest{Name: "", Color: 1}, true},
		{"blank name", domainLabel.CreateLabelRequest{Name: "   ", Color: 1}, true},
		{"name too long", domainLabel.CreateLabelRequest{Name: strings.Repeat("a", 101), Color: 1}, true},
		{"name 100 ok", domainLabel.CreateLabelRequest{Name: strings.Repeat("a", 100), Color: 1}, false},
		{"color 20", domainLabel.CreateLabelRequest{Name: "VIP", Color: 20}, true},
		{"color negative", domainLabel.CreateLabelRequest{Name: "VIP", Color: -1}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCreateLabel(context.Background(), &tt.req)
			assert.Equal(t, tt.wantErr, err != nil)
		})
	}
}

func TestValidateCreateLabelTrimsName(t *testing.T) {
	req := domainLabel.CreateLabelRequest{Name: "  VIP  ", Color: 1}
	assert.NoError(t, ValidateCreateLabel(context.Background(), &req))
	assert.Equal(t, "VIP", req.Name)
}

func TestValidateUpdateAndDeleteLabel(t *testing.T) {
	ctx := context.Background()
	assert.NoError(t, ValidateUpdateLabel(ctx, &domainLabel.UpdateLabelRequest{LabelID: "17", Name: "A", Color: 2}))
	assert.Error(t, ValidateUpdateLabel(ctx, &domainLabel.UpdateLabelRequest{LabelID: "abc", Name: "A", Color: 2}))
	assert.Error(t, ValidateUpdateLabel(ctx, &domainLabel.UpdateLabelRequest{LabelID: "", Name: "A", Color: 2}))
	assert.Error(t, ValidateUpdateLabel(ctx, &domainLabel.UpdateLabelRequest{LabelID: "17", Name: "", Color: 2}))
	assert.Error(t, ValidateUpdateLabel(ctx, &domainLabel.UpdateLabelRequest{LabelID: "17", Name: "A", Color: 25}))
	assert.NoError(t, ValidateDeleteLabel(ctx, &domainLabel.DeleteLabelRequest{LabelID: "17", Name: "A"}))
	assert.Error(t, ValidateDeleteLabel(ctx, &domainLabel.DeleteLabelRequest{LabelID: "1x", Name: "A"}))
	assert.Error(t, ValidateDeleteLabel(ctx, &domainLabel.DeleteLabelRequest{LabelID: "17", Name: " "}))
}

func TestValidateLabelChat(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		req     domainLabel.LabelChatRequest
		wantErr bool
	}{
		{"user", domainLabel.LabelChatRequest{LabelID: "1", ChatJID: "5511999999999@s.whatsapp.net"}, false},
		{"group", domainLabel.LabelChatRequest{LabelID: "1", ChatJID: "120363424051089958@g.us"}, false},
		{"empty jid", domainLabel.LabelChatRequest{LabelID: "1", ChatJID: ""}, true},
		{"garbage jid", domainLabel.LabelChatRequest{LabelID: "1", ChatJID: "a@b@c"}, true},
		{"no server", domainLabel.LabelChatRequest{LabelID: "1", ChatJID: "5511999999999"}, true},
		{"broadcast server", domainLabel.LabelChatRequest{LabelID: "1", ChatJID: "status@broadcast"}, true},
		{"bad label id", domainLabel.LabelChatRequest{LabelID: "x", ChatJID: "5511999999999@s.whatsapp.net"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateLabelChat(ctx, &tt.req)
			assert.Equal(t, tt.wantErr, err != nil)
		})
	}
}
