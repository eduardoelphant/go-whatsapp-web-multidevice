package stablepayload

import (
	"testing"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func TestBuildPollCreationOptions(t *testing.T) {
	m := buildMessage(t, newMessageEvent(&waE2E.Message{PollCreationMessageV3: &waE2E.PollCreationMessage{
		Name:                   proto.String("Almoço?"),
		Options:                []*waE2E.PollCreationMessage_Option{{OptionName: proto.String("Sim")}, {OptionName: proto.String("Não")}},
		SelectableOptionsCount: proto.Uint32(1),
	}}))
	if got := toJSON(t, m.Poll); got != `{"options":["Sim","Não"],"selectable_count":1}` {
		t.Fatalf("poll = %s", got)
	}
}

func TestBuildPollVoteBeforeAndAfterDecryption(t *testing.T) {
	m := buildMessage(t, newMessageEvent(&waE2E.Message{PollUpdateMessage: &waE2E.PollUpdateMessage{
		PollCreationMessageKey: &waCommon.MessageKey{ID: proto.String("POLL1")},
	}}))
	if m.Type != TypePollVote || m.Text != nil {
		t.Fatalf("type=%s text=%v", m.Type, m.Text)
	}
	if got := toJSON(t, m.PollVote); got != `{"poll_id":"POLL1","selected":[],"resolution":"encrypted"}` {
		t.Fatalf("poll_vote = %s", got)
	}
	resolved := WithPollVote(m, "POLL1", []string{"Sim"}, "resolved")
	if got := toJSON(t, resolved.PollVote); got != `{"poll_id":"POLL1","selected":["Sim"],"resolution":"resolved"}` {
		t.Fatalf("resolved poll_vote = %s", got)
	}
}

func TestBuildCallLog(t *testing.T) {
	m := buildMessage(t, newMessageEvent(&waE2E.Message{CallLogMesssage: &waE2E.CallLogMessage{
		CallOutcome: waE2E.CallLogMessage_MISSED.Enum(), IsVideo: proto.Bool(true), DurationSecs: proto.Int64(0), CallType: waE2E.CallLogMessage_REGULAR.Enum(),
	}}))
	if m.Type != TypeCall {
		t.Fatalf("type = %s", m.Type)
	}
	if got := toJSON(t, m.Call); got != `{"outcome":"missed","video":true,"duration":null,"call_type":"regular"}` {
		t.Fatalf("call = %s", got)
	}
}

func TestBuildProductAndOrder(t *testing.T) {
	product := buildMessage(t, newMessageEvent(&waE2E.Message{ProductMessage: &waE2E.ProductMessage{
		Body: proto.String("Olha esse"),
		Product: &waE2E.ProductMessage_ProductSnapshot{
			ProductID: proto.String("P1"), Title: proto.String("Camiseta"), Description: proto.String("Algodão"), CurrencyCode: proto.String("BRL"),
			PriceAmount1000: proto.Int64(59900), SalePriceAmount1000: proto.Int64(49900), RetailerID: proto.String("SKU-9"), URL: proto.String("https://x.test/p1"),
		},
	}}))
	if product.Type != TypeProduct || deref(product.Text) != "Olha esse" {
		t.Fatalf("type=%s text=%s", product.Type, deref(product.Text))
	}
	if got := toJSON(t, product.Product); got != `{"id":"P1","title":"Camiseta","description":"Algodão","retailer_id":"SKU-9","url":"https://x.test/p1","currency":"BRL","price_1000":59900,"sale_price_1000":49900}` {
		t.Fatalf("product = %s", got)
	}

	order := buildMessage(t, newMessageEvent(&waE2E.Message{OrderMessage: &waE2E.OrderMessage{
		OrderID: proto.String("O1"), OrderTitle: proto.String("Pedido 12"), ItemCount: proto.Int32(3), Status: waE2E.OrderMessage_INQUIRY.Enum(),
		Message: proto.String("Pode entregar amanhã?"), TotalAmount1000: proto.Int64(149700), TotalCurrencyCode: proto.String("BRL"),
	}}))
	if order.Type != TypeOrder || deref(order.Text) != "Pode entregar amanhã?" {
		t.Fatalf("type=%s text=%s", order.Type, deref(order.Text))
	}
	if got := toJSON(t, order.Order); got != `{"id":"O1","title":"Pedido 12","item_count":3,"status":"inquiry","currency":"BRL","total_1000":149700}` {
		t.Fatalf("order = %s", got)
	}
}
