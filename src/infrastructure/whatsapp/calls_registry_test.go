package whatsapp

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
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
	oe, od := config.WhatsappCallsEnabled, config.WhatsappCallsDevices
	t.Cleanup(func() { config.WhatsappCallsEnabled, config.WhatsappCallsDevices = oe, od })
	config.WhatsappCallsEnabled, config.WhatsappCallsDevices = enabled, devices
}

func TestAttachCallsNotListed(t *testing.T) {
	withCallsConfig(t, true, []string{"other"})
	client := &whatsmeow.Client{}
	attachCalls("slot-a", client)
	_, ok := callsByClient.Load(client)
	assert.False(t, ok)
	inst := NewDeviceInstance("slot-a", client, nil)
	_, err := CallsFromContext(ContextWithDevice(context.Background(), inst))
	assert.ErrorIs(t, err, ErrCallsOff)
}

func TestWarnCallsNeedRestart(t *testing.T) {
	withCallsConfig(t, true, []string{"5511987654321"})
	client := &whatsmeow.Client{Store: &store.Device{ID: &types.JID{User: "5511987654321", Server: types.DefaultUserServer}}}
	assert.True(t, warnCallsNeedRestart("slot-a", client))

	callsByClient.Store(client, newCallManager(&fakeEngine{}, callManagerOptions{}))
	t.Cleanup(func() { callsByClient.Delete(client) })
	assert.False(t, warnCallsNeedRestart("slot-a", client))

	assert.False(t, warnCallsNeedRestart("slot-a", &whatsmeow.Client{}))
	withCallsConfig(t, false, nil)
	assert.False(t, warnCallsNeedRestart("slot-a", &whatsmeow.Client{Store: &store.Device{ID: &types.JID{User: "1", Server: "s.whatsapp.net"}}}))
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
