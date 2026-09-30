# Fork phase 2, part A: StreamReplaced isolation, config log removal, session lifecycle webhooks

**Date:** 2026-09-24
**Branch:** `elphant`
**Status:** approved design, awaiting implementation plan

## 1. Context

This repository is a fork of `aldinokemal/go-whatsapp-web-multidevice` (GOWA). The fork is
maintained as its own product:

- `main` mirrors upstream and never receives fork commits.
- `elphant` is the latest upstream tag plus the fork's commits, rebased on each new upstream tag.
  It starts at v9.4.0 plus the `import-baileys` work.
- No pull requests or issues are sent upstream.

Upstream ships a release every one to two weeks, so every fork change is a permanent rebase cost.
The rule that keeps that cost low applies to everything in this spec: fork logic lives in **new
files**, and upstream files only receive short hooks of a few lines. The upstream files touched
here and their churn over the last six months: `event_handler.go` (12 commits),
`webhook_forward.go` (15), `cmd/root.go` (15), `device_instance.go` (3),
`ui/rest/helpers/common.go` (0), `usecase/app.go` (5).

Part A covers three changes. Part B (image publishing, rebase automation, deployment) is a
separate spec.

| # | Change | Deliverable |
|---|---|---|
| A1 | StreamReplaced disconnects only the affected device | Commit on `elphant` |
| A2 | Stop printing the whole configuration at startup | Commit on `elphant` |
| A3 | `session.status` webhook, connection events | Commit(s) on `elphant` |
| A4 | `session.status` webhook, pairing events | Commit(s) on `elphant` |

## 2. A1: StreamReplaced isolation

### Problem

`handleStreamReplaced` (`src/infrastructure/whatsapp/event_handler.go:306-308`) calls
`os.Exit(0)`. One session opened elsewhere kills the process and every other session with it.
Under a restart policy the process comes back, reconnects, steals the session back, and the two
holders of the credential fight (upstream issue #449 shows the resulting restart loop).

Removing `os.Exit` alone is not enough. whatsmeow already treats `conflict type="replaced"` as an
expected disconnect and does not reconnect by itself, but the global checker in
`src/ui/rest/helpers/common.go:29-42` calls `Connect()` on the default client every 5 minutes when
it is disconnected. Without a guard, that checker would restart the fight every 5 minutes.

### Behavior

After a StreamReplaced the device stays disconnected until an operator reconnects it. Other
devices are not affected.

- The device is marked `streamReplaced` and its state becomes `disconnected`.
- The session stays in the store. No extra `Disconnect()` call: whatsmeow already closed the socket.
- A websocket broadcast with code `DEVICE_STREAM_REPLACED` and `{device_id}` lets the embedded UI
  show the state, following the `DEVICE_LOGGED_OUT` pattern.
- The 5-minute checker skips a marked device.
- The mark is cleared on `events.Connected`. Any successful reconnect clears it
  (`GET /app/reconnect`, `POST /devices/:id/reconnect`, login), with no usecase changes.
- The mark lives in memory only. A process restart reconnects the device through the existing
  boot auto-connect; restarting is an explicit operator decision.
- No flag. This is the default behavior.

### Code

New file `src/infrastructure/whatsapp/stream_replaced.go`:

- `handleStreamReplaced(instance *DeviceInstance)`: `Warn` log with the device id, mark, set state,
  broadcast.
- `ShouldAutoReconnect(cli *whatsmeow.Client) bool`: finds the `DeviceInstance` that owns `cli`
  through the device manager and returns `false` when it is marked. Returns `true` when no
  instance is found, which keeps today's behavior for unknown clients.

Hooks in upstream files:

- `device_instance.go`: field `streamReplaced bool` guarded by the existing `mu`, plus
  `MarkStreamReplaced()`, `ClearStreamReplaced()` and `StreamReplaced() bool`.
- `event_handler.go`: the `StreamReplaced` case calls `handleStreamReplaced(instance)`; the
  `Connected` case calls `instance.ClearStreamReplaced()` before `handleConnectionEvents`. The
  `os` import goes away if nothing else uses it.
- `ui/rest/helpers/common.go`: the checker loop skips the reconnect when
  `!whatsapp.ShouldAutoReconnect(cli)`. The checker's scope stays the default client only.

## 3. A2: Config log removal

`src/cmd/root.go:80` runs `fmt.Println(viper.AllSettings())` on every start. It prints basic auth
credentials, `DB_URI`, and webhook secrets to stdout, which ends up in container logs. It came in
with `31c57f4` (2025-06-23) as an apparent debug leftover. The line is removed with no replacement.

## 4. A3: `session.status`, connection events

### Envelope

Same envelope as the other events, with one difference: `session_id` is set directly from
`instance.ID()`. It is present even when the JID is missing or does not map to a slot, and the
existing `addWebhookSessionID` enrichment leaves it untouched because the key already exists.

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

`device_id` is the device's non-AD JID, or an empty string before pairing. `timestamp` is RFC3339,
the time of emission.

### Payload contract

The five payload keys are always present. A key that does not apply is `null`, never omitted, and
never changes type.

| Key | Type | Meaning |
|---|---|---|
| `status` | string | One of the values in the tables below |
| `reason` | string or null | Human-readable cause |
| `code` | integer or null | Numeric code from WhatsApp |
| `expires_at` | string (RFC3339) or null | When a ban or a QR code expires |
| `qr_code` | string or null | Raw QR content, only for `status = "qr"` (A4) |

### Mapping

| whatsmeow event | `status` | `code` | `reason` and extras |
|---|---|---|---|
| `Connected` | `connected` | null | null |
| `Disconnected` | `disconnected` | null | null |
| `LoggedOut` | `logged_out` | `int(Reason)` (401, 403, 406, ...) | `Reason.String()` |
| `StreamReplaced` | `stream_replaced` | null | null |
| `TemporaryBan` | `temporary_ban` | `int(Code)` (101 to 106) | `Code.String()`; `expires_at` = now + `Expire`, null when `Expire` is 0 |
| `ConnectFailure` | `connect_failure` | `int(Reason)` | `Message` when not empty, else `Reason.String()` |
| `KeepAliveTimeout` | `keepalive_timeout` | null | null; emitted only when `ErrorCount == 1` |
| `KeepAliveRestored` | `keepalive_restored` | null | null |
| `ClientOutdated` | `client_outdated` | 405 | null |
| `PairSuccess` | `pair_success` | null | null |

Notes:

- whatsmeow dispatches exactly one of `LoggedOut`, `TemporaryBan`, `ClientOutdated` or
  `ConnectFailure` per connect failure, so a single failure never produces two statuses.
- whatsmeow dispatches `Disconnected` only for unexpected drops, followed by its own automatic
  reconnect. Expected disconnects (StreamReplaced, logout, manual `Disconnect()`) do not produce
  it, so there are no duplicate events.
- `KeepAliveTimeout` fires on every failed keepalive (every ~20 to 30 s). Emitting only the first
  one and pairing it with `keepalive_restored` gives consumers an open/close pair without a flood.
- Not emitted: the generic `StreamError` and `CATRefreshError` (Messenger only). Both stay in the
  logs.

### Delivery and filtering

- Sent through `forwardPayloadToConfiguredWebhooks(ctx, body, "session.status")`, so HMAC signing,
  per-device webhook config, `WHATSAPP_WEBHOOK_DEVICE_MERGE_GLOBAL` and the event whitelist work
  unchanged.
- Sent from a goroutine with a 30 s timeout, like `message.ack`, so the whatsmeow event handler
  never blocks. Durable delivery is out of scope (see section 7).
- Filterable through `WHATSAPP_WEBHOOK_EVENTS` and the per-device event list. Never forwarded to
  Chatwoot.
- On by default, like every event upstream has added (`call.offer`, `label.*`, `newsletter.*`).

### Code

New file `src/infrastructure/whatsapp/event_session.go`:

- An exported `SessionStatus` type holding the five payload keys (exported because `usecase/app.go`
  builds QR statuses in A4), status name constants, and a pure mapping function from whatsmeow
  events.
- `EmitSessionStatus(ctx, instance *DeviceInstance, status sessionStatus)`: builds the envelope and
  starts the delivery goroutine. Exported because A4 calls it from `usecase/app.go`.
- `handleSessionEvent(ctx, instance, rawEvt)`: the mapping above, including the `ErrorCount == 1`
  rule.

Hook in `event_handler.go`: one call to `handleSessionEvent(ctx, instance, rawEvt)` at the top of
`handler`, before the existing `switch`, so the existing cases (`Connected`, `LoggedOut`,
`StreamReplaced`, `PairSuccess`) keep their current behavior and the switch gains no new cases.

`EmitSessionStatus` builds the envelope synchronously, before starting the goroutine. Because the
hook runs before the `switch`, a `logged_out` event still carries the device JID: `handleLoggedOut`
clears the persisted JID only afterwards.

## 5. A4: `session.status`, pairing events

### Mapping

| Source | `status` | Extras |
|---|---|---|
| `GetQRChannel` loop in `src/usecase/app.go`, item with `Event == "code"` | `qr` | `qr_code` = `evt.Code`; `expires_at` = now + `evt.Timeout` |
| Same loop, `QRChannelTimeout` | `qr_timeout` | none; the codes ran out (~160 s) without a scan |
| `PairPasskeyRequest` | `passkey_required` | none; the challenge is read from `GET /app/passkey` |
| `PairPasskeyConfirmation` | `passkey_confirmation` | none; the 6-digit code is read from `GET /app/passkey` |
| `PairError` | `pair_error` | `reason` = `Error.Error()` |
| `PairPasskeyError` | `pair_error` | `reason` = `Error.Error()` |

The QR loop has an upstream flaw that would hide most of these events. It hands each QR image path
to the HTTP caller through `chImage` (buffer 1) with a blocking `select`, but the caller reads only
the first one. From the third code on, the loop blocks until the 3-minute `qrCtx` expires and stops
reading the channel, so later codes and `QRChannelTimeout` are never seen. A4 makes that send
non-blocking (`default:` drops the path when nobody is waiting). The first image still reaches the
HTTP caller; the loop keeps reading until whatsmeow closes the channel.

Phone-number pairing codes are not emitted: `LoginWithCode` already returns the code in the HTTP
response.

The QR loop emits one `qr` event per code as the channel hands it out, which follows the channel's
own rotation (first code ~60 s, then ~20 s each).

### Per-device webhook before pairing

Before pairing the device has no JID, so `getWebhookConfigForDevice(deviceJID)` returns no config
and the event would reach only the global webhook. Hook in `forwardPayloadToConfiguredWebhooks`
(`webhook_forward.go`, about 5 lines): when `session_id` is present (only session.status bodies
carry it at routing time), the slot's webhook config wins over the JID lookup; this also covers a
`logged_out` whose record lost its JID to the keep-slot cleanup. Previously written rule, kept for
history: when `device_id` is empty and `session_id` is present, look
up the config with the existing `GetDeviceWebhookConfig(session_id)` (`GetDeviceRecord` does not load
the webhook columns). The JID path does not change. No
repository interface change, so the three storage layers stay untouched.

Tests cover this path through `DeviceManager.storage` with a stub that mirrors the real column behavior.

### Code

- `event_session.go`: `handleSessionEvent` also maps `PairPasskeyRequest`,
  `PairPasskeyConfirmation`, `PairError` and `PairPasskeyError`.
- `usecase/app.go`: the QR loop calls `whatsapp.EmitSessionStatus` for `code` items and for
  `QRChannelTimeout`, and the `chImage` send becomes non-blocking. Three short hooks; the package is
  already imported.

### Security

`qr_code` travels in the HMAC-signed webhook body. The raw QR text lets its holder render the QR
image; pairing still requires the account owner's phone to scan it. The fork doc recommends HTTPS
webhook targets.

## 6. Documentation

New file `docs/reference/elphant-fork.md`, the list of fork differences from upstream. It covers:

- StreamReplaced behavior (device stays down until a manual reconnect) and the fact that a
  container restart policy is no longer needed to survive it.
- The `session.status` event: envelope, payload contract, both mapping tables.
- HTTPS recommendation for webhook targets receiving `qr_code`.

Upstream docs (`docs/webhook-payload.md`, `README.md`) are not edited, so rebases stay clean.

## 7. Out of scope

- Durable webhook delivery (outbox, event id, retry, dead-letter). Until then, a `session.status`
  that fails after the existing ~15 s retry is lost, like every other event today.
- Stable message payload fields, LID endpoints, reachout timelock, batch number check,
  authenticated `/statics`, goroutine `recover`.
- Image publishing, rebase automation, deployment (part B).

## 8. Testing and verification

### Automated tests

New test files, using the existing colocated helpers and seams. Tests that change config or
package globals stay serial and restore them.

- `stream_replaced_test.go`
  - StreamReplaced marks only the affected device and sets it `disconnected`; a second device is
    unchanged; the process keeps running.
  - `Connected` clears the mark.
  - `ShouldAutoReconnect` is `false` for a marked device, `true` for an unmarked one, `true` for a
    client with no instance.
- `event_session_test.go`
  - One case per mapping row in sections 4 and 5, checking `status`, `code`, `reason`,
    `expires_at`, `qr_code`, and that all five keys are present.
  - `KeepAliveTimeout` emits only when `ErrorCount == 1`.
  - `WHATSAPP_WEBHOOK_EVENTS` filtering drops or keeps `session.status`.
  - The event is not forwarded to Chatwoot.
  - `session_id` comes from `instance.ID()` even when the JID is empty.
- Slot lookup (in a new test file): empty `device_id` with a `session_id` uses the slot's device
  webhook config; the JID lookup behaves as before.

Gate: `go vet ./...` and `go test ./...` from `src/` (the `webhook_forward.go` hook touches a shared
contract).

### Runtime verification

Run locally against a throwaway webhook receiver.

1. **Pairing events, no pairing done.** Create a new device slot, request a QR, do not scan it.
   Expected: one `qr` per code, then `qr_timeout`. Safe at any time.
2. **StreamReplaced, real session.** Only with the device owner's explicit approval, and only when
   no other process is running that session. Start a second process from a copy of the session
   database for about two minutes. Expected: the first process stays up, its device reports
   `stream_replaced`, no reconnect attempt after 5 minutes; the second process stays connected.
