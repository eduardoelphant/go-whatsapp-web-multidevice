# G4: stable webhook payload (`payload.stable`), media endpoint, contract and real-traffic audit

**Date:** 2026-09-25
**Branch:** `elphant` (fork `eduardoelphant/go-whatsapp-web-multidevice`, fork-only model)
**Status:** approved design, awaiting implementation plan
**Source requirement:** CRM spec `docs/specs/2026-09-24-whatsapp-gateway.spec.md` §4.3 and item G4 (elphantcrm)

## 1. Goal

The CRM driver for `whatsapp_gowa` must read one fixed set of fields for every message event and
never special-case where WhatsApp put the content. Earlier attempts (wuzapi, Evolution Go) failed
because the same text arrived under different keys and each combination became a PHP branch.

G4 adds a closed, versioned object `payload.stable` to the five message webhook events, a way to
fetch media bytes, a JSON Schema with fixtures, and a production audit that keeps the fixtures
honest against real traffic.

## 2. Findings that shaped the design

1. **Key collisions.** The current payload already uses `location` (raw protobuf), `timestamp`,
   `id` and `is_from_me`. New top-level fields would collide or mix legacy and new formats.
2. **Media is unreachable for the CRM.** With auto-download off, the payload carries only the
   encrypted WhatsApp CDN URL (useless without the media key). With auto-download on, files land in
   `/statics`, which Traefik blocks on purpose. `GET /message/:id/download` also writes to `/statics`.
3. **Each message event has its own shape.** Reaction uses `reacted_message_id`, revoke
   `revoked_message_id`, edit `original_message_id` + `body`, ack its own keys.

## 3. Decisions

| # | Decision | Discarded | Why |
|---|---|---|---|
| G4-D1 | One closed object `payload.stable`; existing fields untouched | Loose top-level fields; replacing current fields | No collisions, nothing breaks (Chatwoot and other consumers keep working), the schema covers a closed object |
| G4-D2 | All five message events carry `stable` with a shared base plus a per-event block | Only `message` now | The CRM parses one base (who, where, when) for everything |
| G4-D3 | New authenticated streaming endpoint `GET /message/:message_id/media`; `stable.media.url` is its relative path | Auto-download + authenticated `/statics`; `url` always null | No media piles up on the server; no dependency on G8 |
| G4-D4 | Synthetic golden fixtures from Go tests are the base; a production audit captures anonymized real shapes continuously and a compare command reports gaps | Synthetic only; real captures only | Deterministic tests without personal data, while real traffic keeps revealing cases the synthetic set lacks |
| G4-D5 | Builder in a pure package; schema validation lives in a separate Go module `contract/` | JSON Schema library in `src/go.mod` | Keeps `src/go.mod` (already a sync conflict point) unchanged |

## 4. The contract

### 4.1 Base (every message event)

All keys always present; a key that does not apply is `null`; a key never changes type.

```json
"stable": {
  "schema": 1,
  "id": "3EB0C767D26A1D2E1F3C",
  "timestamp": "2026-09-25T12:00:00Z",
  "is_from_me": false,
  "chat":   { "pn": "5511999999999@s.whatsapp.net", "lid": "123456789@lid", "is_group": false },
  "sender": { "pn": "5511999999999@s.whatsapp.net", "lid": "123456789@lid", "push_name": "Fulano" }
}
```

| Field | Type | Rule |
|---|---|---|
| `schema` | integer | `1`. A future field bumps it; existing keys never change type |
| `id` | string | WhatsApp message id of this event |
| `timestamp` | string | RFC3339 UTC |
| `is_from_me` | boolean | |
| `chat.pn` | string or null | Phone JID of a 1:1 chat; group JID (`…@g.us`) for groups; `status@broadcast` as is |
| `chat.lid` | string or null | LID JID of a 1:1 chat; `null` for groups and broadcasts |
| `chat.is_group` | boolean | |
| `sender.pn` / `sender.lid` | string or null | Same resolution as the chat; for own messages, the account itself |
| `sender.push_name` | string or null | |

JID resolution: an `@lid` JID fills `lid` and looks up `pn` in the LID map; a phone JID fills `pn`
and looks up `lid`. A missing mapping or a lookup error gives `null` and never fails the webhook.

### 4.2 Per-event blocks

| Event | Extra keys |
|---|---|
| `message` | `type`, `text`, `media`, `location`, `contact`, `quoted`, `forwarded`, `view_once` |
| `message.edited` | `target_id`, `text` |
| `message.reaction` | `target_id`, `emoji` (`null` = reaction removed) |
| `message.revoked` | `target_id` |
| `message.ack` | `status`, `ids` |

`message` details:

| Field | Type |
|---|---|
| `type` | one of `text`, `image`, `video`, `audio`, `document`, `sticker`, `location`, `contact`, `poll`, `unknown` |
| `text` | string or null: body or caption wherever WhatsApp put it; poll question for `poll`; `null` for `unknown` |
| `media` | null, or `{kind, mime, size, sha256, filename, duration, ptt, width, height, url}`; `kind` is `image`, `video`, `audio`, `document` or `sticker`; `sha256` hex of the decrypted file; `url` is `/message/<id>/media`; the others string/integer/boolean or null |
| `location` | null, or `{latitude, longitude, name, address}` (numbers; strings or null) |
| `contact` | null, or `{name, vcard, phones}` (`phones` array of strings) |
| `quoted` | null, or `{id, type, text, sender: {pn, lid}}` |
| `forwarded`, `view_once` | boolean |

`message.ack`: `status` one of `delivered`, `read`, `played`; `ids` array of message ids. Today GOWA
forwards only delivered and read; `played` is in the schema for later.

### 4.3 Type mapping

Always applied after unwrapping ephemeral, view-once (all versions), device-sent and edit wrappers.

| WhatsApp message | `type` |
|---|---|
| `conversation`, `extendedTextMessage` | `text` |
| image, video, audio, document, sticker | same name (`media.ptt` from the voice-note flag) |
| video note (PTV) | `video` |
| location, live location | `location` |
| contact | `contact` |
| contacts array | `contact`, first entry (documented limit) |
| poll creation (all versions) | `poll` |
| anything else (buttons, lists, templates, orders, interactive…) | `unknown`, `text: null` |

## 5. Components

| Component | Location | Notes |
|---|---|---|
| Builder | new package `src/pkg/stablepayload` | Pure: input is the whatsmeow event (or receipt) plus a `Resolver` interface (`PNForLID`, `LIDForPN`); output is Go structs with JSON tags and no `omitempty`, so null keys are always emitted. No whatsmeow client needed in tests |
| Message hook | `src/infrastructure/whatsapp/event_message.go` | One line after the SecretEncryptedMessage edit decryption: `payload["stable"] = …`. The builder detects edit, reaction, revoke and regular message itself |
| Receipt hook | `src/infrastructure/whatsapp/event_receipt.go` | One line in `forwardReceiptToWebhook` |
| Resolver adapter | new file in `src/infrastructure/whatsapp` | Wraps `client.Store.LIDs` |
| Media endpoint | new files in `src/ui/rest` and `src/usecase`; one-line hooks in `domains/message/interfaces.go` and the route registration | Basic Auth + `X-Device-Id`; looks the message up in the device's chat storage, downloads from WhatsApp to a temp file, streams it with its MIME type, deletes the temp file; 404 when the message has no media; 410 when WhatsApp no longer has it |
| Audit | new file `src/infrastructure/whatsapp/stable_audit.go`, enabled by `WHATSAPP_STABLE_AUDIT_DIR` | Anonymizes a copy of each `stable`, computes its shape (event, type, set of non-null keys), stores the first 3 samples per new shape as JSON files. Never affects delivery |
| Contract | new top-level `contract/` with its own `go.mod` | `stable.schema.json`; `fixtures/synthetic/` (written by the `src` tests with `-update`); `fixtures/real/` (reviewed audit samples); a test validating every fixture against the schema; `cmd/compare` reporting real shapes without a synthetic twin and synthetic shapes that disagree with real ones |
| CI | `.github/workflows/elphant-ci.yml` | Adds `go test ./...` in `contract/` |
| Docs | `docs/elphant-fork.md` (fork), `docs/reference/38-gowa.md` (elphantcrm) | Contract summary and schema link; audit cycle and media endpoint in the runbook |

Anonymization: every string is replaced by a type-preserving placeholder (`<text:12>`,
`<jid:pn>`, `<jid:lid>`, `<id>`, `<name>`, `<sha256>`), numbers by `0`, except the closed-set values
kept verbatim: `event`, `schema`, `type`, `media.kind`, `media.mime`, `status`, booleans and the
server part of JIDs (`s.whatsapp.net`, `lid`, `g.us`, `broadcast`).

## 6. Audit cycle

1. The stack gets `WHATSAPP_STABLE_AUDIT_DIR=/app/storages/stable-audit`; samples live in the
   `gowa_storages` volume (already in the backup).
2. On request or during the weekly routine, samples are copied to a local folder outside any repo.
3. The owner reviews them before any commit (the fork is public, even though samples are anonymized).
4. Approved samples move to `contract/fixtures/real/`; `cmd/compare` runs.
5. Each real shape without a synthetic twin becomes a new synthetic case with its test.

## 7. Testing and verification

- `stablepayload` table tests: every type in §4.3, every wrapper, group, LID-only sender, own
  message, reply, the five events, nil proto fields, resolver errors. Each case writes/compares its
  golden fixture in `contract/fixtures/synthetic/`; CI fails on drift.
- `contract/`: every fixture validates against the schema; a fixture with a missing key or a wrong
  type must fail (negative cases in the test).
- Media endpoint: fake downloader test for streaming, temp-file removal, 404 and 410.
- Audit: anonymizer test (no original string survives; closed-set values kept), shape signature
  test, cap of 3 samples per shape.
- Production: image `v9.5.0-elphant.2` with the audit on; the owner sends one message of each type;
  captured shapes are compared with the synthetic set; one media file is fetched through the new
  endpoint and its SHA-256 matches `stable.media.sha256`.

## 8. Out of scope

Durable delivery (G3), LID endpoints (G5), reachout timelock (G6), batch number check (G7),
authenticated `/statics` (G8), the CRM driver itself.
