package whatsapp

import (
	"context"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/stablepayload"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// Fork (elphant): payload.stable, the closed and versioned view of message
// webhooks. Contract: contract/stable.schema.json; docs/elphant-fork.md.

type stableResolver struct{ client *whatsmeow.Client }

func newStableResolver(client *whatsmeow.Client) stablepayload.Resolver {
	if client == nil || client.Store == nil || client.Store.LIDs == nil {
		return nil
	}
	return stableResolver{client: client}
}

func (r stableResolver) PNForLID(ctx context.Context, lid types.JID) (types.JID, bool) {
	pn, err := r.client.Store.LIDs.GetPNForLID(ctx, lid)
	return pn, err == nil && !pn.IsEmpty()
}

func (r stableResolver) LIDForPN(ctx context.Context, pn types.JID) (types.JID, bool) {
	lid, err := r.client.Store.LIDs.GetLIDForPN(ctx, pn)
	return lid, err == nil && !lid.IsEmpty()
}

// addStablePayload sets payload["stable"] for a message event. msg is the
// message buildEventPayload works on (the decrypted edit when applicable).
func addStablePayload(ctx context.Context, client *whatsmeow.Client, evt *events.Message, msg *waE2E.Message, payload map[string]any) {
	event, stable := stablepayload.Build(ctx, evt, msg, newStableResolver(client))
	payload["stable"] = stable
	var protoFields []string
	if m, ok := stable.(stablepayload.Message); ok && m.Type == stablepayload.TypeUnknown {
		protoFields = populatedMessageFields(msg)
	}
	auditStable(event, stable, protoFields)
}

// addStableAck sets payload.stable on a message.ack webhook body.
func addStableAck(ctx context.Context, client *whatsmeow.Client, evt *events.Receipt, body map[string]any) {
	inner, ok := body["payload"].(map[string]any)
	if !ok {
		return
	}
	stable := stablepayload.BuildAck(ctx, evt, newStableResolver(client))
	inner["stable"] = stable
	auditStable(stablepayload.EventAck, stable, nil)
}
