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

### D-10 Minor findings of the G5 review

- **What.** Deferred from the final review of the LID endpoints
  (`docs/specs/2026-09-30-gateway-g5-lid-mappings-design.md`):
  1. A device created but never paired has no client, so the lookups answer `INVALID_WA_CLI`;
     the spec says they only need the device to exist. The map is global, so the usecase could
     use the store container's `LIDMap` directly.
  2. The batch `lids` path calls `GetPNForLID` once per entry: each miss is one SQL query under
     whatsmeow's exclusive cache lock and is cached as empty forever (the `pns` path is batched
     and caches no misses). Batch the LIDs with `WHERE lid IN (...)` in `pkg/lidmap`.
  3. The list handle is not really read only, is never closed and has no pool limit. Use
     `SetMaxOpenConns(2)`, `query_only` on SQLite or `default_transaction_read_only` on
     Postgres, and close it at shutdown.
  4. A table whose size is an exact multiple of `limit` gives a last full page with a `next` and
     one empty page after it. Fetch `limit+1` or reword the docs.
  5. Store errors on the lookups are wrapped as plain errors, so the `500` message carries raw
     driver text, unlike the list. A `file::memory:` DBURI makes the list always fail.
  6. Tests: the usecase tests assert only that an error happens, not that it is a validation
     error; REST has no `400` test for a bad `:phone`, a bad `:lid` or more than 500 entries; no
     lookup test runs on the real `sqlstore`; the route-order test cannot fail.
- **Pay.** One small pass with a test per item.

### D-11 Minor findings of the G6 review

- **What.** Deferred from the final review of the reach-out timelock
  (`docs/specs/2026-09-30-gateway-g6-reachout-timelock-design.md`):
  1. Outside durable mode two `session.timelock` webhooks can arrive out of order (`go deliver()`),
     and `timestamp` is to the second. Add a sequence number or `RFC3339Nano`.
  2. Reactions, revokes, edits (`usecase/message.go`) and the auto reply do not go through
     `wrapSendMessage`: no guard and no 463 marking. Low risk (existing chats); route them through
     a shared helper if it ever matters.
  3. The guard runs after the media upload, so a refused media forward already downloaded and
     uploaded the file. Check before the upload.
  4. The guard also blocks sends to the account's own JID and to bot or PSA JIDs, which have no
     tctoken (whatsmeow exempts PSA and bots). Exempt them.
  5. Tests: no test drives `wrapSendMessage` through a real 463 (marking plus the `429`); no
     config-default test (off, 30); no test that an event on a non-canonical instance and a guard
     check on the slot instance share state; `send_reachout_test.go` calls `NoteReachoutTimelock`
     with the real dispatch, which starts an unstubbed webhook goroutine.
- **Pay.** One small pass with a test per item.

### D-12 Minor findings of the G9 review

- **What.** Deferred from the final review of the resilience work
  (`docs/specs/2026-09-30-gateway-g9-resilience-design.md`):
  1. The watchdog can connect between the `Reconnect` usecase's disconnect and connect (the API
     then answers `ErrAlreadyConnected`), and `Tick` connects devices one at a time, so N down
     devices can take about 50 s each. Connect in parallel, or skip a device with a recent manual
     action.
  2. A logout whose `cli.Logout` fails leaves `Store.ID` set; between `Disconnect` and
     `ResetClient` a tick can redial. Re-check `Store.ID` and deletion just before `Connect`.
  3. An outbox row whose attempt panics restarts the worker every 30 s forever and blocks its
     URL. Mark the row dead after N panics.
  4. `usecase/schedule.go` (multipart pipe) and `webhook_forward.go` (global error) goroutines: a
     panic before the pipe is closed leaves `ReadForm` blocked, and a panic leaves `globalErr`
     nil, so the event is reported as delivered. Close the pipe in a defer and set the error in
     the recover.
  5. Tests: the D4 rule on a real `DeviceInstance` (nil client, `Store.ID` nil, deleted), `Start`
     and the disabled case, auth on `/health/devices`, and the per-pass containment in the
     scheduler, Chatwoot retry worker and sweeper.
- **Pay.** One small pass with a test per item.

### D-13 Signed URLs for `/statics`

- **What.** `APP_STATICS_AUTH` protects `/statics` with Basic Auth, which breaks a browser UI on
  another origin that loads files in `<img>` tags. Signed, expiring URLs (`?exp=&sig=`, an HMAC
  with `APP_STATICS_SECRET`) accepted in place of Basic Auth would serve that case. Do it only if
  such a consumer appears.

### D-5 Lab data cleanup (part B, task 10)

- **What.** Delete `run/`, `exports/` and the logs of the migration lab on the owner's Mac.
  They hold personal data. Only with the owner's OK.

### D-6 Debian 11 on the gateway host

- **What.** The host is out of support. Upgrade in an agreed window, after a backup.
  Do not run SetupOrion on it.

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

## Paid

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
