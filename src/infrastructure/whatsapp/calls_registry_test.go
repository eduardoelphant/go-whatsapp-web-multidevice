package whatsapp

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	meowcaller "github.com/purpshell/meowcaller"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

func TestCallsFromContextDisabled(t *testing.T) {
	orig := config.WhatsappCallsEnabled
	t.Cleanup(func() { config.WhatsappCallsEnabled = orig })
	config.WhatsappCallsEnabled = false

	client := &whatsmeow.Client{}
	attachCalls("slot-a", client)
	inst := NewDeviceInstance("slot-a", client, nil)
	_, err := CallsFromContext(ContextWithDevice(context.Background(), inst))
	assert.ErrorIs(t, err, ErrCallsOff)
}

func TestForgetCallsOnReset(t *testing.T) {
	withCallsConfig(t, true, nil)
	client := &whatsmeow.Client{}
	m := newCallManager(&fakeEngine{}, callManagerOptions{})
	callsByClient.Store(client, m)

	inst := NewDeviceInstance("slot-a", client, nil)
	got, err := CallsFromContext(ContextWithDevice(context.Background(), inst))
	require.NoError(t, err)
	assert.Same(t, m, got)

	inst.ResetClient()
	_, ok := callsByClient.Load(client)
	assert.False(t, ok, "the manager of a reset client must be dropped")
}

func withCallsConfig(t *testing.T, enabled bool, devices []string) {
	oe, od, of := config.WhatsappCallsEnabled, config.WhatsappCallsDevices, callsStoreFn
	resetDeviceCallsCache()
	t.Cleanup(func() {
		config.WhatsappCallsEnabled, config.WhatsappCallsDevices, callsStoreFn = oe, od, of
		resetDeviceCallsCache()
	})
	config.WhatsappCallsEnabled, config.WhatsappCallsDevices = enabled, devices
	callsStoreFn = func() deviceCallsStore { return nil }
}

func TestAttachCallsNotAllowedKeepsOff(t *testing.T) {
	withCallsConfig(t, true, []string{"other"})
	client := &whatsmeow.Client{Store: &store.Device{}}
	attachCalls("slot-a", client)
	t.Cleanup(func() { forgetCalls(client) })
	_, ok := callsByClient.Load(client)
	require.True(t, ok, "manager is attached to every client when the flag is on")
	assert.False(t, hasCallManager(client))
	inst := NewDeviceInstance("slot-a", client, nil)
	_, err := CallsFromContext(ContextWithDevice(context.Background(), inst))
	assert.ErrorIs(t, err, ErrCallsOff)
	assert.False(t, offerFilter("slot-a", client)(nil))
}

func TestAllowedAfterPairing(t *testing.T) {
	withCallsConfig(t, true, []string{"5511987654321"})
	dev := &store.Device{}
	client := &whatsmeow.Client{Store: dev}
	attachCalls("slot-a", client)
	t.Cleanup(func() { forgetCalls(client) })
	filter := offerFilter("slot-a", client)
	assert.False(t, filter(nil))
	assert.False(t, hasCallManager(client))

	dev.ID = &types.JID{User: "5511987654321", Server: types.DefaultUserServer}
	assert.True(t, filter(nil))
	assert.True(t, hasCallManager(client))
	inst := NewDeviceInstance("slot-a", client, nil)
	_, err := CallsFromContext(ContextWithDevice(context.Background(), inst))
	assert.NoError(t, err)
}

func TestCallsAllowedNowNilSafe(t *testing.T) {
	withCallsConfig(t, true, []string{"x"})
	assert.False(t, callsAllowedNow("s", nil))
	assert.False(t, callsAllowedNow("s", &whatsmeow.Client{}))
	assert.True(t, callsAllowedNow("x", &whatsmeow.Client{}))
}

func TestMaskingWriter(t *testing.T) {
	var buf bytes.Buffer
	in := `from 5511987654321@s.whatsapp.net to 123456789012345@lid id 1234567 ok`
	n, err := maskingWriter{&buf}.Write([]byte(in))
	require.NoError(t, err)
	assert.Equal(t, len(in), n)
	assert.Equal(t, `from …4321@s.whatsapp.net to …2345@lid id 1234567 ok`, buf.String())

	buf.Reset()
	ssrc := `{"ssrc":3456789012,"msg":"x"}` + "\n"
	_, _ = maskingWriter{&buf}.Write([]byte(ssrc))
	assert.Equal(t, ssrc, buf.String())

	buf.Reset()
	line := `{"peer_lid":"123456789012345@lid","a":"5511987654321:27@s.whatsapp.net","b":"5511987654321@s.whatsapp.net","ssrc":3456789012}`
	_, _ = maskingWriter{&buf}.Write([]byte(line))
	assert.Equal(t, `{"peer_lid":"…2345@lid","a":"…4321:27@s.whatsapp.net","b":"…4321@s.whatsapp.net","ssrc":3456789012}`, buf.String())
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &parsed))
}

func TestMaskSlot(t *testing.T) {
	assert.Equal(t, "…4321:27@s.whatsapp.net", maskSlot("5511987654321:27@s.whatsapp.net"))
	assert.Equal(t, "slot-a", maskSlot("slot-a"))
}

func TestCloseAll(t *testing.T) {
	eng := &fakeEngine{}
	m := newCallManager(eng, callManagerOptions{MaxCalls: 2})
	ringing := &fakeCall{id: "IN-1"}
	eng.incoming(ringing)
	m.closeAll()
	ringing.mu.Lock()
	defer ringing.mu.Unlock()
	assert.True(t, ringing.rejected)
}

func TestHasCallManager(t *testing.T) {
	withCallsConfig(t, true, nil)
	client := &whatsmeow.Client{}
	assert.False(t, hasCallManager(client))
	assert.False(t, hasCallManager(nil))
	callsByClient.Store(client, newCallManager(&fakeEngine{}, callManagerOptions{}))
	t.Cleanup(func() { callsByClient.Delete(client) })
	assert.True(t, hasCallManager(client))
}

func TestSetClientForgetsReplacedClient(t *testing.T) {
	a, b := &whatsmeow.Client{}, &whatsmeow.Client{}
	callsByClient.Store(a, newCallManager(&fakeEngine{}, callManagerOptions{}))
	t.Cleanup(func() { callsByClient.Delete(a) })
	inst := NewDeviceInstance("slot-a", a, nil)
	inst.SetClient(b)
	assert.False(t, hasCallManager(a))
}

func TestMaskDigits(t *testing.T) {
	got := MaskDigits("usync 5511987654321@s.whatsapp.net, id 1234567, lid 123456789012345:7@lid")
	assert.Equal(t, "usync …4321@s.whatsapp.net, id 1234567, lid …2345:7@lid", got)
	assert.NotRegexp(t, `[0-9]{8,}`, MaskDigits("bare 5511987654321 here"))
}

func TestEffectiveCodec(t *testing.T) {
	t.Cleanup(func() { _ = meowcaller.SetCodec("mlow") })
	codec, missing, err := effectiveCodec("mlow")
	require.NoError(t, err)
	assert.Equal(t, "mlow", codec)
	assert.False(t, missing)

	_, _, err = effectiveCodec("bogus")
	assert.Error(t, err)

	codec, missing, err = effectiveCodec("opus")
	require.NoError(t, err)
	if missing {
		assert.Equal(t, "mlow", codec) // !cgo build
	} else {
		assert.Equal(t, "opus", codec)
	}
}

func TestParseCallsLogLevel(t *testing.T) {
	assert.Equal(t, zerolog.DebugLevel, parseCallsLogLevel("debug"))
	assert.Equal(t, zerolog.WarnLevel, parseCallsLogLevel(" WARN "))
	assert.Equal(t, zerolog.ErrorLevel, parseCallsLogLevel("error"))
	assert.Equal(t, zerolog.InfoLevel, parseCallsLogLevel("info"))
	assert.Equal(t, zerolog.InfoLevel, parseCallsLogLevel("bogus"))
	assert.Equal(t, zerolog.InfoLevel, parseCallsLogLevel(""))
}

func TestAttachCallsUsesCallsMedia(t *testing.T) {
	withCallsConfig(t, true, nil)
	orig := callsMedia
	t.Cleanup(func() { callsMedia = orig })
	callsMedia = &fakeMediaFactory{}

	client := &whatsmeow.Client{Store: &store.Device{}}
	attachCalls("slot-a", client)
	t.Cleanup(func() { forgetCalls(client) })
	v, ok := callsByClient.Load(client)
	require.True(t, ok)
	assert.Same(t, callsMedia, v.(*CallManager).opts.Media)
}

func TestNewBridgeFactoryFromConfig(t *testing.T) {
	orig := config.WhatsappCallsRTCGraceSeconds
	t.Cleanup(func() { config.WhatsappCallsRTCGraceSeconds = orig })
	config.WhatsappCallsRTCGraceSeconds = 7
	f := newBridgeFactory(&fakeRTC{})
	assert.Equal(t, 7*time.Second, f.grace)
	assert.Equal(t, config.PathCalls, f.recordDir)
	require.NotNil(t, f.newRecorder)
}

// Fix round 1: a grace below 1 s would disable B10; it is clamped.
func TestNewBridgeFactoryClampsGrace(t *testing.T) {
	orig := config.WhatsappCallsRTCGraceSeconds
	t.Cleanup(func() { config.WhatsappCallsRTCGraceSeconds = orig })
	for _, v := range []int{0, -5} {
		config.WhatsappCallsRTCGraceSeconds = v
		assert.Equal(t, time.Second, newBridgeFactory(&fakeRTC{}).grace)
	}
}
