# G6: reach-out timelock

> **Status:** written for the owner's review, no code yet. Plan: to write after approval.
> **Clean-room:** this design uses only whatsmeow (MPL), upstream GOWA (MIT) and what the
> gateway observes. `devlikeapro/gows-plus` has no license and was not read.

## Problem

WhatsApp can restrict an account from starting new chats (the reach-out timelock). A send to a
recipient with no prior conversation is then refused with server error 463. Today:

- upstream GOWA already turns a 463 into `WA_REACHOUT_TIMELOCK` (HTTP 429, `ErrWaReachoutTimelock`,
  `usecase/send.go: normalizeSendError`);
- whatsmeow already dispatches `events.NotifyAccountReachoutTimelock` (`EnforcementType`,
  `IsActive`, `TimeEnforcementEnds`) when WhatsApp tells the session about the restriction, and
  GOWA ignores it;
- nothing stops a consumer from sending a whole broadcast into a restricted account, each send
  ending in a 463 that also counts against the account.

## What the libraries give

- `events.NotifyAccountReachoutTimelock` on the device's event stream.
- `Store.PrivacyTokens.GetPrivacyToken(ctx, jid)`: the trusted-contact token (`tctoken`) stored
  for a recipient, keyed by LID when a LID mapping exists. A token older than the 28-day window
  (four 7-day buckets) is expired; whatsmeow's own cutoff helpers are private, so the gateway
  repeats that rule with the same constants.
- No call to **query** the timelock state exists. The mother spec's "query on `Connected`" is
  dropped: it would need protocol details that are not in the free sources (see D-3 in
  `docs/elphant-debt.md`).

## Decisions

| # | Decision | Alternative rejected |
|---|---|---|
| D1 | Per-device in-memory timelock state: `active`, `source` (`event` or `send_463`), `enforcement_type`, `ends_at` | Persist it (a restart loses it until the next event or 463; the cost of persisting is not worth it) |
| D2 | The state comes from the event, and from a 463 on a send (`source=send_463`) | Only the event (WhatsApp may not send it) |
| D3 | A state with no `ends_at` (a 463, or an event without it) expires by itself after `WHATSAPP_REACHOUT_SUSPECT_MINUTES` (default `30`) | Stay locked until an event clears it |
| D4 | New webhook `session.timelock`, sent on transitions only | Fold it into `session.status` (a different lifecycle, and consumers filter by event name) |
| D5 | Send guard `409`, opt-in (`WHATSAPP_REACHOUT_GUARD`, default `false`), active only while the device is locked and the recipient has no valid `tctoken` | Block whenever there is no `tctoken` (blocks sends WhatsApp would accept) |
| D6 | The guard runs in `wrapSendMessage`, the one place every message send goes through | One check per send method |
| D7 | No query endpoint in this step: the webhook and the `409`/`429` errors are the signals | `GET` of the state (add later if a consumer needs it) |

## Behavior

**State machine** (per device, guarded by a mutex; `now` injectable in tests):

| Input | Result |
|---|---|
| event `IsActive=true` | `active`, `source=event`, `enforcement_type` and `ends_at` from the event; with no `ends_at`, expires at `now + suspect` |
| event `IsActive=false` | cleared |
| send refused with 463 | `active`, `source=send_463`, `ends_at = now + suspect`, `enforcement_type` unchanged if already active |
| time passes `ends_at` | cleared on the next read |
| device logged out or removed | cleared with the device instance |

`session.timelock` is emitted when the state changes: cleared to active, active to cleared, or
active with a different `ends_at` or type. Repeated identical inputs (a second 463 in the same
window) emit nothing. A clearing caused by time emits on the next read that notices it (the next
send or event), not from a timer.

**Webhook** (same envelope as `session.status`: `event`, `device_id`, `session_id`, `timestamp`,
`payload`; same delivery path, durable when enabled):

```json
{"event":"session.timelock","device_id":"...","session_id":"...","timestamp":"...",
 "payload":{"active":true,"source":"event","enforcement_type":"...","ends_at":"2026-10-01T12:00:00Z"}}
```

`enforcement_type` and `ends_at` are `null` when unknown. The device's `webhook_events` filter
must include `session.timelock`, as for any event.

**Guard** (only when `WHATSAPP_REACHOUT_GUARD=true`): in `wrapSendMessage`, before the send, when
the device state is active and the recipient is a user JID (phone or LID; not a group, newsletter
or status) and there is no valid `tctoken` for it, return `409` with code `WA_REACHOUT_GUARD` and a
message that says the account is restricted from starting new chats and when it ends, if known. A
recipient with a valid token, a group, and any send while the state is cleared go through
unchanged. The token lookup uses the LID when a mapping exists, like whatsmeow. A lookup error
lets the send through (the guard only blocks on a clear "no token" answer).

## Layers (AGENTS.md contract)

| Layer | Change |
|---|---|
| `infrastructure/whatsapp/reachout_state.go` (new) | the state, its transitions, `TimelockSnapshot`, expiry |
| `infrastructure/whatsapp/event_session.go` (or a new `event_timelock.go`) | map `NotifyAccountReachoutTimelock` to the state; `EmitSessionTimelock` through the existing dispatch |
| `infrastructure/whatsapp/reachout_guard.go` (new) | `ShouldBlockSend(ctx, instance, client, recipient)`: pure decision with injectable token lookup |
| `pkg/error/whatsapp_error.go` | `WaReachoutGuardError` (`WA_REACHOUT_GUARD`, `409`) |
| `usecase/send.go` | `wrapSendMessage`: guard before the send, mark the state on a 463 |
| `config/settings.go`, `cmd/root.go`, `.env.example` | `WhatsappReachoutGuard`, `WhatsappReachoutSuspectMinutes` |
| `docs/reference/elphant-fork.md` | webhook contract, guard, limits |

## Tests

- State machine: event active with and without `ends_at`, event inactive, 463 marking, expiry,
  repeated inputs emit once, transitions emit.
- Webhook body and event name; delivery through the existing dispatch replacement hook used by
  the `session.status` tests.
- Guard decision (fake token lookup and clock): blocked (locked, user JID, no token); allowed
  with a valid token, with an expired token (blocked), for a group, for a newsletter, while
  cleared, with the flag off, and on a lookup error (allowed).
- `wrapSendMessage`: a 463 marks the state and still returns `WA_REACHOUT_TIMELOCK` (`429`); the
  guard returns `409` without calling the client.
- Config: default off, default suspect window 30.

## Out of scope

- Querying the timelock state from WhatsApp (no free source).
- Persisting the state across restarts.
- A `GET` endpoint for the state; MCP tools.
- ElphantCRM changes: add `session.timelock` to the webhook events and show the state (separate
  spec; the CRM can already react to `WA_REACHOUT_TIMELOCK` and `WA_REACHOUT_GUARD` errors).

## Release

`v9.5.0-elphant.10`, only with the owner's OK. The real timelock cannot be provoked, so the
runtime check covers what can be seen: the guard flag stays off in the stack, and the owner
decides when to turn it on. The 463 path and the event are covered by tests with fakes.
