package whatsapp

import (
	"fmt"
	"sync"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/safego"
	"github.com/sirupsen/logrus"
	"go.mau.fi/whatsmeow"
)

// Fork (elphant): the per-device calls switch at runtime (stage 2b, B7). WHATSAPP_CALLS_ENABLED
// is the master switch. Each device slot's own value lives in the device registry
// (elphant_device_calls); WHATSAPP_CALLS_DEVICES only seeds a slot that has no value yet.

var ErrCallsMasterOff = pkgError.ValidationError("calls are disabled on this gateway (WHATSAPP_CALLS_ENABLED=false)")

// DeviceCallsStore is the device registry part that keeps the per-device calls switch.
type DeviceCallsStore interface {
	SetDeviceCallsEnabled(deviceID string, enabled bool) error
	GetDeviceCallsEnabled(deviceID string) (enabled, found bool, err error)
	// SeedDeviceCallsEnabled stores a first value without overwriting an existing one.
	SeedDeviceCallsEnabled(deviceID string, enabled bool) error
	// SetDeviceCallsConfig stores the switch and, when maxCalls is not nil, the call limit
	// (concurrent calls, D-22, D-23); a nil maxCalls keeps the stored limit.
	SetDeviceCallsConfig(deviceID string, enabled bool, maxCalls *int) error
	// GetDeviceCallsMaxCalls reads the call limit; set is false when it was never stored.
	GetDeviceCallsMaxCalls(deviceID string) (maxCalls int, set bool, err error)
}

// deviceCallsMu serializes the cache-miss path and SetDeviceCalls, so a lookup cannot write
// back a stale value over a concurrent PUT.
var deviceCallsMu sync.Mutex

// callsStoreFn returns the registry that keeps the switch, or nil (tests, no chat storage).
var callsStoreFn = func() DeviceCallsStore {
	dm := GetDeviceManager()
	if dm == nil || dm.storage == nil {
		return nil
	}
	return callsStoreOf(dm.storage)
}

// callsStoreOf finds the switch registry behind a repository. The device manager holds the
// per-device wrapper, which does not carry the fork's calls methods; its base repository does.
func callsStoreOf(repo domainChatStorage.IChatStorageRepository) DeviceCallsStore {
	if w, ok := repo.(*deviceChatStorage); ok && w != nil {
		repo = w.base
	}
	reg, _ := repo.(DeviceCallsStore)
	return reg
}

// resolveCallsSlot maps the path's device id (slot id or JID) to the slot id and its client.
var resolveCallsSlot = func(deviceID string) (string, *whatsmeow.Client, error) {
	inst, slotID, err := GetDeviceManager().ResolveDevice(deviceID)
	if err != nil {
		return "", nil, pkgError.ValidationError(err.Error())
	}
	return slotID, inst.GetClient(), nil
}

var deviceCallsCache sync.Map // slot id -> bool

var deviceMaxCallsCache sync.Map // slot id -> int (stored max_calls, 1 when unset)

func resetDeviceCallsCache() {
	for _, c := range []*sync.Map{&deviceCallsCache, &deviceMaxCallsCache} {
		c.Range(func(k, _ any) bool {
			c.Delete(k)
			return true
		})
	}
}

// forgetDeviceCallsCache drops a slot's cached switch and limit (the slot was removed).
func forgetDeviceCallsCache(slotID string) {
	deviceCallsCache.Delete(slotID)
	deviceMaxCallsCache.Delete(slotID)
}

// deviceMaxCalls returns a slot's stored call limit, 1 when it was never set or reads below 1
// (D-22). A store error falls back to 1 and is not cached.
func deviceMaxCalls(slotID string) int {
	if v, ok := deviceMaxCallsCache.Load(slotID); ok {
		return v.(int)
	}
	deviceCallsMu.Lock()
	defer deviceCallsMu.Unlock()
	if v, ok := deviceMaxCallsCache.Load(slotID); ok {
		return v.(int)
	}
	reg := callsStoreFn()
	if reg == nil {
		return 1
	}
	n, set, err := reg.GetDeviceCallsMaxCalls(slotID)
	if err != nil {
		logrus.Warnf("[CALLS] read the call limit of %s: %v", maskSlot(slotID), err)
		return 1
	}
	if !set || n < 1 {
		n = 1
	}
	deviceMaxCallsCache.Store(slotID, n)
	return n
}

// deviceCallsEnabled reports whether calls are on for a slot now: the master switch, then the
// stored value. A slot without one is seeded once from WHATSAPP_CALLS_DEVICES; before pairing a
// phone-number entry cannot match yet, so that seed is not stored.
func deviceCallsEnabled(slotID, jidUser, jidString string) bool {
	if !config.WhatsappCallsEnabled {
		return false
	}
	if v, ok := deviceCallsCache.Load(slotID); ok {
		return v.(bool)
	}
	deviceCallsMu.Lock()
	defer deviceCallsMu.Unlock()
	if v, ok := deviceCallsCache.Load(slotID); ok {
		return v.(bool)
	}
	seed := config.CallsAllowed(slotID, jidUser, jidString)
	reg := callsStoreFn()
	if reg == nil {
		return seed
	}
	enabled, found, err := reg.GetDeviceCallsEnabled(slotID)
	if err != nil {
		logrus.Warnf("[CALLS] read the calls switch of %s: %v", maskSlot(slotID), err)
		return seed
	}
	if !found {
		if !seed && jidUser == "" && len(config.WhatsappCallsDevices) > 0 {
			return false
		}
		if err := reg.SeedDeviceCallsEnabled(slotID, seed); err != nil {
			logrus.Warnf("[CALLS] seed the calls switch of %s: %v", maskSlot(slotID), err)
			return seed
		}
		if enabled, found, err = reg.GetDeviceCallsEnabled(slotID); err != nil || !found {
			return seed
		}
	}
	deviceCallsCache.Store(slotID, enabled)
	return enabled
}

// DeviceCallsState is what GET and PUT /devices/:device_id/calls report (D-23).
type DeviceCallsState struct {
	Enabled         bool
	MaxCalls        int // stored max_calls, 1 when never set
	MaxCallsCeiling int // WHATSAPP_CALLS_MAX_PER_DEVICE, at least 1
	MaxCallsTotal   int // WHATSAPP_CALLS_MAX_TOTAL, 0: no gateway cap
}

func deviceCallsState(slotID string, enabled bool) DeviceCallsState {
	return DeviceCallsState{
		Enabled:         enabled,
		MaxCalls:        deviceMaxCalls(slotID),
		MaxCallsCeiling: config.CallsMaxPerDeviceCeiling(),
		MaxCallsTotal:   config.CallsMaxTotal(),
	}
}

// ConfigureDeviceCalls switches calls on or off for a device and, when maxCalls is not nil,
// stores its call limit. Off ends the device's live calls; its next offers are left to the phone
// (the offer filter reads the switch on every offer). A new limit applies to the next offer or
// start and never ends a live call (D-24).
func ConfigureDeviceCalls(deviceID string, enabled bool, maxCalls *int) (DeviceCallsState, error) {
	if !config.WhatsappCallsEnabled {
		return DeviceCallsState{}, ErrCallsMasterOff
	}
	ceiling := config.CallsMaxPerDeviceCeiling()
	if maxCalls != nil && (*maxCalls < 1 || *maxCalls > ceiling) {
		return DeviceCallsState{}, pkgError.ValidationError(fmt.Sprintf("max_calls must be between 1 and %d", ceiling))
	}
	slotID, client, err := resolveCallsSlot(deviceID)
	if err != nil {
		return DeviceCallsState{}, err
	}
	reg := callsStoreFn()
	if reg == nil {
		return DeviceCallsState{}, pkgError.InternalServerError("device registry unavailable")
	}
	deviceCallsMu.Lock()
	err = reg.SetDeviceCallsConfig(slotID, enabled, maxCalls)
	if err == nil {
		deviceCallsCache.Store(slotID, enabled)
		if maxCalls != nil {
			deviceMaxCallsCache.Store(slotID, *maxCalls)
		}
	}
	deviceCallsMu.Unlock()
	if err != nil {
		return DeviceCallsState{}, err
	}
	if !enabled {
		endCallsOfSlot(slotID, client)
	}
	state := deviceCallsState(slotID, enabled) // after the unlock: deviceMaxCalls takes deviceCallsMu
	logrus.Infof("[CALLS] calls switched %s for device %s (max_calls %d, ceiling %d)", onOff(enabled), maskSlot(slotID), state.MaxCalls, state.MaxCallsCeiling)
	return state, nil
}

// SetDeviceCalls switches calls on or off and keeps the stored call limit.
func SetDeviceCalls(deviceID string, enabled bool) (bool, error) {
	state, err := ConfigureDeviceCalls(deviceID, enabled, nil)
	return state.Enabled, err
}

// DeviceCallsSettings reports a device's switch and limits right now.
func DeviceCallsSettings(deviceID string) (DeviceCallsState, error) {
	slotID, client, err := resolveCallsSlot(deviceID)
	if err != nil {
		return DeviceCallsState{}, err
	}
	return deviceCallsState(slotID, callsAllowedNow(slotID, client)), nil
}

// DeviceCalls reports whether calls are on for a device right now.
func DeviceCalls(deviceID string) (bool, error) {
	state, err := DeviceCallsSettings(deviceID)
	return state.Enabled, err
}

// endCallsOfSlot ends the live calls of the slot's client. A manager is matched by that
// client, or by the slot it currently resolves to (the startup client was attached under its
// AD JID and later moved to a registry slot).
func endCallsOfSlot(slotID string, client *whatsmeow.Client) {
	callsByClient.Range(func(k, v any) bool {
		c := k.(*whatsmeow.Client)
		if m := v.(*CallManager); (client != nil && c == client) || callsSlotOf(c, m.slotID) == slotID {
			safego.Go("calls_switch_off", m.closeAll)
		}
		return true
	})
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}
