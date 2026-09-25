package stablepayload

import (
	"context"
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func newMessageEvent(raw *waE2E.Message, mods ...func(*events.Message)) *events.Message {
	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: pnContact, Sender: pnContact},
			ID:            "3EB0000000000000000001",
			PushName:      "Contact One",
			Timestamp:     testTime,
		},
		RawMessage: raw,
	}
	for _, mod := range mods {
		mod(evt)
	}
	return evt.UnwrapRaw()
}

func buildMessage(t *testing.T, evt *events.Message) Message {
	t.Helper()
	event, stable := Build(context.Background(), evt, nil, resolver)
	if event != EventMessage {
		t.Fatalf("event = %s, want %s", event, EventMessage)
	}
	m, ok := stable.(Message)
	if !ok {
		t.Fatalf("stable is %T, want Message", stable)
	}
	return m
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func TestBuildMessageTypes(t *testing.T) {
	image := &waE2E.ImageMessage{
		Caption: proto.String("look"), Mimetype: proto.String("image/jpeg"),
		FileLength: proto.Uint64(2048), FileSHA256: []byte{0xab, 0xcd},
		Width: proto.Uint32(640), Height: proto.Uint32(480),
	}
	cases := []struct {
		name     string
		raw      *waE2E.Message
		wantType string
		wantText string
		wantKind string
	}{
		{"conversation", &waE2E.Message{Conversation: proto.String("hello")}, TypeText, "hello", ""},
		{"extended text", &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("hi there")}}, TypeText, "hi there", ""},
		{"image", &waE2E.Message{ImageMessage: image}, TypeImage, "look", "image"},
		{"video", &waE2E.Message{VideoMessage: &waE2E.VideoMessage{Caption: proto.String("clip"), Seconds: proto.Uint32(9)}}, TypeVideo, "clip", "video"},
		{"video note", &waE2E.Message{PtvMessage: &waE2E.VideoMessage{Seconds: proto.Uint32(4)}}, TypeVideo, "<nil>", "video"},
		{"audio", &waE2E.Message{AudioMessage: &waE2E.AudioMessage{Seconds: proto.Uint32(7)}}, TypeAudio, "<nil>", "audio"},
		{"document", &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{FileName: proto.String("a.pdf"), Caption: proto.String("contract")}}, TypeDocument, "contract", "document"},
		{"sticker", &waE2E.Message{StickerMessage: &waE2E.StickerMessage{Mimetype: proto.String("image/webp")}}, TypeSticker, "<nil>", "sticker"},
		{"location", &waE2E.Message{LocationMessage: &waE2E.LocationMessage{DegreesLatitude: proto.Float64(-23.5), DegreesLongitude: proto.Float64(-46.6), Name: proto.String("Office")}}, TypeLocation, "<nil>", ""},
		{"live location", &waE2E.Message{LiveLocationMessage: &waE2E.LiveLocationMessage{DegreesLatitude: proto.Float64(-20.3), DegreesLongitude: proto.Float64(-40.3)}}, TypeLocation, "<nil>", ""},
		{"contact", &waE2E.Message{ContactMessage: &waE2E.ContactMessage{DisplayName: proto.String("Ana"), Vcard: proto.String("BEGIN:VCARD\nitem1.TEL;waid=5511900000002:+55 11 90000-0002\nEND:VCARD")}}, TypeContact, "<nil>", ""},
		{"poll", &waE2E.Message{PollCreationMessageV3: &waE2E.PollCreationMessage{Name: proto.String("Lunch?")}}, TypePoll, "Lunch?", ""},
		{"poll v4", &waE2E.Message{PollCreationMessageV4: &waE2E.FutureProofMessage{Message: &waE2E.Message{PollCreationMessage: &waE2E.PollCreationMessage{Name: proto.String("Dinner?")}}}}, TypePoll, "Dinner?", ""},
		{"poll v5", &waE2E.Message{PollCreationMessageV5: &waE2E.PollCreationMessage{Name: proto.String("Coffee?")}}, TypePoll, "Coffee?", ""},
		{"poll v6", &waE2E.Message{PollCreationMessageV6: &waE2E.PollCreationMessage{Name: proto.String("Tea?")}}, TypePoll, "Tea?", ""},
		{"unknown", &waE2E.Message{GroupInviteMessage: &waE2E.GroupInviteMessage{GroupName: proto.String("g")}}, TypeUnknown, "<nil>", ""},
		{"empty", &waE2E.Message{}, TypeUnknown, "<nil>", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := buildMessage(t, newMessageEvent(tc.raw))
			if m.Type != tc.wantType || deref(m.Text) != tc.wantText {
				t.Fatalf("type=%s text=%s, want type=%s text=%s", m.Type, deref(m.Text), tc.wantType, tc.wantText)
			}
			switch {
			case tc.wantKind == "" && m.Media != nil:
				t.Fatalf("media = %+v, want nil", m.Media)
			case tc.wantKind != "" && (m.Media == nil || m.Media.Kind != tc.wantKind):
				t.Fatalf("media = %+v, want kind %s", m.Media, tc.wantKind)
			case tc.wantKind != "" && m.Media.URL != "/message/3EB0000000000000000001/media":
				t.Fatalf("media.url = %s", m.Media.URL)
			}
		})
	}
}

func TestBuildMessageMediaFields(t *testing.T) {
	m := buildMessage(t, newMessageEvent(&waE2E.Message{ImageMessage: &waE2E.ImageMessage{
		Mimetype: proto.String("image/jpeg"), FileLength: proto.Uint64(2048), FileSHA256: []byte{0xab, 0xcd},
		Width: proto.Uint32(640), Height: proto.Uint32(480),
	}}))
	got := toJSON(t, m.Media)
	want := `{"kind":"image","mime":"image/jpeg","size":2048,"sha256":"abcd","filename":null,"duration":null,"ptt":false,"width":640,"height":480,"url":"/message/3EB0000000000000000001/media"}`
	if got != want {
		t.Fatalf("media =\n%s\nwant\n%s", got, want)
	}

	audio := buildMessage(t, newMessageEvent(&waE2E.Message{AudioMessage: &waE2E.AudioMessage{PTT: proto.Bool(true), Seconds: proto.Uint32(7), Mimetype: proto.String("audio/ogg; codecs=opus")}}))
	if !audio.Media.PTT || audio.Media.Duration == nil || *audio.Media.Duration != 7 {
		t.Fatalf("audio media = %+v, want ptt true and duration 7", audio.Media)
	}
}

func TestBuildMessageLocationAndContact(t *testing.T) {
	loc := buildMessage(t, newMessageEvent(&waE2E.Message{LocationMessage: &waE2E.LocationMessage{
		DegreesLatitude: proto.Float64(-23.5), DegreesLongitude: proto.Float64(-46.6), Name: proto.String("Office"),
	}}))
	if got := toJSON(t, loc.Location); got != `{"latitude":-23.5,"longitude":-46.6,"name":"Office","address":null}` {
		t.Fatalf("location = %s", got)
	}

	noPhones := buildMessage(t, newMessageEvent(&waE2E.Message{ContactMessage: &waE2E.ContactMessage{DisplayName: proto.String("Ana")}}))
	if got := toJSON(t, noPhones.Contact); got != `{"name":"Ana","vcard":null,"phones":[]}` {
		t.Fatalf("contact without vcard = %s, want phones []", got)
	}

	array := buildMessage(t, newMessageEvent(&waE2E.Message{ContactsArrayMessage: &waE2E.ContactsArrayMessage{Contacts: []*waE2E.ContactMessage{
		{DisplayName: proto.String("First"), Vcard: proto.String("BEGIN:VCARD\nTEL;type=CELL:+55 11 90000-0003\nEND:VCARD")},
		{DisplayName: proto.String("Second")},
	}}}))
	if array.Type != TypeContact || deref(array.Contact.Name) != "First" || len(array.Contact.Phones) != 1 || array.Contact.Phones[0] != "+55 11 90000-0003" {
		t.Fatalf("contacts array = %+v", array.Contact)
	}
}

func TestBuildMessageWrappers(t *testing.T) {
	inner := &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("secret")}}
	raw := &waE2E.Message{DeviceSentMessage: &waE2E.DeviceSentMessage{
		DestinationJID: proto.String(pnContact.String()),
		Message: &waE2E.Message{EphemeralMessage: &waE2E.FutureProofMessage{Message: &waE2E.Message{
			ViewOnceMessageV2: &waE2E.FutureProofMessage{Message: inner},
		}}},
	}}
	m := buildMessage(t, newMessageEvent(raw, func(e *events.Message) {
		e.Info.IsFromMe = true
		e.Info.Sender = pnMe
	}))
	if m.Type != TypeImage || deref(m.Text) != "secret" || !m.ViewOnce || !m.IsFromMe {
		t.Fatalf("device-sent ephemeral view-once image: type=%s text=%s view_once=%v from_me=%v", m.Type, deref(m.Text), m.ViewOnce, m.IsFromMe)
	}
}

func TestBuildMessageQuotedAndForwarded(t *testing.T) {
	raw := &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
		Text: proto.String("answer"),
		ContextInfo: &waE2E.ContextInfo{
			StanzaID:      proto.String("ORIGINAL1"),
			Participant:   proto.String(lidContact.String()),
			QuotedMessage: &waE2E.Message{Conversation: proto.String("question")},
			IsForwarded:   proto.Bool(true),
		},
	}}
	m := buildMessage(t, newMessageEvent(raw))
	got := toJSON(t, m.Quoted)
	want := `{"id":"ORIGINAL1","type":"text","text":"question","sender":{"pn":"5511900000001@s.whatsapp.net","lid":"100000000000001@lid"}}`
	if got != want || !m.Forwarded {
		t.Fatalf("quoted = %s forwarded=%v, want %s forwarded=true", got, m.Forwarded, want)
	}

	plain := buildMessage(t, newMessageEvent(&waE2E.Message{Conversation: proto.String("x")}))
	if plain.Quoted != nil || plain.Forwarded {
		t.Fatalf("plain message quoted=%v forwarded=%v, want nil/false", plain.Quoted, plain.Forwarded)
	}
}

func TestBuildMessageMoreWrappers(t *testing.T) {
	cases := map[string]struct {
		raw      *waE2E.Message
		wantType string
	}{
		"album child":     {&waE2E.Message{AssociatedChildMessage: &waE2E.FutureProofMessage{Message: &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("1/3")}}}}, TypeImage},
		"group mentioned": {&waE2E.Message{GroupMentionedMessage: &waE2E.FutureProofMessage{Message: &waE2E.Message{Conversation: proto.String("@all")}}}, TypeText},
		"status mention":  {&waE2E.Message{StatusMentionMessage: &waE2E.FutureProofMessage{Message: &waE2E.Message{Conversation: proto.String("seen you")}}}, TypeText},
		"nested lottie":   {&waE2E.Message{EphemeralMessage: &waE2E.FutureProofMessage{Message: &waE2E.Message{LottieStickerMessage: &waE2E.FutureProofMessage{Message: &waE2E.Message{StickerMessage: &waE2E.StickerMessage{}}}}}}, TypeSticker},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if m := buildMessage(t, newMessageEvent(tc.raw)); m.Type != tc.wantType {
				t.Fatalf("type = %s, want %s", m.Type, tc.wantType)
			}
		})
	}
}

func TestBuildEventFollowsTheMessageGOWAInspects(t *testing.T) {
	// GOWA names the event from msg as given; a reaction hidden under a nested
	// device-sent envelope is a "message" there, so stable must say the same.
	msg := &waE2E.Message{DeviceSentMessage: &waE2E.DeviceSentMessage{Message: &waE2E.Message{
		ReactionMessage: &waE2E.ReactionMessage{Text: proto.String("👍")},
	}}}
	event, stable := Build(context.Background(), newMessageEvent(&waE2E.Message{}), msg, resolver)
	if m, ok := stable.(Message); event != EventMessage || !ok || m.Type != TypeUnknown {
		t.Fatalf("event=%s stable=%T %+v, want message/unknown", event, stable, stable)
	}
}

func TestBuildViewOnceFromNestedWrapper(t *testing.T) {
	// UnwrapRaw peels DocumentWithCaption last, so a view-once wrapper inside it
	// survives until stable's own unwrap.
	raw := &waE2E.Message{DocumentWithCaptionMessage: &waE2E.FutureProofMessage{Message: &waE2E.Message{
		ViewOnceMessage: &waE2E.FutureProofMessage{Message: &waE2E.Message{ImageMessage: &waE2E.ImageMessage{}}},
	}}}
	evt := newMessageEvent(raw)
	if evt.IsViewOnce {
		t.Skip("whatsmeow now flags this case itself")
	}
	if m := buildMessage(t, evt); m.Type != TypeImage || !m.ViewOnce {
		t.Fatalf("type=%s view_once=%v, want image/true", m.Type, m.ViewOnce)
	}
}

func TestContextInfoOfMoreTypes(t *testing.T) {
	ci := &waE2E.ContextInfo{IsForwarded: proto.Bool(true), StanzaID: proto.String("Q1"), QuotedMessage: &waE2E.Message{Conversation: proto.String("q")}}
	for name, raw := range map[string]*waE2E.Message{
		"poll":           {PollCreationMessageV3: &waE2E.PollCreationMessage{Name: proto.String("P"), ContextInfo: ci}},
		"live location":  {LiveLocationMessage: &waE2E.LiveLocationMessage{ContextInfo: ci}},
		"contacts array": {ContactsArrayMessage: &waE2E.ContactsArrayMessage{Contacts: []*waE2E.ContactMessage{{DisplayName: proto.String("A")}}, ContextInfo: ci}},
	} {
		t.Run(name, func(t *testing.T) {
			m := buildMessage(t, newMessageEvent(raw))
			if !m.Forwarded || m.Quoted == nil || m.Quoted.ID != "Q1" {
				t.Fatalf("forwarded=%v quoted=%+v", m.Forwarded, m.Quoted)
			}
		})
	}
}
