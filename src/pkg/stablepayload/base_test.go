package stablepayload

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"
)

type fakeResolver struct {
	pnForLID map[string]string
	lidForPN map[string]string
}

func (f fakeResolver) PNForLID(_ context.Context, lid types.JID) (types.JID, bool) {
	if v, ok := f.pnForLID[lid.String()]; ok {
		j, _ := types.ParseJID(v)
		return j, true
	}
	return types.JID{}, false
}

func (f fakeResolver) LIDForPN(_ context.Context, pn types.JID) (types.JID, bool) {
	if v, ok := f.lidForPN[pn.String()]; ok {
		j, _ := types.ParseJID(v)
		return j, true
	}
	return types.JID{}, false
}

var (
	testTime   = time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	pnContact  = types.NewJID("5511900000001", types.DefaultUserServer)
	lidContact = types.NewJID("100000000000001", types.HiddenUserServer)
	pnMe       = types.NewJID("5511900000009", types.DefaultUserServer)
	groupJID   = types.NewJID("120363000000000001", types.GroupServer)
	resolver   = fakeResolver{
		pnForLID: map[string]string{lidContact.String(): pnContact.String()},
		lidForPN: map[string]string{pnContact.String(): lidContact.String()},
	}
)

func toJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestBaseDirectMessageFromPN(t *testing.T) {
	info := types.MessageInfo{
		MessageSource: types.MessageSource{Chat: pnContact, Sender: pnContact},
		ID:            "MSG1", PushName: "Contact One", Timestamp: testTime,
	}
	got := toJSON(t, base(context.Background(), info, resolver))
	want := `{"schema":1,"id":"MSG1","timestamp":"2026-09-25T12:00:00Z","is_from_me":false,` +
		`"chat":{"pn":"5511900000001@s.whatsapp.net","lid":"100000000000001@lid","is_group":false},` +
		`"sender":{"pn":"5511900000001@s.whatsapp.net","lid":"100000000000001@lid","push_name":"Contact One"}}`
	if got != want {
		t.Fatalf("base =\n%s\nwant\n%s", got, want)
	}
}

func TestBaseLIDSenderWithoutMapping(t *testing.T) {
	info := types.MessageInfo{
		MessageSource: types.MessageSource{Chat: lidContact, Sender: lidContact},
		ID:            "MSG2", Timestamp: testTime,
	}
	got := toJSON(t, base(context.Background(), info, fakeResolver{}))
	want := `{"schema":1,"id":"MSG2","timestamp":"2026-09-25T12:00:00Z","is_from_me":false,` +
		`"chat":{"pn":null,"lid":"100000000000001@lid","is_group":false},` +
		`"sender":{"pn":null,"lid":"100000000000001@lid","push_name":null}}`
	if got != want {
		t.Fatalf("base =\n%s\nwant\n%s", got, want)
	}
}

func TestBaseUsesAltJIDBeforeResolver(t *testing.T) {
	info := types.MessageInfo{
		MessageSource: types.MessageSource{Chat: lidContact, Sender: lidContact, SenderAlt: pnContact},
		ID:            "MSG3", Timestamp: testTime,
	}
	got := base(context.Background(), info, nil)
	if got.Sender.PN == nil || *got.Sender.PN != pnContact.String() {
		t.Fatalf("sender.pn = %v, want %s from SenderAlt", got.Sender.PN, pnContact)
	}
	if got.Chat.PN == nil || *got.Chat.PN != pnContact.String() {
		t.Fatalf("chat.pn = %v, want %s from SenderAlt", got.Chat.PN, pnContact)
	}
}

func TestBaseGroupAndOwnMessage(t *testing.T) {
	info := types.MessageInfo{
		MessageSource: types.MessageSource{Chat: groupJID, Sender: pnMe, IsFromMe: true, IsGroup: true},
		ID:            "MSG4", Timestamp: testTime,
	}
	got := toJSON(t, base(context.Background(), info, nil))
	want := `{"schema":1,"id":"MSG4","timestamp":"2026-09-25T12:00:00Z","is_from_me":true,` +
		`"chat":{"pn":"120363000000000001@g.us","lid":null,"is_group":true},` +
		`"sender":{"pn":"5511900000009@s.whatsapp.net","lid":null,"push_name":null}}`
	if got != want {
		t.Fatalf("base =\n%s\nwant\n%s", got, want)
	}
}

func TestResolveStripsDeviceSuffix(t *testing.T) {
	withDevice := types.NewADJID("5511900000001", 0, 12)
	pn, lid := resolve(context.Background(), withDevice, types.JID{}, nil)
	if pn == nil || *pn != "5511900000001@s.whatsapp.net" || lid != nil {
		t.Fatalf("resolve = (%v, %v), want (5511900000001@s.whatsapp.net, nil)", pn, lid)
	}
}
