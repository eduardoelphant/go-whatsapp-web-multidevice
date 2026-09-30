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

### D-3 Timelock G6

- **What.** Reachout timelock handling (G6). Needs a spec first. Clean-room: do not read
  `devlikeapro/gows-plus` (no license).

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

### D-4 Gateway items G8, G9

- **What.** `/statics` served before Basic Auth (G8), stability work such as goroutines without
  `recover` (G9). Each needs a spec first.

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
