# G6 Reach-out Timelock Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans (inline, chosen by the owner). Steps use checkbox (`- [ ]`) syntax.

**Goal:** the gateway tracks the account's reach-out timelock, reports it as `session.timelock`, and can refuse sends to recipients without a `tctoken` while the account is locked.

**Architecture:** a small per-device state (`reachoutState`) fed by whatsmeow's `NotifyAccountReachoutTimelock` event and by 463 send failures; a pure guard decision over injectable token and LID lookups; `wrapSendMessage` calls the guard before sending and marks the state on a 463.

**Tech Stack:** Go 1.26 (`GOTOOLCHAIN=auto`, from `src/`), whatsmeow, testify-free stdlib tests in `infrastructure/whatsapp` (match neighbors).

**Spec:** `docs/specs/2026-09-30-gateway-g6-reachout-timelock-design.md` (clean-room: no `gows-plus`).

## Global Constraints

- Run Go from `src/` with `GOTOOLCHAIN=auto`; `src/go.mod` unchanged.
- Guard off by default (`WHATSAPP_REACHOUT_GUARD=false`); suspect window default 30 minutes.
- Guard blocks only when the state is active, the recipient is a user JID (phone or LID) and there is no valid `tctoken`; a token lookup error lets the send through.
- `tctoken` validity repeats whatsmeow's rule: 7-day buckets, 4 buckets (28 days).
- No phones, LIDs or tokens in logs. Tests that swap globals restore them; no `t.Parallel()` there.
- Commits in English, no attribution. Push, tag and release only with the owner's OK.

## Review Focus

- A 463 while the state is already active must not emit a second `session.timelock` (Task 1).
- An expired state must read as cleared and emit the clearing once (Task 1).
- The guard must never block a group, a newsletter, or a recipient with a valid token (Task 2).
- A token lookup error must let the send through (Task 2).
- The flag off must leave `wrapSendMessage` behavior unchanged (Task 3).

## File map

| File | Responsibility |
|---|---|
| `infrastructure/whatsapp/reachout_state.go` (+ test) | state, transitions, expiry, snapshot |
| `infrastructure/whatsapp/event_timelock.go` (+ test) | event mapping, `session.timelock` body and emission, `NoteReachoutTimelock` |
| `infrastructure/whatsapp/reachout_guard.go` (+ test) | token validity, guard decision, `CheckReachoutGuard` |
| `pkg/error/whatsapp_error.go` | `WaReachoutGuardError` |
| `usecase/send.go` (+ test) | guard before send, mark on 463 |
| `config/settings.go`, `cmd/root.go`, `.env.example` | flag and suspect minutes |
| `docs/reference/elphant-fork.md`, `docs/elphant-debt.md` | docs |

---

### Task 1: State machine and `session.timelock`

**Produces:** `ReachoutSnapshot{Active bool; Source, EnforcementType string; EndsAt *time.Time}`; on `*DeviceInstance`: `ReachoutSnapshot(now time.Time) ReachoutSnapshot`; package funcs `HandleReachoutTimelockEvent(ctx, instance, *events.NotifyAccountReachoutTimelock, now)`, `NoteReachoutTimelock(ctx, instance)` (463 marking); `SessionTimelockEvent = "session.timelock"`.

- [ ] Tests (`reachout_state_test.go`, table and sequence tests on `reachoutState` with an injected `now` and suspect duration):
  - event active with `ends_at` → active, `source=event`, ends known;
  - event active without `ends_at` → expires at `now + suspect`, `EndsAt` nil;
  - event inactive → cleared;
  - 463 when cleared → active, `source=send_463`, `EndsAt` nil;
  - 463 when already active → unchanged, reports no change;
  - identical event twice → second reports no change; different `ends_at` or type → change;
  - time passes the expiry → snapshot is cleared and reports `expired` once;
  - `Snapshot` of a never-touched state → inactive.
- [ ] Tests (`event_timelock_test.go`, pattern of `event_session_test.go`: `captureSessionWebhooks`, `trackSessionDispatch`): `handler` given a `*events.NotifyAccountReachoutTimelock{IsActive: true, EnforcementType: "x", TimeEnforcementEnds: <unix>}` emits one `session.timelock` body with `payload{active, source:"event", enforcement_type, ends_at}` and the device ids; the same event again emits nothing; an inactive event emits `active:false`; `NoteReachoutTimelock` emits `source:"send_463"` once and not twice; a nil instance is a no-op.
- [ ] Implement `reachout_state.go`:

```go
package whatsapp

import (
	"sync"
	"time"
)

// Fork (elphant): per-device reach-out timelock state (spec 2026-09-30-gateway-g6).
// In memory: a restart forgets it until the next event or 463.
const (
	ReachoutSourceEvent   = "event"
	ReachoutSourceSend463 = "send_463"
)

type ReachoutSnapshot struct {
	Active          bool
	Source          string
	EnforcementType string
	EndsAt          *time.Time // nil when WhatsApp gave no end
}

type reachoutState struct {
	mu        sync.Mutex
	active    bool
	source    string
	typ       string
	until     time.Time // when the state expires
	endsKnown bool      // until came from WhatsApp, not from the suspect window
}

// clearLocked resets the fields; the mutex itself must never be overwritten while held.
func (s *reachoutState) clearLocked() {
	s.active, s.source, s.typ = false, "", ""
	s.until, s.endsKnown = time.Time{}, false
}

func (s *reachoutState) snapshotLocked() ReachoutSnapshot {
	snap := ReachoutSnapshot{Active: s.active, Source: s.source, EnforcementType: s.typ}
	if s.active && s.endsKnown {
		ends := s.until
		snap.EndsAt = &ends
	}
	return snap
}

// expireLocked clears an expired state and reports whether it did.
func (s *reachoutState) expireLocked(now time.Time) bool {
	if s.active && !now.Before(s.until) {
		s.clearLocked()
		return true
	}
	return false
}

// read returns the snapshot at now and whether reading it cleared an expired state.
func (s *reachoutState) read(now time.Time) (ReachoutSnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	expired := s.expireLocked(now)
	return s.snapshotLocked(), expired
}

// applyEvent applies WhatsApp's notification and reports whether the visible state changed.
func (s *reachoutState) applyEvent(now time.Time, suspect time.Duration, active bool, typ string, ends time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked(now)
	before := s.snapshotLocked()
	if !active {
		s.clearLocked()
	} else {
		s.active, s.source, s.typ = true, ReachoutSourceEvent, typ
		if ends.IsZero() {
			s.until, s.endsKnown = now.Add(suspect), false
		} else {
			s.until, s.endsKnown = ends, true
		}
	}
	return !sameSnapshot(before, s.snapshotLocked())
}

// mark463 records a send refused with 463 and reports whether the state changed. An already
// active state is left as it is.
func (s *reachoutState) mark463(now time.Time, suspect time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked(now)
	if s.active {
		return false
	}
	s.active, s.source, s.typ = true, ReachoutSourceSend463, ""
	s.until, s.endsKnown = now.Add(suspect), false
	return true
}

func sameSnapshot(a, b ReachoutSnapshot) bool {
	if a.Active != b.Active || a.Source != b.Source || a.EnforcementType != b.EnforcementType {
		return false
	}
	if (a.EndsAt == nil) != (b.EndsAt == nil) {
		return false
	}
	return a.EndsAt == nil || a.EndsAt.Equal(*b.EndsAt)
}
```

  A `reachout reachoutState` field on `DeviceInstance` (value, own mutex) and `func (d *DeviceInstance) ReachoutSnapshot(now time.Time) ReachoutSnapshot` returning `read(now)`'s first value (emitting the clearing is done by the callers that hold a context, see `CheckReachoutGuard` in Task 2 and the event path).
- [ ] Implement `event_timelock.go`: `SessionTimelockEvent`, `suspectWindow()` (`time.Duration(config.WhatsappReachoutSuspectMinutes) * time.Minute`, minimum 1 minute), `timelockPayload(ReachoutSnapshot)` (keys `active`, `source`, `enforcement_type`, `ends_at`; nil as JSON null; `source` is `null` when cleared), `EmitSessionTimelock(ctx, instance, snap)` mirroring `EmitSessionStatus` (same `canonicalInstance`, same `sessionStatusDispatch`, event name `session.timelock`), `HandleReachoutTimelockEvent`, `NoteReachoutTimelock`. Both use `canonicalInstance(instance)` for the state and emit only when the state changed. `handleSessionEvent` also routes `*events.NotifyAccountReachoutTimelock` to `HandleReachoutTimelockEvent`.
- [ ] Config (same task, needed by `suspectWindow`): `WhatsappReachoutSuspectMinutes = 30` and `WhatsappReachoutGuard = false` in `config/settings.go`; viper keys `whatsapp_reachout_suspect_minutes`, `whatsapp_reachout_guard` and flags `--whatsapp-reachout-suspect-minutes`, `--whatsapp-reachout-guard` in `cmd/root.go`; `.env.example` lines for both.
- [ ] Run `go test ./infrastructure/whatsapp/ -run 'Reachout|Timelock' -race`; commit `feat(whatsapp): track the reach-out timelock and report it as session.timelock`.

---

### Task 2: Guard decision

**Produces:** `type reachoutLookups struct{ Token func(ctx, types.JID) (*store.PrivacyToken, error); LID func(ctx, types.JID) (types.JID, error) }`; `reachoutBlocks(ctx, enabled bool, snap ReachoutSnapshot, recipient types.JID, lookups reachoutLookups, now time.Time) error`; `CheckReachoutGuard(ctx, instance, client, recipient) error`; `pkgError.WaReachoutGuardError`, `pkgError.NewWaReachoutGuard(endsAt *time.Time)`.

- [ ] Tests (`reachout_guard_test.go`, fake lookups, fixed `now`): blocked (enabled, active, user JID, no token); allowed with a valid token; blocked with an expired token (older than the 28-day cutoff); allowed for a group JID and a newsletter JID; allowed while cleared; allowed with the flag off; allowed on a token lookup error; the LID is used for the token lookup when a mapping exists (fake records the JID asked); LID JID recipient looks up as is; the error is `409` with code `WA_REACHOUT_GUARD` and, when `EndsAt` is known, the message carries it; `CheckReachoutGuard` with a nil instance or nil client is a no-op.
- [ ] Implement `pkg/error`: `WaReachoutGuardError string` with `ErrCode() = "WA_REACHOUT_GUARD"` and `StatusCode() = http.StatusConflict`; `NewWaReachoutGuard(endsAt *time.Time)` builds the message ("The account is restricted from starting new chats (reach-out timelock)" plus " until <RFC3339>" when known, plus the hint to reply inside an existing conversation or wait).
- [ ] Implement `reachout_guard.go`: `tcTokenValid(t store.PrivacyToken, now)` with `bucket = 604800`, `numBuckets = 4`, `cutoff = time.Unix((now.Unix()/bucket-(numBuckets-1))*bucket, 0)`, valid when `len(Token) > 0 && !t.Timestamp.IsZero() && !t.Timestamp.Before(cutoff)`; `reachoutBlocks` as described (user JID = server `s.whatsapp.net` or `lid`; token keyed by the LID for a phone when `lookups.LID` returns one); `CheckReachoutGuard` reads `instance.ReachoutSnapshot(now)`, emits the clearing when the read expired the state, builds lookups from `client.Store.PrivacyTokens` and `client.Store.LIDs`, and calls `reachoutBlocks` with `config.WhatsappReachoutGuard`.
- [ ] Run tests with `-race`; commit `feat(whatsapp): refuse sends to recipients without a tctoken while the account is timelocked`.

---

### Task 3: Wire `wrapSendMessage`

- [ ] Tests (`usecase/send_reachout_test.go`): `noteReachoutFailure(ctx, err)` marks the state for `fmt.Errorf("%w %d", whatsmeow.ErrServerReturnedError, 463)` and not for another error or nil (assert through `instance.ReachoutSnapshot`); a `wrapSendMessage` call with the guard enabled, the device locked (`ApplyReachout...` test hook or a real 463 mark), a user JID and a `&whatsmeow.Client{Store: &store.Device{...}}` with fake stores that have no token returns the guard error without sending; with the flag off the guard is skipped (the send then fails on the unconnected client, not with the guard error).
- [ ] Implement in `usecase/send.go`: in `wrapSendMessage`, before `client.SendMessage`, `if err := whatsapp.CheckReachoutGuard(ctx, instanceFromContext(ctx), client, recipient); err != nil { return whatsmeow.SendResponse{}, err }`; on a send error call `noteReachoutFailure(ctx, err)` before `normalizeSendError`. `noteReachoutFailure` calls `whatsapp.NoteReachoutTimelock(ctx, inst)` when `whatsapp.IsReachoutTimelockError(err)`.
- [ ] Run `go test ./usecase/ -race`; commit `feat(send): guard and record the reach-out timelock in wrapSendMessage`.

---

### Task 4: Docs and final checks

- [ ] `docs/reference/elphant-fork.md`: a section "Reach-out timelock" with the `session.timelock` body, the state rules, the guard flag and its conditions, the `409 WA_REACHOUT_GUARD` and the existing `429 WA_REACHOUT_TIMELOCK`, the limits (no query, restart forgets), and that `webhook_events` must include `session.timelock`.
- [ ] `docs/elphant-debt.md`: D-3 keeps only the query (no free source) and the CRM side; mention the flag off by default.
- [ ] `cd src && GOTOOLCHAIN=auto go vet ./... && GOTOOLCHAIN=auto go test ./...` → PASS.
- [ ] Commit `docs: document the reach-out timelock`.
