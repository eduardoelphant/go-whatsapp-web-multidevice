package whatsapp

import (
	"context"
	"sync"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/safego"
	meowcaller "github.com/purpshell/meowcaller"
	"github.com/sirupsen/logrus"
	"go.mau.fi/whatsmeow"
)

// callsByClient maps a *whatsmeow.Client to its CallManager. It is keyed by client,
// not by slot, because a slot gets a new client after logout and re-pair, and the
// default instance shares its client with a registry slot.
var callsByClient sync.Map

var setCodecOnce sync.Once

// attachCalls installs a call manager on client when calls are allowed for it. It must
// run before client.Connect: meowcaller installs raw call handlers that must see the
// first stanza.
func attachCalls(slotID string, client *whatsmeow.Client) {
	if client == nil {
		return
	}
	user, jid := "", ""
	if client.Store != nil && client.Store.ID != nil {
		user, jid = client.Store.ID.User, client.Store.ID.String()
	}
	if !config.CallsAllowed(slotID, user, jid) {
		return
	}
	setCodecOnce.Do(func() {
		if err := meowcaller.SetCodec(config.WhatsappCallsCodec); err != nil {
			logrus.Errorf("[CALLS] %v; using opus", err)
			_ = meowcaller.SetCodec("opus")
		}
	})
	m := newCallManager(newMeowEngine(client), callManagerOptions{
		MaxCalls:   config.WhatsappCallsMaxPerDevice,
		RingWindow: time.Minute,
		TestAudio:  config.WhatsappCallsTestAudio,
		RecordDir:  config.PathCalls,
	})
	callsByClient.Store(client, m)
	logrus.Infof("[CALLS] calls enabled for device %s (codec %s)", maskSlot(slotID), config.WhatsappCallsCodec)
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

// warnCallsNeedRestart warns when calls are allowed for a connected device that has no
// manager, which happens when it was paired after start. It reports whether it warned.
func warnCallsNeedRestart(slotID string, client *whatsmeow.Client) bool {
	if client == nil || client.Store == nil || client.Store.ID == nil {
		return false
	}
	if !config.CallsAllowed(slotID, client.Store.ID.User, client.Store.ID.String()) {
		return false
	}
	if _, ok := callsByClient.Load(client); ok {
		return false
	}
	logrus.Warnf("[CALLS] calls are allowed for device %s but it was paired after start; restart GOWA to enable calls", maskSlot(slotID))
	return true
}

// CallsFromContext returns the call manager of the request's device.
func CallsFromContext(ctx context.Context) (*CallManager, error) {
	client := ClientFromContext(ctx)
	if client == nil {
		return nil, ErrCallsOff
	}
	if v, ok := callsByClient.Load(client); ok {
		return v.(*CallManager), nil
	}
	return nil, ErrCallsOff
}
