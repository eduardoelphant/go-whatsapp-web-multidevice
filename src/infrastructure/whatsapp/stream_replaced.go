package whatsapp

import (
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
func handleStreamReplaced(instance *DeviceInstance) {
	logrus.Warnf("[STREAM_REPLACED] Device %s was opened elsewhere; it stays disconnected until reconnected", instance.ID())
	instance.MarkStreamReplaced()
	instance.SetState(domainDevice.DeviceStateDisconnected)

	websocket.Broadcast <- websocket.BroadcastMessage{
		Code:    "DEVICE_STREAM_REPLACED",
		Message: "Device session was opened elsewhere; reconnect to resume",
		Result:  map[string]string{"device_id": instance.ID()},
	}
}

// ShouldAutoReconnect reports whether a background reconnect loop may reconnect cli.
// It is false only for a device whose session was taken over elsewhere, so the
// loop does not fight the other holder of the credentials.
func ShouldAutoReconnect(cli *whatsmeow.Client) bool {
	dm := GetDeviceManager()
	if dm == nil || cli == nil {
		return true
	}
	for _, instance := range dm.ListDevices() {
		if instance.GetClient() == cli {
			return !instance.StreamReplaced()
		}
	}
	return true
}
