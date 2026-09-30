# Fork Phase 2 Part A Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A replaced WhatsApp session stops only its own device, startup no longer prints secrets, and every device lifecycle change (connection and pairing) reaches webhooks as a `session.status` event.

**Architecture:** Fork logic lives in new files under `src/infrastructure/whatsapp/` (`stream_replaced.go`, `event_session.go`, `webhook_slot.go`). Upstream files only get short hooks marked `// Fork (elphant):`. `session.status` goes through the existing `forwardPayloadToConfiguredWebhooks`, so HMAC, per-device config, and event filters apply unchanged.

**Tech Stack:** Go 1.26 (`GOTOOLCHAIN=auto`), whatsmeow, Fiber, standard `testing`.

**Spec:** `docs/specs/2026-09-24-gateway-phase2-a-design.md`

## Global Constraints

- Branch `elphant`. Local commits only: no push, no PR, no issue.
- Run every Go command from `src/` with `GOTOOLCHAIN=auto` (local Go is 1.22, `go.mod` asks for 1.26).
- Put fork logic in new files. Hooks in upstream files stay a few lines each and carry a `// Fork (elphant):` comment.
- Do not edit upstream docs (`docs/webhook-payload.md`, `README.md`, `AGENTS.md`). Fork docs go in `docs/reference/elphant-fork.md`.
- The event name is exactly `session.status`. The payload always has the five keys `status`, `reason`, `code`, `expires_at`, `qr_code`, with `null` for keys that do not apply.
- Commit messages are in English, `type(scope): subject`, with no attribution line.
- Tests do not call `t.Parallel()`. Every test that changes a package global or `config.*` restores it.
- Never run the app against the lab session or a real phone number, except in Task 10 after the owner explicitly approves it.

## Review Focus

1. **Remote logout.** `handleLoggedOut` clears the device JID. The `logged_out` webhook must still carry it. Test in Task 5 (`TestHandlerLoggedOutWebhookKeepsJID`).
2. **Slow or dead webhook endpoint.** The whatsmeow event handler must not wait for delivery. Test in Task 5 (`TestHandlerDoesNotBlockOnSlowSessionWebhook`).
3. **Device webhook filter on the slot path.** A device whose `webhook_events` omits `session.status` must not get pre-pairing events. Test in Task 6 (case `slot event filter drops event`).
4. **Keepalive flapping.** `KeepAliveTimeout` with `ErrorCount` 2 and up must stay silent. Test in Task 4 (`TestSessionStatusFromEventSkipsOtherEvents`).
5. **HTTP login after the QR loop change.** `GET /app/login` must still return the first QR once the image send is non-blocking. Covered by the runtime check in Task 9, step 5 (no unit seam exists for the loop).

---

### Task 1: StreamReplaced marks the device instead of exiting

**Files:**
- Create: `src/infrastructure/whatsapp/stream_replaced.go`
- Modify: `src/infrastructure/whatsapp/device_instance.go` (struct at lines 14-31; append methods at end of file)
- Modify: `src/infrastructure/whatsapp/event_handler.go:5-8` (imports), `:50-53` (cases), `:306-308` (remove old func)
- Test: `src/infrastructure/whatsapp/stream_replaced_test.go`

**Interfaces:**
- Consumes: `recvBroadcast(t)` from `event_handler_passkey_test.go` (same package).
- Produces:
  - `func (d *DeviceInstance) MarkStreamReplaced()`
  - `func (d *DeviceInstance) ClearStreamReplaced()`
  - `func (d *DeviceInstance) StreamReplaced() bool`
  - `func handleStreamReplaced(instance *DeviceInstance)`

- [ ] **Step 1: Write the failing test**

Create `src/infrastructure/whatsapp/stream_replaced_test.go`:

```go
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd src && GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/ -run 'StreamReplaced' -count=1`
Expected: FAIL to compile with `instance.MarkStreamReplaced undefined` (and `StreamReplaced undefined`).

- [ ] **Step 3: Add the mark to `DeviceInstance`**

In `src/infrastructure/whatsapp/device_instance.go`, add the field at the end of the `DeviceInstance` struct, after `passkeySkipHandoffUX bool`:

```go
	passkeySkipHandoffUX bool

	// Fork (elphant): set when the session was opened elsewhere (StreamReplaced);
	// cleared on the next successful connection. See stream_replaced.go.
	streamReplaced bool
}
```

Append at the end of the file:

```go
// MarkStreamReplaced records that this device's session was taken over elsewhere.
func (d *DeviceInstance) MarkStreamReplaced() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.streamReplaced = true
}

// ClearStreamReplaced removes the stream replaced mark.
func (d *DeviceInstance) ClearStreamReplaced() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.streamReplaced = false
}

// StreamReplaced reports whether the session was taken over elsewhere and has not
// reconnected since.
func (d *DeviceInstance) StreamReplaced() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.streamReplaced
}
```

- [ ] **Step 4: Create `stream_replaced.go`**

```go
package whatsapp

import (
	domainDevice "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/device"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/ui/websocket"
	"github.com/sirupsen/logrus"
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
```

- [ ] **Step 5: Hook the handler**

In `src/infrastructure/whatsapp/event_handler.go`, replace:

```go
	case *events.Connected, *events.PushNameSetting:
		handleConnectionEvents(ctx, client, instance)
	case *events.StreamReplaced:
		handleStreamReplaced(ctx)
```

with:

```go
	case *events.Connected:
		// Fork (elphant): a successful connection ends any stream replaced state.
		instance.ClearStreamReplaced()
		handleConnectionEvents(ctx, client, instance)
	case *events.PushNameSetting:
		handleConnectionEvents(ctx, client, instance)
	case *events.StreamReplaced:
		// Fork (elphant): stop only this device instead of os.Exit. See stream_replaced.go.
		handleStreamReplaced(instance)
```

Delete the old function (lines 306-308):

```go
func handleStreamReplaced(_ context.Context) {
	os.Exit(0)
}
```

Remove `"os"` from the import block (it was only used by `os.Exit`).

- [ ] **Step 6: Run the tests to verify they pass**

Run: `cd src && GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/ -run 'StreamReplaced|Passkey' -count=1`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add src/infrastructure/whatsapp/stream_replaced.go src/infrastructure/whatsapp/stream_replaced_test.go src/infrastructure/whatsapp/device_instance.go src/infrastructure/whatsapp/event_handler.go
git commit -m "fix(whatsapp): stop only the replaced device instead of exiting"
```

---

### Task 2: The 5-minute auto-reconnect skips replaced devices

**Files:**
- Modify: `src/infrastructure/whatsapp/stream_replaced.go` (add `ShouldAutoReconnect`)
- Modify: `src/ui/rest/helpers/common.go:35-41` (checker loop) and imports
- Test: `src/infrastructure/whatsapp/stream_replaced_test.go`

**Interfaces:**
- Consumes: `(*DeviceInstance).StreamReplaced()`, `(*DeviceInstance).MarkStreamReplaced()` from Task 1; `GetDeviceManager()`, `(*DeviceManager).ListDevices()`, `NewDeviceManager`, `(*DeviceManager).AddDevice` (existing).
- Produces: `func ShouldAutoReconnect(cli *whatsmeow.Client) bool`

- [ ] **Step 1: Write the failing test**

Append to `src/infrastructure/whatsapp/stream_replaced_test.go`, and add `"go.mau.fi/whatsmeow"` to its imports:

```go
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd src && GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/ -run 'TestShouldAutoReconnect' -count=1`
Expected: FAIL to compile with `undefined: ShouldAutoReconnect`.

- [ ] **Step 3: Implement `ShouldAutoReconnect`**

Add `"go.mau.fi/whatsmeow"` to the imports of `src/infrastructure/whatsapp/stream_replaced.go` and append:

```go
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
```

- [ ] **Step 4: Hook the checker**

In `src/ui/rest/helpers/common.go`, add the import `"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"` and replace:

```go
			if !cli.IsConnected() {
				_ = cli.Connect()
			}
```

with:

```go
			// Fork (elphant): leave a device whose session was opened elsewhere alone.
			if !cli.IsConnected() && whatsapp.ShouldAutoReconnect(cli) {
				_ = cli.Connect()
			}
```

- [ ] **Step 5: Run tests and build**

Run: `cd src && GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/ -run 'StreamReplaced|ShouldAutoReconnect' -count=1 && GOTOOLCHAIN=auto go build ./...`
Expected: PASS, build succeeds (no import cycle).

- [ ] **Step 6: Commit**

```bash
git add src/infrastructure/whatsapp/stream_replaced.go src/infrastructure/whatsapp/stream_replaced_test.go src/ui/rest/helpers/common.go
git commit -m "fix(whatsapp): skip auto-reconnect for devices replaced elsewhere"
```

---

### Task 3: Stop printing the configuration at startup

**Files:**
- Modify: `src/cmd/root.go:80`

**Interfaces:** none.

- [ ] **Step 1: Remove the line**

In `src/cmd/root.go`, inside `initEnvConfig`, delete:

```go
	fmt.Println(viper.AllSettings())
```

`fmt` stays imported: the file still uses it elsewhere.

- [ ] **Step 2: Verify nothing else prints the settings, then build and test**

Run: `cd src && grep -rn "AllSettings" --include='*.go' . ; GOTOOLCHAIN=auto go build ./... && GOTOOLCHAIN=auto go test ./cmd/... -count=1`
Expected: grep prints nothing; build succeeds; tests PASS.

- [ ] **Step 3: Commit**

```bash
git add src/cmd/root.go
git commit -m "fix(cmd): stop printing the whole configuration at startup"
```

---

### Task 4: `SessionStatus` type and connection event mapping

**Files:**
- Create: `src/infrastructure/whatsapp/event_session.go`
- Test: `src/infrastructure/whatsapp/event_session_test.go`

**Interfaces:**
- Produces:
  - `const SessionStatusEvent = "session.status"`
  - status constants `SessionStatusConnected`, `SessionStatusDisconnected`, `SessionStatusLoggedOut`, `SessionStatusStreamReplaced`, `SessionStatusTemporaryBan`, `SessionStatusConnectFailure`, `SessionStatusKeepAliveTimeout`, `SessionStatusKeepAliveRestored`, `SessionStatusClientOutdated`, `SessionStatusPairSuccess`, `SessionStatusQR`, `SessionStatusQRTimeout`, `SessionStatusPasskeyRequired`, `SessionStatusPasskeyConfirmation`, `SessionStatusPairError` (all `string`)
  - `type SessionStatus struct { Status string; Reason *string; Code *int; ExpiresAt *time.Time; QRCode *string }`
  - `func (s SessionStatus) Payload() map[string]any`
  - `func sessionStatusFromEvent(rawEvt any, now time.Time) (SessionStatus, bool)`
  - `func buildSessionStatusBody(instance *DeviceInstance, status SessionStatus) map[string]any`
  - `func sessionPtr[T any](v T) *T`

- [ ] **Step 1: Write the failing tests**

Create `src/infrastructure/whatsapp/event_session_test.go`:

```go
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/ -run 'SessionStatus' -count=1`
Expected: FAIL to compile with `undefined: SessionStatus` and `undefined: sessionStatusFromEvent`.

- [ ] **Step 3: Create `event_session.go`**

```go
package whatsapp

import (
	"time"

	"go.mau.fi/whatsmeow/types/events"
)

// Fork (elphant): the session.status webhook reports device lifecycle changes
// (connection, logout, bans, pairing). See docs/reference/elphant-fork.md for the contract.

// SessionStatusEvent is the webhook event name for device lifecycle changes.
const SessionStatusEvent = "session.status"

// Values of the session.status payload "status" key.
const (
	SessionStatusConnected           = "connected"
	SessionStatusDisconnected        = "disconnected"
	SessionStatusLoggedOut           = "logged_out"
	SessionStatusStreamReplaced      = "stream_replaced"
	SessionStatusTemporaryBan        = "temporary_ban"
	SessionStatusConnectFailure      = "connect_failure"
	SessionStatusKeepAliveTimeout    = "keepalive_timeout"
	SessionStatusKeepAliveRestored   = "keepalive_restored"
	SessionStatusClientOutdated      = "client_outdated"
	SessionStatusPairSuccess         = "pair_success"
	SessionStatusQR                  = "qr"
	SessionStatusQRTimeout           = "qr_timeout"
	SessionStatusPasskeyRequired     = "passkey_required"
	SessionStatusPasskeyConfirmation = "passkey_confirmation"
	SessionStatusPairError           = "pair_error"
)

// SessionStatus is one session.status payload. Nil fields are sent as JSON null.
type SessionStatus struct {
	Status    string
	Reason    *string
	Code      *int
	ExpiresAt *time.Time
	QRCode    *string
}

// Payload returns the webhook payload. All five keys are always present.
func (s SessionStatus) Payload() map[string]any {
	payload := map[string]any{
		"status":     s.Status,
		"reason":     nil,
		"code":       nil,
		"expires_at": nil,
		"qr_code":    nil,
	}
	if s.Reason != nil {
		payload["reason"] = *s.Reason
	}
	if s.Code != nil {
		payload["code"] = *s.Code
	}
	if s.ExpiresAt != nil {
		payload["expires_at"] = s.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if s.QRCode != nil {
		payload["qr_code"] = *s.QRCode
	}
	return payload
}

func sessionPtr[T any](v T) *T {
	return &v
}

// sessionStatusFromEvent maps a whatsmeow event to a session.status payload.
// ok is false for events that do not produce one.
func sessionStatusFromEvent(rawEvt any, now time.Time) (SessionStatus, bool) {
	switch evt := rawEvt.(type) {
	case *events.Connected:
		return SessionStatus{Status: SessionStatusConnected}, true
	case *events.Disconnected:
		return SessionStatus{Status: SessionStatusDisconnected}, true
	case *events.LoggedOut:
		return SessionStatus{
			Status: SessionStatusLoggedOut,
			Code:   sessionPtr(int(evt.Reason)),
			Reason: sessionPtr(evt.Reason.String()),
		}, true
	case *events.StreamReplaced:
		return SessionStatus{Status: SessionStatusStreamReplaced}, true
	case *events.TemporaryBan:
		status := SessionStatus{
			Status: SessionStatusTemporaryBan,
			Code:   sessionPtr(int(evt.Code)),
			Reason: sessionPtr(evt.Code.String()),
		}
		if evt.Expire > 0 {
			status.ExpiresAt = sessionPtr(now.Add(evt.Expire))
		}
		return status, true
	case *events.ConnectFailure:
		reason := evt.Message
		if reason == "" {
			reason = evt.Reason.String()
		}
		return SessionStatus{
			Status: SessionStatusConnectFailure,
			Code:   sessionPtr(int(evt.Reason)),
			Reason: &reason,
		}, true
	case *events.KeepAliveTimeout:
		// whatsmeow fires this on every failed keepalive; report only the first
		// failure and let keepalive_restored close it.
		if evt.ErrorCount != 1 {
			return SessionStatus{}, false
		}
		return SessionStatus{Status: SessionStatusKeepAliveTimeout}, true
	case *events.KeepAliveRestored:
		return SessionStatus{Status: SessionStatusKeepAliveRestored}, true
	case *events.ClientOutdated:
		return SessionStatus{
			Status: SessionStatusClientOutdated,
			Code:   sessionPtr(int(events.ConnectFailureClientOutdated)),
		}, true
	case *events.PairSuccess:
		return SessionStatus{Status: SessionStatusPairSuccess}, true
	}
	return SessionStatus{}, false
}

// buildSessionStatusBody builds the webhook body. session_id comes straight from
// the device slot so it is present even before pairing, when device_id is empty.
func buildSessionStatusBody(instance *DeviceInstance, status SessionStatus) map[string]any {
	return map[string]any{
		"event":      SessionStatusEvent,
		"device_id":  instance.JID(),
		"session_id": instance.ID(),
		"timestamp":  time.Now().Format(time.RFC3339),
		"payload":    status.Payload(),
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/ -run 'SessionStatus' -count=1 && GOTOOLCHAIN=auto go vet ./infrastructure/whatsapp/`
Expected: PASS; vet clean.

- [ ] **Step 5: Commit**

```bash
git add src/infrastructure/whatsapp/event_session.go src/infrastructure/whatsapp/event_session_test.go
git commit -m "feat(whatsapp): map lifecycle events to session.status payloads"
```

---

### Task 5: Deliver `session.status` from the event handler

**Files:**
- Modify: `src/infrastructure/whatsapp/event_session.go` (add `EmitSessionStatus`, `handleSessionEvent`)
- Modify: `src/infrastructure/whatsapp/event_handler.go:30-34` (one hook before the `switch`)
- Test: `src/infrastructure/whatsapp/event_session_test.go`

**Interfaces:**
- Consumes: `SessionStatus`, `sessionStatusFromEvent`, `buildSessionStatusBody`, `SessionStatusEvent` (Task 4); `forwardPayloadToConfiguredWebhooks`, `submitWebhookFn`, `webhookStorageForTest`, `shouldForwardEventToChatwoot` (existing); `recvBroadcast` (existing test helper).
- Produces:
  - `func EmitSessionStatus(ctx context.Context, instance *DeviceInstance, status SessionStatus)`
  - `func handleSessionEvent(ctx context.Context, instance *DeviceInstance, rawEvt any)`
  - test helpers `captureSessionWebhooks(t) <-chan map[string]any` and `waitSessionWebhook(t, got, sessionID) map[string]any` (used again in Task 7 tests)

- [ ] **Step 1: Write the failing tests**

Add these imports to `src/infrastructure/whatsapp/event_session_test.go`: `"context"`, `"github.com/aldinokemal/go-whatsapp-web-multidevice/config"`, `"github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"`. Append:

```go
// captureSessionWebhooks points the global webhook at a stub and returns the
// bodies it receives.
func captureSessionWebhooks(t *testing.T) <-chan map[string]any {
	t.Helper()
	originalWebhooks := config.WhatsappWebhook
	originalEvents := config.WhatsappWebhookEvents
	originalSubmit := submitWebhookFn
	originalStorage := webhookStorageForTest
	config.WhatsappWebhook = []string{"https://session-status.test"}
	config.WhatsappWebhookEvents = nil
	webhookStorageForTest = func(string) (*chatstorage.DeviceRecord, error) { return nil, nil }

	got := make(chan map[string]any, 32)
	submitWebhookFn = func(_ context.Context, payload map[string]any, _ string, _ *chatstorage.DeviceWebhookConfig) error {
		got <- payload
		return nil
	}
	t.Cleanup(func() {
		config.WhatsappWebhook = originalWebhooks
		config.WhatsappWebhookEvents = originalEvents
		submitWebhookFn = originalSubmit
		webhookStorageForTest = originalStorage
	})
	return got
}

// waitSessionWebhook returns the first captured body for sessionID, skipping
// bodies left over from other tests' goroutines.
func waitSessionWebhook(t *testing.T, got <-chan map[string]any, sessionID string) map[string]any {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case body := <-got:
			if body["event"] == SessionStatusEvent && body["session_id"] == sessionID {
				return body
			}
		case <-deadline:
			t.Fatalf("no %s webhook for session %s", SessionStatusEvent, sessionID)
			return nil
		}
	}
}

func TestHandlerEmitsSessionStatusWebhook(t *testing.T) {
	got := captureSessionWebhooks(t)
	instance := NewDeviceInstance("session-e2e", nil, nil)

	handler(context.Background(), instance, &events.KeepAliveRestored{})

	body := waitSessionWebhook(t, got, "session-e2e")
	if body["device_id"] != "" {
		t.Errorf("device_id = %v, want empty for an unpaired slot", body["device_id"])
	}
	payload, _ := body["payload"].(map[string]any)
	if payload["status"] != SessionStatusKeepAliveRestored {
		t.Errorf("status = %v, want %s", payload["status"], SessionStatusKeepAliveRestored)
	}
}

func TestHandlerLoggedOutWebhookKeepsJID(t *testing.T) {
	got := captureSessionWebhooks(t)
	instance := &DeviceInstance{id: "session-logout", jid: "5511999999999@s.whatsapp.net", createdAt: time.Now()}
	// Mirrors the manager's keep-slot callback, which clears the JID.
	instance.SetOnLoggedOut(func(string) { instance.ResetClient() })

	go handler(context.Background(), instance, &events.LoggedOut{Reason: events.ConnectFailureLoggedOut})
	if msg := recvBroadcast(t); msg.Code != "DEVICE_LOGGED_OUT" {
		t.Fatalf("broadcast code = %s, want DEVICE_LOGGED_OUT", msg.Code)
	}

	body := waitSessionWebhook(t, got, "session-logout")
	if body["device_id"] != "5511999999999@s.whatsapp.net" {
		t.Errorf("device_id = %v, want the JID from before the logout", body["device_id"])
	}
	payload, _ := body["payload"].(map[string]any)
	if payload["status"] != SessionStatusLoggedOut || payload["code"] != 401 {
		t.Errorf("payload = %#v, want logged_out with code 401", payload)
	}
	if instance.JID() != "" {
		t.Error("handleLoggedOut should still clear the JID")
	}
}

func TestHandlerDoesNotBlockOnSlowSessionWebhook(t *testing.T) {
	originalWebhooks := config.WhatsappWebhook
	originalSubmit := submitWebhookFn
	originalStorage := webhookStorageForTest
	config.WhatsappWebhook = []string{"https://slow.test"}
	webhookStorageForTest = func(string) (*chatstorage.DeviceRecord, error) { return nil, nil }
	release := make(chan struct{})
	submitWebhookFn = func(context.Context, map[string]any, string, *chatstorage.DeviceWebhookConfig) error {
		<-release
		return nil
	}
	t.Cleanup(func() {
		close(release)
		config.WhatsappWebhook = originalWebhooks
		submitWebhookFn = originalSubmit
		webhookStorageForTest = originalStorage
	})

	done := make(chan struct{})
	go func() {
		handler(context.Background(), NewDeviceInstance("session-slow", nil, nil), &events.KeepAliveRestored{})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("handler blocked on a slow session.status webhook")
	}
}

func TestSessionStatusRespectsEventWhitelist(t *testing.T) {
	originalWebhooks := config.WhatsappWebhook
	originalEvents := config.WhatsappWebhookEvents
	originalSubmit := submitWebhookFn
	config.WhatsappWebhook = []string{"https://session-status.test"}
	calls := 0
	submitWebhookFn = func(context.Context, map[string]any, string, *chatstorage.DeviceWebhookConfig) error {
		calls++
		return nil
	}
	t.Cleanup(func() {
		config.WhatsappWebhook = originalWebhooks
		config.WhatsappWebhookEvents = originalEvents
		submitWebhookFn = originalSubmit
	})
	instance := NewDeviceInstance("session-filter", nil, nil)

	config.WhatsappWebhookEvents = []string{"message"}
	if err := forwardPayloadToConfiguredWebhooks(context.Background(), buildSessionStatusBody(instance, SessionStatus{Status: SessionStatusConnected}), SessionStatusEvent); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("session.status delivered %d times with a whitelist that omits it", calls)
	}

	config.WhatsappWebhookEvents = []string{"message", SessionStatusEvent}
	if err := forwardPayloadToConfiguredWebhooks(context.Background(), buildSessionStatusBody(instance, SessionStatus{Status: SessionStatusConnected}), SessionStatusEvent); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("session.status delivered %d times with a whitelist that includes it, want 1", calls)
	}
}

func TestSessionStatusNotForwardedToChatwoot(t *testing.T) {
	if shouldForwardEventToChatwoot(SessionStatusEvent) {
		t.Fatal("session.status must not be forwarded to Chatwoot")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/ -run 'SessionStatus|SessionWebhook|LoggedOutWebhook' -count=1`
Expected: FAIL. `TestHandlerEmitsSessionStatusWebhook` and `TestHandlerLoggedOutWebhookKeepsJID` fail with `no session.status webhook for session ...`. The whitelist and Chatwoot tests already pass (they exercise existing code with the new event name).

- [ ] **Step 3: Add delivery to `event_session.go`**

Add `"context"` and `"github.com/sirupsen/logrus"` to the imports and append:

```go
// EmitSessionStatus sends a session.status webhook for the device without
// blocking the caller. The body is built before the goroutine starts, so it
// captures the device JID even if a handler clears it right after.
func EmitSessionStatus(ctx context.Context, instance *DeviceInstance, status SessionStatus) {
	if instance == nil {
		return
	}
	body := buildSessionStatusBody(instance, status)
	go func() {
		webhookCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if err := forwardPayloadToConfiguredWebhooks(webhookCtx, body, SessionStatusEvent); err != nil {
			logrus.Errorf("Failed to forward %s %q for device %s: %v", SessionStatusEvent, status.Status, instance.ID(), err)
		}
	}()
}

// handleSessionEvent emits session.status for lifecycle events and ignores the rest.
func handleSessionEvent(ctx context.Context, instance *DeviceInstance, rawEvt any) {
	if status, ok := sessionStatusFromEvent(rawEvt, time.Now()); ok {
		EmitSessionStatus(ctx, instance, status)
	}
}
```

- [ ] **Step 4: Hook the handler**

In `src/infrastructure/whatsapp/event_handler.go`, between `client := instance.GetClient()` and `switch evt := rawEvt.(type) {`, insert:

```go

	// Fork (elphant): session.status webhook. Runs before the switch so the body
	// captures the device JID before handlers such as handleLoggedOut clear it.
	handleSessionEvent(ctx, instance, rawEvt)
```

- [ ] **Step 5: Run the package tests**

Run: `cd src && GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/ -count=1`
Expected: PASS (whole package, including the existing passkey and StreamReplaced tests).

- [ ] **Step 6: Commit**

```bash
git add src/infrastructure/whatsapp/event_session.go src/infrastructure/whatsapp/event_session_test.go src/infrastructure/whatsapp/event_handler.go
git commit -m "feat(whatsapp): emit session.status webhook on connection changes"
```

---

### Task 6: Resolve the device webhook by slot before pairing

**Files:**
- Create: `src/infrastructure/whatsapp/webhook_slot.go`
- Modify: `src/infrastructure/whatsapp/webhook_forward.go:102-103` (hook after the JID lookup)
- Test: `src/infrastructure/whatsapp/webhook_slot_test.go`

**Interfaces:**
- Consumes: `GetDeviceManager()`, `DeviceManager.storage` (field), `IChatStorageRepository.GetDeviceRecord(deviceID string) (*DeviceRecord, error)` (existing; returns `nil, nil` when not found); `SessionStatusEvent` (Task 4).
- Produces:
  - `var webhookSlotStorageForTest func(slotID string) (*domainChatStorage.DeviceRecord, error)`
  - `func getWebhookConfigForSlot(payload map[string]any) (*domainChatStorage.DeviceWebhookConfig, error)`

- [ ] **Step 1: Write the failing test**

Create `src/infrastructure/whatsapp/webhook_slot_test.go`:

```go
package whatsapp

import (
	"context"
	"reflect"
	"testing"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
)

func TestForwardPayloadResolvesSlotWebhookBeforePairing(t *testing.T) {
	const globalURL = "https://global-webhook.test"
	deviceURL := "https://slot-webhook.test"

	cases := []struct {
		name         string
		payload      map[string]any
		slotRecord   *chatstorage.DeviceRecord
		wantURLs     []string
		wantLookedUp []string
	}{
		{
			name:         "before pairing uses slot config",
			payload:      map[string]any{"event": SessionStatusEvent, "device_id": "", "session_id": "slot-a"},
			slotRecord:   &chatstorage.DeviceRecord{DeviceID: "slot-a", WebhookURL: &deviceURL},
			wantURLs:     []string{deviceURL},
			wantLookedUp: []string{"slot-a"},
		},
		{
			name:         "slot event filter drops event",
			payload:      map[string]any{"event": SessionStatusEvent, "device_id": "", "session_id": "slot-a"},
			slotRecord:   &chatstorage.DeviceRecord{DeviceID: "slot-a", WebhookURL: &deviceURL, WebhookEvents: "message"},
			wantURLs:     nil,
			wantLookedUp: []string{"slot-a"},
		},
		{
			name:         "slot without webhook falls back to global",
			payload:      map[string]any{"event": SessionStatusEvent, "device_id": "", "session_id": "slot-a"},
			slotRecord:   &chatstorage.DeviceRecord{DeviceID: "slot-a"},
			wantURLs:     []string{globalURL},
			wantLookedUp: []string{"slot-a"},
		},
		{
			name:         "no session id falls back to global",
			payload:      map[string]any{"event": SessionStatusEvent, "device_id": ""},
			slotRecord:   &chatstorage.DeviceRecord{DeviceID: "slot-a", WebhookURL: &deviceURL},
			wantURLs:     []string{globalURL},
			wantLookedUp: nil,
		},
		{
			name:         "paired device keeps JID lookup",
			payload:      map[string]any{"event": SessionStatusEvent, "device_id": "5511999999999@s.whatsapp.net", "session_id": "slot-a"},
			slotRecord:   &chatstorage.DeviceRecord{DeviceID: "slot-a", WebhookURL: &deviceURL},
			wantURLs:     []string{globalURL},
			wantLookedUp: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			originalWebhooks := config.WhatsappWebhook
			originalEvents := config.WhatsappWebhookEvents
			originalSubmit := submitWebhookFn
			originalStorage := webhookStorageForTest
			originalSlot := webhookSlotStorageForTest
			t.Cleanup(func() {
				config.WhatsappWebhook = originalWebhooks
				config.WhatsappWebhookEvents = originalEvents
				submitWebhookFn = originalSubmit
				webhookStorageForTest = originalStorage
				webhookSlotStorageForTest = originalSlot
			})

			config.WhatsappWebhook = []string{globalURL}
			config.WhatsappWebhookEvents = nil
			webhookStorageForTest = func(string) (*chatstorage.DeviceRecord, error) { return nil, nil }
			var lookedUp []string
			webhookSlotStorageForTest = func(slotID string) (*chatstorage.DeviceRecord, error) {
				lookedUp = append(lookedUp, slotID)
				return tc.slotRecord, nil
			}
			var calledURLs []string
			submitWebhookFn = func(_ context.Context, _ map[string]any, url string, _ *chatstorage.DeviceWebhookConfig) error {
				calledURLs = append(calledURLs, url)
				return nil
			}

			if err := forwardPayloadToConfiguredWebhooks(context.Background(), tc.payload, SessionStatusEvent); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(calledURLs, tc.wantURLs) {
				t.Errorf("delivered to %v, want %v", calledURLs, tc.wantURLs)
			}
			if !reflect.DeepEqual(lookedUp, tc.wantLookedUp) {
				t.Errorf("slot lookups %v, want %v", lookedUp, tc.wantLookedUp)
			}
		})
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd src && GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/ -run 'TestForwardPayloadResolvesSlotWebhookBeforePairing' -count=1`
Expected: FAIL to compile with `undefined: webhookSlotStorageForTest`.

- [ ] **Step 3: Create `webhook_slot.go`**

```go
package whatsapp

import (
	"fmt"

	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
)

// Fork (elphant): events emitted before pairing (QR, passkey) have no JID, so the
// JID-based device webhook lookup finds nothing. They carry the slot id in
// session_id instead, and this resolves the device webhook config from it.

// webhookSlotStorageForTest replaces the slot record lookup in tests.
var webhookSlotStorageForTest func(slotID string) (*domainChatStorage.DeviceRecord, error)

// getWebhookConfigForSlot returns the device webhook config for the payload's
// session_id, or nil when there is no slot id or the slot has no webhook URL.
func getWebhookConfigForSlot(payload map[string]any) (*domainChatStorage.DeviceWebhookConfig, error) {
	slotID, _ := payload["session_id"].(string)
	if slotID == "" {
		return nil, nil
	}
	record, err := getDeviceRecordBySlot(slotID)
	if err != nil {
		return nil, fmt.Errorf("failed to get device record for slot %s: %w", slotID, err)
	}
	if record == nil || record.WebhookURL == nil || *record.WebhookURL == "" {
		return nil, nil
	}
	return &domainChatStorage.DeviceWebhookConfig{
		WebhookURL:                record.WebhookURL,
		WebhookSecret:             record.WebhookSecret,
		WebhookEvents:             record.WebhookEvents,
		WebhookInsecureSkipVerify: record.WebhookInsecureSkipVerify,
	}, nil
}

func getDeviceRecordBySlot(slotID string) (*domainChatStorage.DeviceRecord, error) {
	if webhookSlotStorageForTest != nil {
		return webhookSlotStorageForTest(slotID)
	}
	dm := GetDeviceManager()
	if dm != nil && dm.storage != nil {
		return dm.storage.GetDeviceRecord(slotID)
	}
	return nil, nil
}
```

- [ ] **Step 4: Hook `forwardPayloadToConfiguredWebhooks`**

In `src/infrastructure/whatsapp/webhook_forward.go`, replace:

```go
	deviceJID, _ := payload["device_id"].(string)
	webhookConfig, err := getWebhookConfigForDevice(deviceJID)
	if err != nil {
```

with:

```go
	deviceJID, _ := payload["device_id"].(string)
	webhookConfig, err := getWebhookConfigForDevice(deviceJID)
	if deviceJID == "" {
		// Fork (elphant): pre-pairing events carry only the slot id. See webhook_slot.go.
		webhookConfig, err = getWebhookConfigForSlot(payload)
	}
	if err != nil {
```

- [ ] **Step 5: Run the package tests**

Run: `cd src && GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/ -count=1`
Expected: PASS (the new cases and every existing `TestForwardPayloadToConfiguredWebhooks_*` / `TestGetWebhookConfigForDevice_*` test).

- [ ] **Step 6: Commit**

```bash
git add src/infrastructure/whatsapp/webhook_slot.go src/infrastructure/whatsapp/webhook_slot_test.go src/infrastructure/whatsapp/webhook_forward.go
git commit -m "feat(whatsapp): resolve the device webhook by slot before pairing"
```

---

### Task 7: `session.status` for QR and passkey pairing

**Files:**
- Modify: `src/infrastructure/whatsapp/event_session.go` (pairing cases, `sessionErrorReason`, `NewQRSessionStatus`)
- Modify: `src/usecase/app.go:81-104` (QR loop)
- Test: `src/infrastructure/whatsapp/event_session_test.go`

**Interfaces:**
- Consumes: `SessionStatus`, status constants, `sessionPtr`, `EmitSessionStatus` (Tasks 4 and 5).
- Produces:
  - `func NewQRSessionStatus(code string, timeout time.Duration, now time.Time) SessionStatus`
  - `func sessionErrorReason(err error) *string`

- [ ] **Step 1: Write the failing tests**

Append to `src/infrastructure/whatsapp/event_session_test.go` and add `"errors"` to its imports:

```go
func TestSessionStatusFromPairingEvents(t *testing.T) {
	cases := []struct {
		name string
		evt  any
		want SessionStatus
	}{
		{"passkey request", &events.PairPasskeyRequest{}, SessionStatus{Status: SessionStatusPasskeyRequired}},
		{"passkey confirmation", &events.PairPasskeyConfirmation{Code: "ABCD-EFGH"}, SessionStatus{Status: SessionStatusPasskeyConfirmation}},
		{"pair error", &events.PairError{Error: errors.New("bad signature")},
			SessionStatus{Status: SessionStatusPairError, Reason: sessionPtr("bad signature")}},
		{"passkey error", &events.PairPasskeyError{Error: errors.New("assertion rejected")},
			SessionStatus{Status: SessionStatusPairError, Reason: sessionPtr("assertion rejected")}},
		{"pair error without error value", &events.PairError{}, SessionStatus{Status: SessionStatusPairError}},
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

func TestNewQRSessionStatus(t *testing.T) {
	payload := NewQRSessionStatus("2@abc,def", 60*time.Second, sessionTestNow).Payload()
	want := map[string]any{
		"status":     SessionStatusQR,
		"reason":     nil,
		"code":       nil,
		"expires_at": "2026-09-24T12:01:00Z",
		"qr_code":    "2@abc,def",
	}
	if !reflect.DeepEqual(payload, want) {
		t.Fatalf("payload = %#v, want %#v", payload, want)
	}
}

func TestHandlerPasskeyRequestEmitsSessionStatus(t *testing.T) {
	got := captureSessionWebhooks(t)
	instance := NewDeviceInstance("session-passkey", nil, nil)

	go handler(context.Background(), instance, &events.PairPasskeyRequest{})
	if msg := recvBroadcast(t); msg.Code != "PASSKEY_REQUEST" {
		t.Fatalf("broadcast code = %s, want PASSKEY_REQUEST", msg.Code)
	}

	body := waitSessionWebhook(t, got, "session-passkey")
	payload, _ := body["payload"].(map[string]any)
	if payload["status"] != SessionStatusPasskeyRequired {
		t.Fatalf("status = %v, want %s", payload["status"], SessionStatusPasskeyRequired)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/ -run 'Pairing|QRSessionStatus|PasskeyRequestEmits' -count=1`
Expected: FAIL to compile with `undefined: NewQRSessionStatus`.

- [ ] **Step 3: Extend `event_session.go`**

In `sessionStatusFromEvent`, add these cases before the closing `}` of the `switch`:

```go
	case *events.PairPasskeyRequest:
		return SessionStatus{Status: SessionStatusPasskeyRequired}, true
	case *events.PairPasskeyConfirmation:
		return SessionStatus{Status: SessionStatusPasskeyConfirmation}, true
	case *events.PairError:
		return SessionStatus{Status: SessionStatusPairError, Reason: sessionErrorReason(evt.Error)}, true
	case *events.PairPasskeyError:
		return SessionStatus{Status: SessionStatusPairError, Reason: sessionErrorReason(evt.Error)}, true
```

Append to the file:

```go
// NewQRSessionStatus describes a QR code handed out by the login QR channel.
// The code expires after timeout, when the channel hands out the next one.
func NewQRSessionStatus(code string, timeout time.Duration, now time.Time) SessionStatus {
	return SessionStatus{
		Status:    SessionStatusQR,
		QRCode:    &code,
		ExpiresAt: sessionPtr(now.Add(timeout)),
	}
}

func sessionErrorReason(err error) *string {
	if err == nil {
		return nil
	}
	return sessionPtr(err.Error())
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/ -count=1`
Expected: PASS (including the existing `TestHandlerPasskeyEvents`).

- [ ] **Step 5: Hook the QR loop**

In `src/usecase/app.go`, inside the goroutine that reads `ch`:

1. Right after `if evt.Event == "code" {`, insert:

```go
				// Fork (elphant): session.status webhook for each QR code.
				whatsapp.EmitSessionStatus(context.Background(), instance, whatsapp.NewQRSessionStatus(evt.Code, evt.Timeout, time.Now()))
```

2. Replace the blocking image send:

```go
				select {
				case chImage <- qrPath:
				case <-qrCtx.Done():
					logrus.Warnf("[LOGIN][%s] QR context canceled while sending QR path", deviceID)
					return
				}
```

with:

```go
				// Fork (elphant): the HTTP caller reads only the first image. A blocking
				// send stalled this loop from the third code on, so later codes and the
				// timeout were never read. Drop paths nobody is waiting for.
				select {
				case chImage <- qrPath:
				default:
				}
```

3. Replace:

```go
			} else if evt.Event == whatsmeow.QRChannelEventPasskeyRequest || evt.Event == whatsmeow.QRChannelEventPasskeyResponse {
```

with:

```go
			} else if evt.Event == whatsmeow.QRChannelTimeout.Event {
				// Fork (elphant): the QR codes ran out without a scan.
				logrus.Warnf("[LOGIN][%s] QR codes expired without a scan", deviceID)
				whatsapp.EmitSessionStatus(context.Background(), instance, whatsapp.SessionStatus{Status: whatsapp.SessionStatusQRTimeout})
			} else if evt.Event == whatsmeow.QRChannelEventPasskeyRequest || evt.Event == whatsmeow.QRChannelEventPasskeyResponse {
```

`context`, `time`, `whatsmeow`, `logrus` and the `whatsapp` package are already imported in this file. `instance` is the `*whatsapp.DeviceInstance` returned by `service.ensureClient` at the top of `Login`.

- [ ] **Step 6: Build, vet and test the touched packages**

Run: `cd src && GOTOOLCHAIN=auto go build ./... && GOTOOLCHAIN=auto go vet ./usecase/ ./infrastructure/whatsapp/ && GOTOOLCHAIN=auto go test ./usecase/ ./infrastructure/whatsapp/ -count=1`
Expected: build succeeds; vet clean; PASS. The QR loop has no unit seam; Task 9 checks it at runtime.

- [ ] **Step 7: Commit**

```bash
git add src/infrastructure/whatsapp/event_session.go src/infrastructure/whatsapp/event_session_test.go src/usecase/app.go
git commit -m "feat(whatsapp): emit session.status for QR and passkey pairing"
```

---

### Task 8: Document the fork differences

**Files:**
- Create: `docs/reference/elphant-fork.md`

**Interfaces:** none.

- [ ] **Step 1: Write `docs/reference/elphant-fork.md`**

````markdown
# Fork differences (elphant)

This fork tracks upstream `aldinokemal/go-whatsapp-web-multidevice`. Branch `elphant` is the
latest upstream tag plus the commits listed here. Upstream docs are not edited; this page lists
every behavior that differs from them.

## StreamReplaced stops only the affected device

Upstream exits the whole process when WhatsApp reports that the session was opened elsewhere
(`<conflict type="replaced"/>`). Every other device goes down with it, and a container restart
policy brings the process back to fight the other holder of the credentials.

In this fork:

- Only the affected device disconnects. Other devices keep running.
- The device stays disconnected until you reconnect it with `GET /app/reconnect` or
  `POST /devices/:device_id/reconnect`, or restart the process. The 5-minute background
  reconnect skips it.
- The embedded UI receives the websocket code `DEVICE_STREAM_REPLACED`, and webhooks receive
  `session.status` with `status: "stream_replaced"`.
- A restart policy is no longer needed to survive this event.

Never run the same session in two processes at once. The two will keep replacing each other.

## Startup does not print the configuration

Upstream prints every setting to stdout on startup, including basic auth credentials, the
database URI and webhook secrets. This fork does not.

## `session.status` webhook

Reports device lifecycle changes. It is sent to the same targets as other events, signed the same
way, and can be filtered with `WHATSAPP_WEBHOOK_EVENTS` or the per-device `webhook_events`. It is
not forwarded to Chatwoot.

```json
{
  "event": "session.status",
  "device_id": "5511999999999@s.whatsapp.net",
  "session_id": "org_2",
  "timestamp": "2026-09-24T12:00:00Z",
  "payload": {
    "status": "temporary_ban",
    "reason": "101: you sent too many messages to people who don't have you in their address books",
    "code": 101,
    "expires_at": "2026-09-25T12:00:00Z",
    "qr_code": null
  }
}
```

- `device_id` is the device JID, or an empty string before pairing.
- `session_id` is the device slot id (the one registered through `POST /devices`), always present.
  Before pairing, the per-device webhook configuration is resolved from it.
- The five `payload` keys are always present. A key that does not apply is `null`.

| Field | Type | Meaning |
|---|---|---|
| `status` | string | See the tables below |
| `reason` | string or null | Human-readable cause |
| `code` | integer or null | Numeric code from WhatsApp |
| `expires_at` | string (RFC3339, UTC) or null | When a ban or a QR code expires |
| `qr_code` | string or null | Raw QR content, only for `qr` |

### Connection statuses

| `status` | When | `code` | `reason` / `expires_at` |
|---|---|---|---|
| `connected` | Session connected and logged in | null | null |
| `disconnected` | Unexpected drop; the client reconnects by itself | null | null |
| `logged_out` | Session removed (from the phone, or by WhatsApp) | 401, 403, 406 | WhatsApp reason |
| `stream_replaced` | Session opened in another process | null | null |
| `temporary_ban` | Account temporarily banned | 101 to 106 | Ban reason; `expires_at` when WhatsApp sends a duration |
| `connect_failure` | Server refused the connection for another reason | 4xx or 5xx | Server message |
| `keepalive_timeout` | First failed keepalive in a row | null | null |
| `keepalive_restored` | Keepalive works again | null | null |
| `client_outdated` | WhatsApp rejects the client version; update the image | 405 | null |
| `pair_success` | Pairing finished | null | null |

### Pairing statuses

| `status` | When | Extras |
|---|---|---|
| `qr` | A new QR code is available (`GET /app/login`) | `qr_code`, `expires_at` |
| `qr_timeout` | The QR codes ran out without a scan | none; call login again |
| `passkey_required` | Pairing needs a passkey | read the challenge from `GET /app/passkey` |
| `passkey_confirmation` | A confirmation code is ready | read the code from `GET /app/passkey` |
| `pair_error` | Pairing failed | `reason` |

`qr_code` lets anyone who holds it render the QR image, so use HTTPS webhook targets. Pairing still
requires the account owner's phone to scan it.
````

- [ ] **Step 2: Check the doc against the code**

Run: `cd src && grep -o 'SessionStatus[A-Za-z]* *= "[a-z_]*"' infrastructure/whatsapp/event_session.go`
Expected: the 15 status strings printed match the two tables in the doc exactly (10 connection + 5 pairing).

- [ ] **Step 3: Commit**

```bash
git add docs/reference/elphant-fork.md
git commit -m "docs: document fork differences from upstream"
```

---

### Task 9: Full gate and runtime check of the pairing events

**Files:** none (verification only). Temporary files go in the session scratchpad and are deleted at the end.

**Interfaces:** none.

- [ ] **Step 1: Full test suite and vet**

Run: `cd src && GOTOOLCHAIN=auto go vet ./... && GOTOOLCHAIN=auto go test ./... -count=1 2>&1 | grep -v 'no test files'`
Expected: vet clean; every package `ok`.

- [ ] **Step 2: Build the binary and prepare a throwaway run directory**

```bash
SCRATCH=/private/tmp/claude-501/-Users-eduardocarlos-Documents-whatsapp-gateway/997f9c4d-0bcf-496d-98ae-edf33908f2fb/scratchpad
mkdir -p "$SCRATCH/qr-check/run"
cd src && GOTOOLCHAIN=auto go build -o "$SCRATCH/qr-check/gowa" .
```

Expected: binary built. This run directory has a fresh database: no real session is involved.

- [ ] **Step 3: Start a webhook receiver that redacts QR content**

Write `$SCRATCH/qr-check/receiver.py`:

```python
import http.server
import json


class Handler(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        try:
            data = json.loads(body)
            if data.get("event") == "session.status":
                payload = dict(data["payload"])
                if payload.get("qr_code"):
                    payload["qr_code"] = "<redacted, %d chars>" % len(payload["qr_code"])
                print(json.dumps({"session_id": data.get("session_id"), "device_id": data.get("device_id"), **payload}), flush=True)
        except Exception as exc:
            print("bad body:", exc, flush=True)
        self.send_response(200)
        self.end_headers()

    def log_message(self, *args):
        pass


http.server.HTTPServer(("127.0.0.1", 3999), Handler).serve_forever()
```

Run it in the background: `python3 "$SCRATCH/qr-check/receiver.py" > "$SCRATCH/qr-check/receiver.log" 2>&1`

- [ ] **Step 4: Start the server without a global webhook**

Run in the background from `$SCRATCH/qr-check/run`:

```bash
../gowa rest --port 3031 --db-uri "file:storages/whatsapp.db?_foreign_keys=on" --auto-download-media=false > gowa.log 2>&1
```

Port 3031 avoids the lab instance (3030). Then confirm the startup no longer prints the settings map:

Run: `head -3 "$SCRATCH/qr-check/run/gowa.log"`
Expected: no line starting with `map[` (upstream printed the whole settings map as the first line).

- [ ] **Step 5: Create a slot with its own webhook and request a QR**

```bash
curl -s -X POST localhost:3031/devices -H 'Content-Type: application/json' \
  -d '{"device_id":"qr-check","webhook_url":"http://127.0.0.1:3999/hook"}' -o /dev/null -w '%{http_code}\n'
curl -s -H 'X-Device-Id: qr-check' localhost:3031/app/login -o /dev/null -w '%{http_code}\n'
```

Expected: the first prints a 2xx code (`200` or `201`), the second prints `200`. The second one proves the HTTP login still gets its first QR after the non-blocking change (Review Focus 5). Do not print the login response body: it holds the QR.

- [ ] **Step 6: Wait for the QR window to close, then read the receiver log**

Wait about 3 minutes (the QR window is ~160 s), then run: `cat "$SCRATCH/qr-check/receiver.log"`

Expected:
- several lines with `"status": "qr"`, `"session_id": "qr-check"`, `"device_id": ""`, a redacted `qr_code` and an `expires_at` roughly 60 s (first) or 20 s (later) after the previous line;
- more than two `qr` lines (before the fix the loop stalled at the third code);
- one final line with `"status": "qr_timeout"`.

The events reached the per-device URL with no global webhook configured, so this also proves the slot lookup (Task 6).

- [ ] **Step 7: Stop both processes and delete the run directory**

```bash
pkill -f "$SCRATCH/qr-check/gowa" ; pkill -f "$SCRATCH/qr-check/receiver.py"
rm -rf "$SCRATCH/qr-check"
```

Expected: nothing from the check remains on disk.

---

### Task 10 (gated): StreamReplaced with the real lab session

**Do not run this task without the owner's explicit approval, given at the time.** It uses the owner's personal number. It may only start after the 24-hour observation ends (2026-09-25 18:45, Brasília) and after the owner confirms that their own lab process is stopped.

**Risk to explain before asking:** during the test, two processes hold copies of the same Signal state. The one that is live last has the newest state. The test therefore ends by keeping the second copy's database. For about 2 minutes this is equivalent to opening WhatsApp Web in two tabs.

**Files:** none in the repository. Lab directory: `~/Documents/whatsapp-gateway-lab/`.

- [ ] **Step 1: Build the branch binary into the lab**

Run: `cd src && GOTOOLCHAIN=auto go build -o ~/Documents/whatsapp-gateway-lab/gowa-elphant .`

- [ ] **Step 2: Start process A on the lab database**

From `~/Documents/whatsapp-gateway-lab/run`, in the background:

```bash
../gowa-elphant rest --port 3030 -b "lab:$(cat .basic-pass)" --db-uri "file:storages/whatsapp.db?_foreign_keys=on" --debug=true --auto-download-media=false > gowa-a.log 2>&1
```

Wait until `gowa-a.log` shows the device connected.

- [ ] **Step 3: Start process B from a copy, then stop A's competitor after ~2 minutes**

```bash
cp -R ~/Documents/whatsapp-gateway-lab/run ~/Documents/whatsapp-gateway-lab/run-b
cd ~/Documents/whatsapp-gateway-lab/run-b
../gowa-elphant rest --port 3032 -b "lab:$(cat .basic-pass)" --db-uri "file:storages/whatsapp.db?_foreign_keys=on" --debug=true --auto-download-media=false > gowa-b.log 2>&1
```

Expected in `gowa-a.log`: `[STREAM_REPLACED] Device ... was opened elsewhere`. Process A keeps answering `GET /devices` (with basic auth) and its device shows as disconnected. Process B stays connected.

- [ ] **Step 4: Confirm there is no reconnect fight**

Leave both running for 6 minutes. Then run:
`grep -ciE 'stream_replaced|STREAM_REPLACED' ~/Documents/whatsapp-gateway-lab/run/gowa-a.log ~/Documents/whatsapp-gateway-lab/run-b/gowa-b.log`

Expected: exactly one StreamReplaced in A's log and none in B's. More than one means the 5-minute checker reconnected A.

- [ ] **Step 5: End the test on B's state**

Stop A, then B. Replace the lab database with B's (the newest Signal state), then delete the copy:

```bash
rm -rf ~/Documents/whatsapp-gateway-lab/run/storages
mv ~/Documents/whatsapp-gateway-lab/run-b/storages ~/Documents/whatsapp-gateway-lab/run/storages
rm -rf ~/Documents/whatsapp-gateway-lab/run-b
```

Report the result to the owner, including the log counts above. Do not print message content or credentials from the logs.
