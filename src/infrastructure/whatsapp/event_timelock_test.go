package whatsapp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types/events"
)

func timelockEvent(t *testing.T, raw string) *events.NotifyAccountReachoutTimelock {
	t.Helper()
	var evt events.NotifyAccountReachoutTimelock
	if err := json.Unmarshal([]byte(raw), &evt); err != nil {
		t.Fatalf("bad test event: %v", err)
	}
	return &evt
}

// waitTimelockWebhook returns the next session.timelock body for sessionID.
func waitTimelockWebhook(t *testing.T, got <-chan map[string]any, sessionID string, wait time.Duration) (map[string]any, bool) {
	t.Helper()
	deadline := time.After(wait)
	for {
		select {
		case body := <-got:
			if body["event"] == SessionTimelockEvent && body["session_id"] == sessionID {
				return body, true
			}
		case <-deadline:
			return nil, false
		}
	}
}

func TestHandlerEmitsSessionTimelockOnActiveEvent(t *testing.T) {
	got := captureSessionWebhooks(t)
	instance := NewDeviceInstance("timelock-active", nil, nil)
	ends := time.Now().Add(3 * time.Hour).Unix()
	evt := timelockEvent(t, `{"enforcement_type":"spam","is_active":true,"time_enforcement_ends":"`+itoa(ends)+`"}`)

	handler(context.Background(), instance, evt)

	body, ok := waitTimelockWebhook(t, got, "timelock-active", 2*time.Second)
	if !ok {
		t.Fatal("no session.timelock webhook")
	}
	payload, _ := body["payload"].(map[string]any)
	if payload["active"] != true || payload["source"] != ReachoutSourceEvent || payload["enforcement_type"] != "spam" {
		t.Fatalf("payload = %v", payload)
	}
	if want := time.Unix(ends, 0).UTC().Format(time.RFC3339); payload["ends_at"] != want {
		t.Fatalf("ends_at = %v, want %v", payload["ends_at"], want)
	}
}

func TestSameTimelockEventTwiceEmitsOnce(t *testing.T) {
	got := captureSessionWebhooks(t)
	instance := NewDeviceInstance("timelock-twice", nil, nil)
	evt := timelockEvent(t, `{"enforcement_type":"spam","is_active":true}`)

	handler(context.Background(), instance, evt)
	handler(context.Background(), instance, evt)

	if _, ok := waitTimelockWebhook(t, got, "timelock-twice", 2*time.Second); !ok {
		t.Fatal("no first webhook")
	}
	if _, ok := waitTimelockWebhook(t, got, "timelock-twice", 300*time.Millisecond); ok {
		t.Fatal("an identical event must not emit a second webhook")
	}
}

func TestInactiveTimelockEventEmitsCleared(t *testing.T) {
	got := captureSessionWebhooks(t)
	instance := NewDeviceInstance("timelock-clear", nil, nil)
	handler(context.Background(), instance, timelockEvent(t, `{"enforcement_type":"spam","is_active":true}`))
	waitTimelockWebhook(t, got, "timelock-clear", 2*time.Second)

	handler(context.Background(), instance, timelockEvent(t, `{"is_active":false}`))

	body, ok := waitTimelockWebhook(t, got, "timelock-clear", 2*time.Second)
	if !ok {
		t.Fatal("no clearing webhook")
	}
	payload, _ := body["payload"].(map[string]any)
	if payload["active"] != false || payload["source"] != nil || payload["ends_at"] != nil {
		t.Fatalf("payload = %v, want cleared with null source and ends_at", payload)
	}
}

func TestNoteReachoutTimelockEmitsOnceFromA463(t *testing.T) {
	got := captureSessionWebhooks(t)
	instance := NewDeviceInstance("timelock-463", nil, nil)

	NoteReachoutTimelock(context.Background(), instance)
	NoteReachoutTimelock(context.Background(), instance)

	body, ok := waitTimelockWebhook(t, got, "timelock-463", 2*time.Second)
	if !ok {
		t.Fatal("no webhook for the 463")
	}
	payload, _ := body["payload"].(map[string]any)
	if payload["active"] != true || payload["source"] != ReachoutSourceSend463 || payload["ends_at"] != nil {
		t.Fatalf("payload = %v", payload)
	}
	if _, again := waitTimelockWebhook(t, got, "timelock-463", 300*time.Millisecond); again {
		t.Fatal("a second 463 in the same window must not emit again")
	}
}

func TestReachoutHelpersIgnoreANilInstance(t *testing.T) {
	NoteReachoutTimelock(context.Background(), nil)
	HandleReachoutTimelockEvent(context.Background(), nil, &events.NotifyAccountReachoutTimelock{IsActive: true}, time.Now())
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
