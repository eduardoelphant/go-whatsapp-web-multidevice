# Fork differences (elphant)

This fork tracks upstream `aldinokemal/go-whatsapp-web-multidevice`. Branch `elphant` is the
latest upstream tag plus the fork's own commits. Upstream docs are not edited; this page lists
every behavior that differs from them.

## Baileys session import

The `import-baileys` command converts a Baileys auth state into a whatsmeow device, so a
session moves over without scanning a new QR code. See [import-baileys.md](import-baileys.md).

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
  The per-device webhook configuration is resolved from it, so pre-pairing events and a
  `logged_out` reach the slot's webhook even without a JID.
- The five `payload` keys are always present. A key that does not apply is `null`.
- Arrival order is not guaranteed: each event is delivered on its own, and whatsmeow itself
  dispatches connection events concurrently. After a burst such as `disconnected` then
  `connected`, confirm the current state with `GET /devices/:device_id/status`.

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

## `payload.stable`

Every message webhook event (`message`, `message.edited`, `message.reaction`,
`message.revoked`, `message.ack`) carries `payload.stable`, a closed object whose keys are always
present (`null` when unknown) and never change type. The existing payload fields are unchanged.
The contract is `contract/stable.schema.json`; examples are in `contract/fixtures/synthetic/`.

```json
"stable": {
  "schema": 1, "id": "3EB0…", "timestamp": "2026-09-25T12:00:00Z", "is_from_me": false,
  "chat": { "pn": "5511…@s.whatsapp.net", "lid": "123…@lid", "is_group": false },
  "sender": { "pn": "5511…@s.whatsapp.net", "lid": "123…@lid", "push_name": "Fulano" },
  "type": "image", "text": "caption", "media": { "kind": "image", "mime": "image/jpeg", "size": 2048,
  "sha256": "…", "filename": null, "duration": null, "ptt": false, "width": 640, "height": 480,
  "url": "/message/3EB0…/media" }, "location": null, "contact": null, "quoted": null,
  "forwarded": false, "view_once": false
}
```

Per event: `message` adds `type`, `text`, `media`, `location`, `contact`, `quoted`, `forwarded`,
`view_once`, `interactive`, `reply`, `referral`, `poll`, `poll_vote`, `call`, `product` and `order`; `message.edited` adds `target_id`, `text`; `message.reaction` adds `target_id`,
`emoji` (`null` = removed); `message.revoked` adds `target_id`; `message.ack` adds `status`
(`delivered`, `read`, `played`) and `ids`.

Buttons and templates:

- `type: "interactive"`: a message with buttons, a template, a list or a native flow (usually
  from business accounts and bots). `text` is its body; `interactive` has `kind` (`buttons`,
  `template`, `list`, `native_flow`), `header`, `footer`, `buttons` (each with `kind`: `reply`,
  `url`, `call`, `copy`, `menu` or `other`, plus `id`, `text` and `value`, the link, phone number
  or code) and `sections` (list rows with `id`, `title`, `description`). Header media and carousels
  are not included.
- `type: "interactive_reply"`: the option someone chose. `text` is the chosen label; `reply` has
  `kind` and `id`; `quoted` points to the interactive message when WhatsApp says which one.

Polls, calls and catalog:

- `type: "poll"`: `text` is the question; `poll` has `options` and `selectable_count`.
- `type: "poll_vote"`: `poll_vote` has `poll_id`, `selected` (option names) and `resolution`
  (`resolved`, `partially_resolved`, `definition_missing`, `decrypt_failed`, or `encrypted` when
  GOWA did not decrypt it). Votes are encrypted by WhatsApp; GOWA decrypts them when it stored the
  poll.
- `type: "call"`: the call log entry after a call; `call` has `outcome` (`connected`, `missed`,
  `rejected`, …, WhatsApp's names in lowercase), `video`, `duration` (seconds) and `call_type`.
- `type: "product"`: a catalog product; `text` is the message body; `product` has `id`, `title`,
  `description`, `retailer_id`, `url`, `currency`, `price_1000` and `sale_price_1000`.
- `type: "order"`: a catalog order; `text` is the buyer's note; `order` has `id`, `title`,
  `item_count`, `status` (`inquiry`, `accepted`, `declined`), `currency` and `total_1000`.
- Amounts are in thousandths of the currency unit, as WhatsApp sends them (R$ 12,99 = `12990`).

Ads and entry points: `referral` is set when the message came from a Click-to-WhatsApp ad
(Facebook/Instagram) or from a link carrying source/medium. It has `source_type`, `source_app`,
`source_id` (the ad id), `source_url`, `ctwa_clid` (the click id used by Meta's Conversions API),
`ref`, `title`, `body`, `media_type`, `thumbnail_url`, `media_url` and `entry_point` (`source`,
`app`, `external_source`, `external_medium`). WhatsApp sends it only on the first message after
the click, so store it when it arrives.

On `message.ack`, `is_from_me` tells the direction: `false` means the contact received or read
your messages listed in `ids`; `true` means you read the contact's messages on another device
(WhatsApp's "read self" receipt).

## `GET /message/:message_id/media`

Streams the media of a stored message (Basic Auth, `X-Device-Id`). The file goes through a temp
file that is deleted after the response; nothing is written to `/statics`. `404` when the message
has no media, `410` when WhatsApp no longer has it: download soon after the webhook arrives.

- `stable.media.url` is relative to the server root; with `APP_BASE_PATH` set, prefix it.
- The response `Content-Type` is sniffed from the file (with the extension as fallback); the
  webhook's `stable.media.mime` is the authoritative value.
- The download has its own 10-minute deadline (the global request timeout is 45 s). The temp file
  lives in the container's temp directory until the response ends.

## Stable payload audit

With `WHATSAPP_STABLE_AUDIT_DIR` set as a real environment variable (it is not read from
`src/.env`), each new shape of `payload.stable` (event, type and non-null keys) is saved
anonymized, at most 3 samples per shape, under `<dir>/<event>/<type>/`. Samples of `type:
"unknown"` messages get a `.fields.txt` sidecar with the names (never the values) of the populated
WhatsApp proto fields. Writing never delays delivery: when the disk is slow, samples are dropped. Reviewed samples go to
`contract/fixtures/real/`, and `cd contract && go run ./cmd/compare` reports event/type groups
with no synthetic fixture (`MISSING`), non-null paths the synthetic fixtures never exercise
(`UNCOVERED`) and JSON kind conflicts (`KIND`). Nullability combinations alone are not gaps.

## Durable webhook delivery

Upstream sends each webhook from a goroutine started after WhatsApp was already told the message
arrived, and gives up after 5 attempts over about 15 seconds. An event is lost when the receiver is
down for longer, or when the process stops in between.

Set `WHATSAPP_WEBHOOK_DELIVERY=durable` (default `direct`, the upstream behavior) to queue every
webhook event except `chat_presence` in a SQLite outbox at `WHATSAPP_WEBHOOK_OUTBOX_DB` (default
`file:storages/webhook-outbox.db`). Both variables are read from the process environment only,
not from `.env`.

- Message events, `message.ack` and `session.status` are written to the outbox inside the WhatsApp
  event handler. If the write fails, the handler reports failure and whatsmeow does not acknowledge
  the message, so WhatsApp delivers it again. Durable mode turns on whatsmeow's decrypted-event
  buffer, so the redelivered message is read back instead of failing to decrypt.
- A message redelivered this way runs the whole handler again: chat storage, Chatwoot and
  auto-reply (a second auto-reply) included.
- Group, label, call, newsletter and app-state events are queued from their existing goroutines; a
  crash between the WhatsApp acknowledgement and the write can still lose one of them.
- `chat_presence` (typing) is sent directly, as upstream does.
- With `WHATSAPP_AUTO_DOWNLOAD_MEDIA=true` the media download happens inside the handler and slows
  message processing down. Keep it off; fetch media with `GET /message/:message_id/media`.

Delivery: one worker per destination URL sends rows oldest first. A failing row holds back the rows
behind it until it succeeds or is given up. Network errors, timeouts, `5xx`, `408` and `429` retry
after 10 s, 30 s, 1, 2, 5, 10 and 30 minutes, then every hour; `Retry-After` on `429` is honored. A
row still failing 72 hours after it was queued becomes `dead`; any other `4xx` makes it `dead` at
once. Queued rows survive restarts; a row in flight during a crash is sent again. Delivered rows are
kept 7 days, dead rows 30 days.

Contract (durable mode only):

- The body gets a top-level `event_id` (ULID), the same on every attempt, redelivery and replay.
  Each destination URL gets its own `event_id` for the same event.
- Headers: `X-Webhook-Id` (the `event_id`), `X-Webhook-Timestamp` (when the event was queued,
  RFC3339 UTC), `X-Webhook-Attempt` (1, 2, 3…) and `X-Webhook-Replay: true` on redeliveries and
  replays. `X-Hub-Signature-256` is unchanged. The secret is read from the current configuration at
  every attempt, so a rotated secret applies to queued rows.
- Answer `2xx` to confirm, and deduplicate by `event_id`: the same event can arrive more than once.

Operations API (Basic Auth; `404` in direct mode):

| Endpoint | Purpose |
|---|---|
| `GET /webhooks/stats` | counts per status and per URL, the oldest pending row and its age |
| `GET /webhooks/deliveries?status=&limit=&before_id=` | rows newest first, without bodies (`limit` 1-500, default 50) |
| `GET /webhooks/deliveries/:event_id` | one row with its body |
| `POST /webhooks/deliveries/:event_id/redeliver` | back to `pending`, attempts reset, same `event_id`, sent with `X-Webhook-Replay` |
| `POST /webhooks/replay?since=<RFC3339>[&url=]` | every delivered or dead row queued since then goes back to `pending` |

A redelivered or replayed row keeps its id, so it is sent before newer rows of the same URL.
