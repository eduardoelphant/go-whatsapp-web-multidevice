# Voice calls: concurrent calls per number and a gateway-wide cap

> **Status:** approved by the owner (2026-11-14). It builds on voice calls stage 2b
> (`docs/specs/2026-10-14-gateway-voice-calls-2b-design.md`, B12) and replaces B12's fixed cap of
> one call per number. Target release: `v9.7.0-elphant.12`, published only with the owner's OK.
> Plan: `docs/plans/2026-11-14-gateway-concurrent-calls.md`.
> D-21 (a ringing inbound call holds its slot) changes today's code behaviour and awaits the
> owner's confirmation; see D-21.

## Problem

Stage 2b caps calls with `WHATSAPP_CALLS_MAX_PER_DEVICE` (default `1`), a process-wide value read
once at start. That cap has three problems:

- **It is the same for every number.** The CRM sells calls per channel. One number may need three
  agents on calls at once while another must stay at one. Today the only lever is the env, so it
  applies to every device and needs a restart.
- **Nothing bounds the gateway as a whole.** The gateway host has 4 vCPU shared with other
  services, and the GOWA container is limited to 1 GiB. Stage 2a measured 10% to 25% of a core per
  call with one leg. The browser bridge adds a 48 kHz Opus encode, a decode and resampling, and
  that cost is unmeasured. Raising the per-device cap to 3 on ten numbers would allow 30 calls,
  far past what the host can carry.
- **A refused offer still rings the CRM.** `call.offer` leaves from `handleCallOffer` on its own
  goroutine, independently of the manager's admission. An offer the manager rejects for the cap
  still reaches the CRM as `call.offer`, and its agents ring until `call.ended` `MISSED` arrives.
- **The code and the docs disagree on ringing calls.** B12 and `elphant-fork.md` say that "above
  the cap the offer is rejected". The code only counts calls that are answered, being answered or
  outbound (`activeCountLocked` in `calls.go`). A second inbound offer while the first one still
  rings is tracked and offered to the CRM, and the cap only bites when someone answers it.

## Goal

1. The CRM sets each number's limit at runtime, without a restart: `max_calls` on
   `PUT /devices/:device_id/calls`, bounded by a platform ceiling per number.
2. A gateway-wide cap bounds the sum of simultaneous calls over every device of the process.
3. One counting rule, the same for the per-device limit and the gateway cap, with the slot taken
   atomically. Two devices can never both take the last slot.
4. Refusals are distinguishable: the webhook says whether the device or the gateway was full, and
   the outbound error text lets the CRM tell the two apart by substring.
5. A refused offer never rings an agent: it sends no `call.offer`, only `call.ended`.
6. A live measurement on the gateway host, with the owner present, tells the deploy how high the
   two caps can go.

## Shape

```
                   +-------------------- gateway slot pool ---------------------+
                   |  held = sum of the slots of every device (leaf mutex)       |
                   |  cap  = WHATSAPP_CALLS_MAX_TOTAL (0: none)                  |
                   +-------------------------------------------------------------+
                         ^ acquire / release            ^ acquire / release
   CallManager (device A, m.mu)                   CallManager (device B, m.mu)
   limit = min(max_calls of A or 1,               limit = min(max_calls of B or 1,
               WHATSAPP_CALLS_MAX_PER_DEVICE)                 WHATSAPP_CALLS_MAX_PER_DEVICE)
   held  = reserved + tracked calls               held  = reserved + tracked calls
```

A call takes one slot on its device and one in the gateway pool in the same critical section
(under its manager's `m.mu`, with the pool's mutex nested inside it). It keeps both until it ends.

## Decisions

The numbering continues the fork's D-numbers. The highest number in `docs/` before this spec was
D-18, in `docs/elphant-debt.md`.

| # | Decision | Alternative rejected |
|---|---|---|
| D-19 | `WHATSAPP_CALLS_MAX_PER_DEVICE` stays and becomes the **platform ceiling per number**. The code default stays `1`, and the deploy sets `3`. A value below 1 counts as 1. No device can be set above it. | Dropping the env and trusting the CRM's value alone. |
| D-20 | New env `WHATSAPP_CALLS_MAX_TOTAL`: the cap on simultaneous calls summed over every device of the gateway process. The code default is `0`, meaning no gateway cap, which keeps today's behaviour. The deploy sets `6`. A negative value counts as `0`. One package-level slot pool holds the count behind a leaf mutex, shared by every `CallManager`. It is read at use time and needs no restart. | Summing every manager's count at check time (it needs every manager's lock: lock-order risk and no atomicity). |
| D-21 | **What holds a slot.** Every call a manager tracks holds one slot on its device and one in the pool, from the moment it is tracked until it ends. That includes an inbound call that is still ringing. An outbound call holds its slots from the reservation before dialing. Both counts use this one rule. The device count is `reserved + len(calls)`. Inbound and outbound reserve the same way: check, then `reserved++` and a pool acquire under `m.mu`, then track the call, then hand the reservation over to the tracked call. **This changes today's behaviour:** today a ringing inbound call holds no slot (see Problem). At limit 1, a second offer while the first one rings is now refused (`MISSED`/`device_limit`), where today both ring. The owner's decision text says ringing calls count "like today's per-device count", and the code does not do that today. This is flagged for the owner in the plan's report. Flipping back to today's rule means that the inbound path checks without reserving, and `Answer` reserves. | Counting only answered calls (today's code: the CRM then shows a ringing call that cannot be answered). |
| D-22 | Per-device value: a new nullable column `max_calls INTEGER` in `elphant_device_calls`. `NULL` (and a missing row) means 1. The column is added idempotently on first use: `CREATE TABLE` carries it for new databases, and older tables get `ALTER TABLE ... ADD COLUMN max_calls INTEGER` once, after a `pragma_table_info` check. A concurrent "duplicate column name" error counts as success. Existing rows read `NULL`, so their limit stays 1. No upstream migration is touched. | An upstream numbered migration (conflicts on every sync). A `NOT NULL DEFAULT 1` column (it hides "never set"). |
| D-23 | `PUT /devices/:device_id/calls` accepts `{"enabled": bool, "max_calls": int}`. `enabled` stays required. If `max_calls` is omitted or `null`, the stored value is left unchanged. `max_calls < 1` or `max_calls > ceiling` returns 400 `VALIDATION_ERROR` "max_calls must be between 1 and N", where N is the ceiling. `GET` and `PUT` return `results: {enabled, max_calls, max_calls_ceiling, max_calls_total}`. `max_calls` is the stored value (1 when `NULL` or never set). `max_calls_ceiling` is the effective `WHATSAPP_CALLS_MAX_PER_DEVICE` (at least 1). `max_calls_total` is the effective `WHATSAPP_CALLS_MAX_TOTAL` (0: no gateway cap). The value applies at once, without a restart. The master switch off still returns 400 on `PUT`, as in B7. | A separate `PUT .../calls/limit` route (two writes for one form in the CRM). |
| D-24 | The effective per-device limit is `min(stored max_calls or 1, ceiling)`, read at each offer and each start through a per-slot cache. A miss reads the store, outside `m.mu`. The limit follows the device's registry slot at use time (`callsSlotOf`), like the switch does, because the startup client moves from its AD JID to its ULID slot. Lowering the limit never ends a live call: it applies to the next offer or start. A device above its new limit simply refuses until it drops below. | Ending calls above a lowered limit. |
| D-25 | **Inbound over a limit:** GOWA rejects the offer on WhatsApp and sends `call.ended` with `status` `MISSED` and `end_reason` `device_limit` (the device is full) or `gateway_limit` (the gateway pool is full). The device check runs first, so a call refused by both reports `device_limit`. Today this case sends `end_reason` `rejected`. `webhook-payload.md` and `contract/call.schema.json` gain both values. | Leaving the offer to the phone (the CRM would never see it). |
| D-26 | **Outbound over a limit:** the device limit keeps the 400 `ErrCallLimit` "too many calls on this device". The gateway cap gets a new 400 `VALIDATION_ERROR`, `ErrGatewayCallLimit`, with the text "call capacity of this gateway reached". That text must never contain the substring "too many calls", because the CRM matches substrings. The device check runs first. | 409 or 503 (the CRM's driver already maps 400 with a message). |
| D-27 | `POST /call/answer` never fails for capacity. A tracked inbound call already holds its slots (D-21), so `Answer` drops its limit check. That also covers a limit lowered while a call rings: the call was admitted, so it can be answered. | Re-checking at answer time (it would count the call against itself). |
| D-28 | Every refusal is logged at info level with its reason and the limit in force. Inbound: `[CALLS] rejecting <id> from <peer>: device at its call limit (N)` or `... gateway at its call capacity (N)`. Outbound: `[CALLS] refusing an outbound call on device <slot>: ...` with the same two reasons. Peers and slots stay masked (`maskPeer`, `maskSlot`). | |
| D-29 | The version target is `v9.7.0-elphant.12`. The tag, the image and the stack change happen only with the owner's explicit OK. They are a release step after the plan's code tasks, not one of those tasks. | |
| D-30 | **Live measurement before the deploy values are trusted.** It runs on the gateway host with the owner present, never by a subagent alone. Calls run inbound and outbound on the test number, 1, 2 and 3 at a time. It records the container's CPU and memory, the audio quality on each call, and what WhatsApp does when a second offer reaches a number that is already on a call. The results go to `docs/reference/elphant-fork.md`, with no number, IP or token. | Setting 3 and 6 from the 2a figure alone (one leg, no bridge). |
| D-31 | **No `call.offer` for a refused offer.** An offer that the manager refuses for `device_limit` or `gateway_limit` sends no `call.offer` webhook, only `call.ended` `MISSED` with the reason. Today `handleCallOffer` (`event_call.go`, from `event_handler.go:93`) forwards `call.offer` on its own goroutine, independently of `CallManager.onIncoming`. A refused call then still reaches the CRM as `call.offer` (`calls_enabled: true`, no `offer_sdp`) and rings agents until `call.ended` arrives. This already happens with `MaxCalls=1` (verified in the code: `addCallOfferFields` → `OfferSDP` → `waitTracked` returns `ErrCallNotFound` from `gone`, and the offer is sent anyway). **Ordering:** the manager records the refusal reason (`refused[id]`) in the same `m.mu` section that marks the id `gone`, before it rejects. `addCallOfferFields` first waits for the manager's admission decision through the existing `waitTracked` loop. That loop already absorbs the race noted at `calls.go` `waitTracked` (the webhook can run before meowcaller sees the offer): it waits up to `OfferWait` (3 s) for the id to be tracked or gone. A refused id makes `addCallOfferFields` report "suppress". `createCallOfferPayload` then returns nil, and `forwardCallOfferToWebhook` sends nothing. The wait runs before `OfferSDP`, so it also holds when the browser leg is unavailable (`OfferSDP` returns `ErrCallsNoMedia` at once). Unchanged: offers that are admitted, offers of devices with calls off, group offers and the master switch off (upstream behaviour). An offer that meowcaller never reports within `OfferWait` is still sent without `offer_sdp`, as today. The call record (`CreateIncomingCallRecord`) is still stored for a refused offer. | Sending `call.offer` with a `refused` flag (the CRM would still ring before reading it). Moving the limit check into `handleCallOffer` (a second admission path that can disagree with meowcaller's). |

## API

`PUT /devices/:device_id/calls`

```json
{ "enabled": true, "max_calls": 3 }
```

```json
{
  "status": 200,
  "code": "SUCCESS",
  "message": "Calls switch updated",
  "results": { "enabled": true, "max_calls": 3, "max_calls_ceiling": 3, "max_calls_total": 6 }
}
```

- `max_calls` omitted or `null`: the stored value is kept.
- `max_calls: 0`, a negative value, or a value above the ceiling: 400 `VALIDATION_ERROR`
  "max_calls must be between 1 and 3".
- `enabled` missing: 400 "enabled is required" (unchanged).
- `WHATSAPP_CALLS_ENABLED=false`: 400 (unchanged, `ErrCallsMasterOff`).
- `enabled: false` with `max_calls`: both are stored. Live calls end as today (B7).

`GET /devices/:device_id/calls` returns the same `results`.

`POST /call/start` over the gateway cap:

```json
{ "status": 400, "code": "VALIDATION_ERROR", "message": "call capacity of this gateway reached" }
```

## Webhooks

`call.ended` for a refused inbound offer:

```json
{
  "event": "call.ended",
  "payload": {
    "call_id": "…",
    "direction": "USER_INITIATED",
    "status": "MISSED",
    "end_reason": "device_limit",
    "duration": 0
  }
}
```

`end_reason` gains `device_limit` and `gateway_limit`. Both always come with `MISSED`. A refused
offer sends **no** `call.offer` (D-31): the CRM sees only this `call.ended`, for a `call_id` it
never saw offered. Today it gets a `call.offer` first.

## Edge cases

- **Two devices race for the last gateway slot.** The pool's mutex makes the check and the
  increment one step, so exactly one wins. The loser refuses with `gateway_limit` (inbound) or
  `ErrGatewayCallLimit` (outbound).
- **The call ends while it is being tracked.** A handle may fire its end synchronously inside
  `OnEnd`. The call is then never inserted into the manager, and the reservation is released at
  hand-over (`reserved--` plus a pool release). This covers both inbound and outbound
  (`TestEndBeforeTrackLeavesNoEntry`).
- **Every end path frees the slot exactly once.** The paths are hangup, peer end, reject, the
  ring-window timeout, max duration, browser-leg loss, a failed `Answer`, the device switched off,
  the client discarded and shutdown. All of them run `finish`, which releases the slot only when it
  removes the call from `calls`. Failures before tracking (bad SDP, engine error) release the
  reservation. A release with nothing held is logged and ignored, never negative.
- **A ringing call holds a slot for up to the ring window (1 minute).** An unanswered or spam call
  can block an outbound call for that long. The ring window, the phone answering (it then ends as
  `answered_elsewhere`) and the caller cancelling all free it.
- **The limit is lowered below the live count.** Nothing ends. New offers are refused until the
  count drops below the new limit. Calls that are already ringing can still be answered (D-27).
- **The ceiling is lowered by env below a stored value.** The effective limit is the ceiling. `GET`
  still shows the stored `max_calls` next to `max_calls_ceiling`, so the CRM can show the clamp. A
  later `PUT` above the new ceiling gets 400.
- **`WHATSAPP_CALLS_MAX_TOTAL` below the ceiling** (for example total 2, ceiling 3) is allowed. The
  gateway cap binds first.
- **The device is switched off or its client is discarded.** `closeAll` ends its calls, and each
  `finish` frees their slots. A re-paired device gets a new manager. While the old manager's calls
  close in the background, the pool can briefly count both.
- **A store read error on the limit.** The read falls back to 1, is not cached, and is logged, the
  same as the switch's read path.
- **`call.offer` versus the refusal (D-31).** The webhook goroutine can run before meowcaller
  reports the offer. It waits up to `OfferWait` (3 s) for the manager's decision. A refused offer
  sends no `call.offer`, and an admitted one sends it with `offer_sdp`. If meowcaller never reports
  the offer in time, `call.offer` goes out without `offer_sdp`, as today, and no `call.ended`
  follows from the manager. A device with calls off, a group call and the master switch off keep
  upstream's `call.offer`.
- **Group calls** never reach the manager (offer filter) and never count.
- **A second offer to a number already on a call.** Whether WhatsApp delivers it, sends a busy
  signal, or rings only the phone is unknown. D-30 measures it.

## Out of scope

- A per-device ceiling above the env, and per-workspace quotas in GOWA. The CRM owns plans and
  limits.
- Queueing a refused inbound call, call waiting, or holding one call to take another.
- A metric or health field for slots in use. `GET /calls/active` already lists a device's calls.
- Changing the env values on the host. That is a deploy step with the owner's OK (D-29).
- Any CRM change. The CRM sends `max_calls`, shows the ceiling and the gateway cap, and maps
  `device_limit`/`gateway_limit` and the new 400 text in its own spec.

## Docs to update

- `docs/reference/elphant-fork.md`, "Voice calls": `WHATSAPP_CALLS_MAX_PER_DEVICE` as the ceiling,
  `WHATSAPP_CALLS_MAX_TOTAL`, the `max_calls` field, the counting rule (ringing calls count), the
  refusal reasons, the CPU line (replaced by the D-30 results) and a "Concurrent calls,
  measurement" subsection.
- `src/.env.example`: `WHATSAPP_CALLS_MAX_PER_DEVICE` as the ceiling, and
  `WHATSAPP_CALLS_MAX_TOTAL=0` with a comment.
- `docs/webhook-payload.md`: the `call.ended` `status` and `end_reason` rows.
- `contract/call.schema.json` and a new fixture, `contract/call-fixtures/call-ended-limit.json`.
- `docs/openapi.yaml`: the `PUT` body and the `results` of `/devices/{device_id}/calls`, and the
  new 400 text on `/call/start`.
- `docs/webhook-payload.md`: the note that `call.offer` and the other call events leave from
  different paths now says that a refused offer sends no `call.offer`.
- `docs/elphant-debt.md`: the open items left after the code (D-30 until it runs) under a new
  entry, D-32.
