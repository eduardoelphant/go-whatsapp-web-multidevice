package stablepayload

import (
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func TestBuildInteractiveMessages(t *testing.T) {
	cases := []struct {
		name string
		raw  *waE2E.Message
		want string // JSON of stable.interactive
		text string
	}{
		{
			"buttons",
			&waE2E.Message{ButtonsMessage: &waE2E.ButtonsMessage{
				Header: &waE2E.ButtonsMessage_Text{Text: "Loja X"}, ContentText: proto.String("Escolha"), FooterText: proto.String("Rodapé"),
				Buttons: []*waE2E.ButtonsMessage_Button{{ButtonID: proto.String("b1"), ButtonText: &waE2E.ButtonsMessage_Button_ButtonText{DisplayText: proto.String("Atendente")}}},
			}},
			`{"kind":"buttons","header":"Loja X","footer":"Rodapé","buttons":[{"kind":"reply","id":"b1","text":"Atendente","value":null}],"sections":[]}`,
			"Escolha",
		},
		{
			"template",
			&waE2E.Message{TemplateMessage: &waE2E.TemplateMessage{Format: &waE2E.TemplateMessage_HydratedFourRowTemplate_{HydratedFourRowTemplate: &waE2E.TemplateMessage_HydratedFourRowTemplate{
				Title:               &waE2E.TemplateMessage_HydratedFourRowTemplate_HydratedTitleText{HydratedTitleText: "Pedido"},
				HydratedContentText: proto.String("Seu pedido saiu"), HydratedFooterText: proto.String("Obrigado"),
				HydratedButtons: []*waE2E.HydratedTemplateButton{
					{HydratedButton: &waE2E.HydratedTemplateButton_QuickReplyButton{QuickReplyButton: &waE2E.HydratedTemplateButton_HydratedQuickReplyButton{DisplayText: proto.String("OK"), ID: proto.String("q1")}}},
					{HydratedButton: &waE2E.HydratedTemplateButton_UrlButton{UrlButton: &waE2E.HydratedTemplateButton_HydratedURLButton{DisplayText: proto.String("Rastrear"), URL: proto.String("https://x.test/t")}}},
					{HydratedButton: &waE2E.HydratedTemplateButton_CallButton{CallButton: &waE2E.HydratedTemplateButton_HydratedCallButton{DisplayText: proto.String("Ligar"), PhoneNumber: proto.String("+5511900000000")}}},
				},
			}}}},
			`{"kind":"template","header":"Pedido","footer":"Obrigado","buttons":[{"kind":"reply","id":"q1","text":"OK","value":null},{"kind":"url","id":null,"text":"Rastrear","value":"https://x.test/t"},{"kind":"call","id":null,"text":"Ligar","value":"+5511900000000"}],"sections":[]}`,
			"Seu pedido saiu",
		},
		{
			"list",
			&waE2E.Message{ListMessage: &waE2E.ListMessage{
				Title: proto.String("Menu"), Description: proto.String("Veja as opções"), ButtonText: proto.String("Abrir"), FooterText: proto.String("fim"),
				Sections: []*waE2E.ListMessage_Section{{Title: proto.String("Produtos"), Rows: []*waE2E.ListMessage_Row{{RowID: proto.String("r1"), Title: proto.String("Camiseta"), Description: proto.String("M")}}}},
			}},
			`{"kind":"list","header":"Menu","footer":"fim","buttons":[{"kind":"menu","id":null,"text":"Abrir","value":null}],"sections":[{"title":"Produtos","rows":[{"id":"r1","title":"Camiseta","description":"M"}]}]}`,
			"Veja as opções",
		},
		{
			"native flow",
			&waE2E.Message{InteractiveMessage: &waE2E.InteractiveMessage{
				Header: &waE2E.InteractiveMessage_Header{Title: proto.String("Oferta")},
				Body:   &waE2E.InteractiveMessage_Body{Text: proto.String("Aproveite")},
				Footer: &waE2E.InteractiveMessage_Footer{Text: proto.String("até sexta")},
				InteractiveMessage: &waE2E.InteractiveMessage_NativeFlowMessage_{NativeFlowMessage: &waE2E.InteractiveMessage_NativeFlowMessage{Buttons: []*waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
					{Name: proto.String("quick_reply"), ButtonParamsJSON: proto.String(`{"display_text":"Quero","id":"n1"}`)},
					{Name: proto.String("cta_url"), ButtonParamsJSON: proto.String(`{"display_text":"Site","url":"https://x.test"}`)},
					{Name: proto.String("cta_copy"), ButtonParamsJSON: proto.String(`{"display_text":"Copiar cupom","copy_code":"PRIMAVERA10"}`)},
					{Name: proto.String("single_select"), ButtonParamsJSON: proto.String(`{"title":"Tamanhos","sections":[{"title":"Camisetas","rows":[{"id":"s1","title":"P","description":"pequeno"}]}]}`)},
				}}},
			}},
			`{"kind":"native_flow","header":"Oferta","footer":"até sexta","buttons":[{"kind":"reply","id":"n1","text":"Quero","value":null},{"kind":"url","id":null,"text":"Site","value":"https://x.test"},{"kind":"copy","id":null,"text":"Copiar cupom","value":"PRIMAVERA10"},{"kind":"menu","id":null,"text":"Tamanhos","value":null}],"sections":[{"title":"Camisetas","rows":[{"id":"s1","title":"P","description":"pequeno"}]}]}`,
			"Aproveite",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := buildMessage(t, newMessageEvent(tc.raw))
			if m.Type != TypeInteractive || deref(m.Text) != tc.text || m.Reply != nil {
				t.Fatalf("type=%s text=%s reply=%v", m.Type, deref(m.Text), m.Reply)
			}
			if got := toJSON(t, m.Interactive); got != tc.want {
				t.Fatalf("interactive =\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}

func TestBuildInteractiveReplies(t *testing.T) {
	ci := &waE2E.ContextInfo{StanzaID: proto.String("MENU1"), QuotedMessage: &waE2E.Message{ButtonsMessage: &waE2E.ButtonsMessage{ContentText: proto.String("Escolha")}}}
	cases := []struct {
		name, want, text string
		raw              *waE2E.Message
	}{
		{"buttons", `{"kind":"buttons","id":"b1"}`, "Atendente", &waE2E.Message{ButtonsResponseMessage: &waE2E.ButtonsResponseMessage{
			SelectedButtonID: proto.String("b1"), Response: &waE2E.ButtonsResponseMessage_SelectedDisplayText{SelectedDisplayText: "Atendente"}, ContextInfo: ci}}},
		{"template", `{"kind":"template","id":"q1"}`, "OK", &waE2E.Message{TemplateButtonReplyMessage: &waE2E.TemplateButtonReplyMessage{
			SelectedID: proto.String("q1"), SelectedDisplayText: proto.String("OK"), ContextInfo: ci}}},
		{"list", `{"kind":"list","id":"r1"}`, "Camiseta", &waE2E.Message{ListResponseMessage: &waE2E.ListResponseMessage{
			Title: proto.String("Camiseta"), SingleSelectReply: &waE2E.ListResponseMessage_SingleSelectReply{SelectedRowID: proto.String("r1")}, ContextInfo: ci}}},
		{"native flow", `{"kind":"native_flow","id":"n1"}`, "Quero", &waE2E.Message{InteractiveResponseMessage: &waE2E.InteractiveResponseMessage{
			Body: &waE2E.InteractiveResponseMessage_Body{Text: proto.String("Quero")},
			InteractiveResponseMessage: &waE2E.InteractiveResponseMessage_NativeFlowResponseMessage_{NativeFlowResponseMessage: &waE2E.InteractiveResponseMessage_NativeFlowResponseMessage{
				Name: proto.String("quick_reply"), ParamsJSON: proto.String(`{"id":"n1"}`)}},
			ContextInfo: ci}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := buildMessage(t, newMessageEvent(tc.raw))
			if m.Type != TypeInteractiveReply || deref(m.Text) != tc.text || m.Interactive != nil {
				t.Fatalf("type=%s text=%s interactive=%v", m.Type, deref(m.Text), m.Interactive)
			}
			if got := toJSON(t, m.Reply); got != tc.want {
				t.Fatalf("reply = %s, want %s", got, tc.want)
			}
			if m.Quoted == nil || m.Quoted.ID != "MENU1" || m.Quoted.Type != TypeInteractive {
				t.Fatalf("quoted = %+v, want the interactive message MENU1", m.Quoted)
			}
		})
	}
}

func TestBuildReferralFromAd(t *testing.T) {
	raw := &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
		Text: proto.String("Olá, vi o anúncio"),
		ContextInfo: &waE2E.ContextInfo{
			ExternalAdReply: &waE2E.ContextInfo_ExternalAdReplyInfo{
				SourceType: proto.String("ad"), SourceApp: proto.String("instagram"), SourceID: proto.String("120211234567890123"),
				SourceURL: proto.String("https://fb.me/xyz"), CtwaClid: proto.String("ARAkLclid"), Title: proto.String("Promo"),
				Body: proto.String("Fale conosco"), MediaType: waE2E.ContextInfo_ExternalAdReplyInfo_IMAGE.Enum(),
				ThumbnailURL: proto.String("https://x.test/thumb.jpg"), MediaURL: proto.String("https://x.test/ad.jpg"),
			},
			EntryPointConversionSource: proto.String("ctwa_ad"), EntryPointConversionApp: proto.String("instagram"),
		},
	}}
	m := buildMessage(t, newMessageEvent(raw))
	want := `{"source_type":"ad","source_app":"instagram","source_id":"120211234567890123","source_url":"https://fb.me/xyz","ctwa_clid":"ARAkLclid","ref":null,"title":"Promo","body":"Fale conosco","media_type":"image","thumbnail_url":"https://x.test/thumb.jpg","media_url":"https://x.test/ad.jpg","entry_point":{"source":"ctwa_ad","app":"instagram","external_source":null,"external_medium":null}}`
	if got := toJSON(t, m.Referral); got != want {
		t.Fatalf("referral =\n%s\nwant\n%s", got, want)
	}
	if m.Type != TypeText || deref(m.Text) != "Olá, vi o anúncio" {
		t.Fatalf("type=%s text=%s", m.Type, deref(m.Text))
	}
}

func TestBuildReferralFromLinkWithoutAd(t *testing.T) {
	raw := &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("oi"), ContextInfo: &waE2E.ContextInfo{
		EntryPointConversionExternalSource: proto.String("newsletter"), EntryPointConversionExternalMedium: proto.String("email"),
	}}}
	m := buildMessage(t, newMessageEvent(raw))
	want := `{"source_type":null,"source_app":null,"source_id":null,"source_url":null,"ctwa_clid":null,"ref":null,"title":null,"body":null,"media_type":null,"thumbnail_url":null,"media_url":null,"entry_point":{"source":null,"app":null,"external_source":"newsletter","external_medium":"email"}}`
	if got := toJSON(t, m.Referral); got != want {
		t.Fatalf("referral =\n%s\nwant\n%s", got, want)
	}
	plain := buildMessage(t, newMessageEvent(&waE2E.Message{Conversation: proto.String("x")}))
	if plain.Referral != nil || plain.Interactive != nil || plain.Reply != nil {
		t.Fatal("a plain message must have referral, interactive and reply null")
	}
}
