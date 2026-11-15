package whatsapp

import (
	"sync"
	"testing"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

type fakeCallsStore struct {
	mu     sync.Mutex
	rows   map[string]bool
	limits map[string]int
}

func newFakeCallsStore() *fakeCallsStore {
	return &fakeCallsStore{rows: map[string]bool{}, limits: map[string]int{}}
}

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

// registryRepo is a chat repository that also keeps the calls switch, like SQLiteRepository.
type registryRepo struct {
	domainChatStorage.IChatStorageRepository
	*fakeCallsStore
}

func TestCallsStoreOfUnwrapsTheDeviceWrapper(t *testing.T) {
	base := registryRepo{fakeCallsStore: newFakeCallsStore()}
	wrapped := newDeviceChatStorage("slot-1", base)
	if callsStoreOf(wrapped) == nil {
		t.Fatal("the device manager's per-device wrapper must expose the base registry")
	}
	if callsStoreOf(base) == nil {
		t.Fatal("a bare registry must be used as is")
	}
	if callsStoreOf(nil) != nil {
		t.Fatal("no repository means no registry")
	}
}

func (s *fakeCallsStore) SetDeviceCallsConfig(id string, on bool, maxCalls *int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[id] = on
	if maxCalls != nil {
		s.limits[id] = *maxCalls
	}
	return nil
}

func (s *fakeCallsStore) GetDeviceCallsMaxCalls(id string) (int, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.limits[id]
	return n, ok, nil
}

func intPtr(v int) *int { return &v }

// withCallsLimits sets the two env limits for one test.
func withCallsLimits(t *testing.T, ceiling, total int) {
	per, tot := config.WhatsappCallsMaxPerDevice, config.WhatsappCallsMaxTotal
	t.Cleanup(func() { config.WhatsappCallsMaxPerDevice, config.WhatsappCallsMaxTotal = per, tot })
	config.WhatsappCallsMaxPerDevice, config.WhatsappCallsMaxTotal = ceiling, total
}

func TestConfigureDeviceCallsStoresTheLimit(t *testing.T) {
	withCallsConfig(t, true, nil)
	withCallsLimits(t, 3, 6)
	reg := newFakeCallsStore()
	withCallsStore(t, reg)
	withResolvedSlot(t, "slot-a", &whatsmeow.Client{Store: &store.Device{}})

	got, err := DeviceCallsSettings("slot-a")
	require.NoError(t, err)
	assert.Equal(t, DeviceCallsState{Enabled: true, MaxCalls: 1, MaxCallsCeiling: 3, MaxCallsTotal: 6}, got, "never set reads 1")

	got, err = ConfigureDeviceCalls("slot-a", true, intPtr(3))
	require.NoError(t, err)
	assert.Equal(t, DeviceCallsState{Enabled: true, MaxCalls: 3, MaxCallsCeiling: 3, MaxCallsTotal: 6}, got)
	assert.Equal(t, 3, deviceMaxCalls("slot-a"))

	got, err = ConfigureDeviceCalls("slot-a", true, nil) // omitted: unchanged
	require.NoError(t, err)
	assert.Equal(t, 3, got.MaxCalls)

	got, err = ConfigureDeviceCalls("slot-a", false, intPtr(2)) // both stored
	require.NoError(t, err)
	assert.False(t, got.Enabled)
	assert.Equal(t, 2, got.MaxCalls)
	assert.Equal(t, 2, reg.limits["slot-a"])
}

func TestConfigureDeviceCallsValidatesTheRange(t *testing.T) {
	withCallsConfig(t, true, nil)
	withCallsLimits(t, 3, 0)
	reg := newFakeCallsStore()
	withCallsStore(t, reg)
	withResolvedSlot(t, "slot-a", &whatsmeow.Client{Store: &store.Device{}})

	for _, v := range []int{0, -1, 4} {
		_, err := ConfigureDeviceCalls("slot-a", true, intPtr(v))
		require.Error(t, err, "max_calls %d", v)
		assert.Equal(t, "max_calls must be between 1 and 3", err.Error())
		var ge pkgError.GenericError
		require.ErrorAs(t, err, &ge)
		assert.Equal(t, 400, ge.StatusCode())
	}
	assert.Empty(t, reg.snapshot(), "a refused value stores nothing")

	config.WhatsappCallsEnabled = false
	_, err := ConfigureDeviceCalls("slot-a", true, intPtr(9))
	assert.ErrorIs(t, err, ErrCallsMasterOff, "the master switch is checked first")
}

func TestDeviceMaxCallsClampsAndCaches(t *testing.T) {
	withCallsConfig(t, true, nil)
	reg := newFakeCallsStore()
	reg.limits["slot-bad"] = 0
	withCallsStore(t, reg)
	assert.Equal(t, 1, deviceMaxCalls("slot-none"))
	assert.Equal(t, 1, deviceMaxCalls("slot-bad"), "a stored value below 1 reads 1")

	reg.limits["slot-none"] = 3 // written behind the cache's back
	assert.Equal(t, 1, deviceMaxCalls("slot-none"), "cached")
	forgetDeviceCallsCache("slot-none")
	assert.Equal(t, 3, deviceMaxCalls("slot-none"))
}
