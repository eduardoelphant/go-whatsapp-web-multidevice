package whatsapp

import (
	"context"
	"testing"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mau.fi/whatsmeow"
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
