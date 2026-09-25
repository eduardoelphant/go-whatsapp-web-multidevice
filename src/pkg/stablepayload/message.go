package stablepayload

import (
	"context"
	"encoding/hex"
	"strings"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// Build returns the webhook event name and the stable object for a message
// event. msg is the message GOWA works on (the decrypted edit when WhatsApp
// sent a SecretEncryptedMessage); nil means evt.Message. The branch order
// matches GOWA's own event naming: protocol revoke/edit, reaction, message.
func Build(ctx context.Context, evt *events.Message, msg *waE2E.Message, r Resolver) (string, any) {
	if msg == nil {
		msg = evt.Message
	}
	b := base(ctx, evt.Info, r)

	// The event is chosen from msg as given, exactly as GOWA's buildEventPayload
	// does, so stable never disagrees with the webhook "event" name.
	if pm := msg.GetProtocolMessage(); pm != nil {
		switch pm.GetType() {
		case waE2E.ProtocolMessage_REVOKE:
			return EventRevoked, Revoked{Base: b, TargetID: pm.GetKey().GetID()}
		case waE2E.ProtocolMessage_MESSAGE_EDIT:
			edited, _ := unwrap(pm.GetEditedMessage())
			_, text, _, _, _ := classify(edited, evt.Info.ID)
			return EventEdited, Edited{Base: b, TargetID: pm.GetKey().GetID(), Text: text}
		}
	}
	if rm := msg.GetReactionMessage(); rm != nil {
		return EventReaction, Reaction{Base: b, TargetID: rm.GetKey().GetID(), Emoji: strPtr(rm.GetText())}
	}

	content, nestedViewOnce := unwrap(msg)
	typ, text, media, loc, contact := classify(content, evt.Info.ID)
	m := Message{Base: b, Type: typ, Text: text, Media: media, Location: loc, Contact: contact, ViewOnce: evt.IsViewOnce || nestedViewOnce}
	switch typ {
	case TypeInteractive:
		m.Interactive, _ = interactiveOf(content)
	case TypeInteractiveReply:
		m.Reply, _ = replyOf(content)
	case TypePoll:
		m.Poll = pollOf(pollCreation(content))
	case TypePollVote:
		m = WithPollVote(m, content.GetPollUpdateMessage().GetPollCreationMessageKey().GetID(), nil, pollVoteEncrypted)
	case TypeCall:
		m.Call = callOf(content.GetCallLogMesssage())
	case TypeProduct:
		m.Product = productOf(content.GetProductMessage())
	case TypeOrder:
		m.Order = orderOf(content.GetOrderMessage())
	}
	if ci := contextInfo(content); ci != nil {
		m.Forwarded = ci.GetIsForwarded()
		m.Quoted = quoted(ctx, ci, r)
		m.Referral = referralOf(ci)
	}
	return EventMessage, m
}

// unwrap removes the containers whatsmeow's UnwrapRaw may leave nested and
// reports whether one of them was a view-once wrapper.
func unwrap(msg *waE2E.Message) (*waE2E.Message, bool) {
	viewOnce := false
	for i := 0; i < 8 && msg != nil; i++ {
		switch {
		case msg.GetDeviceSentMessage().GetMessage() != nil:
			msg = msg.GetDeviceSentMessage().GetMessage()
		case msg.GetEphemeralMessage().GetMessage() != nil:
			msg = msg.GetEphemeralMessage().GetMessage()
		case msg.GetViewOnceMessage().GetMessage() != nil:
			msg, viewOnce = msg.GetViewOnceMessage().GetMessage(), true
		case msg.GetViewOnceMessageV2().GetMessage() != nil:
			msg, viewOnce = msg.GetViewOnceMessageV2().GetMessage(), true
		case msg.GetViewOnceMessageV2Extension().GetMessage() != nil:
			msg, viewOnce = msg.GetViewOnceMessageV2Extension().GetMessage(), true
		case msg.GetDocumentWithCaptionMessage().GetMessage() != nil:
			msg = msg.GetDocumentWithCaptionMessage().GetMessage()
		case msg.GetAssociatedChildMessage().GetMessage() != nil:
			msg = msg.GetAssociatedChildMessage().GetMessage()
		case msg.GetGroupMentionedMessage().GetMessage() != nil:
			msg = msg.GetGroupMentionedMessage().GetMessage()
		case msg.GetStatusMentionMessage().GetMessage() != nil:
			msg = msg.GetStatusMentionMessage().GetMessage()
		case msg.GetLottieStickerMessage().GetMessage() != nil:
			msg = msg.GetLottieStickerMessage().GetMessage()
		default:
			return msg, viewOnce
		}
	}
	return msg, viewOnce
}

func classify(msg *waE2E.Message, messageID string) (string, *string, *Media, *Location, *Contact) {
	url := "/message/" + messageID + "/media"
	switch {
	case msg == nil:
		return TypeUnknown, nil, nil, nil, nil
	case msg.GetConversation() != "":
		return TypeText, strPtr(msg.GetConversation()), nil, nil, nil
	case msg.GetExtendedTextMessage() != nil:
		return TypeText, strPtr(msg.GetExtendedTextMessage().GetText()), nil, nil, nil
	case msg.GetImageMessage() != nil:
		im := msg.GetImageMessage()
		return TypeImage, strPtr(im.GetCaption()), &Media{
			Kind: "image", Mime: strPtr(im.GetMimetype()), Size: sizePtr(im.GetFileLength()), SHA256: hexPtr(im.GetFileSHA256()),
			Width: intPtr(im.GetWidth()), Height: intPtr(im.GetHeight()), URL: url,
		}, nil, nil
	case msg.GetVideoMessage() != nil || msg.GetPtvMessage() != nil:
		vm := msg.GetVideoMessage()
		if vm == nil {
			vm = msg.GetPtvMessage()
		}
		return TypeVideo, strPtr(vm.GetCaption()), &Media{
			Kind: "video", Mime: strPtr(vm.GetMimetype()), Size: sizePtr(vm.GetFileLength()), SHA256: hexPtr(vm.GetFileSHA256()),
			Duration: intPtr(vm.GetSeconds()), Width: intPtr(vm.GetWidth()), Height: intPtr(vm.GetHeight()), URL: url,
		}, nil, nil
	case msg.GetAudioMessage() != nil:
		am := msg.GetAudioMessage()
		return TypeAudio, nil, &Media{
			Kind: "audio", Mime: strPtr(am.GetMimetype()), Size: sizePtr(am.GetFileLength()), SHA256: hexPtr(am.GetFileSHA256()),
			Duration: intPtr(am.GetSeconds()), PTT: am.GetPTT(), URL: url,
		}, nil, nil
	case msg.GetDocumentMessage() != nil:
		dm := msg.GetDocumentMessage()
		return TypeDocument, strPtr(dm.GetCaption()), &Media{
			Kind: "document", Mime: strPtr(dm.GetMimetype()), Size: sizePtr(dm.GetFileLength()), SHA256: hexPtr(dm.GetFileSHA256()),
			Filename: strPtr(dm.GetFileName()), URL: url,
		}, nil, nil
	case msg.GetStickerMessage() != nil:
		sm := msg.GetStickerMessage()
		return TypeSticker, nil, &Media{
			Kind: "sticker", Mime: strPtr(sm.GetMimetype()), Size: sizePtr(sm.GetFileLength()), SHA256: hexPtr(sm.GetFileSHA256()),
			Width: intPtr(sm.GetWidth()), Height: intPtr(sm.GetHeight()), URL: url,
		}, nil, nil
	case msg.GetLocationMessage() != nil:
		lm := msg.GetLocationMessage()
		return TypeLocation, nil, nil, &Location{
			Latitude: lm.GetDegreesLatitude(), Longitude: lm.GetDegreesLongitude(),
			Name: strPtr(lm.GetName()), Address: strPtr(lm.GetAddress()),
		}, nil
	case msg.GetLiveLocationMessage() != nil:
		ll := msg.GetLiveLocationMessage()
		return TypeLocation, nil, nil, &Location{Latitude: ll.GetDegreesLatitude(), Longitude: ll.GetDegreesLongitude()}, nil
	case msg.GetContactMessage() != nil:
		return TypeContact, nil, nil, nil, contactOf(msg.GetContactMessage())
	case msg.GetContactsArrayMessage() != nil && len(msg.GetContactsArrayMessage().GetContacts()) > 0:
		// Documented limit: a contacts array keeps only its first entry.
		return TypeContact, nil, nil, nil, contactOf(msg.GetContactsArrayMessage().GetContacts()[0])
	case msg.GetPollCreationMessage() != nil:
		return TypePoll, strPtr(msg.GetPollCreationMessage().GetName()), nil, nil, nil
	case msg.GetPollCreationMessageV2() != nil:
		return TypePoll, strPtr(msg.GetPollCreationMessageV2().GetName()), nil, nil, nil
	case msg.GetPollCreationMessageV3() != nil:
		return TypePoll, strPtr(msg.GetPollCreationMessageV3().GetName()), nil, nil, nil
	case msg.GetPollCreationMessageV4().GetMessage() != nil:
		// V4 is a FutureProofMessage wrapping one of the other poll versions.
		inner, _ := unwrap(msg.GetPollCreationMessageV4().GetMessage())
		if typ, text, _, _, _ := classify(inner, messageID); typ == TypePoll {
			return TypePoll, text, nil, nil, nil
		}
	case msg.GetPollCreationMessageV5() != nil:
		return TypePoll, strPtr(msg.GetPollCreationMessageV5().GetName()), nil, nil, nil
	case msg.GetPollCreationMessageV6() != nil:
		return TypePoll, strPtr(msg.GetPollCreationMessageV6().GetName()), nil, nil, nil
	}
	if in, text := interactiveOf(msg); in != nil {
		return TypeInteractive, text, nil, nil, nil
	}
	switch {
	case msg.GetPollUpdateMessage() != nil:
		return TypePollVote, nil, nil, nil, nil
	case msg.GetCallLogMesssage() != nil:
		return TypeCall, nil, nil, nil, nil
	case msg.GetProductMessage() != nil:
		return TypeProduct, strPtr(msg.GetProductMessage().GetBody()), nil, nil, nil
	case msg.GetOrderMessage() != nil:
		return TypeOrder, strPtr(msg.GetOrderMessage().GetMessage()), nil, nil, nil
	}
	if reply, text := replyOf(msg); reply != nil {
		return TypeInteractiveReply, text, nil, nil, nil
	}
	return TypeUnknown, nil, nil, nil, nil
}

func contactOf(c *waE2E.ContactMessage) *Contact {
	return &Contact{Name: strPtr(c.GetDisplayName()), VCard: strPtr(c.GetVcard()), Phones: vcardPhones(c.GetVcard())}
}

// vcardPhones returns the values of TEL lines ("TEL;…:+55 …" and "item1.TEL;…:…").
func vcardPhones(vcard string) []string {
	phones := []string{}
	for _, line := range strings.Split(strings.ReplaceAll(vcard, "\r\n", "\n"), "\n") {
		upper := strings.ToUpper(line)
		if !strings.HasPrefix(upper, "TEL") && !strings.Contains(upper, ".TEL") {
			continue
		}
		if i := strings.LastIndex(line, ":"); i >= 0 {
			if phone := strings.TrimSpace(line[i+1:]); phone != "" {
				phones = append(phones, phone)
			}
		}
	}
	return phones
}

func contextInfo(msg *waE2E.Message) *waE2E.ContextInfo {
	for _, ci := range []*waE2E.ContextInfo{
		msg.GetExtendedTextMessage().GetContextInfo(),
		msg.GetImageMessage().GetContextInfo(),
		msg.GetVideoMessage().GetContextInfo(),
		msg.GetPtvMessage().GetContextInfo(),
		msg.GetAudioMessage().GetContextInfo(),
		msg.GetDocumentMessage().GetContextInfo(),
		msg.GetStickerMessage().GetContextInfo(),
		msg.GetLocationMessage().GetContextInfo(),
		msg.GetContactMessage().GetContextInfo(),
		msg.GetContactsArrayMessage().GetContextInfo(),
		msg.GetLiveLocationMessage().GetContextInfo(),
		msg.GetPollCreationMessage().GetContextInfo(),
		msg.GetPollCreationMessageV2().GetContextInfo(),
		msg.GetPollCreationMessageV3().GetContextInfo(),
		msg.GetPollCreationMessageV5().GetContextInfo(),
		msg.GetPollCreationMessageV6().GetContextInfo(),
		msg.GetButtonsMessage().GetContextInfo(),
		msg.GetTemplateMessage().GetContextInfo(),
		msg.GetListMessage().GetContextInfo(),
		msg.GetInteractiveMessage().GetContextInfo(),
		msg.GetButtonsResponseMessage().GetContextInfo(),
		msg.GetTemplateButtonReplyMessage().GetContextInfo(),
		msg.GetListResponseMessage().GetContextInfo(),
		msg.GetInteractiveResponseMessage().GetContextInfo(),
		msg.GetProductMessage().GetContextInfo(),
		msg.GetOrderMessage().GetContextInfo(),
	} {
		if ci != nil {
			return ci
		}
	}
	return nil
}

func quoted(ctx context.Context, ci *waE2E.ContextInfo, r Resolver) *Quoted {
	if ci.GetStanzaID() == "" {
		return nil
	}
	quotedMsg, _ := unwrap(ci.GetQuotedMessage())
	typ, text, _, _, _ := classify(quotedMsg, ci.GetStanzaID())
	q := &Quoted{ID: ci.GetStanzaID(), Type: typ, Text: text}
	if participant, err := types.ParseJID(ci.GetParticipant()); err == nil && ci.GetParticipant() != "" {
		q.Sender.PN, q.Sender.LID = resolve(ctx, participant, types.JID{}, r)
	}
	return q
}

func sizePtr(v uint64) *int64 {
	if v == 0 {
		return nil
	}
	n := int64(v)
	return &n
}

func intPtr(v uint32) *int {
	if v == 0 {
		return nil
	}
	n := int(v)
	return &n
}

func hexPtr(b []byte) *string {
	if len(b) == 0 {
		return nil
	}
	return strPtr(hex.EncodeToString(b))
}
