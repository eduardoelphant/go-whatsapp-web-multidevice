package whatsapp

import (
	"sync"
	"testing"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

type fakeCallsStore struct {
	mu   sync.Mutex
	rows map[string]bool
}

func newFakeCallsStore() *fakeCallsStore { return &fakeCallsStore{rows: map[string]bool{}} }

func (s *fakeCallsStore) SetDeviceCallsEnabled(id string, on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[id] = on
	return nil
}

func (s *fakeCallsStore) GetDeviceCallsEnabled(id string) (bool, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	on, ok := s.rows[id]
	return on, ok, nil
}

func (s *fakeCallsStore) SeedDeviceCallsEnabled(id string, on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.rows[id]; !ok {
		s.rows[id] = on
	}
	return nil
}

func (s *fakeCallsStore) snapshot() map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]bool{}
	for k, v := range s.rows {
		out[k] = v
	}
	return out
}

// withCallsStore must run after withCallsConfig (cleanups run in reverse order).
func withCallsStore(t *testing.T, reg DeviceCallsStore) {
	orig := callsStoreFn
	callsStoreFn = func() DeviceCallsStore { return reg }
	resetDeviceCallsCache()
	t.Cleanup(func() { callsStoreFn = orig; resetDeviceCallsCache() })
}

func withResolvedSlot(t *testing.T, slotID string, client *whatsmeow.Client) {
	orig := resolveCallsSlot
	resolveCallsSlot = func(string) (string, *whatsmeow.Client, error) { return slotID, client, nil }
	t.Cleanup(func() { resolveCallsSlot = orig })
}

func TestDeviceCallsSeededOnceFromEnv(t *testing.T) {
	withCallsConfig(t, true, []string{"slot-a"})
	reg := newFakeCallsStore()
	withCallsStore(t, reg)

	assert.True(t, deviceCallsEnabled("slot-a", "", ""))
	assert.False(t, deviceCallsEnabled("slot-b", "5511900000001", "5511900000001:1@s.whatsapp.net"))
	assert.Equal(t, map[string]bool{"slot-a": true, "slot-b": false}, reg.snapshot())

	// A restart with another list: the stored values win.
	config.WhatsappCallsDevices = []string{"slot-b"}
	resetDeviceCallsCache()
	assert.True(t, deviceCallsEnabled("slot-a", "", ""))
	assert.False(t, deviceCallsEnabled("slot-b", "5511900000001", "5511900000001:1@s.whatsapp.net"))
}

func TestDeviceCallsNotSeededBeforePairing(t *testing.T) {
	withCallsConfig(t, true, []string{"5511987654321"})
	reg := newFakeCallsStore()
	withCallsStore(t, reg)

	assert.False(t, deviceCallsEnabled("slot-a", "", ""))
	assert.Empty(t, reg.snapshot(), "an unpaired device must not persist a seed")
	assert.True(t, deviceCallsEnabled("slot-a", "5511987654321", "5511987654321:3@s.whatsapp.net"))
	assert.Equal(t, map[string]bool{"slot-a": true}, reg.snapshot())
}

func TestDeviceCallsMasterSwitch(t *testing.T) {
	withCallsConfig(t, false, nil)
	reg := newFakeCallsStore()
	reg.rows["slot-a"] = true
	withCallsStore(t, reg)

	assert.False(t, deviceCallsEnabled("slot-a", "", ""))
	_, err := SetDeviceCalls("slot-a", true)
	assert.ErrorIs(t, err, ErrCallsMasterOff)
}

func TestSetDeviceCallsAppliesAtOnce(t *testing.T) {
	withCallsConfig(t, true, nil)
	reg := newFakeCallsStore()
	withCallsStore(t, reg)
	client := &whatsmeow.Client{Store: &store.Device{}}
	withResolvedSlot(t, "slot-a", client)

	filter := offerFilter("slot-a", client)
	assert.True(t, filter(&events.CallOffer{}))

	on, err := SetDeviceCalls("slot-a", false)
	require.NoError(t, err)
	assert.False(t, on)
	assert.False(t, filter(&events.CallOffer{}), "a disabled device's offers are left to the phone")
	got, err := DeviceCalls("slot-a")
	require.NoError(t, err)
	assert.False(t, got)
	assert.Equal(t, map[string]bool{"slot-a": false}, reg.snapshot())

	_, err = SetDeviceCalls("slot-a", true)
	require.NoError(t, err)
	assert.True(t, filter(&events.CallOffer{}))
}

func TestSwitchOffEndsLiveCalls(t *testing.T) {
	withCallsConfig(t, true, nil)
	withCallsStore(t, newFakeCallsStore())
	client := &whatsmeow.Client{}
	withResolvedSlot(t, "slot-a", client)

	eng := &fakeEngine{}
	m := newCallManager(eng, callManagerOptions{MaxCalls: 1})
	m.slotID = "slot-a"
	callsByClient.Store(client, m)
	t.Cleanup(func() { callsByClient.Delete(client) })
	in := &fakeCall{id: "IN-1"}
	eng.incoming(in)
	require.NoError(t, m.Answer("IN-1", ""))

	_, err := SetDeviceCalls("slot-a", false)
	require.NoError(t, err)
	assert.Eventually(t, func() bool {
		in.mu.Lock()
		defer in.mu.Unlock()
		return in.hungUp
	}, time.Second, 10*time.Millisecond)
}

func TestOfferFilterLeavesGroupCalls(t *testing.T) {
	withCallsConfig(t, true, nil)
	filter := offerFilter("slot-a", &whatsmeow.Client{})
	assert.True(t, filter(&events.CallOffer{}))
	group := &events.CallOffer{}
	group.GroupJID = types.NewJID("120363000000000000", types.GroupServer)
	assert.False(t, filter(group))
}

func TestSwitchOffRacesLookups(t *testing.T) {
	withCallsConfig(t, true, nil)
	reg := newFakeCallsStore()
	withCallsStore(t, reg)
	withResolvedSlot(t, "slot-a", &whatsmeow.Client{})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				deviceCallsEnabled("slot-a", "", "")
			}
		}()
	}
	_, err := SetDeviceCalls("slot-a", false)
	require.NoError(t, err)
	wg.Wait()

	v, ok := deviceCallsCache.Load("slot-a")
	require.True(t, ok)
	assert.Equal(t, false, v)
	assert.Equal(t, map[string]bool{"slot-a": false}, reg.snapshot())
}
