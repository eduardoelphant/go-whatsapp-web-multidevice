package whatsapp

import (
	"context"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	meowcaller "github.com/purpshell/meowcaller"
	"github.com/rs/zerolog"
	"github.com/sirupsen/logrus"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// Fork (elphant): adapter from meowcaller to the callEngine/callHandle seams.

type meowEngine struct {
	client *meowcaller.Client
	wa     *whatsmeow.Client
}

// parseCallsLogLevel maps the configured level to zerolog; anything else is info.
func parseCallsLogLevel(s string) zerolog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return zerolog.DebugLevel
	case "warn":
		return zerolog.WarnLevel
	case "error":
		return zerolog.ErrorLevel
	}
	return zerolog.InfoLevel
}

// parseCallsEarlyMedia maps the configured mode to meowcaller; anything else is off.
func parseCallsEarlyMedia(s string) meowcaller.EarlyMediaMode {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "off":
		return meowcaller.EarlyMediaOff
	case "listen":
		return meowcaller.EarlyMediaListen
	case "full":
		return meowcaller.EarlyMediaFull
	}
	logrus.Warnf("[CALLS] invalid WHATSAPP_CALLS_EARLY_MEDIA %q; using off", s)
	return meowcaller.EarlyMediaOff
}

func newMeowEngine(wa *whatsmeow.Client, filter func(*events.CallOffer) bool) *meowEngine {
	log := zerolog.New(maskingWriter{os.Stdout}).With().Timestamp().Str("component", "calls").Logger().Level(parseCallsLogLevel(config.WhatsappCallsLogLevel))
	return &meowEngine{wa: wa, client: meowcaller.NewClient(wa, meowcaller.WithLogger(log), meowcaller.WithOfferFilter(filter), meowcaller.WithEarlyMedia(parseCallsEarlyMedia(config.WhatsappCallsEarlyMedia)))}
}

func (e *meowEngine) OnIncomingCall(fn func(callHandle)) {
	e.client.OnIncomingCall(func(c *meowcaller.Call) {
		fn(meowCall{c: c, peer: resolvePeer(e.wa, c.Peer(), "")})
	})
}

func (e *meowEngine) Call(ctx context.Context, target string) (callHandle, error) {
	c, err := e.client.Call(ctx, target)
	if err != nil {
		return nil, err
	}
	return meowCall{c: c, peer: resolvePeer(e.wa, c.Peer(), targetPN(target))}, nil
}

type meowCall struct {
	c    *meowcaller.Call
	peer CallPeer
}

func (m meowCall) ID() string     { return m.c.ID() }
func (m meowCall) Peer() CallPeer { return m.peer }
func (m meowCall) Answer() error  { return m.c.Answer() }
func (m meowCall) Reject() error  { return m.c.Reject() }
func (m meowCall) Hangup() error  { return m.c.Hangup() }

// OnEnd registers fn. meowcaller does not replay an end to a callback registered
// after the call ended, so check the phase after registering; the Once guard stops a
// double call when the end fires concurrently with that check.
func (m meowCall) OnEnd(fn func(string)) {
	var once sync.Once
	wrapped := func(reason string) { once.Do(func() { fn(reason) }) }
	m.c.OnEnd(wrapped)
	if m.c.State() == meowcaller.CallPhaseEnded {
		wrapped("ended")
	}
}

// OnRinging fires when an outbound call's peer device pre-accepts (the phone rings). The
// preaccept often arrives within a second of the offer, before Start registers this
// callback, and meowcaller does not replay it; check the phase after registering, as
// OnEnd does, with a Once guard against a concurrent state change.
func (m meowCall) OnRinging(fn func()) {
	var once sync.Once
	wrapped := func() { once.Do(fn) }
	m.c.OnStateChange(func(p meowcaller.CallPhase) {
		if p == meowcaller.CallPhaseRinging {
			wrapped()
		}
	})
	if m.c.State() == meowcaller.CallPhaseRinging {
		wrapped()
	}
}

func (m meowCall) OnAccept(fn func()) { m.c.OnPeerAccept(fn) }
func (m meowCall) OnMedia(fn func())  { m.c.OnReady(fn) }

// Bridge attaches the call's audio. sink receives the peer's decoded 16 kHz frames; src is
// pulled every 60 ms by the send loop once media runs (it is not wrapped in OnReady: that is a
// single slot that may already have fired).
func (m meowCall) Bridge(src pcmSource, sink pcmSink) {
	m.c.Receive(sink)
	m.c.Play(src)
}

// resolvePeer builds the peer's phone-number JID and LID from what the engine knows plus the
// device's LID store. Either may stay "".
func resolvePeer(wa *whatsmeow.Client, peer types.JID, pnHint string) CallPeer {
	var out CallPeer
	peer = peer.ToNonAD()
	switch peer.Server {
	case types.HiddenUserServer:
		out.LID = peer.String()
	case types.DefaultUserServer:
		out.PN = peer.String()
	}
	if pnHint != "" {
		out.PN = pnHint
	}
	if wa == nil || wa.Store == nil || wa.Store.LIDs == nil {
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if out.PN == "" && out.LID != "" {
		if pn, err := wa.Store.LIDs.GetPNForLID(ctx, peer); err == nil && !pn.IsEmpty() {
			out.PN = pn.ToNonAD().String()
		}
	}
	if out.LID == "" && out.PN != "" {
		if pnJID, err := types.ParseJID(out.PN); err == nil {
			if lid, err := wa.Store.LIDs.GetLIDForPN(ctx, pnJID); err == nil && !lid.IsEmpty() {
				out.LID = lid.ToNonAD().String()
			}
		}
	}
	return out
}

var nonDigits = regexp.MustCompile(`[^0-9]`)

// targetPN is the phone-number JID of a call target given as digits, "+55 ..." or a JID.
func targetPN(target string) string {
	if strings.Contains(target, "@") {
		j, err := types.ParseJID(target)
		if err != nil || j.Server != types.DefaultUserServer {
			return ""
		}
		return j.ToNonAD().String()
	}
	digits := nonDigits.ReplaceAllString(target, "")
	if digits == "" {
		return ""
	}
	return digits + "@" + types.DefaultUserServer
}

var jidDigits = regexp.MustCompile(`([0-9]{8,})((?::[0-9]+)?@)`)

// maskSlot masks the phone number in a slot id that is a JID.
func maskSlot(s string) string { return string(maskJIDDigits([]byte(s))) }

func maskJIDDigits(p []byte) []byte {
	return jidDigits.ReplaceAllFunc(p, func(b []byte) []byte {
		m := jidDigits.FindSubmatch(b)
		out := append([]byte("…"), m[1][len(m[1])-4:]...)
		return append(out, m[2]...)
	})
}

// maskingWriter hides phone numbers in meowcaller's logs: every run of 8 or more
// digits in a JID (before "@" or ":device@") is cut to an ellipsis plus its last 4 digits.
type maskingWriter struct{ w io.Writer }

func (mw maskingWriter) Write(p []byte) (int, error) {
	masked := maskJIDDigits(p)
	if _, err := mw.w.Write(masked); err != nil {
		return 0, err
	}
	return len(p), nil
}

var digitRun = regexp.MustCompile(`[0-9]{8,}`)

// MaskDigits replaces every run of 8 or more digits with an ellipsis plus its last 4,
// so error text from the call engine can be returned to API clients without phone numbers.
func MaskDigits(s string) string {
	return digitRun.ReplaceAllStringFunc(s, func(r string) string { return "…" + r[len(r)-4:] })
}
