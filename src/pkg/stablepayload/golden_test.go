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
		"message-unknown":        msg(&waE2E.Message{GroupInviteMessage: &waE2E.GroupInviteMessage{GroupName: proto.String("g")}}),
		"message-ephemeral":      msg(&waE2E.Message{EphemeralMessage: &waE2E.FutureProofMessage{Message: &waE2E.Message{Conversation: proto.String("disappearing")}}}),
		"message-view-once":      msg(&waE2E.Message{ViewOnceMessageV2: &waE2E.FutureProofMessage{Message: &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Mimetype: proto.String("image/jpeg")}}}}),
		"message-device-sent":    msg(&waE2E.Message{DeviceSentMessage: &waE2E.DeviceSentMessage{DestinationJID: proto.String(pnContact.String()), Message: &waE2E.Message{Conversation: proto.String("sent from phone")}}}, fromMe),
		"message-group":          msg(&waE2E.Message{Conversation: proto.String("hi group")}, group),
		"message-lid-only": func() (string, any) {
			return Build(ctx, newMessageEvent(&waE2E.Message{Conversation: proto.String("who?")}, func(e *events.Message) { e.Info.Chat = lidContact; e.Info.Sender = lidContact }), nil, fakeResolver{})
		},
		"message-reply":             msg(&waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("answer"), ContextInfo: &waE2E.ContextInfo{StanzaID: proto.String("3EB0000000000000000000"), Participant: proto.String(lidContact.String()), QuotedMessage: &waE2E.Message{Conversation: proto.String("question")}}}}),
		"message-forwarded":         msg(&waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("fwd"), ContextInfo: &waE2E.ContextInfo{IsForwarded: proto.Bool(true)}}}),
		"message-edited":            msg(&waE2E.Message{EditedMessage: &waE2E.FutureProofMessage{Message: &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(), Key: key, EditedMessage: &waE2E.Message{Conversation: proto.String("fixed")}}}}}),
		"message-reaction":          msg(&waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{Key: key, Text: proto.String("👍")}}),
		"message-reaction-removed":  msg(&waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{Key: key, Text: proto.String("")}}),
		"message-revoked":           msg(&waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_REVOKE.Enum(), Key: key}}),
		"message-buttons":           msg(&waE2E.Message{ButtonsMessage: &waE2E.ButtonsMessage{Header: &waE2E.ButtonsMessage_Text{Text: "Loja X"}, ContentText: proto.String("Escolha"), FooterText: proto.String("Rodapé"), Buttons: []*waE2E.ButtonsMessage_Button{{ButtonID: proto.String("b1"), ButtonText: &waE2E.ButtonsMessage_Button_ButtonText{DisplayText: proto.String("Atendente")}}}}}),
		"message-template":          msg(&waE2E.Message{TemplateMessage: &waE2E.TemplateMessage{Format: &waE2E.TemplateMessage_HydratedFourRowTemplate_{HydratedFourRowTemplate: &waE2E.TemplateMessage_HydratedFourRowTemplate{Title: &waE2E.TemplateMessage_HydratedFourRowTemplate_HydratedTitleText{HydratedTitleText: "Pedido"}, HydratedContentText: proto.String("Seu pedido saiu"), HydratedFooterText: proto.String("Obrigado"), HydratedButtons: []*waE2E.HydratedTemplateButton{{HydratedButton: &waE2E.HydratedTemplateButton_QuickReplyButton{QuickReplyButton: &waE2E.HydratedTemplateButton_HydratedQuickReplyButton{DisplayText: proto.String("OK"), ID: proto.String("q1")}}}, {HydratedButton: &waE2E.HydratedTemplateButton_UrlButton{UrlButton: &waE2E.HydratedTemplateButton_HydratedURLButton{DisplayText: proto.String("Rastrear"), URL: proto.String("https://x.test/t")}}}, {HydratedButton: &waE2E.HydratedTemplateButton_CallButton{CallButton: &waE2E.HydratedTemplateButton_HydratedCallButton{DisplayText: proto.String("Ligar"), PhoneNumber: proto.String("+5511900000000")}}}}}}}}),
		"message-list":              msg(&waE2E.Message{ListMessage: &waE2E.ListMessage{Title: proto.String("Menu"), Description: proto.String("Veja as opções"), ButtonText: proto.String("Abrir"), FooterText: proto.String("fim"), Sections: []*waE2E.ListMessage_Section{{Title: proto.String("Produtos"), Rows: []*waE2E.ListMessage_Row{{RowID: proto.String("r1"), Title: proto.String("Camiseta"), Description: proto.String("M")}}}}}}),
		"message-native-flow":       msg(&waE2E.Message{InteractiveMessage: &waE2E.InteractiveMessage{Header: &waE2E.InteractiveMessage_Header{Title: proto.String("Oferta")}, Body: &waE2E.InteractiveMessage_Body{Text: proto.String("Aproveite")}, Footer: &waE2E.InteractiveMessage_Footer{Text: proto.String("até sexta")}, InteractiveMessage: &waE2E.InteractiveMessage_NativeFlowMessage_{NativeFlowMessage: &waE2E.InteractiveMessage_NativeFlowMessage{Buttons: []*waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{{Name: proto.String("quick_reply"), ButtonParamsJSON: proto.String(`{"display_text":"Quero","id":"n1"}`)}, {Name: proto.String("cta_call"), ButtonParamsJSON: proto.String(`{"display_text":"Ligar","phone_number":"+5511900000000"}`)}, {Name: proto.String("cta_copy"), ButtonParamsJSON: proto.String(`{"display_text":"Copiar","copy_code":"CUPOM10"}`)}, {Name: proto.String("single_select"), ButtonParamsJSON: proto.String(`{"title":"Tamanhos","sections":[{"title":"Camisetas","rows":[{"id":"s1","title":"P","description":"pequeno"}]}]}`)}, {Name: proto.String("send_location"), ButtonParamsJSON: proto.String(`{"display_text":"Enviar localização"}`)}}}}}}),
		"message-buttons-reply":     msg(&waE2E.Message{ButtonsResponseMessage: &waE2E.ButtonsResponseMessage{SelectedButtonID: proto.String("b1"), Response: &waE2E.ButtonsResponseMessage_SelectedDisplayText{SelectedDisplayText: "Atendente"}, ContextInfo: &waE2E.ContextInfo{StanzaID: proto.String("3EB0000000000000000000"), QuotedMessage: &waE2E.Message{ButtonsMessage: &waE2E.ButtonsMessage{ContentText: proto.String("Escolha")}}}}}),
		"message-template-reply":    msg(&waE2E.Message{TemplateButtonReplyMessage: &waE2E.TemplateButtonReplyMessage{SelectedID: proto.String("q1"), SelectedDisplayText: proto.String("OK")}}),
		"message-list-reply":        msg(&waE2E.Message{ListResponseMessage: &waE2E.ListResponseMessage{Title: proto.String("Camiseta"), SingleSelectReply: &waE2E.ListResponseMessage_SingleSelectReply{SelectedRowID: proto.String("r1")}}}),
		"message-native-flow-reply": msg(&waE2E.Message{InteractiveResponseMessage: &waE2E.InteractiveResponseMessage{Body: &waE2E.InteractiveResponseMessage_Body{Text: proto.String("Quero")}, InteractiveResponseMessage: &waE2E.InteractiveResponseMessage_NativeFlowResponseMessage_{NativeFlowResponseMessage: &waE2E.InteractiveResponseMessage_NativeFlowResponseMessage{Name: proto.String("quick_reply"), ParamsJSON: proto.String(`{"id":"n1"}`)}}}}),
		"message-ad-referral":       msg(&waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("Olá, vi o anúncio"), ContextInfo: &waE2E.ContextInfo{ExternalAdReply: &waE2E.ContextInfo_ExternalAdReplyInfo{SourceType: proto.String("ad"), SourceApp: proto.String("instagram"), SourceID: proto.String("120211234567890123"), SourceURL: proto.String("https://fb.me/xyz"), CtwaClid: proto.String("ARAkLclid"), Ref: proto.String("promo-primavera"), Title: proto.String("Promo"), Body: proto.String("Fale conosco"), MediaType: waE2E.ContextInfo_ExternalAdReplyInfo_IMAGE.Enum(), ThumbnailURL: proto.String("https://x.test/thumb.jpg"), MediaURL: proto.String("https://x.test/ad.jpg")}, EntryPointConversionSource: proto.String("ctwa_ad"), EntryPointConversionApp: proto.String("instagram"), EntryPointConversionExternalSource: proto.String("meta"), EntryPointConversionExternalMedium: proto.String("paid")}}}),
		"message-link-entry-point":  msg(&waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("oi"), ContextInfo: &waE2E.ContextInfo{EntryPointConversionExternalSource: proto.String("newsletter"), EntryPointConversionExternalMedium: proto.String("email")}}}),
		"message-poll-options":      msg(&waE2E.Message{PollCreationMessageV3: &waE2E.PollCreationMessage{Name: proto.String("Almoço?"), Options: []*waE2E.PollCreationMessage_Option{{OptionName: proto.String("Sim")}, {OptionName: proto.String("Não")}}, SelectableOptionsCount: proto.Uint32(1)}}),
		"message-poll-vote":         msg(&waE2E.Message{PollUpdateMessage: &waE2E.PollUpdateMessage{PollCreationMessageKey: &waCommon.MessageKey{ID: proto.String("3EB0000000000000000000")}}}),
		"message-poll-vote-resolved": func() (string, any) {
			event, stable := Build(ctx, newMessageEvent(&waE2E.Message{PollUpdateMessage: &waE2E.PollUpdateMessage{PollCreationMessageKey: &waCommon.MessageKey{ID: proto.String("3EB0000000000000000000")}}}), nil, resolver)
			return event, WithPollVote(stable.(Message), "3EB0000000000000000000", []string{"Sim"}, "resolved")
		},
		"message-call-missed":    msg(&waE2E.Message{CallLogMesssage: &waE2E.CallLogMessage{CallOutcome: waE2E.CallLogMessage_MISSED.Enum(), IsVideo: proto.Bool(false), CallType: waE2E.CallLogMessage_REGULAR.Enum()}}),
		"message-call-connected": msg(&waE2E.Message{CallLogMesssage: &waE2E.CallLogMessage{CallOutcome: waE2E.CallLogMessage_CONNECTED.Enum(), IsVideo: proto.Bool(true), DurationSecs: proto.Int64(125), CallType: waE2E.CallLogMessage_REGULAR.Enum()}}),
		"message-product":        msg(&waE2E.Message{ProductMessage: &waE2E.ProductMessage{Body: proto.String("Olha esse"), Product: &waE2E.ProductMessage_ProductSnapshot{ProductID: proto.String("P1"), Title: proto.String("Camiseta"), Description: proto.String("Algodão"), CurrencyCode: proto.String("BRL"), PriceAmount1000: proto.Int64(59900), SalePriceAmount1000: proto.Int64(49900), RetailerID: proto.String("SKU-9"), URL: proto.String("https://x.test/p1")}}}),
		"message-order":          msg(&waE2E.Message{OrderMessage: &waE2E.OrderMessage{OrderID: proto.String("O1"), OrderTitle: proto.String("Pedido 12"), ItemCount: proto.Int32(3), Status: waE2E.OrderMessage_INQUIRY.Enum(), Message: proto.String("Pode entregar amanhã?"), TotalAmount1000: proto.Int64(149700), TotalCurrencyCode: proto.String("BRL")}}),
		"message-ack-delivered":  ack(types.ReceiptTypeDelivered),
		"message-ack-read":       ack(types.ReceiptTypeRead),
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
