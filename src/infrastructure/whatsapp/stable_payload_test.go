package whatsapp

import (
	"context"
	"testing"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/stablepayload"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestBuildEventPayloadAddsStable(t *testing.T) {
	jid := types.NewJID("5511900000001", types.DefaultUserServer)
	evt := (&events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: jid, Sender: jid},
			ID:            "STABLE1", Timestamp: time.Now(),
		},
		RawMessage: &waE2E.Message{Conversation: proto.String("hello")},
	}).UnwrapRaw()

	event, payload, err := buildEventPayload(context.Background(), nil, evt)
	if err != nil {
		t.Fatal(err)
	}
	stable, ok := payload["stable"].(stablepayload.Message)
	if event != "message" || !ok || stable.Type != stablepayload.TypeText || stable.ID != "STABLE1" {
		t.Fatalf("event=%s stable=%#v", event, payload["stable"])
	}
	if payload["body"] != "hello" {
		t.Fatalf("legacy body = %v, want hello (existing fields must stay)", payload["body"])
	}
}

func TestAddStableAck(t *testing.T) {
	jid := types.NewJID("5511900000001", types.DefaultUserServer)
	evt := &events.Receipt{
		MessageSource: types.MessageSource{Chat: jid, Sender: jid},
		MessageIDs:    []types.MessageID{"ACK1"}, Timestamp: time.Now(), Type: types.ReceiptTypeRead,
	}
	body := createReceiptPayload(context.Background(), evt, "", nil)
	addStableAck(context.Background(), nil, evt, body)
	inner := body["payload"].(map[string]any)
	ack, ok := inner["stable"].(stablepayload.Ack)
	if !ok || ack.Status != "read" || len(ack.IDs) != 1 || ack.IDs[0] != "ACK1" {
		t.Fatalf("stable = %#v", inner["stable"])
	}
}

func TestBuildEventPayloadResolvesPollVote(t *testing.T) {
	jid := types.NewJID("5511900000001", types.DefaultUserServer)
	evt := (&events.Message{
		Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: jid, Sender: jid}, ID: "VOTE1", Timestamp: time.Now()},
		RawMessage: &waE2E.Message{PollUpdateMessage: &waE2E.PollUpdateMessage{
			PollCreationMessageKey: &waCommon.MessageKey{ID: proto.String("POLL1")},
		}},
	}).UnwrapRaw()
	selected := []string{"Sim"}
	poll := &webhookPollPayload{Type: "vote", PollID: "POLL1", SelectedOptions: &selected, ResolutionStatus: pollResolutionResolved}

	_, payload, err := buildEventPayload(context.Background(), nil, evt, poll)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := payload["stable"].(stablepayload.Message)
	if !ok || m.Type != stablepayload.TypePollVote || m.PollVote == nil ||
		m.PollVote.Resolution != "resolved" || len(m.PollVote.Selected) != 1 || m.PollVote.Selected[0] != "Sim" {
		t.Fatalf("stable = %#v", payload["stable"])
	}
}
