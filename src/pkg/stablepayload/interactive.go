package stablepayload

import (
	"encoding/json"
	"strings"

	"go.mau.fi/whatsmeow/proto/waE2E"
)

// Interactive kinds (interactive.kind and reply.kind).
const (
	kindButtons    = "buttons"
	kindTemplate   = "template"
	kindList       = "list"
	kindNativeFlow = "native_flow"
)

// interactiveOf returns the interactive block and its main text, or nil.
func interactiveOf(msg *waE2E.Message) (*Interactive, *string) {
	switch {
	case msg.GetButtonsMessage() != nil:
		bm := msg.GetButtonsMessage()
		in := newInteractive(kindButtons, bm.GetText(), bm.GetFooterText())
		for _, b := range bm.GetButtons() {
			if b.GetType() == waE2E.ButtonsMessage_Button_NATIVE_FLOW && b.GetNativeFlowInfo() != nil {
				btn, sections := nativeFlowButton(b.GetNativeFlowInfo().GetName(), b.GetNativeFlowInfo().GetParamsJSON())
				in.Buttons = append(in.Buttons, btn)
				in.Sections = append(in.Sections, sections...)
				continue
			}
			in.Buttons = append(in.Buttons, Button{Kind: "reply", ID: strPtr(b.GetButtonID()), Text: strPtr(b.GetButtonText().GetDisplayText())})
		}
		return in, strPtr(bm.GetContentText())
	case msg.GetTemplateMessage() != nil:
		tm := msg.GetTemplateMessage()
		t := tm.GetHydratedFourRowTemplate()
		if t == nil {
			t = tm.GetHydratedTemplate()
		}
		if t == nil {
			if im := tm.GetInteractiveMessageTemplate(); im != nil {
				in, text := fromInteractiveMessage(im)
				in.Kind = kindTemplate
				return in, text
			}
			return newInteractive(kindTemplate, "", ""), nil
		}
		in := newInteractive(kindTemplate, t.GetHydratedTitleText(), t.GetHydratedFooterText())
		for _, hb := range t.GetHydratedButtons() {
			switch {
			case hb.GetQuickReplyButton() != nil:
				q := hb.GetQuickReplyButton()
				in.Buttons = append(in.Buttons, Button{Kind: "reply", ID: strPtr(q.GetID()), Text: strPtr(q.GetDisplayText())})
			case hb.GetUrlButton() != nil:
				u := hb.GetUrlButton()
				in.Buttons = append(in.Buttons, Button{Kind: "url", Text: strPtr(u.GetDisplayText()), Value: strPtr(u.GetURL())})
			case hb.GetCallButton() != nil:
				c := hb.GetCallButton()
				in.Buttons = append(in.Buttons, Button{Kind: "call", Text: strPtr(c.GetDisplayText()), Value: strPtr(c.GetPhoneNumber())})
			}
		}
		return in, strPtr(t.GetHydratedContentText())
	case msg.GetListMessage() != nil:
		lm := msg.GetListMessage()
		in := newInteractive(kindList, lm.GetTitle(), lm.GetFooterText())
		if lm.GetButtonText() != "" {
			in.Buttons = append(in.Buttons, Button{Kind: "menu", Text: strPtr(lm.GetButtonText())})
		}
		for _, sec := range lm.GetSections() {
			section := Section{Title: strPtr(sec.GetTitle()), Rows: []Row{}}
			for _, row := range sec.GetRows() {
				section.Rows = append(section.Rows, Row{ID: strPtr(row.GetRowID()), Title: strPtr(row.GetTitle()), Description: strPtr(row.GetDescription())})
			}
			in.Sections = append(in.Sections, section)
		}
		return in, strPtr(lm.GetDescription())
	case msg.GetInteractiveMessage() != nil:
		return fromInteractiveMessage(msg.GetInteractiveMessage())
	}
	return nil, nil
}

func newInteractive(kind, header, footer string) *Interactive {
	return &Interactive{Kind: kind, Header: strPtr(header), Footer: strPtr(footer), Buttons: []Button{}, Sections: []Section{}}
}

func fromInteractiveMessage(im *waE2E.InteractiveMessage) (*Interactive, *string) {
	header := im.GetHeader().GetTitle()
	if header == "" {
		header = im.GetHeader().GetSubtitle()
	}
	in := newInteractive(kindNativeFlow, header, im.GetFooter().GetText())
	for _, b := range im.GetNativeFlowMessage().GetButtons() {
		btn, sections := nativeFlowButton(b.GetName(), b.GetButtonParamsJSON())
		in.Buttons = append(in.Buttons, btn)
		in.Sections = append(in.Sections, sections...)
	}
	return in, strPtr(im.GetBody().GetText())
}

type nativeFlowParams struct {
	DisplayText string `json:"display_text"`
	ID          string `json:"id"`
	URL         string `json:"url"`
	PhoneNumber string `json:"phone_number"`
	CopyCode    string `json:"copy_code"`
	Title       string `json:"title"`
	Sections    []struct {
		Title string `json:"title"`
		Rows  []struct {
			ID          string `json:"id"`
			Title       string `json:"title"`
			Description string `json:"description"`
		} `json:"rows"`
	} `json:"sections"`
}

// nativeFlowButton maps a native flow button (name + JSON params) to a Button;
// single_select menus also return their sections.
func nativeFlowButton(name, paramsJSON string) (Button, []Section) {
	var p nativeFlowParams
	_ = json.Unmarshal([]byte(paramsJSON), &p)
	switch name {
	case "quick_reply":
		return Button{Kind: "reply", ID: strPtr(p.ID), Text: strPtr(p.DisplayText)}, nil
	case "cta_url":
		return Button{Kind: "url", Text: strPtr(p.DisplayText), Value: strPtr(p.URL)}, nil
	case "cta_call":
		return Button{Kind: "call", Text: strPtr(p.DisplayText), Value: strPtr(p.PhoneNumber)}, nil
	case "cta_copy":
		return Button{Kind: "copy", Text: strPtr(p.DisplayText), Value: strPtr(p.CopyCode)}, nil
	case "single_select":
		sections := make([]Section, 0, len(p.Sections))
		for _, sec := range p.Sections {
			section := Section{Title: strPtr(sec.Title), Rows: []Row{}}
			for _, row := range sec.Rows {
				section.Rows = append(section.Rows, Row{ID: strPtr(row.ID), Title: strPtr(row.Title), Description: strPtr(row.Description)})
			}
			sections = append(sections, section)
		}
		return Button{Kind: "menu", Text: strPtr(p.Title)}, sections
	}
	return Button{Kind: "other", ID: strPtr(p.ID), Text: strPtr(p.DisplayText)}, nil
}

// replyOf returns the reply block and the chosen option's text, or nil.
func replyOf(msg *waE2E.Message) (*Reply, *string) {
	switch {
	case msg.GetButtonsResponseMessage() != nil:
		r := msg.GetButtonsResponseMessage()
		return &Reply{Kind: kindButtons, ID: strPtr(r.GetSelectedButtonID())}, strPtr(r.GetSelectedDisplayText())
	case msg.GetTemplateButtonReplyMessage() != nil:
		r := msg.GetTemplateButtonReplyMessage()
		return &Reply{Kind: kindTemplate, ID: strPtr(r.GetSelectedID())}, strPtr(r.GetSelectedDisplayText())
	case msg.GetListResponseMessage() != nil:
		r := msg.GetListResponseMessage()
		return &Reply{Kind: kindList, ID: strPtr(r.GetSingleSelectReply().GetSelectedRowID())}, strPtr(r.GetTitle())
	case msg.GetInteractiveResponseMessage() != nil:
		r := msg.GetInteractiveResponseMessage()
		var p struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal([]byte(r.GetNativeFlowResponseMessage().GetParamsJSON()), &p)
		return &Reply{Kind: kindNativeFlow, ID: strPtr(p.ID)}, strPtr(r.GetBody().GetText())
	}
	return nil, nil
}

// referralOf returns ad attribution and entry point data, or nil when the
// message carries neither.
func referralOf(ci *waE2E.ContextInfo) *Referral {
	if ci == nil {
		return nil
	}
	ep := EntryPoint{
		Source:         strPtr(ci.GetEntryPointConversionSource()),
		App:            strPtr(ci.GetEntryPointConversionApp()),
		ExternalSource: strPtr(ci.GetEntryPointConversionExternalSource()),
		ExternalMedium: strPtr(ci.GetEntryPointConversionExternalMedium()),
	}
	ad := ci.GetExternalAdReply()
	if ad == nil && ep.Source == nil && ep.App == nil && ep.ExternalSource == nil && ep.ExternalMedium == nil {
		return nil
	}
	r := &Referral{EntryPoint: ep}
	if ad != nil {
		r.SourceType = strPtr(ad.GetSourceType())
		r.SourceApp = strPtr(ad.GetSourceApp())
		r.SourceID = strPtr(ad.GetSourceID())
		r.SourceURL = strPtr(ad.GetSourceURL())
		r.CtwaClid = strPtr(ad.GetCtwaClid())
		r.Ref = strPtr(ad.GetRef())
		r.Title = strPtr(ad.GetTitle())
		r.Body = strPtr(ad.GetBody())
		if ad.MediaType != nil {
			r.MediaType = strPtr(strings.ToLower(ad.GetMediaType().String()))
		}
		r.ThumbnailURL = strPtr(ad.GetThumbnailURL())
		r.MediaURL = strPtr(ad.GetMediaURL())
	}
	return r
}
