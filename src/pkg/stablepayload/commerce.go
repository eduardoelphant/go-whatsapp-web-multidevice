package stablepayload

import (
	"strings"

	"go.mau.fi/whatsmeow/proto/waE2E"
)

// Poll vote resolutions: GOWA's own values plus "encrypted" when no
// decryption was attempted.
const pollVoteEncrypted = "encrypted"

// WithPollVote fills the vote of a poll_vote message with what GOWA decrypted.
func WithPollVote(m Message, pollID string, selected []string, resolution string) Message {
	if selected == nil {
		selected = []string{}
	}
	m.PollVote = &PollVote{PollID: pollID, Selected: selected, Resolution: resolution}
	return m
}

func pollOf(p *waE2E.PollCreationMessage) *Poll {
	poll := &Poll{Options: []string{}}
	for _, o := range p.GetOptions() {
		poll.Options = append(poll.Options, o.GetOptionName())
	}
	if n := p.GetSelectableOptionsCount(); n > 0 {
		v := int(n)
		poll.SelectableCount = &v
	}
	return poll
}

// pollCreation returns the poll creation of any version, unwrapping V4.
func pollCreation(msg *waE2E.Message) *waE2E.PollCreationMessage {
	for _, p := range []*waE2E.PollCreationMessage{
		msg.GetPollCreationMessage(), msg.GetPollCreationMessageV2(), msg.GetPollCreationMessageV3(),
		msg.GetPollCreationMessageV5(), msg.GetPollCreationMessageV6(),
	} {
		if p != nil {
			return p
		}
	}
	if inner := msg.GetPollCreationMessageV4().GetMessage(); inner != nil {
		inner, _ = unwrap(inner)
		return pollCreation(inner)
	}
	return nil
}

func lowerEnum(name string, set bool) *string {
	if !set {
		return nil
	}
	return strPtr(strings.ToLower(name))
}

func callOf(c *waE2E.CallLogMessage) *Call {
	call := &Call{
		Outcome:  lowerEnum(c.GetCallOutcome().String(), c.CallOutcome != nil),
		Video:    c.GetIsVideo(),
		CallType: lowerEnum(c.GetCallType().String(), c.CallType != nil),
	}
	if d := c.GetDurationSecs(); d > 0 {
		v := int(d)
		call.Duration = &v
	}
	return call
}

func int64Ptr(v int64, set bool) *int64 {
	if !set {
		return nil
	}
	return &v
}

func productOf(p *waE2E.ProductMessage) *Product {
	snap := p.GetProduct()
	return &Product{
		ID: strPtr(snap.GetProductID()), Title: strPtr(snap.GetTitle()), Description: strPtr(snap.GetDescription()),
		RetailerID: strPtr(snap.GetRetailerID()), URL: strPtr(snap.GetURL()), Currency: strPtr(snap.GetCurrencyCode()),
		Price1000:     int64Ptr(snap.GetPriceAmount1000(), snap.PriceAmount1000 != nil),
		SalePrice1000: int64Ptr(snap.GetSalePriceAmount1000(), snap.SalePriceAmount1000 != nil),
	}
}

func orderOf(o *waE2E.OrderMessage) *Order {
	order := &Order{
		ID: strPtr(o.GetOrderID()), Title: strPtr(o.GetOrderTitle()),
		Status:    lowerEnum(o.GetStatus().String(), o.Status != nil),
		Currency:  strPtr(o.GetTotalCurrencyCode()),
		Total1000: int64Ptr(o.GetTotalAmount1000(), o.TotalAmount1000 != nil),
	}
	if o.ItemCount != nil {
		v := int(o.GetItemCount())
		order.ItemCount = &v
	}
	return order
}
