# G9: panic containment, per-device reconnect watchdog, device health and metrics

> **Status:** approved by the owner in chat (scope and the Basic Auth decision); implemented per
> `docs/plans/2026-09-30-gateway-g9-resilience.md`.

## Problem

- 39 production goroutines are launched with `go`; only four files recover panics. A panic in any
  goroutine GOWA starts (receipts, presence, groups, newsletters, webhook delivery, storing a sent
  message) ends the whole process and every device with it. whatsmeow already contains panics in
  its own event handlers; the goroutines GOWA opens are not covered.
- The reconnect loop (`SetAutoReconnectChecking`) watches only the default client. A second
  device that drops and is not recovered by whatsmeow's own auto reconnect stays down.
- `/health` is public and answers OK or 503 without saying which device is unwell.
- There are no metrics.

## Decisions

| # | Decision | Alternative rejected |
|---|---|---|
| D1 | `pkg/safego`: `Go(name, fn)` for named functions and `defer safego.Recover(name)` as the first statement of a goroutine closure; a panic is logged with its stack, counted, and the goroutine ends | Restart goroutines after a panic (hides a bug that will repeat) |
| D2 | A test walks the source and fails on a `go` statement that is not protected | Trust reviews |
| D3 | One watchdog in the fork replaces the default-client loop: every `WHATSAPP_WATCHDOG_INTERVAL_SECONDS` (default `120`, `0` disables) it reconnects each paired, disconnected device that is not in `StreamReplaced`, with a wait that grows per device (interval, 2x, 4x ... capped at 15 minutes) and resets when the device connects | Keep the loop, add a loop per device |
| D4 | Never connect a device that is not paired (`Store.ID == nil`): a connect would start a pairing | Connect anything disconnected |
| D5 | `GET /health/devices` behind Basic Auth: per device id, state, connected, logged in, stream replaced, last connected time, reconnect attempts, next attempt time; `/health` stays public and unchanged | Put the detail on the public route |
| D6 | `GET /metrics` in the Prometheus text format, written by hand, behind Basic Auth; no new dependency (`go.mod` unchanged) | The Prometheus client library |

## Watchdog rules

Per device, on each tick at time `now`:

| Device | Action |
|---|---|
| connected | reset attempts and wait; record the last connected time |
| not paired, or no client, or `StreamReplaced` | skip (no attempt, no state change) |
| disconnected, paired, and `now` before the next attempt time | skip |
| disconnected, paired, due | `Connect()`; count the attempt; on success count a success; set the next attempt time to `now + min(15m, interval * 2^(attempts-1))` |

A device removed from the manager is forgotten. The watchdog is a single goroutine started with
the server; a panic in a tick is contained by `safego` and the next tick runs.

## Contract

`GET /health/devices` (Basic Auth):

```json
{"code":"SUCCESS","message":"Device health","results":{"devices":[
  {"id":"...","state":"logged_in","connected":true,"logged_in":true,"stream_replaced":false,
   "last_connected_at":"2026-09-30T12:00:00Z","reconnect_attempts":0,"next_attempt_at":null}],
  "panics_recovered":0}}
```

`GET /metrics` (Basic Auth, `text/plain; version=0.0.4`):

```
gowa_goroutine_panics_total N
gowa_reconnect_attempts_total N
gowa_reconnect_successes_total N
gowa_devices{state="logged_in"} N
```

with `# HELP` and `# TYPE` lines. The device ids never appear in the metrics.

## Layers

| Layer | Change |
|---|---|
| `pkg/safego` (new) | `Go`, `Recover`, `Panics`, the source walking test |
| `infrastructure/whatsapp/device_watchdog.go` (new) | the watchdog, its snapshot, counters |
| `ui/rest/health.go` (new) | the two routes over a small provider interface |
| `cmd/rest.go`, `cmd/helpers.go`, `ui/rest/helpers/common.go` | start the watchdog, mount the routes, remove the default-client loop |
| the 39 goroutine sites | protected as in D1 |
| `config`, `cmd/root.go`, `.env.example` | `WHATSAPP_WATCHDOG_INTERVAL_SECONDS` |

## Tests

- `safego`: `Go` and `Recover` contain a panic, count it and log the name; a normal run counts
  nothing; the source walk fails on an unprotected `go` statement (checked on a temporary file).
- Watchdog with fake devices and a fake clock: connected resets; unpaired, replaced and client-less
  are skipped; a due device is connected and counted; the wait grows 2x per failure and is
  capped; a success resets the wait; a removed device is forgotten; a panic in `Connect` does not
  stop the tick.
- Health and metrics: the JSON shape, the text format (types, labels, no device ids), a nil
  provider answers empty, both need auth at the router (`TestDeviceGroupIsRegisteredLast` still
  holds).

## Out of scope

- Restarting a goroutine after a panic, tracing, per-route HTTP metrics, alert rules.
- ElphantCRM changes.

## Release

`v9.5.0-elphant.11`, only with the owner's OK. Runtime check: `/health/devices` and `/metrics`
with credentials, and the service log of the watchdog tick.
