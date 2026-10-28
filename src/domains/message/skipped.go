package message

import (
	"context"
	"errors"
	"time"
)

// Fork (elphant): GET /message/:message_id/skipped.

var ErrSkippedMessageNotFound = errors.New("message was not skipped by chat storage")

// SkippedMessageResponse says what a message without text or media was.
// Reason is a short label such as "protocol:EPHEMERAL_SETTING", "album",
// "placeholder:MASK_LINKED_DEVICES" or "field:<protoField>"; Fields lists the
// populated proto field names (never values).
type SkippedMessageResponse struct {
	MessageID string    `json:"message_id"`
	ChatJID   string    `json:"chat_jid"`
	SenderJID string    `json:"sender_jid"`
	IsFromMe  bool      `json:"is_from_me"`
	Reason    string    `json:"reason"`
	Fields    []string  `json:"fields"`
	Timestamp time.Time `json:"timestamp"`
}

type IMessageSkipped interface {
	SkippedMessage(ctx context.Context, messageID string) (SkippedMessageResponse, error)
}
