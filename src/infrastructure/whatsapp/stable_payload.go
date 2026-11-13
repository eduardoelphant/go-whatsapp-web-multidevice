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
// webhooks. Contract: contract/stable.schema.json; docs/reference/elphant-fork.md.

type stableResolver struct{ client *whatsmeow.Client }

func newStableResolver(client *whatsmeow.Client) stablepayload.Resolver {
	if client == nil || client.Store == nil {
		return nil
	}
	return stableResolver{client: client}
}

// OwnJIDs returns the connected account's phone and LID JIDs. Fork (elphant).
func (r stableResolver) OwnJIDs() []types.JID { return ownJIDs(r.client) }

func ownJIDs(client *whatsmeow.Client) []types.JID {
	if client == nil || client.Store == nil {
		return nil
	}
	var own []types.JID
	if client.Store.ID != nil {
		own = append(own, *client.Store.ID)
	}
	if !client.Store.LID.IsEmpty() {
		own = append(own, client.Store.LID)
	}
	return own
}

func (r stableResolver) PNForLID(ctx context.Context, lid types.JID) (types.JID, bool) {
	if r.client.Store.LIDs == nil {
		return types.JID{}, false
	}
	pn, err := r.client.Store.LIDs.GetPNForLID(ctx, lid)
	return pn, err == nil && !pn.IsEmpty()
}

func (r stableResolver) LIDForPN(ctx context.Context, pn types.JID) (types.JID, bool) {
	if r.client.Store.LIDs == nil {
		return types.JID{}, false
	}
	lid, err := r.client.Store.LIDs.GetLIDForPN(ctx, pn)
	return lid, err == nil && !lid.IsEmpty()
}

// addStablePayload sets payload["stable"] for a message event. msg is the
// message buildEventPayload works on (the decrypted edit when applicable).
// preparedPoll is GOWA's decrypted poll data, when the message is a poll vote.
func addStablePayload(ctx context.Context, client *whatsmeow.Client, evt *events.Message, msg *waE2E.Message, payload map[string]any, preparedPoll ...*webhookPollPayload) {
	event, stable := stablepayload.Build(ctx, evt, msg, newStableResolver(client))
	if m, ok := stable.(stablepayload.Message); ok && m.Type == stablepayload.TypePollVote &&
		len(preparedPoll) > 0 && preparedPoll[0] != nil && preparedPoll[0].Type == "vote" {
		var selected []string
		if preparedPoll[0].SelectedOptions != nil {
			selected = *preparedPoll[0].SelectedOptions
		}
		stable = stablepayload.WithPollVote(m, preparedPoll[0].PollID, selected, preparedPoll[0].ResolutionStatus)
	}
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
