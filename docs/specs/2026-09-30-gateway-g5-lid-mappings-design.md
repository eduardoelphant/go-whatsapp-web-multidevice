# G5: LID and phone mappings

> **Status:** written for the owner's review, no code yet. Plan: to write after approval.

## Problem

WhatsApp identifies a person by a LID (`123456789@lid`) and by a phone JID
(`5511999999999@s.whatsapp.net`). whatsmeow keeps the pairs it learns in the table
`whatsmeow_lid_map (lid, pn)`. GOWA already uses the pairs inside webhooks (`payload.stable`
carries `chat{pn,lid}` and `sender{pn,lid,push_name}`, G4) and inside `POST /user/check`
(G7, `pn` and `lid` per number). There is no way to ask the gateway for a pair, or to list the
pairs it knows: a consumer that has only a phone (an old contact) cannot learn its LID without a
send or a number check, and a consumer that receives a LID with no phone cannot ask for it.

## What the store gives

- `Store.LIDs` (`CachedLIDMap`) answers `GetLIDForPN`, `GetPNForLID` and `GetManyLIDsForPNs`
  from memory, then from SQL. It has **no list method**.
- `whatsmeow_lid_map` has no device column: it is shared by every device of the gateway, and so
  is `Store.LIDs`. A mapping learned by one device is visible to the others.
- `sqlstore.Container` keeps its database handle private, so listing needs a second handle.
  `infrastructure/whatsapp.ResolveDBDriver(config.DBURI)` already gives the driver and DSN used
  by the store (SQLite pragmas included).

## Decisions

| # | Decision | Alternative rejected |
|---|---|---|
| D1 | Three lookups and one list, all read only | Write endpoints (whatsmeow owns the map) |
| D2 | Lookups use the request device's `Store.LIDs` (same shared map, same cache) | A separate query path |
| D3 | The list reads `whatsmeow_lid_map` through a second read-only handle opened with `ResolveDBDriver` | Derive the list from chat storage (partial, only chats the device saw) |
| D4 | The list is global to the gateway and documented so; it ignores `X-Device-Id` | Pretend it is device scoped (it is not) |
| D5 | Keyset pagination on `lid`, `limit` default 100, max 1000 | Offset pagination (slow and unstable on a growing table) |
| D6 | A contract test opens a real temporary `sqlstore` and lists through the raw query, so a whatsmeow upgrade that renames the table or columns fails a test | Trust the schema |
| D7 | Strings use the same forms as the webhook: `pn` is `<digits>@s.whatsapp.net`, `lid` is `<digits>@lid` | Bare digits |
| D8 | "Webhook always with `{pn, lid}`" from the mother spec is already delivered by G4 and is not part of G5 | Repeat it |

## Contract

All endpoints: Basic Auth. The lookups take the device from `X-Device-Id`.

| Endpoint | Purpose | Answer |
|---|---|---|
| `GET /lids/pn/:phone` | LID of a phone (`:phone` digits, `+` and formatting accepted, or a user JID) | `{"pn": "...@s.whatsapp.net", "lid": "...@lid" \| null}` |
| `GET /lids/:lid` | phone of a LID (`:lid` digits or `...@lid`) | `{"lid": "...@lid", "pn": "...@s.whatsapp.net" \| null}` |
| `POST /lids/lookup` | batch: `{"pns": [...], "lids": [...]}`, at most 500 entries in total | `{"pns": [{pn, lid\|null}...], "lids": [{lid, pn\|null}...]}`, same order as the input |
| `GET /lids?limit=&after=` | every pair the gateway knows, ordered by `lid` | `{"items": [{lid, pn}...], "next": "<lid>" \| null}` |

- A pair that is not known answers `null` in the other field, never an error: "unknown" is an
  ordinary answer (WhatsApp may not have told the device yet).
- A bad `:phone` or `:lid` answers `400`. An empty list, more than 500 entries or a malformed
  body answers `400`. `limit` above 1000 is clamped.
- `after` is the `lid` of the last item of the previous page (digits, as returned in `next`
  without the server part stripped: the value is opaque to the client).
- Unknown pairs are not fetched from WhatsApp here. To make the gateway learn a pair, use
  `POST /user/check` (G7), which stores the mappings it receives.
- Route order: `/lids/pn/:phone` and `/lids/lookup` are registered before `/lids/:lid`.

## Behavior

- Lookups never call WhatsApp: they read the store, so they need the device to exist, not to be
  connected. A request for a device that is not found answers like the other device routes.
- The list opens its handle lazily on the first call, read only, and closes it when the process
  stops. A failure to open or read answers `500` with no SQL in the body.
- Logs carry counts only, never phones or LIDs.

## Layers (AGENTS.md contract)

| Layer | Change |
|---|---|
| `domains/lid` (new) | `LookupRequest`, `PNItem`, `LIDItem`, `ListResponse`, `ILIDUsecase` |
| `validations` | normalization of `:phone` and `:lid`, batch limits (reuses `NormalizeBatchPhone`) |
| `pkg/lidmap` (new) | `List(ctx, db, after, limit)` over `whatsmeow_lid_map`, keyset on `lid`; the only place that knows the table name |
| `usecase/lid.go` | lookups over a small `lidStore` interface (fake in tests), list over `pkg/lidmap` |
| `ui/rest/lid.go` | the four routes, device from the request |
| `ui/rest/rest.go` wiring, `cmd/rest.go` | register the routes |

## Tests

- `pkg/lidmap`: list order, keyset pagination across pages, `limit` clamp, empty table, and the
  contract test on a real temporary `sqlstore` SQLite (insert with `Store.LIDs.PutLIDMapping`,
  read back with `List`).
- Usecase with a fake store: known pair, unknown pair, batch order, duplicates, mixed `pns` and
  `lids`, more than 500, bad formats.
- REST: each route, `400`s, route order (`/lids/pn/x` is not read as a LID), device scoping on the
  lookups.

## Out of scope

- Writing or deleting mappings.
- Asking WhatsApp for unknown pairs (G7 does that).
- ElphantCRM changes (a backfill of `external_ids` for old contacts is a separate spec: it would
  call `POST /lids/lookup` with the contacts' phones).
- MCP tools (same usecase, add later if wanted) and `docs/openapi.yaml` (upstream file; the fork
  page documents the endpoints).

## Release

`v9.5.0-elphant.9`, only with the owner's OK. Runtime check on the devias device: look up the
owner's own phone and LID, and list with `limit=2`.
