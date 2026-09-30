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

### D-3 Validator G7 and timelock G6

- **What.** Batch number check (G7) and timelock handling (G6). Each needs a spec first.
  G6 is clean-room: do not read `devlikeapro/gows-plus` (no license).

### D-7 `GET /user/check` ignores the device header

- **What.** `UserCheck` passes `c.Context()` without `ContextWithDevice`, so
  `ClientFromContext` falls back to the global default client and `X-Device-Id` has no effect.
  Found while writing the G7 spec (`docs/specs/2026-09-29-gateway-g7-batch-user-check-design.md`).
  The new `POST /user/check` scopes the device correctly.
- **Pay.** Add `ContextWithDevice(c.Context(), getDeviceFromCtx(c))`, with a test on two devices.

### D-4 Gateway items G5, G8, G9

- **What.** LID handling (G5), `/statics` served before Basic Auth (G8), stability work
  such as goroutines without `recover` (G9). Each needs a spec first.

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
