package whatsapp

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types/events"
)

var sessionTestNow = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

func TestSessionStatusFromEvent(t *testing.T) {
	cases := []struct {
		name string
		evt  any
		want SessionStatus
	}{
		{"connected", &events.Connected{}, SessionStatus{Status: SessionStatusConnected}},
		{"disconnected", &events.Disconnected{}, SessionStatus{Status: SessionStatusDisconnected}},
		{"logged out", &events.LoggedOut{Reason: events.ConnectFailureLoggedOut},
			SessionStatus{Status: SessionStatusLoggedOut, Code: sessionPtr(401), Reason: sessionPtr("401: logged out from another device")}},
		{"stream replaced", &events.StreamReplaced{}, SessionStatus{Status: SessionStatusStreamReplaced}},
		{"temporary ban", &events.TemporaryBan{Code: events.TempBanSentToTooManyPeople, Expire: 24 * time.Hour},
			SessionStatus{
				Status:    SessionStatusTemporaryBan,
				Code:      sessionPtr(101),
				Reason:    sessionPtr("101: you sent too many messages to people who don't have you in their address books"),
				ExpiresAt: sessionPtr(sessionTestNow.Add(24 * time.Hour)),
			}},
		{"temporary ban without expiry", &events.TemporaryBan{Code: events.TempBanBlockedByUsers},
			SessionStatus{Status: SessionStatusTemporaryBan, Code: sessionPtr(102), Reason: sessionPtr("102: too many people blocked you")}},
		{"connect failure with message", &events.ConnectFailure{Reason: events.ConnectFailureGeneric, Message: "bad request"},
			SessionStatus{Status: SessionStatusConnectFailure, Code: sessionPtr(400), Reason: sessionPtr("bad request")}},
		{"connect failure without message", &events.ConnectFailure{Reason: events.ConnectFailureServiceUnavailable},
			SessionStatus{Status: SessionStatusConnectFailure, Code: sessionPtr(503), Reason: sessionPtr("503: unknown error")}},
		{"first keepalive timeout", &events.KeepAliveTimeout{ErrorCount: 1}, SessionStatus{Status: SessionStatusKeepAliveTimeout}},
		{"keepalive restored", &events.KeepAliveRestored{}, SessionStatus{Status: SessionStatusKeepAliveRestored}},
		{"client outdated", &events.ClientOutdated{}, SessionStatus{Status: SessionStatusClientOutdated, Code: sessionPtr(405)}},
		{"pair success", &events.PairSuccess{}, SessionStatus{Status: SessionStatusPairSuccess}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := sessionStatusFromEvent(tc.evt, sessionTestNow)
			if !ok {
				t.Fatalf("sessionStatusFromEvent(%T) returned ok=false", tc.evt)
			}
			if !reflect.DeepEqual(got.Payload(), tc.want.Payload()) {
				t.Fatalf("payload = %#v, want %#v", got.Payload(), tc.want.Payload())
			}
		})
	}
}

func TestSessionStatusFromEventSkipsOtherEvents(t *testing.T) {
	for _, evt := range []any{
		&events.KeepAliveTimeout{ErrorCount: 2},
		&events.KeepAliveTimeout{ErrorCount: 7},
		&events.StreamError{Code: "500"},
		&events.CATRefreshError{},
		&events.Message{},
	} {
		if status, ok := sessionStatusFromEvent(evt, sessionTestNow); ok {
			t.Errorf("%T %+v produced status %q, want none", evt, evt, status.Status)
		}
	}
}

func TestSessionStatusPayloadAlwaysHasFiveKeys(t *testing.T) {
	raw, err := json.Marshal(SessionStatus{Status: SessionStatusConnected}.Payload())
	if err != nil {
		t.Fatal(err)
	}
	want := `{"code":null,"expires_at":null,"qr_code":null,"reason":null,"status":"connected"}`
	if string(raw) != want {
		t.Fatalf("payload JSON = %s, want %s", raw, want)
	}

	banned := SessionStatus{Status: SessionStatusTemporaryBan, ExpiresAt: sessionPtr(sessionTestNow.Add(time.Hour))}
	if got := banned.Payload()["expires_at"]; got != "2026-09-24T13:00:00Z" {
		t.Fatalf("expires_at = %v, want 2026-09-24T13:00:00Z", got)
	}
}

func TestBuildSessionStatusBody(t *testing.T) {
	instance := &DeviceInstance{id: "org_2", jid: "5511999999999@s.whatsapp.net", createdAt: time.Now()}
	body := buildSessionStatusBody(instance, SessionStatus{Status: SessionStatusConnected})

	if body["event"] != SessionStatusEvent {
		t.Errorf("event = %v, want %s", body["event"], SessionStatusEvent)
	}
	if body["device_id"] != "5511999999999@s.whatsapp.net" {
		t.Errorf("device_id = %v", body["device_id"])
	}
	if body["session_id"] != "org_2" {
		t.Errorf("session_id = %v, want org_2", body["session_id"])
	}
	if _, err := time.Parse(time.RFC3339, body["timestamp"].(string)); err != nil {
		t.Errorf("timestamp not RFC3339: %v", err)
	}
	payload, _ := body["payload"].(map[string]any)
	if payload["status"] != SessionStatusConnected {
		t.Errorf("payload status = %v", payload["status"])
	}

	unpaired := buildSessionStatusBody(NewDeviceInstance("fresh-slot", nil, nil), SessionStatus{Status: SessionStatusQRTimeout})
	if unpaired["device_id"] != "" || unpaired["session_id"] != "fresh-slot" {
		t.Errorf("unpaired body device_id=%v session_id=%v, want empty and fresh-slot", unpaired["device_id"], unpaired["session_id"])
	}
}
