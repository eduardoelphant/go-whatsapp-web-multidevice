package whatsapp

import (
	"context"
	"testing"

	domainDevice "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/device"
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
