# G7: batch number check with `pn` and `lid`

> **Status:** approved design, no code yet. Plan: `docs/plans/2026-09-29-gateway-g7-batch-user-check.md` (to write).

## Problem

`GET /user/check?phone=` checks one number per call. `utils.IsOnWhatsapp` turns every error
(timeout, network, usync failure) into `false`, so a transient failure looks like "number does
not exist". A consumer that validates a broadcast audience then discards valid numbers. The
answer also carries no identity: WhatsApp already returns the LID and the phone number for each
match, and whatsmeow stores the pair, but the endpoint throws both away.

## Decisions

| # | Decision | Alternative rejected |
|---|---|---|
| D1 | New `POST /user/check` with a batch body; `GET /user/check` stays as is | Change the GET (breaks current clients) |
| D2 | Three states per number: `exists`, `not_exists`, `error` | Boolean (hides failures) |
| D3 | Return `pn` and `lid` per number, same shape as `chat{pn,lid}` in `payload.stable` | Only a boolean; a separate LID endpoint (that is G5) |
| D4 | `pn` is the one WhatsApp returns, `query` is what was asked | Echo the query as `pn` (wrong for the Brazilian ninth digit) |
| D5 | A failed batch call marks its numbers `error`, HTTP 200 | HTTP 5xx for the whole call (client loses the answers that did work) |
| D6 | One check at a time per device, minimum interval between batches | No limit (ban risk on large audiences) |
| D7 | Sync request, up to 100 numbers | Async job with polling (needed only for very large lists; the CRM batches) |
| D8 | Brazilian mobile numbers are also asked in their other ninth-digit form, same call (added after a real-device check showed WhatsApp does not fix a missing 9) | Fix the digit blindly (wrong for accounts registered either way) |

## Contract

`POST /user/check`, Basic Auth, device from `X-Device-Id` like every device-scoped endpoint.

```json
{"phones": ["5511999999999", "+55 11 98888-7777", "5511977776666@s.whatsapp.net"]}
```

- 1 to 100 entries, otherwise `400`. Digits, `+`, spaces, dashes and parentheses are accepted
  and stripped. A `@s.whatsapp.net` JID is reduced to its user part. `@lid`, group and other
  servers are not converted: that entry is an `error` (`invalid_number`).
- An entry with fewer than 7 or more than 15 digits is `error` / `invalid_number`.

Response (`results` is the usual envelope), one item per input entry, same order, duplicates
repeated:

```json
{"code":"SUCCESS","message":"Success check users","results":[
  {"query":"5511988887777","status":"exists",
   "pn":"5511988887777@s.whatsapp.net","lid":"123456789@lid",
   "verified_name":null,"error":null},
  {"query":"5511900000000","status":"not_exists","pn":null,"lid":null,"verified_name":null,"error":null},
  {"query":"12","status":"error","pn":null,"lid":null,"verified_name":null,"error":"invalid_number"}
]}
```

| Field | Rule |
|---|---|
| `query` | the entry as cleaned (digits only); for an `invalid_number` entry, the trimmed text as sent |
| `status` | `exists` when WhatsApp answers `type="in"`; `not_exists` when it answers otherwise or omits the number and the call succeeded; `error` otherwise |
| `pn` | JID from the WhatsApp answer (its `pn_jid`, or `jid` when that is a phone JID). It can differ from `query` (Brazilian ninth digit). `null` unless `exists` |
| `lid` | the LID in the WhatsApp answer; else `Store.LIDs.GetLIDForPN(pn)`; else `null`. `null` unless `exists` |
| `verified_name` | business verified name when WhatsApp sends one, else `null` |
| `error` | `invalid_number`, `upstream` (call failed or timed out), `null` otherwise |

Matching an answer to an entry uses the `Query` field whatsmeow fills, compared as digits, and
the returned phone as a fallback. An `in` answer that matches no requested number turns the
unanswered entries into `error` / `upstream`.

**Ninth digit (D8).** For `55` numbers with 13 digits (`55`, DDD, `9`, eight digits) the entry
without the 9 is also asked; for 12 digits whose subscriber part starts with 6 to 9 the entry
with the 9 is asked. The request to WhatsApp can carry up to twice the entries. The asked form
wins when it exists, else the other form gives `pn`; `query` is always what the client sent.
Landlines and other countries have no variant.

## Behavior

- **Call:** valid entries (deduplicated) go to `client.IsOnWhatsApp` in one call, 20 s timeout.
  whatsmeow already asks with `addressing_mode=lid` and stores the LID mappings it receives.
- **Call error:** every valid entry of that call is `error` / `upstream`. HTTP stays 200. The
  caller retries only those.
- **Not logged in / no client:** the same error the other endpoints return; no partial result.
- **Pacing:** a per-device gate serializes calls and waits at least
  `WHATSAPP_USER_CHECK_MIN_INTERVAL_MS` (default `500`) after the previous call finished.
  Waiting honors request cancellation (a cancelled request returns without calling WhatsApp).
- **Logging:** counts only (entries, exists, not_exists, error, duration). Never the numbers.

## Scope

In:
- `POST /user/check`, usecase, DTOs, validation, pacing gate, tests, section in
  `docs/reference/elphant-fork.md`.

Out:
- MCP tool (same usecase; add later if wanted).
- Listing every known LID/phone pair (G5, own spec).
- Editing `docs/openapi.yaml` (upstream file; the fork page documents the endpoint).
- The ElphantCRM side (`WhatsappNumberValidator` calling the batch, LID into `external_ids`):
  separate spec and plan in the CRM repo, backend only.
- Changing `GET /user/check`.

## Layers (AGENTS.md contract)

| Layer | Change |
|---|---|
| `domains/user` | `CheckBatchRequest{Phones []string}`, `CheckBatchItem`, `CheckBatchResponse`; `IsOnWhatsAppBatch` on `IUserUsecase` |
| `validations` | `ValidateCheckBatch`: size and entry cleaning |
| `usecase` | `IsOnWhatsAppBatch` on a small `phoneChecker` interface (`IsOnWhatsApp(ctx, []string)`, `LIDForPN(ctx, jid)`) so tests use a fake; a per-device pacing gate |
| `ui/rest` | `POST /user/check` in `user.go`, device via `ContextWithDevice(getDeviceFromCtx(c))` |
| `config` | `WhatsappUserCheckMinIntervalMs`, env `WHATSAPP_USER_CHECK_MIN_INTERVAL_MS`, flag/`.env.example` |

## Tests

- Validation: empty, 101 entries, formats accepted, `@lid` and group rejected per entry, too
  short and too long.
- Usecase with a fake checker: exists, not_exists, omitted number, `pn` different from `query`,
  `lid` from the answer, from the local map, absent, business verified name, call error marks
  only valid entries `error`, duplicates and order, no client.
- Pacing: two concurrent calls on one device are serialized and spaced by the interval; two
  devices do not block each other; a cancelled waiter never calls the checker.
- REST: `POST` happy path, 400 on bad body, device scoping (a second device is used, not the
  default).

## Known issue found

`UserCheck` (GET) passes `c.Context()` without the device, so `ClientFromContext` falls back to
the global default client and ignores `X-Device-Id`. Not fixed here; listed in
`docs/elphant-debt.md`.

## Release

`v9.5.0-elphant.7`, only with the owner's OK (push, tag, image, stack).
