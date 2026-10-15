package whatsapp

import (
	"sync"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/safego"
	"github.com/sirupsen/logrus"
	"go.mau.fi/whatsmeow"
)

// Fork (elphant): the per-device calls switch at runtime (stage 2b, B7). WHATSAPP_CALLS_ENABLED
// is the master switch. Each device slot's own value lives in the device registry
// (elphant_device_calls); WHATSAPP_CALLS_DEVICES only seeds a slot that has no value yet.

var ErrCallsMasterOff = pkgError.ValidationError("calls are disabled on this gateway (WHATSAPP_CALLS_ENABLED=false)")

type deviceCallsStore interface {
	SetDeviceCallsEnabled(deviceID string, enabled bool) error
	GetDeviceCallsEnabled(deviceID string) (enabled, found bool, err error)
}

// callsStoreFn returns the registry that keeps the switch, or nil (tests, no chat storage).
var callsStoreFn = func() deviceCallsStore {
	dm := GetDeviceManager()
	if dm == nil || dm.storage == nil {
		return nil
	}
	reg, _ := dm.storage.(deviceCallsStore)
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

func resetDeviceCallsCache() {
	deviceCallsCache.Range(func(k, _ any) bool {
		deviceCallsCache.Delete(k)
		return true
	})
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
		enabled = seed
		if err := reg.SetDeviceCallsEnabled(slotID, enabled); err != nil {
			logrus.Warnf("[CALLS] seed the calls switch of %s: %v", maskSlot(slotID), err)
			return enabled
		}
	}
	deviceCallsCache.Store(slotID, enabled)
	return enabled
}

// SetDeviceCalls switches calls on or off for a device. Off ends the device's live calls; its
// next offers are left to the phone (the offer filter reads the switch on every offer).
func SetDeviceCalls(deviceID string, enabled bool) (bool, error) {
	if !config.WhatsappCallsEnabled {
		return false, ErrCallsMasterOff
	}
	slotID, _, err := resolveCallsSlot(deviceID)
	if err != nil {
		return false, err
	}
	reg := callsStoreFn()
	if reg == nil {
		return false, pkgError.InternalServerError("device registry unavailable")
	}
	if err := reg.SetDeviceCallsEnabled(slotID, enabled); err != nil {
		return false, err
	}
	deviceCallsCache.Store(slotID, enabled)
	if !enabled {
		endCallsOfSlot(slotID)
	}
	logrus.Infof("[CALLS] calls switched %s for device %s", onOff(enabled), maskSlot(slotID))
	return enabled, nil
}

// DeviceCalls reports whether calls are on for a device right now.
func DeviceCalls(deviceID string) (bool, error) {
	slotID, client, err := resolveCallsSlot(deviceID)
	if err != nil {
		return false, err
	}
	return callsAllowedNow(slotID, client), nil
}

func endCallsOfSlot(slotID string) {
	callsByClient.Range(func(_, v any) bool {
		if m := v.(*CallManager); m.slotID == slotID {
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
