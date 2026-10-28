package usecase

import (
	"context"
	"fmt"

	domainMessage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/message"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
)

// Fork (elphant): what a message chat storage skipped (no text, no media) was.
func (service serviceMessage) SkippedMessage(ctx context.Context, messageID string) (domainMessage.SkippedMessageResponse, error) {
	client := whatsapp.ClientFromContext(ctx)
	if client == nil || client.Store == nil || client.Store.ID == nil {
		return domainMessage.SkippedMessageResponse{}, pkgError.ErrWaCLI
	}
	skipped, err := service.chatStorageRepo.GetSkippedMessageByDevice(client.Store.ID.ToNonAD().String(), messageID)
	if err != nil {
		return domainMessage.SkippedMessageResponse{}, fmt.Errorf("look up skipped message %s: %w", messageID, err)
	}
	if skipped == nil {
		return domainMessage.SkippedMessageResponse{}, domainMessage.ErrSkippedMessageNotFound
	}
	fields := skipped.Fields
	if fields == nil {
		fields = []string{}
	}
	return domainMessage.SkippedMessageResponse{
		MessageID: skipped.ID,
		ChatJID:   skipped.ChatJID,
		SenderJID: skipped.Sender,
		IsFromMe:  skipped.IsFromMe,
		Reason:    skipped.Reason,
		Fields:    fields,
		Timestamp: skipped.Timestamp,
	}, nil
}
