package stablepayload

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

var update = flag.Bool("update", false, "rewrite contract/fixtures/synthetic")

var fixtureDir = filepath.Join("..", "..", "..", "contract", "fixtures", "synthetic")

func goldenCases() map[string]func() (string, any) {
	ctx := context.Background()
	msg := func(raw *waE2E.Message, mods ...func(*events.Message)) func() (string, any) {
		return func() (string, any) { return Build(ctx, newMessageEvent(raw, mods...), nil, resolver) }
	}
	group := func(e *events.Message) {
		e.Info.Chat = groupJID
		e.Info.IsGroup = true
		e.Info.Sender = lidContact
	}
	fromMe := func(e *events.Message) {
		e.Info.IsFromMe = true
		e.Info.Sender = pnMe
		e.Info.PushName = ""
	}
	ack := func(rt types.ReceiptType) func() (string, any) {
		return func() (string, any) {
			return EventAck, BuildAck(ctx, &events.Receipt{
				MessageSource: types.MessageSource{Chat: pnContact, Sender: pnContact},
				MessageIDs:    []types.MessageID{"3EB0000000000000000001"}, Timestamp: testTime, Type: rt,
			}, resolver)
		}
	}
	key := &waCommon.MessageKey{ID: proto.String("3EB0000000000000000000")}
	return map[string]func() (string, any){
		"message-text":           msg(&waE2E.Message{Conversation: proto.String("hello")}),
		"message-text-extended":  msg(&waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("hi there")}}),
		"message-image":          msg(&waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("look"), Mimetype: proto.String("image/jpeg"), FileLength: proto.Uint64(2048), FileSHA256: []byte{0xab, 0xcd}, Width: proto.Uint32(640), Height: proto.Uint32(480)}}),
		"message-video":          msg(&waE2E.Message{VideoMessage: &waE2E.VideoMessage{Caption: proto.String("clip"), Mimetype: proto.String("video/mp4"), FileLength: proto.Uint64(4096), FileSHA256: []byte{0x01, 0x02}, Seconds: proto.Uint32(9), Width: proto.Uint32(480), Height: proto.Uint32(848)}}),
		"message-video-note":     msg(&waE2E.Message{PtvMessage: &waE2E.VideoMessage{Mimetype: proto.String("video/mp4"), Seconds: proto.Uint32(4)}}),
		"message-audio":          msg(&waE2E.Message{AudioMessage: &waE2E.AudioMessage{Mimetype: proto.String("audio/mpeg"), Seconds: proto.Uint32(30)}}),
		"message-audio-ptt":      msg(&waE2E.Message{AudioMessage: &waE2E.AudioMessage{Mimetype: proto.String("audio/ogg; codecs=opus"), FileLength: proto.Uint64(12000), FileSHA256: []byte{0x03, 0x04}, Seconds: proto.Uint32(7), PTT: proto.Bool(true)}}),
		"message-document":       msg(&waE2E.Message{DocumentWithCaptionMessage: &waE2E.FutureProofMessage{Message: &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{FileName: proto.String("contract.pdf"), Caption: proto.String("please sign"), Mimetype: proto.String("application/pdf"), FileLength: proto.Uint64(9000), FileSHA256: []byte{0x05, 0x06}}}}}),
		"message-sticker":        msg(&waE2E.Message{StickerMessage: &waE2E.StickerMessage{Mimetype: proto.String("image/webp"), FileLength: proto.Uint64(30000), FileSHA256: []byte{0x07, 0x08}, Width: proto.Uint32(512), Height: proto.Uint32(512)}}),
		"message-location":       msg(&waE2E.Message{LocationMessage: &waE2E.LocationMessage{DegreesLatitude: proto.Float64(-23.5), DegreesLongitude: proto.Float64(-46.6), Name: proto.String("Office"), Address: proto.String("Main St 1")}}),
		"message-live-location":  msg(&waE2E.Message{LiveLocationMessage: &waE2E.LiveLocationMessage{DegreesLatitude: proto.Float64(-20.3), DegreesLongitude: proto.Float64(-40.3)}}),
		"message-contact":        msg(&waE2E.Message{ContactMessage: &waE2E.ContactMessage{DisplayName: proto.String("Ana"), Vcard: proto.String("BEGIN:VCARD\nitem1.TEL;waid=5511900000002:+55 11 90000-0002\nEND:VCARD")}}),
		"message-contacts-array": msg(&waE2E.Message{ContactsArrayMessage: &waE2E.ContactsArrayMessage{Contacts: []*waE2E.ContactMessage{{DisplayName: proto.String("First")}, {DisplayName: proto.String("Second")}}}}),
		"message-poll-v4":        msg(&waE2E.Message{PollCreationMessageV4: &waE2E.FutureProofMessage{Message: &waE2E.Message{PollCreationMessage: &waE2E.PollCreationMessage{Name: proto.String("Dinner?")}}}}),
		"message-poll-v5":        msg(&waE2E.Message{PollCreationMessageV5: &waE2E.PollCreationMessage{Name: proto.String("Coffee?")}}),
		"message-poll":           msg(&waE2E.Message{PollCreationMessageV3: &waE2E.PollCreationMessage{Name: proto.String("Lunch?")}}),
		"message-unknown":        msg(&waE2E.Message{ButtonsMessage: &waE2E.ButtonsMessage{ContentText: proto.String("pick")}}),
		"message-ephemeral":      msg(&waE2E.Message{EphemeralMessage: &waE2E.FutureProofMessage{Message: &waE2E.Message{Conversation: proto.String("disappearing")}}}),
		"message-view-once":      msg(&waE2E.Message{ViewOnceMessageV2: &waE2E.FutureProofMessage{Message: &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Mimetype: proto.String("image/jpeg")}}}}),
		"message-device-sent":    msg(&waE2E.Message{DeviceSentMessage: &waE2E.DeviceSentMessage{DestinationJID: proto.String(pnContact.String()), Message: &waE2E.Message{Conversation: proto.String("sent from phone")}}}, fromMe),
		"message-group":          msg(&waE2E.Message{Conversation: proto.String("hi group")}, group),
		"message-lid-only": func() (string, any) {
			return Build(ctx, newMessageEvent(&waE2E.Message{Conversation: proto.String("who?")}, func(e *events.Message) { e.Info.Chat = lidContact; e.Info.Sender = lidContact }), nil, fakeResolver{})
		},
		"message-reply":            msg(&waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("answer"), ContextInfo: &waE2E.ContextInfo{StanzaID: proto.String("3EB0000000000000000000"), Participant: proto.String(lidContact.String()), QuotedMessage: &waE2E.Message{Conversation: proto.String("question")}}}}),
		"message-forwarded":        msg(&waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("fwd"), ContextInfo: &waE2E.ContextInfo{IsForwarded: proto.Bool(true)}}}),
		"message-edited":           msg(&waE2E.Message{EditedMessage: &waE2E.FutureProofMessage{Message: &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(), Key: key, EditedMessage: &waE2E.Message{Conversation: proto.String("fixed")}}}}}),
		"message-reaction":         msg(&waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{Key: key, Text: proto.String("👍")}}),
		"message-reaction-removed": msg(&waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{Key: key, Text: proto.String("")}}),
		"message-revoked":          msg(&waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_REVOKE.Enum(), Key: key}}),
		"message-ack-delivered":    ack(types.ReceiptTypeDelivered),
		"message-ack-read":         ack(types.ReceiptTypeRead),
	}
}

func TestGoldenFixtures(t *testing.T) {
	if *update {
		if err := os.MkdirAll(fixtureDir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, build := range goldenCases() {
		t.Run(name, func(t *testing.T) {
			event, stable := build()
			got, err := json.MarshalIndent(map[string]any{"event": event, "stable": stable}, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			path := filepath.Join(fixtureDir, name+".json")
			if *update {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("missing fixture %s (run with -update): %v", path, err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("fixture %s drifted; rerun with -update if intended\n--- got\n%s", path, got)
			}
		})
	}
}

func TestNoOrphanFixtures(t *testing.T) {
	cases := goldenCases()
	files, err := filepath.Glob(filepath.Join(fixtureDir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".json")
		if _, ok := cases[name]; !ok {
			t.Errorf("fixture %s has no golden case; delete it or add the case", f)
		}
	}
}
