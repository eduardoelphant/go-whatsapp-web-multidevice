package whatsapp

import (
	"context"
	"testing"

	domainDevice "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/device"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types/events"
)

func TestHandlerStreamReplacedMarksOnlyAffectedDevice(t *testing.T) {
	affectedClient := &whatsmeow.Client{}
	otherClient := &whatsmeow.Client{}
	affected := NewDeviceInstance("replaced-a", affectedClient, nil)
	NewDeviceInstance("replaced-b", otherClient, nil)
	t.Cleanup(func() { clearStreamReplaced(affectedClient) })

	go handler(context.Background(), affected, &events.StreamReplaced{})

	msg := recvBroadcast(t)
	if msg.Code != "DEVICE_STREAM_REPLACED" {
		t.Fatalf("broadcast code = %s, want DEVICE_STREAM_REPLACED", msg.Code)
	}
	result, _ := msg.Result.(map[string]string)
	if result["device_id"] != "replaced-a" {
		t.Fatalf("broadcast device_id = %q, want replaced-a", result["device_id"])
	}
	if ShouldAutoReconnect(affectedClient) {
		t.Fatal("affected client should be skipped by the reconnect checker")
	}
	if affected.State() != domainDevice.DeviceStateDisconnected {
		t.Fatalf("affected state = %s, want disconnected", affected.State())
	}
	if !ShouldAutoReconnect(otherClient) {
		t.Fatal("other client must not be affected")
	}
}

func TestHandlerConnectedClearsStreamReplaced(t *testing.T) {
	client := &whatsmeow.Client{Store: &store.Device{}}
	instance := NewDeviceInstance("replaced-c", client, nil)
	markStreamReplaced(client)
	t.Cleanup(func() { clearStreamReplaced(client) })

	handler(context.Background(), instance, &events.PushNameSetting{})
	if ShouldAutoReconnect(client) {
		t.Fatal("PushNameSetting must not clear the stream replaced mark")
	}

	handler(context.Background(), instance, &events.Connected{})
	if !ShouldAutoReconnect(client) {
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
	markStreamReplaced(marked)
	t.Cleanup(func() { clearStreamReplaced(marked) })

	if ShouldAutoReconnect(marked) {
		t.Error("marked client must not auto-reconnect")
	}
	if !ShouldAutoReconnect(unmarked) {
		t.Error("unmarked client should auto-reconnect")
	}
	if !ShouldAutoReconnect(nil) {
		t.Error("nil client should report true (nothing to block)")
	}
}

// At startup the default client's event handler stays bound to the instance
// InitWaCLI created, while loadFromRegistry moves the same client into the named
// slot's instance. Events then arrive on an instance the manager no longer holds.
func newMovedClient(t *testing.T) (client *whatsmeow.Client, orphan *DeviceInstance) {
	t.Helper()
	client = &whatsmeow.Client{Store: &store.Device{}}
	orphan = NewDeviceInstance("5511999999999:12@s.whatsapp.net", client, nil)
	manager := NewDeviceManager(nil, nil, nil)
	manager.AddDevice(NewDeviceInstance("org_2", client, nil))
	withDeviceManager(t, manager)
	return client, orphan
}

func TestStreamReplacedOnClientMovedToRegistrySlot(t *testing.T) {
	client, orphan := newMovedClient(t)
	t.Cleanup(func() { clearStreamReplaced(client) })

	go handler(context.Background(), orphan, &events.StreamReplaced{})
	msg := recvBroadcast(t)
	result, _ := msg.Result.(map[string]string)
	if result["device_id"] != "org_2" {
		t.Errorf("broadcast device_id = %q, want the slot id org_2", result["device_id"])
	}
	if ShouldAutoReconnect(client) {
		t.Fatal("the reconnect checker must skip a client whose session was replaced")
	}

	handler(context.Background(), orphan, &events.Connected{})
	if !ShouldAutoReconnect(client) {
		t.Fatal("Connected should let the checker reconnect the client again")
	}
}
