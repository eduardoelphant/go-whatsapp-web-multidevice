package whatsapp

import (
	"testing"

	"github.com/purpshell/meowcaller"
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
