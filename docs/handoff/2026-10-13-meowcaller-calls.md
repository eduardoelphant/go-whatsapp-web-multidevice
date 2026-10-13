# Handoff: WhatsApp voice calls with meowcaller (spike)

**Date:** 2026-10-13
**For:** a new session working only on this subject.
**Worktree:** `~/Documents/whatsapp-gateway-calls`, branch `spike/meowcaller-calls` (created from
`elphant` at `dbadc70`; local only, never pushed).
**Language with the owner:** Portuguese (BR). Code, commits and docs in this repository: English,
no attribution line.

## 1. Goal

Find out, with a throwaway prototype, whether WhatsApp voice calls (answer an incoming call, place
an outgoing call, two-way audio) work reliably on top of whatsmeow using
[meowcaller](https://github.com/purpshell/meowcaller), and then whether it can live inside GOWA
without breaking anything. This is a spike: the output is an answer and measurements, not a
feature. Nothing here goes to production.

Two stages, in order:

1. **Standalone prototype**, outside GOWA's module: does a call ring, connect and carry audio both
   ways for minutes, and what does it cost (CPU, memory, bandwidth)?
2. **Cohesion check**, only if stage 1 works: can meowcaller run in the same process as GOWA, on
   the same `go.mau.fi/whatsmeow` version GOWA uses, with GOWA's full test suite still green?

## 2. Hard rules

- **Never use the owner's personal number**, and never the device paired on the gateway host
  ("devias (gowa)"). A dedicated test number is required; ask the owner for it and for the OK
  before pairing. Two processes holding the same WhatsApp session fight (`StreamReplaced`).
- **No production, no server.** Do not touch the gateway host, its Portainer stack or any CRM
  environment. Everything runs on the owner's Mac.
- **No push, tag, image or PR** without the owner's explicit OK. Never open issues or PRs upstream
  (the fork is fork-only).
- **Never print credentials**: session keys, the SQLite session database, QR content, pairing
  codes. Show names, counts and states only. Keep session databases out of git (`.gitignore`).
- **Licenses:** meowcaller and WaCalls are MIT; copying code from them requires keeping their
  license and attribution. **AstraCalls is AGPL-3.0: do not copy code from it**, read it only for
  ideas if at all. (A gateway that copied meowcaller's call code without attribution, `quepasa`,
  had that directory taken down by a DMCA notice from meowcaller's author on 2026-06-30.)
- Go: run from the module directory with `GOTOOLCHAIN=auto` (local Go is older than the modules
  require). The prototype must not change `src/go.mod` during stage 1.

## 3. What is known (checked on 2026-10-13)

- GOWA today only sees and rejects calls: the `call.offer` webhook, `POST /call/reject`, and
  `WHATSAPP_AUTO_REJECT_CALL`. whatsmeow parses call signaling (`events.CallOffer`, `CallAccept`,
  `CallTerminate`, ...) and has `RejectCall`, but no media.
- The missing media layer (Meta's TURN relays, SRTP keyed from the signaling, the proprietary
  MLow voice codec) is implemented in pure Go by:

  | Project | What | License | Notes |
  |---|---|---|---|
  | `purpshell/meowcaller` | Library on top of whatsmeow: answer, call, play/record audio, video, experimental group calls | MIT | 277 stars, last commit 2026-09-06, no cgo |
  | `JotaDev66/WaCalls` | Full browser call server, multi-account, concurrent 1:1 calls | MIT | 312 stars, says "stable" |
  | `AstraOnlineWeb/AstraCalls` | "Production" version: voice and video, recording, Chatwoot, SIP/PBX | AGPL-3.0 | do not copy |

- meowcaller usage, from its README: `meowcaller.NewClient(wa)`, `OnIncomingCall` with
  `call.Answer()`, `call.Play(source)`, `call.Receive(sink)`, and `client.Call(ctx, "+55...")`.
- **Dependency catch:** meowcaller's current `main` (commit `0eebabe`, 2026-09-06) swapped its
  import path to a whatsmeow fork, `github.com/polymorfa/hypermeow`. GOWA uses the official
  `go.mau.fi/whatsmeow v0.0.0-20260929112325-8b41cfe6d9c4`. The commit message says that change is
  a path rewrite with no code changes. For stage 2, either pin a meowcaller revision from before
  `0eebabe` (still on the official whatsmeow) or vendor meowcaller with the import path rewritten
  back; test which one builds and works against GOWA's whatsmeow.
- Background reading: whatsmeow issue #555 (VoIP/call support, technical notes on TURN auth and
  SRTP), GOWA upstream issue #741 (feature request citing WaCalls and AstraCalls, no maintainer
  reply), the WhatsApp Calls Research Group linked from meowcaller's README.
- The official alternative is the WhatsApp Cloud API Calling (no ban risk); the CRM already has a
  base for Cloud calls. This spike is only about the unofficial path.

## 4. Stage 1: standalone prototype

Put it in `spikes/meowcaller/` inside the worktree as **its own Go module** (`go mod init`), so
GOWA's `src/go.mod` stays untouched. Keep its session database under `spikes/meowcaller/data/`,
git-ignored.

1. Pair the dedicated test number (QR or pairing code), owner present, with the owner's OK.
2. **Inbound:** another phone calls the test number; the prototype answers, plays a short audio
   file, records the caller to WAV, hangs up after a set time.
3. **Outbound:** the prototype calls another phone; same checks.
4. **Duration:** at least one 5-minute and one 15-minute call. Earlier attempts by others dropped
   after about 20 seconds; this is the main thing to rule out.
5. **Measure:** CPU and memory of the process during a call, network traffic (UDP to the relays),
   time to ring and to connect, audio quality heard on the phone and in the recording.
6. **Watch for trouble:** any "number restricted", logout, `StreamReplaced`, call refused by
   WhatsApp, or warnings in the phone app. Stop at the first sign of an account restriction and
   tell the owner.

Report: what worked, what failed with the exact error, the measurements, and a recommendation.

## 5. Stage 2: cohesion with GOWA (only if stage 1 works)

1. Make meowcaller build against GOWA's whatsmeow version (see the dependency catch above).
2. Add it to GOWA behind a flag, off by default, wiring calls to the existing device instances:
   one meowcaller client per device's whatsmeow client. Keep the change small; a REST answer/call
   endpoint is enough to prove it.
3. Prove nothing else broke: from `src/`, `GOTOOLCHAIN=auto go vet ./...` and
   `GOTOOLCHAIN=auto go test ./...` green, including `TestEveryBackgroundGoroutineIsProtected`
   (every goroutine started by the fork must use `pkg/safego`; meowcaller's own goroutines are in
   a dependency and are not scanned, but anything GOWA starts around it is), the durable webhook
   tests, and the reconnect watchdog tests.
4. Check that messages still flow while a call is up (send and receive a text during a call).
5. Note the deployment needs for later, without doing them: UDP egress from the container to
   Meta's relays, CPU per concurrent call, and the Docker Swarm stack changes.

## 6. Questions to bring back to the owner

- Is the result reliable enough to offer, and as what (beta, opt-in per channel)?
- Does the owner accept the ban risk for numbers that use calls, and on which numbers?
- Stage 2 design and where calls would surface in the CRM (that is CRM v3 work and screen work
  for the Materio team; nothing here touches the CRM).

## 7. Where to record things

- Findings and the final recommendation: a spec or a reference page under `docs/` in the worktree
  (`docs/specs/` for a design, `docs/reference/` for measured facts). Never `docs/superpowers/`.
- Open items: `docs/elphant-debt.md`.
- Keep the worktree and branch local until the owner decides.
