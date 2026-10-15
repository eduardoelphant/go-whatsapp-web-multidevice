package whatsapp

import (
	"testing"

	"github.com/purpshell/meowcaller"
	"github.com/stretchr/testify/assert"
	"go.mau.fi/whatsmeow/types"
)

func TestParseCallsEarlyMedia(t *testing.T) {
	cases := map[string]meowcaller.EarlyMediaMode{
		"off": meowcaller.EarlyMediaOff, "": meowcaller.EarlyMediaOff, "bogus": meowcaller.EarlyMediaOff,
		"listen": meowcaller.EarlyMediaListen, " LISTEN ": meowcaller.EarlyMediaListen,
		"full": meowcaller.EarlyMediaFull, "Full": meowcaller.EarlyMediaFull,
	}
	for in, want := range cases {
		if got := parseCallsEarlyMedia(in); got != want {
			t.Errorf("%q: got %v want %v", in, got, want)
		}
	}
}

func TestTargetPN(t *testing.T) {
	assert.Equal(t, "5511900000000@s.whatsapp.net", targetPN("+55 (11) 90000-0000"))
	assert.Equal(t, "5511900000000@s.whatsapp.net", targetPN("5511900000000@s.whatsapp.net"))
	assert.Equal(t, "", targetPN("123456789012345@lid"))
	assert.Equal(t, "", targetPN(""))
}

func TestResolvePeerWithoutStore(t *testing.T) {
	lid := types.NewJID("123456789012345", types.HiddenUserServer)
	assert.Equal(t, CallPeer{LID: "123456789012345@lid"}, resolvePeer(nil, lid, ""))
	assert.Equal(t, CallPeer{PN: "5511900000000@s.whatsapp.net", LID: "123456789012345@lid"},
		resolvePeer(nil, lid, "5511900000000@s.whatsapp.net"))
	pn := types.NewJID("5511900000000", types.DefaultUserServer)
	assert.Equal(t, CallPeer{PN: "5511900000000@s.whatsapp.net"}, resolvePeer(nil, pn, ""))
}

func TestMaskPeer(t *testing.T) {
	assert.Equal(t, "…2345@lid", maskPeer(CallPeer{PN: "5511900000000@s.whatsapp.net", LID: "123456789012345@lid"}))
	assert.Equal(t, "…0000@s.whatsapp.net", maskPeer(CallPeer{PN: "5511900000000@s.whatsapp.net"}))
	assert.Equal(t, "unknown", maskPeer(CallPeer{}))
}
