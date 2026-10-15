package whatsapp

import (
	"testing"
	"time"

	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// The startup client is attached under its AD JID (InitWaCLI) and then moved to its registry
// slot (loadFromRegistry). The switch is keyed by the registry slot, and every reader must
// follow the client there.
func TestCallsSwitchFollowsTheStartupClientToItsRegistrySlot(t *testing.T) {
	withCallsConfig(t, true, nil)
	reg := newFakeCallsStore()
	withCallsStore(t, reg)
	dm := NewDeviceManager(nil, nil, nil)
	withDeviceManager(t, dm)

	ad := types.NewADJID("5511900000001", 0, 3)
	client := &whatsmeow.Client{Store: &store.Device{ID: &ad}}
	adSlot := ad.String()
	dm.EnsureDefault(NewDeviceInstance(adSlot, client, nil))

	// As attachCalls(adSlot, client) does, with a fake engine to hold live calls.
	eng := &fakeEngine{}
	m := newCallManager(eng, callManagerOptions{MaxCalls: 1})
	m.slotID = adSlot
	callsByClient.Store(client, m)
	t.Cleanup(func() { callsByClient.Delete(client) })
	filter := offerFilter(adSlot, client)

	const ulid = "01JTESTSLOT0000000000000000"
	dm.loadFromRegistry([]*domainChatStorage.DeviceRecord{{DeviceID: ulid, JID: ad.ToNonAD().String(), ADJID: adSlot}})
	_, stillThere := dm.GetDevice(adSlot)
	require.False(t, stillThere, "the AD JID instance is replaced by the registry slot")
	require.Equal(t, ulid, dm.slotIDOfClient(client))

	in := &fakeCall{id: "IN-1"}
	eng.incoming(in)
	require.NoError(t, m.Answer("IN-1", ""))

	assert.True(t, filter(&events.CallOffer{}))
	assert.True(t, hasCallManager(client))

	on, err := SetDeviceCalls(ulid, false)
	require.NoError(t, err)
	assert.False(t, on)
	assert.False(t, filter(&events.CallOffer{}), "the offer filter reads the registry slot's switch")
	assert.False(t, hasCallManager(client))
	got, err := DeviceCalls(ulid)
	require.NoError(t, err)
	assert.False(t, got)
	assert.Eventually(t, func() bool {
		in.mu.Lock()
		defer in.mu.Unlock()
		return in.hungUp
	}, time.Second, 10*time.Millisecond, "switch-off ends the moved client's live calls")
	_, adKey := reg.snapshot()[adSlot]
	assert.False(t, adKey, "no switch row under the AD JID")

	_, err = SetDeviceCalls(ulid, true)
	require.NoError(t, err)
	assert.True(t, filter(&events.CallOffer{}))
	assert.True(t, hasCallManager(client))
	assert.Equal(t, map[string]bool{ulid: true}, reg.snapshot())
}

func TestRemoveDeviceDropsItsCallsSwitchCache(t *testing.T) {
	withCallsConfig(t, true, nil)
	withCallsStore(t, newFakeCallsStore())
	dm := NewDeviceManager(nil, nil, nil)
	dm.AddDevice(NewDeviceInstance("slot-a", nil, nil))
	assert.True(t, deviceCallsEnabled("slot-a", "", ""))
	_, cached := deviceCallsCache.Load("slot-a")
	require.True(t, cached)

	dm.RemoveDevice("slot-a")
	_, cached = deviceCallsCache.Load("slot-a")
	assert.False(t, cached)
}

// whatsmeow clears Store.ID on logout before the client is reset and its calls end; call.ended
// keeps the device_id the call started with.
func TestCallEndedKeepsDeviceIDAfterLogout(t *testing.T) {
	sink := &callWebhookSink{}
	stubCallWebhook(t, sink.forward)
	ad := types.NewADJID("5511900000001", 0, 3)
	client := &whatsmeow.Client{Store: &store.Device{ID: &ad}}

	send := callEventSender(client)
	send(CallEvent{Type: EventCallStatus, Call: CallInfo{CallID: "LOGOUT-1"}, Status: "RINGING", At: time.Now()})
	client.Store.ID = nil
	send(CallEvent{Type: EventCallEnded, Call: CallInfo{CallID: "LOGOUT-1"}, Status: "COMPLETED", At: time.Now()})

	deviceIDOf := func(event string) any {
		sink.mu.Lock()
		defer sink.mu.Unlock()
		for _, b := range sink.bodies {
			if b["event"] == event && b["payload"].(map[string]any)["call_id"] == "LOGOUT-1" {
				return b["device_id"]
			}
		}
		return nil
	}
	require.Eventually(t, func() bool { return deviceIDOf(EventCallEnded) != nil }, 2*time.Second, 5*time.Millisecond)
	assert.Equal(t, ad.ToNonAD().String(), deviceIDOf(EventCallStatus))
	assert.Equal(t, ad.ToNonAD().String(), deviceIDOf(EventCallEnded))
}
