package whatsapp

import (
	"context"
	"testing"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/stablepayload"
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
