package stablepayload

import (
	"context"
	"time"

	"go.mau.fi/whatsmeow/types"
)

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// resolve fills pn and lid for a JID. A LID fills lid and looks up pn; a phone
// JID fills pn and looks up lid; groups, broadcasts and newsletters fill pn only.
// alt (whatsmeow's SenderAlt/RecipientAlt) is used before the resolver.
func resolve(ctx context.Context, jid, alt types.JID, r Resolver) (pn, lid *string) {
	if jid.IsEmpty() {
		return nil, nil
	}
	j := jid.ToNonAD()
	switch j.Server {
	case types.HiddenUserServer:
		lid = strPtr(j.String())
		pn = other(alt, types.DefaultUserServer, func() (types.JID, bool) {
			if r == nil {
				return types.JID{}, false
			}
			return r.PNForLID(ctx, j)
		})
	case types.DefaultUserServer:
		pn = strPtr(j.String())
		lid = other(alt, types.HiddenUserServer, func() (types.JID, bool) {
			if r == nil {
				return types.JID{}, false
			}
			return r.LIDForPN(ctx, j)
		})
	default:
		pn = strPtr(j.String())
	}
	return pn, lid
}

func other(alt types.JID, server string, lookup func() (types.JID, bool)) *string {
	if !alt.IsEmpty() && alt.Server == server {
		return strPtr(alt.ToNonAD().String())
	}
	if found, ok := lookup(); ok && !found.IsEmpty() {
		return strPtr(found.ToNonAD().String())
	}
	return nil
}

func base(ctx context.Context, info types.MessageInfo, r Resolver) Base {
	b := Base{
		Schema:    SchemaVersion,
		ID:        info.ID,
		Timestamp: info.Timestamp.UTC().Format(time.RFC3339),
		IsFromMe:  info.IsFromMe,
	}
	var chatAlt types.JID
	if info.Chat.Server != types.GroupServer {
		if info.IsFromMe {
			chatAlt = info.RecipientAlt
		} else {
			chatAlt = info.SenderAlt
		}
	}
	b.Chat.PN, b.Chat.LID = resolve(ctx, info.Chat, chatAlt, r)
	b.Chat.IsGroup = info.Chat.Server == types.GroupServer
	b.Sender.PN, b.Sender.LID = resolve(ctx, info.Sender, info.SenderAlt, r)
	b.Sender.PushName = strPtr(info.PushName)
	return b
}
