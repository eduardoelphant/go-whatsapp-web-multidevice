# Open debt (elphant)

Work the fork knows about and has not done yet. One entry per item. When an item is paid,
move it to "Paid" with the commit that closed it, so the history of this file follows the
history of the work.

## Open

### D-1 Connection screen for the `whatsapp_gowa` driver (ElphantCRM)

- **What.** The CRM driver works end to end, but a channel can only be attached by the
  `gowa:attach` artisan command. `GOWA_NEW_CONNECTIONS` stays `false` until the screen exists.
- **Blocked by.** The Design System (Materio) worktrees. The owner says when they finish;
  do not start before that. The handoff is in the CRM repo:
  `docs/handoff/2026-09-25-gowa-tela-de-conexao.md`.
- **Includes.** The `pairing_requested` flag: a pairing confirmed by `sync` can leave
  `meta.status` false, so the channel stays off.

### D-3 Timelock G6: query and ElphantCRM side

- **What.** G6 is implemented (`session.timelock`, opt-in guard). Still open: asking WhatsApp for
  the timelock state on connect (no free source for the query; clean-room, do not read
  `devlikeapro/gows-plus`), and the ElphantCRM side (listen to `session.timelock`, show the state,
  decide when to turn `WHATSAPP_REACHOUT_GUARD` on).

### D-12 Production device gets `STREAM_REPLACED` shortly after connecting

- **What.** Seen 2026-09-30: the only production device connected on start and, about five
  minutes later, got `[STREAM_REPLACED] ... was opened elsewhere`, then stayed `disconnected`
  (the same happened earlier the same day). Something else opens the same credential. Prime
  suspect: the migration lab gateway still running on the owner's machine (see D-5). Not
  confirmed.
- **Also.** After `STREAM_REPLACED` the device stays down until someone reconnects it, by design
  (`it stays disconnected until reconnected`). The health signal for the CRM is `GET /devices`
  showing `disconnected`; nothing alerts on it.
- **Pay.** Confirm the lab process is off and the device holds `connected`. Then decide whether
  the gateway should alert (webhook or health check) when a device stays `disconnected`.

### D-13 A disconnected client shows up as `Panic recovered in middleware`

- **What.** While the device was down the logs carried `level=error msg="Panic recovered in
  middleware: you are not connect to services server, please reconnect"` and `... download media
  <id>: failed to refresh media connections`, repeated for the same media id. These are expected
  states, not panics. Check what status code the client gets; a disconnected device should be a
  clean 4xx/5xx with a stable error code, not a recovered panic.
- **Pay.** Find where the panic comes from (media download and send paths), return a typed error,
  add a test with a disconnected fake client.

### D-14 Benchmark GOWA vs Evolution per session (pending)

- **What.** The owner wants CPU and memory per session, one Evolution instance against one GOWA
  device, and an estimate of how many sessions the host holds. First read-only pass on 2026-09-30
  was inconclusive for the GOWA side because its device was disconnected (D-12).
- **Readings so far.** Evolution: one Node process for ~30 open instances, ~420 MiB and ~10% of
  one core (about 14 MiB and 0.35% per instance if split evenly; the fixed part of the process
  cannot be separated); its Redis holds ~2.9 MiB per instance on average and Postgres is shared
  with other databases. GOWA: one Go process with one device, ~29 MiB and ~0.5% of one core, but
  disconnected, so an upper bound at best. Load was low (about 0.1 message per second across all
  Evolution instances). Host: 4 vCPU, 16 GB, no swap, ~12 GB available.
- **Why it is hard.** Neither gateway reports CPU or memory per session (each runs all sessions
  in one process). A fair comparison needs the same account profile on both, and one account
  cannot be on both at once (same credential collides). The capacity number needs a
  mass-reconnect test, which is the real peak.
- **Missing.** Host monitoring (none today). Owner is thinking about a reproducible setup, for
  example a dedicated test number and a sampler run for 24 h.
- **Pay.** After D-12: repeat with the GOWA device connected, then run the same sampling with
  more sessions and fit CPU and memory per session; estimate capacity from the smaller of the RAM
  and the mass-reconnect CPU limit. Read-only on the host, no restarts.

## Won't do

### D-6 Debian 11 on the gateway host

Decision of the owner (30/09/2026): the host is **not** upgraded in place. A Debian upgrade only
happens as part of a server built from scratch (a new host, stack moved over with its volumes and
the backup restored). Do not run `apt upgrade` or SetupOrion on the current host.

## Paid

### D-13 Signed URLs for `/statics`

Paid in the commit "feat(statics): signed, expiring URLs": `pkg/staticurl` (HMAC over the decoded
path and the expiry), the `/statics` gate accepts a valid signature or Basic Auth, and the login
`qr_link` and the download `file_url` are signed when `APP_STATICS_AUTH`, `APP_STATICS_SECRET`
and Basic Auth are all set (`APP_STATICS_URL_TTL_MINUTES`, default 15). Webhook `path` fields are
not signed.

### D-5 Lab data cleanup (part B, task 10)

Done on 30/09/2026 with the owner's OK: `exports/` (the exported Evolution sessions) and `run/` (the
copied SQLite session, the log with personal messages and the lab password) were deleted from
`~/Documents/whatsapp-gateway-lab`. The session lives only on the gateway host. The two binaries
and `export-evolution-session.sh` were kept: they hold no personal data.

### D-12 (rest) Open parts of the G9 review

Paid in the commit "fix(resilience): poison rows, parallel watchdog ticks, manual reconnect hold":
a row whose attempt panics is tried again after a pause and marked dead after three panics, so the
rows behind it are delivered (`attemptContained`); a watchdog tick connects devices in parallel
(up to 8), so one slow connect no longer delays the others; `Reconnect` from the API holds the
watchdog off the client for 30 s (`HoldReconnect`), so a tick cannot dial between its disconnect
and connect. Left as accepted: no test drives the logout block, the panic of the global webhook
leg or the auth of `/health/devices` on the assembled server, because `restServer` has no seam
(the route order is pinned by `TestDeviceGroupIsRegisteredLast`).

### D-12 (part) Minor findings of the G9 review

Paid in the commit "fix(resilience): minors of the review": the multipart pipe of a scheduled
send is closed by a defer, so a panic no longer leaves `ReadForm` blocked; a panic in the global
webhook leg leaves its error set, so the event is reported failed instead of delivered; a logout
keeps the reconnect watchdog off the client being logged out, so a failed `Logout` cannot be
redialed before the reset; tests for an unpaired client value, the disabled watchdog and `Start`
ticking until the context ends.

### D-11 Minor findings of the G6 review

Paid in the commit "fix(reachout): minors of the review": the `session.timelock` timestamp has
sub-second precision so a consumer can order two webhooks; the guard also runs before a media
upload (`uploadMedia`), not only before the send; bots, PSA and the account's own phone and LID
are never refused; a test pins the defaults of the fork's settings (guard off, 30 minutes, watchdog
120 s, statics auth off, user check 500 ms). Left as accepted: reactions, revokes, edits and the auto
reply bypass `wrapSendMessage` (they target existing chats), and the integration tests that would
drive a real 463 through `wrapSendMessage` need a fake whatsmeow client that does not exist.

### D-10 Minor findings of the G5 review

Paid in the commit "fix(lid): minors of the review": lookups fall back to the shared LID map when
the device has no client (created, never paired); the batch `lids` path is one query
(`lidmap.PNsForLIDs`), straight from the table, with no per-entry query under whatsmeow's cache
lock and no cached misses; the list handle is one connection with `query_only` (SQLite) or a
read-only session (Postgres); a page that ends exactly on the limit has no `next`; lookup
failures answer a generic 500 with the cause only in the log; tests for validation errors, REST
`400`s through the real usecase and lookups and the list on a real store. Left as accepted: the
handle is not closed at shutdown (process exit closes it; `Close` exists) and a `file::memory:`
`DB_URI` makes the list fail, which no deployment uses.

### D-8 and D-9 Minor findings of the G7 review

Paid in the commit "fix(user): batch check minors": `pn` from the local map when only the LID came
back; a number with no answer when WhatsApp answered with an error is `error/upstream`, never
`not_exists`; the pacer refuses an already cancelled request; a leading `0` is `invalid_number`
and the server part of a JID is case-insensitive; tests for the usecase entry point; the spec says
what `query` holds for an invalid entry. Left as accepted: the pacer keeps one small slot per
device id, bounded by the number of devices.

### D-4 Gateway item G8 (`/statics` before Basic Auth)

Paid by the `APP_STATICS_AUTH` flag (`mountStatics` in `cmd/rest.go`, tests in
`cmd/rest_statics_test.go`); the flag is off by default, the owner decides when to turn it on.

### D-2 Message order after a redelivery (ElphantCRM, B-166)

Paid in ElphantCRM `b9c9a1339` (branch `feature/whatsapp-gateway`, not yet in `develop`): a received
message is placed at the WhatsApp time, capped at now. Details in `34-known-debt.md`, section
"Pagas".

### D-7 `GET /user/check` ignores the device header

Paid in the commit that follows `8122419` (see `git log -- src/ui/rest/user_check_device_test.go`):
`UserCheck` now passes `ContextWithDevice(c.Context(), getDeviceFromCtx(c))`, so `X-Device-Id`
selects the client instead of the default one. Test:
`TestUserCheckScopesTheRequestDevice`.
