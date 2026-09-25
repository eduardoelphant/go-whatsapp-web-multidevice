package stablepayload

import (
	"context"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// BuildAck returns the stable object of a message.ack event. The base id is
// the first acknowledged message id; ids lists all of them.
func BuildAck(ctx context.Context, evt *events.Receipt, r Resolver) Ack {
	ids := make([]string, 0, len(evt.MessageIDs))
	for _, id := range evt.MessageIDs {
		ids = append(ids, string(id))
	}
	first := ""
	if len(ids) > 0 {
		first = ids[0]
	}
	b := base(ctx, types.MessageInfo{MessageSource: evt.MessageSource, ID: first, Timestamp: evt.Timestamp}, r)
	return Ack{Base: b, Status: ackStatus(evt.Type), IDs: ids}
}

func ackStatus(t types.ReceiptType) string {
	switch t {
	case types.ReceiptTypeRead, types.ReceiptTypeReadSelf:
		return "read"
	case types.ReceiptTypePlayed, types.ReceiptTypePlayedSelf:
		return "played"
	default:
		return "delivered"
	}
}
