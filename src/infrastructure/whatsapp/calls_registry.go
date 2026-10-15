package whatsapp

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/safego"
	meowcaller "github.com/purpshell/meowcaller"
	"github.com/sirupsen/logrus"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types/events"
)

// callsByClient maps a *whatsmeow.Client to its CallManager. It is keyed by client,
// not by slot, because a slot gets a new client after logout and re-pair, and the
// default instance shares its client with a registry slot.
var callsByClient sync.Map

var (
	setCodecOnce       sync.Once
	effectiveCodecName = "mlow"
)

// callsAllowedNow reports whether calls are allowed for client right now. The phone
// number and JID are read from the store at call time, so a device paired after start
// is covered without a restart.
func callsAllowedNow(slotID string, client *whatsmeow.Client) bool {
	user, jid := "", ""
	if client != nil && client.Store != nil && client.Store.ID != nil {
		user, jid = client.Store.ID.User, client.Store.ID.String()
	}
	return deviceCallsEnabled(slotID, user, jid) // Fork (elphant): per-device switch (stage 2b)
}

// offerFilter gates inbound offers in the call engine: a false return makes it ignore the
// offer completely, so the phone rings as usual. Group calls are out of scope and always left
// to the phone.
func offerFilter(slotID string, client *whatsmeow.Client) func(*events.CallOffer) bool {
	return func(evt *events.CallOffer) bool {
		if evt != nil && !evt.GroupJID.IsEmpty() {
			return false
		}
		return callsAllowedNow(slotID, client)
	}
}

// effectiveCodec applies the configured codec and returns the one in effect and whether
// opus was wanted but unavailable. Builds without cgo cannot do opus and stay on mlow.
func effectiveCodec(want string) (codec string, opusMissing bool, err error) {
	err = meowcaller.SetCodec(want)
	if errors.Is(err, meowcaller.ErrOpusUnavailable) {
		return "mlow", true, nil
	}
	if err != nil {
		return "", false, err
	}
	return want, false, nil
}

// attachCalls installs a call manager on client when calls are enabled. It must run
// before client.Connect: meowcaller installs raw call handlers that must see the first
// stanza. Per-device permission is checked at use time (offer filter, CallsFromContext).
func attachCalls(slotID string, client *whatsmeow.Client) {
	if client == nil || !config.WhatsappCallsEnabled {
		return
	}
	setCodecOnce.Do(func() {
		codec, missing, err := effectiveCodec(config.WhatsappCallsCodec)
		if err != nil {
			logrus.Errorf("[CALLS] %v; using opus", err)
			codec, missing, _ = effectiveCodec("opus")
		}
		if missing {
			logrus.Warn("[CALLS] opus unavailable in this build; using mlow")
		}
		effectiveCodecName = codec
	})
	m := newCallManager(newMeowEngine(client, offerFilter(slotID, client)), callManagerOptions{
		MaxCalls:    config.WhatsappCallsMaxPerDevice,
		RingWindow:  time.Minute,
		MaxDuration: time.Duration(config.WhatsappCallsMaxDurationMinutes) * time.Minute,
		Media:       callsMedia,
		Emit:        callEventSender(client),
	})
	m.slotID = slotID
	callsByClient.Store(client, m)
	logrus.Infof("[CALLS] calls enabled for device %s (codec %s, early media %s)", maskSlot(slotID), effectiveCodecName, parseCallsEarlyMedia(config.WhatsappCallsEarlyMedia))
}

// forgetCalls drops the manager of a client that is being discarded and ends its live
// calls in the background (handle calls may do network I/O, and callers hold locks).
func forgetCalls(client *whatsmeow.Client) {
	if client == nil {
		return
	}
	if v, ok := callsByClient.LoadAndDelete(client); ok {
		safego.Go("calls_close", v.(*CallManager).closeAll)
	}
}

// CallsFromContext returns the call manager of the request's device.
func CallsFromContext(ctx context.Context) (*CallManager, error) {
	client := ClientFromContext(ctx)
	if client == nil {
		return nil, ErrCallsOff
	}
	if v, ok := callsByClient.Load(client); ok {
		m := v.(*CallManager)
		if callsAllowedNow(m.slotID, client) {
			return m, nil
		}
	}
	return nil, ErrCallsOff
}

// hasCallManager reports whether client has a call manager (calls enabled for it).
func hasCallManager(client *whatsmeow.Client) bool {
	if client == nil {
		return false
	}
	v, ok := callsByClient.Load(client)
	return ok && callsAllowedNow(v.(*CallManager).slotID, client)
}
