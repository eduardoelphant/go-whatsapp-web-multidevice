# G3: durable webhook delivery

**Date:** 2026-09-25
**Branch:** `elphant` (fork `eduardoelphant/go-whatsapp-web-multidevice`, fork-only model)
**Status:** approved design, awaiting implementation plan
**Source requirement:** CRM spec `docs/specs/2026-09-24-whatsapp-gateway.spec.md` D6, §5.1 G3, §8.4 (elphantcrm)

## 1. Goal

No webhook event is lost when the CRM is down. Events wait in a durable queue, are retried for
up to 72 hours in order, then move to a dead-letter state from which they can be redelivered.
A replay can re-send everything since a given time. If GOWA crashes, nothing is lost either.

Success: with the CRM down for hours, it receives every event, in order and without duplicates
it cannot detect, when it comes back; a GOWA crash at any point loses no message.

## 2. Today

- `submitWebhook` (`src/infrastructure/whatsapp/webhook.go`) tries 5 times over ~15 s
  (1, 2, 4, 8 s) and gives up.
- Sending runs in a goroutine started after whatsmeow already acknowledged the message to
  WhatsApp (`handleWebhookForward`, receipts, `session.status`, groups, labels, calls,
  newsletters). A crash in that window loses the event.
- whatsmeow supports `AddEventHandlerWithSuccessStatus`: when a handler returns `false`,
  `handleDecryptedMessage` returns before acknowledging, and the WhatsApp server redelivers.

## 3. Decisions

| # | Decision | Discarded | Why |
|---|---|---|---|
| G3-D1 | Full guarantee: message events are built and queued synchronously in the event handler; a queue write failure makes the handler return `false` so WhatsApp redelivers | Queue written from the existing goroutines | Closes the crash window between WhatsApp's ack and the queue write |
| G3-D2 | Strict order per destination URL; temporary failures retry, permanent 4xx go to dead-letter at once | Independent retry schedule per event | The CRM receives events in the order they happened without reordering in PHP; one bad event never blocks the queue |
| G3-D3 | Own SQLite file `storages/webhook-outbox.db` | Table in `chatstorage.db`; Postgres | Fork-only code, no upstream migration or repository-interface changes, no lock contention with chat storage; same volume, same consistent backup |
| G3-D4 | Every webhook event except `chat_presence` is queued | Only message events and `session.status`; everything | "Typing…" delivered hours later is noise; everything else matters |
| G3-D5 | Intercept at the per-URL send (`submitWebhookFn`) | Before routing; separate service | Upstream routing (device/global webhooks, event filters, merge, signing) stays untouched; one well-defined seam |
| G3-D6 | Opt-in `WHATSAPP_WEBHOOK_DELIVERY=durable` (default `direct`, today's behavior) | Always on | Upstream tests and behavior stay valid; the devias (gowa) stack turns it on |

## 4. Contract (additive)

- Body: new top-level key `"event_id": "<ULID>"` next to `event`. Stable across retries,
  redeliveries and replays.
- Headers:
  - `X-Webhook-Id`: the `event_id`.
  - `X-Webhook-Timestamp`: when the event was queued (RFC3339 UTC), the same on every attempt.
  - `X-Webhook-Attempt`: 1, 2, 3…
  - `X-Webhook-Replay: true` on redeliveries and replays.
  - `X-Hub-Signature-256` unchanged, over the whole body.
- The CRM answers 2xx to confirm. A repeated `event_id` is answered 2xx and ignored, unless
  `X-Webhook-Replay` is set and the CRM wants to reprocess (e.g. after restoring its database).
- In `direct` mode nothing changes: no `event_id`, no new headers.

## 5. Queue

SQLite file `storages/webhook-outbox.db` (path from `WHATSAPP_WEBHOOK_OUTBOX_DB`, default
`file:storages/webhook-outbox.db`), WAL mode, one table:

| Column | Meaning |
|---|---|
| `id` | integer primary key (queue order) |
| `event_id` | ULID, unique |
| `target_url` | destination URL |
| `config_ref` | `global`, `device:<session_id>` or `jid:<device jid>`: the HMAC secret and TLS-skip flag are resolved from the current configuration at each send, so a rotated secret applies to queued events |
| `event_name` | e.g. `message`, `message.ack`, `session.status` |
| `body_json` | the body GOWA built, plus `event_id`; never the secret |
| `status` | `pending`, `delivered`, `dead` |
| `attempts`, `next_attempt_at`, `last_error`, `last_status_code` | retry control |
| `replay` | 1 when requeued by redeliver/replay |
| `created_at`, `delivered_at` | event and delivery times |

Indexes: `(target_url, status, id)` for the worker, `(status, created_at)` for cleanup and stats,
`(event_id)` unique.

Queueing: in durable mode `submitWebhookFn` inserts one row per call and returns. Each
destination gets its own copy of the body (the device and global legs run concurrently and must
not share a mutable map). The same event sent to two URLs gets two rows with different
`event_id`s.

`config_ref`: `global` when the call carries no device config; otherwise `device:<session_id>`
from the body, or `jid:<device_id>` when the body has no `session_id`.

## 6. Synchronous queueing and the WhatsApp ack

- Both event handler registrations (`init.go`, `device_manager.go`) switch to
  `AddEventHandlerWithSuccessStatus`. The wrapper runs the existing `handler` with a context
  carrying a failure flag and returns `false` when the flag is set.
- Message events: in durable mode `handleWebhookForward` runs the forward synchronously (no
  goroutine). A queue write error sets the flag. Payload building stays as is; with
  `WHATSAPP_AUTO_DOWNLOAD_MEDIA=true` the media download then happens inside the handler (the
  devias stack keeps auto-download off; documented).
- `message.ack` and `session.status`: queued synchronously too (the failure flag is set for
  receipts as well; `session.status` is not a WhatsApp message, so it only logs).
- Other events (groups, labels, calls, newsletters, app state): queued from their existing
  goroutines; a crash between WhatsApp's ack and the insert can lose one of them (documented).
- `chat_presence`: never queued; sent directly as today.

## 7. Delivery

One worker per destination URL, started for URLs present in the queue at boot and whenever a new
URL is queued:

1. Take the oldest `pending` row for the URL.
2. If `next_attempt_at` is in the future, wait for it or for a new-row signal.
3. POST with the headers above, 10 s timeout, signature with the secret resolved from
   `config_ref`.
4. Outcome:

| Response | Result |
|---|---|
| 2xx | `delivered`, next row |
| network error, timeout, 5xx, 408, 429 | retry after 10 s, 30 s, 1 min, 2 min, 5 min, 10 min, 30 min, then every hour; `Retry-After` on 429 is honored; a row older than 72 h becomes `dead` |
| any other 4xx (including 401 for a bad signature) | `dead` at once; the next row goes |

- Restart: the queue lives on disk and workers resume; a row in flight during a crash is sent
  again (the CRM deduplicates by `event_id`).
- A URL removed from the configuration keeps retrying until 72 h, then `dead`.
- Cleanup, hourly: `delivered` older than 7 days and `dead` older than 30 days are deleted.
- Logs: one line when a URL starts failing and one when it recovers, with the queue size; not
  one per attempt.

## 8. Operations API (Basic Auth)

| Endpoint | Purpose |
|---|---|
| `GET /webhooks/stats` | counts per status and per URL, age of the oldest pending row |
| `GET /webhooks/deliveries?status=&limit=&before_id=` | list without bodies: id, event_id, event, URL, attempts, last error and status code, times |
| `GET /webhooks/deliveries/:event_id` | one row with its body |
| `POST /webhooks/deliveries/:event_id/redeliver` | back to `pending` (attempts reset, `replay=1`, same `event_id`) |
| `POST /webhooks/replay?since=<RFC3339>[&url=]` | every `delivered`/`dead` row since then back to `pending` with `replay=1` (bounded by the 7-day retention) |

Available only in durable mode; in `direct` mode they answer 404.

## 9. Components

| Component | Location |
|---|---|
| Queue store (schema, insert, head-of-queue, mark, requeue, stats, cleanup) | new package `src/pkg/webhookoutbox` (pure, own SQLite handle, tested with a temp file) |
| Worker (per-URL loops, retry policy, HTTP send with headers and signature) | same package, with the HTTP client and secret resolver injected |
| Wiring (open the store at startup, start workers, `submitWebhookFn` switch, secret resolver over `getWebhookConfigForDevice`/global config, failure flag) | new file `src/infrastructure/whatsapp/webhook_durable.go` |
| Hooks in upstream files | handler registration (`init.go`, `device_manager.go`), synchronous branch in `handleWebhookForward` and receipt forwarding, `chat_presence` bypass, startup call in `cmd/rest.go` |
| REST endpoints | new file `src/ui/rest/webhook_outbox.go`, one registration line |
| ULID | generated inside `webhookoutbox` (48-bit millisecond time + 80 random bits from `crypto/rand`, Crockford base32, 26 chars, monotonic within the same millisecond); no new dependency, `src/go.mod` unchanged. SQLite access uses GOWA's existing `pkg/sqlite` driver |
| Docs | `docs/reference/elphant-fork.md`; elphantcrm `docs/reference/38-gowa.md` (stack flag, what to watch in `/webhooks/stats`, how to redeliver) |

## 10. Testing and verification

- Store (temp SQLite): insert, FIFO head per URL, retry schedule, 72 h to `dead`, cleanup,
  redeliver, replay, stats.
- Worker (httptest server): 5xx twice then 200 delivers in order; 4xx goes `dead` without blocking
  the next; 429 with `Retry-After`; rotated secret applies to queued rows; headers and body
  `event_id` correct; restart resumes pending rows.
- Handler: with the queue unavailable the success-status handler returns `false`; in `direct`
  mode behavior and existing tests are unchanged; `chat_presence` bypasses the queue.
- Local end-to-end: a test GOWA without a real number sends through durable mode to a receiver
  that is stopped and restarted; order and delivery checked.
- Production: enable `WHATSAPP_WEBHOOK_DELIVERY=durable` on devias (gowa); GOWA starts, workers
  idle (no webhook configured on the test number), `/webhooks/stats` answers. The full CRM round
  trip is verified once the CRM driver exists.

## 11. Out of scope

Multiple GOWA replicas sharing a queue; per-chat parallelism; delivery to Chatwoot (it keeps its
own retry queue); the CRM driver.
