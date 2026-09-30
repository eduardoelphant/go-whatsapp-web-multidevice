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

### D-8 Minor findings of the G7 review

- **What.** Deferred from the final review of `POST /user/check`
  (`docs/specs/2026-09-29-gateway-g7-batch-user-check-design.md`):
  1. An `exists` answer with an LID and no phone JID gives `pn: null`; no `GetPNForLID` fallback.
  2. The pacer can run a function for a request that is already cancelled if the slot and the
     cancellation are ready together; add `ctx.Err()` after acquiring.
  3. The pacer keeps one slot per device id forever (tiny, bounded by devices).
  4. Numbers with a leading `0` (`0055...`) are accepted and come back `not_exists`, and an
     upper-case `@S.WHATSAPP.NET` is rejected; reject a leading `0` as `invalid_number`.
  5. The spec says `query` is always digits; invalid entries carry the trimmed text (the fork
     page says so). Align the spec.
  6. No test drives `IsOnWhatsAppBatch` itself (no client, validation errors, pacer wiring,
     400 for an empty list or 101 entries end to end).
- **Pay.** One small pass with a test per item.

### D-9 Partial batch answer with an error can report a number as not registered

- **What.** In `runCheckBatch`, when `client.IsOnWhatsApp` returns `err != nil` together with
  partial answers (the "failed to store LID mappings" case), a number that got no answer at all
  (neither itself nor its ninth-digit variant) and no unmatched "in" answer becomes `not_exists`.
  Found in the review of the ElphantCRM validator (B-168).
- **Pay.** With `err != nil`, report unanswered numbers as `error` / `upstream`, with a test.

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

### D-4 Gateway item G8

- **What.** `/statics` served before Basic Auth (G8). Needs a spec first.

### D-5 Lab data cleanup (part B, task 10)

- **What.** Delete `run/`, `exports/` and the logs of the migration lab on the owner's Mac.
  They hold personal data. Only with the owner's OK.

### D-6 Debian 11 on the gateway host

- **What.** The host is out of support. Upgrade in an agreed window, after a backup.
  Do not run SetupOrion on it.

## Paid

### D-2 Message order after a redelivery (ElphantCRM, B-166)

Paid in ElphantCRM `b9c9a1339` (branch `feature/whatsapp-gateway`, not yet in `develop`): a received
message is placed at the WhatsApp time, capped at now. Details in `34-known-debt.md`, section
"Pagas".

### D-7 `GET /user/check` ignores the device header

Paid in the commit that follows `8122419` (see `git log -- src/ui/rest/user_check_device_test.go`):
`UserCheck` now passes `ContextWithDevice(c.Context(), getDeviceFromCtx(c))`, so `X-Device-Id`
selects the client instead of the default one. Test:
`TestUserCheckScopesTheRequestDevice`.
