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
  Before pairing, the per-device webhook configuration is resolved from it.
- The five `payload` keys are always present. A key that does not apply is `null`.

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
