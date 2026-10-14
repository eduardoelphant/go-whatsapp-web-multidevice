package whatsapp

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	meowcaller "github.com/purpshell/meowcaller"
	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// Fork (elphant): adapter from meowcaller to the callEngine/callHandle seams.

type meowEngine struct{ client *meowcaller.Client }

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

func newMeowEngine(wa *whatsmeow.Client, filter func(*events.CallOffer) bool) *meowEngine {
	log := zerolog.New(maskingWriter{os.Stdout}).With().Timestamp().Str("component", "calls").Logger().Level(parseCallsLogLevel(config.WhatsappCallsLogLevel))
	return &meowEngine{client: meowcaller.NewClient(wa, meowcaller.WithLogger(log), meowcaller.WithOfferFilter(filter))}
}

func (e *meowEngine) OnIncomingCall(fn func(callHandle)) {
	e.client.OnIncomingCall(func(c *meowcaller.Call) { fn(meowCall{c}) })
}

func (e *meowEngine) Call(ctx context.Context, target string) (callHandle, error) {
	c, err := e.client.Call(ctx, target)
	if err != nil {
		return nil, err
	}
	return meowCall{c}, nil
}

type meowCall struct{ c *meowcaller.Call }

func (m meowCall) ID() string         { return m.c.ID() }
func (m meowCall) PeerMasked() string { return maskCallJID(m.c.Peer()) }
func (m meowCall) Answer() error      { return m.c.Answer() }
func (m meowCall) Reject() error      { return m.c.Reject() }
func (m meowCall) Hangup() error      { return m.c.Hangup() }

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

func (m meowCall) Play(path string) error {
	var src meowcaller.AudioSource
	var err error
	switch strings.ToLower(filepath.Ext(path)) {
	case ".wav":
		src, err = meowcaller.WAVFile(path)
	case ".mp3":
		src, err = meowcaller.MP3File(path)
	case ".opus":
		src, err = meowcaller.OpusFile(path)
	default:
		return fmt.Errorf("unsupported audio file %q", path)
	}
	if err != nil {
		return err
	}
	// Not wrapped in OnReady: it is a single slot that may already have fired, and the
	// send loop only pulls player frames once media runs.
	m.c.Play(src)
	return nil
}

func (m meowCall) Record(path string) error {
	if m.c.State() == meowcaller.CallPhaseEnded {
		return fmt.Errorf("call already ended")
	}
	sink, err := meowcaller.WAVRecorder(path)
	if err != nil {
		return err
	}
	m.c.Receive(sink)
	return nil
}

func maskCallJID(j types.JID) string {
	u := j.User
	if len(u) > 4 {
		u = "…" + u[len(u)-4:]
	}
	return u + "@" + j.Server
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
