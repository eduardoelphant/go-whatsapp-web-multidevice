package whatsapp

import (
	"context"
	"testing"

	domainDevice "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/device"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types/events"
)

func TestHandlerStreamReplacedMarksOnlyAffectedDevice(t *testing.T) {
	affected := NewDeviceInstance("replaced-a", nil, nil)
	other := NewDeviceInstance("replaced-b", nil, nil)

	go handler(context.Background(), affected, &events.StreamReplaced{})

	msg := recvBroadcast(t)
	if msg.Code != "DEVICE_STREAM_REPLACED" {
		t.Fatalf("broadcast code = %s, want DEVICE_STREAM_REPLACED", msg.Code)
	}
	result, _ := msg.Result.(map[string]string)
	if result["device_id"] != "replaced-a" {
		t.Fatalf("broadcast device_id = %q, want replaced-a", result["device_id"])
	}
	if !affected.StreamReplaced() {
		t.Fatal("affected device should be marked stream replaced")
	}
	if affected.State() != domainDevice.DeviceStateDisconnected {
		t.Fatalf("affected state = %s, want disconnected", affected.State())
	}
	if other.StreamReplaced() {
		t.Fatal("other device must not be marked")
	}
}

func TestHandlerConnectedClearsStreamReplaced(t *testing.T) {
	instance := NewDeviceInstance("replaced-c", nil, nil)
	instance.MarkStreamReplaced()

	handler(context.Background(), instance, &events.PushNameSetting{})
	if !instance.StreamReplaced() {
		t.Fatal("PushNameSetting must not clear the stream replaced mark")
	}

	handler(context.Background(), instance, &events.Connected{})
	if instance.StreamReplaced() {
		t.Fatal("Connected should clear the stream replaced mark")
	}
}

// withDeviceManager swaps the global device manager for the duration of a test.
func withDeviceManager(t *testing.T, m *DeviceManager) {
	t.Helper()
	globalStateMu.Lock()
	previous := deviceManager
	deviceManager = m
	globalStateMu.Unlock()
	t.Cleanup(func() {
		globalStateMu.Lock()
		deviceManager = previous
		globalStateMu.Unlock()
	})
}

func TestShouldAutoReconnect(t *testing.T) {
	marked := &whatsmeow.Client{}
	unmarked := &whatsmeow.Client{}
	unknown := &whatsmeow.Client{}

	markedInstance := NewDeviceInstance("auto-marked", marked, nil)
	markedInstance.MarkStreamReplaced()
	manager := NewDeviceManager(nil, nil, nil)
	manager.AddDevice(markedInstance)
	manager.AddDevice(NewDeviceInstance("auto-unmarked", unmarked, nil))
	withDeviceManager(t, manager)

	if ShouldAutoReconnect(marked) {
		t.Error("marked device must not auto-reconnect")
	}
	if !ShouldAutoReconnect(unmarked) {
		t.Error("unmarked device should auto-reconnect")
	}
	if !ShouldAutoReconnect(unknown) {
		t.Error("client without an instance should keep auto-reconnecting")
	}
	if !ShouldAutoReconnect(nil) {
		t.Error("nil client should report true (no instance to block)")
	}

	withDeviceManager(t, nil)
	if !ShouldAutoReconnect(marked) {
		t.Error("without a device manager every client should auto-reconnect")
	}
}
