package stablepayload

import (
	"context"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestBuildEdited(t *testing.T) {
	raw := &waE2E.Message{EditedMessage: &waE2E.FutureProofMessage{Message: &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type:          waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
		Key:           &waCommon.MessageKey{ID: proto.String("ORIGINAL1")},
		EditedMessage: &waE2E.Message{Conversation: proto.String("fixed text")},
	}}}}
	event, stable := Build(context.Background(), newMessageEvent(raw), nil, resolver)
	e, ok := stable.(Edited)
	if event != EventEdited || !ok || e.TargetID != "ORIGINAL1" || deref(e.Text) != "fixed text" {
		t.Fatalf("event=%s stable=%+v", event, stable)
	}
}

func TestBuildEditedFromDecryptedMessage(t *testing.T) {
	decrypted := &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type:          waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
		Key:           &waCommon.MessageKey{ID: proto.String("ORIGINAL2")},
		EditedMessage: &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("new")}},
	}}
	event, stable := Build(context.Background(), newMessageEvent(&waE2E.Message{}), decrypted, resolver)
	if e, ok := stable.(Edited); event != EventEdited || !ok || e.TargetID != "ORIGINAL2" || deref(e.Text) != "new" {
		t.Fatalf("event=%s stable=%+v", event, stable)
	}
}

func TestBuildReaction(t *testing.T) {
	react := func(text string) *waE2E.Message {
		return &waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{Key: &waCommon.MessageKey{ID: proto.String("TARGET1")}, Text: proto.String(text)}}
	}
	event, stable := Build(context.Background(), newMessageEvent(react("👍")), nil, resolver)
	if r, ok := stable.(Reaction); event != EventReaction || !ok || r.TargetID != "TARGET1" || deref(r.Emoji) != "👍" {
		t.Fatalf("event=%s stable=%+v", event, stable)
	}
	_, removed := Build(context.Background(), newMessageEvent(react("")), nil, resolver)
	if got := toJSON(t, removed); !strings.Contains(got, `"emoji":null`) {
		t.Fatalf("removed reaction = %s, want emoji null", got)
	}
}

func TestBuildRevoked(t *testing.T) {
	raw := &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_REVOKE.Enum(),
		Key:  &waCommon.MessageKey{ID: proto.String("GONE1")},
	}}
	event, stable := Build(context.Background(), newMessageEvent(raw), nil, resolver)
	if r, ok := stable.(Revoked); event != EventRevoked || !ok || r.TargetID != "GONE1" {
		t.Fatalf("event=%s stable=%+v", event, stable)
	}
}

func TestBuildOtherProtocolMessageIsUnknownMessage(t *testing.T) {
	raw := &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_EPHEMERAL_SETTING.Enum()}}
	event, stable := Build(context.Background(), newMessageEvent(raw), nil, resolver)
	if m, ok := stable.(Message); event != EventMessage || !ok || m.Type != TypeUnknown {
		t.Fatalf("event=%s stable=%+v", event, stable)
	}
}

func TestBuildAck(t *testing.T) {
	evt := &events.Receipt{
		MessageSource: types.MessageSource{Chat: pnContact, Sender: pnContact},
		MessageIDs:    []types.MessageID{"A1", "A2"},
		Timestamp:     testTime,
		Type:          types.ReceiptTypeRead,
	}
	got := toJSON(t, BuildAck(context.Background(), evt, resolver))
	want := `{"schema":1,"id":"A1","timestamp":"2026-09-25T12:00:00Z","is_from_me":false,` +
		`"chat":{"pn":"5511900000001@s.whatsapp.net","lid":"100000000000001@lid","is_group":false},` +
		`"sender":{"pn":"5511900000001@s.whatsapp.net","lid":"100000000000001@lid","push_name":null},` +
		`"status":"read","ids":["A1","A2"]}`
	if got != want {
		t.Fatalf("ack =\n%s\nwant\n%s", got, want)
	}

	for rt, want := range map[types.ReceiptType]string{
		types.ReceiptTypeDelivered: "delivered", types.ReceiptTypeReadSelf: "read",
		types.ReceiptTypePlayed: "played", types.ReceiptTypePlayedSelf: "played",
	} {
		if got := BuildAck(context.Background(), &events.Receipt{Type: rt, MessageIDs: []types.MessageID{"X"}}, nil).Status; got != want {
			t.Errorf("status for %q = %s, want %s", rt, got, want)
		}
	}
	if got := toJSON(t, BuildAck(context.Background(), &events.Receipt{}, nil)); !strings.Contains(got, `"ids":[]`) {
		t.Fatalf("empty ack = %s, want ids []", got)
	}
}
