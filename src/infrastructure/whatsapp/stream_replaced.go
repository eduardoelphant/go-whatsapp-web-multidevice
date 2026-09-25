package whatsapp

import (
	"sync"

	domainDevice "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/device"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/ui/websocket"
	"github.com/sirupsen/logrus"
	"go.mau.fi/whatsmeow"
)

// Fork (elphant): a StreamReplaced event means the same credentials connected from
// another process. Upstream exits the whole process, which takes every other device
// down and, under a restart policy, starts a reconnect fight with the other holder.
// Here only the affected device stops. whatsmeow already treats the event as an
// expected disconnect and does not reconnect by itself; the device stays down until
// an operator reconnects it (or the process restarts).

// streamReplacedClients holds the clients whose session was opened elsewhere and
// that have not connected since. It is keyed by client, not by DeviceInstance: at
// startup the default client's event handler stays bound to the instance InitWaCLI
// created, while the manager moves that client into the named slot's instance
// (loadFromRegistry), so the two sides would see different instances.
var streamReplacedClients sync.Map // *whatsmeow.Client -> struct{}

func handleStreamReplaced(instance *DeviceInstance) {
	instance = canonicalInstance(instance)
	logrus.Warnf("[STREAM_REPLACED] Device %s was opened elsewhere; it stays disconnected until reconnected", instance.ID())
	markStreamReplaced(instance.GetClient())
	instance.SetState(domainDevice.DeviceStateDisconnected)

	websocket.Broadcast <- websocket.BroadcastMessage{
		Code:    "DEVICE_STREAM_REPLACED",
		Message: "Device session was opened elsewhere; reconnect to resume",
		Result:  map[string]string{"device_id": instance.ID()},
	}
}

func markStreamReplaced(cli *whatsmeow.Client) {
	if cli != nil {
		streamReplacedClients.Store(cli, struct{}{})
	}
}

// clearStreamReplaced lets the reconnect checker handle cli again. Called on
// every successful connection.
func clearStreamReplaced(cli *whatsmeow.Client) {
	if cli != nil {
		streamReplacedClients.Delete(cli)
	}
}

// ShouldAutoReconnect reports whether a background reconnect loop may reconnect cli.
// It is false only for a client whose session was taken over elsewhere, so the
// loop does not fight the other holder of the credentials.
func ShouldAutoReconnect(cli *whatsmeow.Client) bool {
	if cli == nil {
		return true
	}
	_, replaced := streamReplacedClients.Load(cli)
	return !replaced
}

// canonicalInstance returns the device manager's instance that holds the same
// client as instance (the named slot), or instance itself when there is none.
func canonicalInstance(instance *DeviceInstance) *DeviceInstance {
	client := instance.GetClient()
	dm := GetDeviceManager()
	if client == nil || dm == nil {
		return instance
	}
	for _, candidate := range dm.ListDevices() {
		if candidate.GetClient() == client {
			return candidate
		}
	}
	return instance
}
