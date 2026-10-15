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

### D-3 Timelock G6: ask WhatsApp for the state

- **What.** The ElphantCRM side is done (webhook subscription, state on the channel, payload, send
  errors; spec `2026-09-30-gowa-timelock-crm` in the CRM repo). Still open: asking WhatsApp for the
  timelock state on connect (no free source for the query; clean-room, do not read
  `devlikeapro/gows-plus`). Turning `WHATSAPP_REACHOUT_GUARD` on stays the owner's decision.

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

### D-14 Put the `.15` release and the statics secret in production

- **What.** Owner's decision (30/09/2026): nothing of this batch goes to the production stack yet.
  The image `v9.5.0-elphant.15` (signed `/statics` URLs, D-13) is published but the `gowa` stack stays
  on `v9.5.0-elphant.14`. Still to do, when the owner says so: update `GOWA_VERSION`, and decide
  whether to generate `APP_STATICS_SECRET` (a long random value) and add it to the stack
  environment with `APP_STATICS_AUTH` already on; without the secret links are not signed and
  nothing changes. Then turn `WHATSAPP_REACHOUT_GUARD` on only if the owner wants the guard.
- **Upstream sync (05/10/2026):** `v9.6.0-elphant.1` (upstream v9.6.0 plus #864, whatsmeow
  `20260929`) is published and `elphant` points at it; the stack stays on `v9.5.0-elphant.14`. It
  replaces `.15` as the next image to roll out (it contains the same fork code). Before the
  rollout, check on beta or a spare device that a read receipt from the account's own linked device
  (new upstream behavior) carries `is_from_me` in the `message.ack` webhook, so the CRM filter drops it.
- **Also pending, ElphantCRM:** after the `develop` deploy reaches production, run
  `php artisan gowa:webhook-sync` once so the existing channels subscribe to `session.timelock`.

### D-15 Use the account's own read receipts to mark conversations read in the CRM

- **What.** Since upstream v9.6.0 (#864) the gateway forwards `message.ack` read receipts sent by
  the account's own linked devices (WhatsApp Web, Desktop, other companions). The CRM drops acks
  with `is_from_me`, so nothing changes today. A future ElphantCRM change could treat that ack as
  "read by the owner" and clear the conversation's unread counter when someone reads the chat
  outside the CRM. Only useful if the connected number is also used in parallel on WhatsApp; the
  receipt only covers messages the contact sent. Not needed now (owner's decision, 05/10/2026).
- **Before relying on it.** Check in a real receipt that the `message.ack` payload carries
  `is_from_me`, so the current filter keeps dropping it until this is built.

### D-16 GOWA in the ElphantCRM v3: beta answers 422, dead webhook rows, bring the work over

- **What.** Owner's decision (05/10/2026): the CRM is migrating versions and GOWA goes into **v3**,
  not v2. The v2 repository (`elphantcrm`) `develop` was reset to the `master` line, so it has none
  of the GOWA commits (driver, B-166, number validator, LID promotion, timelock state, 30-number
  batch cap), and the beta (v2) answers `HTTP 422` to every GOWA webhook. Since 04/10 17:12 UTC the
  gateway holds more than 1600 `dead` rows for the beta test channel (outbox, `/webhooks/stats`).
  Test traffic only; the gateway is fine and production CRM never had the driver. Ignored on
  purpose until a staging option exists.
- **Where the work is.** Checked on 05/10/2026: the separate private repository
  `eduardoelphant/elphantcrm-v3` already has commit `823089919` on both `master` and `develop`, with
  `WhatsAppGowaDriver.php` on both, so the work is safe there. As a second copy, a git bundle of the
  history up to that commit is at `~/Documents/backups-elphantcrm/elphantcrm-gowa-823089919.bundle`
  on the owner's machine (not in any repository; `git bundle verify` passes). To restore from it:
  `git clone <bundle> dir` or `git fetch <bundle> archive-gowa-tmp:gowa-restore`.
- **Webhook removed (05/10/2026, owner's decision).** The device webhook on the gateway host was
  cleared (`PATCH /devices/<id>/webhook` with an empty URL, secret and events) so the gateway stops
  generating `422` rows. The count stopped at 1749 `dead` and `pending` is 0. While it is cleared the
  gateway does not queue webhook events for that device, so nothing reaches any CRM. To turn it back
  on: `php artisan gowa:webhook-sync` in the CRM that carries GOWA (it reapplies URL, secret and
  events from the channel's own block), or `PATCH` the device by hand with the CRM's webhook URL,
  the channel's `webhook_secret` and the events list.
- **To do, when it is time.**
  1. Confirm in the v3 repository that the GOWA code is what the v3 line should carry; the v3
     branch point is later than the v2 reset, so check the later commits that touch channels.
  2. After the v3 deploy that carries it, run `php artisan gowa:webhook-sync`.
  3. Decide about the dead rows: replay them (`POST /webhooks/replay?since=`) or let the
     retention clean them.
  4. The Materio connection screen (D-1) targets whatever line carries GOWA.

### D-17 Calls spike (meowcaller): open items before stage 2

- **What.** Stage 1 works (5 and 15 minute calls, both directions); results in
  `docs/reference/meowcaller-calls-spike.md`. Open before deciding on stage 2:
  1. In-call CPU: the 48% per call came from subnormal floats in the MLow encoder; dither plus
     PR #38 brought it to about 11.5% (patches in `spikes/meowcaller/patches/`). Still open: the
     audio check with dither, and the Opus option (the apps still accept Opus when the MLow
     capability bit is off; whatsapp-rust does it), which should cost a few percent.
  2. Measure CPU per call on the gateway host's hardware.
  3. Check the recordings by ear: they cover 50% to 85% of the call time.
  4. Send and receive text messages during a call.
- **Stage 2a (integration in GOWA).** Code done on branch `spike/meowcaller-calls`; runtime
  checks pending (Task 7). Open items:
  - (a) CLOSED: fork v0.2.0-elphant.1 has `WithOfferFilter`; calls attach to every client and are
    gated at offer/lookup time, so a newly paired device needs no restart. The GOWA codec log now
    shows the codec in effect (warns when opus is unavailable). Inbound-audio fix candidates
    (fork PR #26/#43) are in fork v0.2.0, pending live verification.
  - (b) Deferred minors:
    - Config: `WHATSAPP_CALLS_CODEC` / `MAX_PER_DEVICE` are not validated at config time (fallback and clamp cover it).
    - CLOSED in 2b: An expired offer is dropped without Reject; a failed Answer is not followed by Reject.
    - Comment on ringing calls is not counted in the cap.
    - CLOSED in 2b: Start returns State "active" even if the call ended during setup.
    - Concurrent EnsureClient on one slot can leave the losing client's manager in the map (pre-existing race).
    - The WARN repeats on every reconnect; closeAll Hangup path and Record-on-ended are untested.
    - A bare-digit slot alias is not masked in logs (JID context only).
    - Missing tests: ErrCallsOff at usecase level, hangup success and empty-field REST cases, zero StartedAt rendering.
  - (c) DONE in fork v0.2.0 (CGO_ENABLED=0 go build -tags purego . passes): add a `//go:build !cgo` stub to the fork's opus package returning errors, so
    CGO-free (`-tags purego`) builds work when calls are off. Today libopus is required always.
  - (d) CLOSED in 2b: REST and webhooks carry the full `pn`/`lid`; logs stay masked. Was: REST responses (`/calls/active`, `/call/start`) return a masked peer; the CRM needs the
    full JID. Masking belongs in logs, not API responses; change before stage 2b/CRM.
  - (e) CLOSED in 2b: `WHATSAPP_CALLS_MAX_DURATION_MINUTES` finishes a call even when the engine never reports its end. Was: a stale call can hold the cap if OnEnd never fires (media death or a terminate lost
    during reconnect). Add a max call duration and a media-ended to finish path.
  - (f) CLOSED in 2b: retention deletes recordings (and 2a's `.wav` files). Was: recordings in `storages/calls` have no retention yet (personal data); retention lands
    with the recording stage (spec D7).
- **Stage 2b (incl. the former 2c), live since 2026-10-15 (`v9.6.0-elphant.10` now).** Open after the code:
  - Runtime: first live calls passed (see "Stage 2b runtime" below); still open are the webhooks
    end to end, inbound answered in the page, per-call CPU and Chrome `jitterBufferDelay`.
  - Group calls stay out (offer filter): meowcaller's group mixer calls `sink.WriteFrame` outside
    `audioPlayoutMu`. A group offer on a call-enabled device is neither handled nor auto-rejected.
  - For a call refused by the cap, `call.ended` can reach the webhook before `call.offer`
    (different paths); consumers match by `call_id`.
  - The bridge sends the three 20 ms browser packets of each 60 ms WhatsApp frame at once; pace
    them if a browser reports jitter.
  - No TURN; RTP from the browser is not reordered before decoding.
  - pion adds no `a=ptime:20`; GOWA sends 20 ms frames anyway.
  - Policy rulings: outbound grace starts while the phone rings (a browser that never connects
    hangs up a ringing outbound call after 10 s); grace `<= 0` is clamped to 1 s; agent-to-customer
    FIFO prefill of 1600 samples costs about 40 ms extra latency.
  - Deferred minors, grouped:
    - Fork/audio: `ELPHANT.md` Versions lacks `.2`-`.4`; the stereo test does not prove channel
      separation and the bad-input test misses `channels=0`; `callaudio` Upsampler, Downsampler and
      Rechunker lack a "not safe for concurrent use" note, and have filter droop above 6 kHz, no 9 kHz
      alias test, a loose FIFO overflow test, `Rechunker(0)` loops and prefill above max never plays.
    - Recording: pion's `oggwriter` fixed pre-skip trims about 73 ms at the start; a failed
      `oggwriter.New` leaves an empty file (retention cleans it); a close error with zero frames is dropped.
      The recording link opens as a download (`Content-Disposition: attachment`), by design.
    - Call manager: every media factory error maps to 400 (a typed SDP error would be better); the
      ring-window prune is lazy; an already-ended `OnEnd` reason `ended` maps to `hangup_peer`; no test
      that the max-duration timer stops on a normal end; a failed `handle.Answer` racing a `Hangup` can
      send a Reject on a dead call; customer FIFO underrun stats are noisy (Debug only); the
      `legError` non-opus branch is unreachable.
    - CLOSED in 2b: the switch of the startup client was keyed by its AD JID while `PUT
      /devices/:id/calls` wrote the registry slot (toggle and switch-off had no effect); the slot is
      now resolved from the client at use time. `call.ended` after a logout kept no `device_id`; the
      device JID is now remembered from the call's earlier events.
    - Switch and webhooks: a stale `elphant_device_calls` row stays after a device is deleted (the
      cached value is dropped); an
      offer in flight during switch-off rings until the ring window ends; the browser leg is
      allocated for `call.offer` even with no webhook configured (closed at call end); a call ending
      at the same instant as SIGTERM may enqueue `call.ended` after the drain sees 0.
    - REST/OpenAPI: the 409 uses the BadRequest schema; the 400 of `GET /devices/:id/calls` is
      undocumented; a raw `handle.Answer` failure maps to 400, not 500; a malformed JSON body gives 500
      (pre-existing).
    - Test page: the page test checks substrings only; Hang up is unavailable during setup.
- **Cleanup.** The prototype's linked device on the test number (paired 2026-10-13) and
  `/root/meowcaller-spike/` on the gateway host (binary, scripts, session backup and logs) stay
  until the owner says the spike is over.

## Won't do

- **Runtime on the gateway host (2026-10-14, `v9.6.0-elphant.3`, calls on the DDD 11 device only):**
  - inbound from WhatsApp Web answered through `POST /call/answer`, test audio heard by the caller;
  - text messages sent and received during a call;
  - `POST /call/hangup` and `GET /calls/active` behave;
  - a 64 s call gave a 54 s recording with speech throughout;
  - CPU: about 10% to 25% of one core during a call against under 1% idle (container on 4 vCPU).
  - **Open:** in one of two inbound calls the relay never bridged the caller's audio (no "relay is bridging" log, empty recording) while our audio reached the caller. A host and container UDP capture on the next call showed every relay packet reaching the container, so it is not Docker NAT. Likely meowcaller issues #36/#40; investigate in the fork before the CRM beta.

- **Live results on the gateway host (2026-10-15, `v9.6.0-elphant.4`..`.6`, calls on the DDD 11 device):**
  - **Missing inbound audio**: the caller sometimes sent RED audio (payload type 122), which meowcaller dropped. It is fixed in fork `v0.2.0-elphant.3`, which reads both RED layouts. 3/3 inbound calls worked afterwards.
  - **Early media (PR #43, "Full")**: the caller saw the call as answered at the first ring, and the phone stopped ringing. Fork `v0.2.0-elphant.4` adds `WithEarlyMedia`, and GOWA gets `WHATSAPP_CALLS_EARLY_MEDIA`, `off` by default.
  - **`listen`**: the phone rang normally and could answer (`accepted_elsewhere`) or reject (`rejected_elsewhere`), with no file descriptors left open. 3/3 calls answered by GOWA 6 s after ringing started had inbound audio from the first packet and recordings of about 20 s with speech. **Use `listen` for the CRM**, where a human answers late.
  - **Still open**:
    - Off and Listen do not cover inbound group calls.
    - Mapped to `hangup_peer` in 2b: the end reason is empty when the peer hangs up.
    - The "calls enabled" log line also appears for devices that are not allowed, and twice for the default client.
    - GitHub Actions is refused on the gateway repository ("Actions has been disabled for this repository"), so images `.4`..`.6` were built on the host and are not in ghcr.io.

- **Stage 2b runtime on the gateway host (2026-10-15, `v9.6.0-elphant.7`, calls on the DDD 11 device only):**
  - Stack: UDP 3478 published in `mode: host`, `WHATSAPP_CALLS_RTC_PUBLIC_IP` set, `APP_STATICS_SECRET`
    added as a Portainer stack variable, `stop_grace_period: 30s`, `WHATSAPP_CALLS_TEST_AUDIO` removed.
    The host firewall accepts all input; no provider firewall blocked the port.
  - Per-device switch after startup: DDD 11 (registry slot) on, the other device off.
  - Three outbound calls from `/calls/phone`. Two to another person's phone were answered with the
    browser leg connected through the public IP and "media flowing on both legs", ended
    `hangup_agent` (12 s) and `hangup_peer` (18 s), both `COMPLETED`; the owner heard and was heard
    clearly both ways. Each produced a 2-channel Ogg Opus recording whose size matches the length.
  - The first call, to the owner's own second number, ended `REJECTED` in under a second, with no
    recording. The rejection came from another linked device of the called number (device 15), not from
    GOWA: that number's GOWA device saw the offer, the offer filter ignored it (switch off), and GOWA's
    auto-reject is off. Likely another integration linked to that number rejects calls automatically;
    check the called number's linked devices. A repeat at 13:32 showed the same reject from device 15
    (GOWA's device for that number is 17 in the whatsmeow store), and it showed a defect on our side: the called phone kept
    ringing, the owner answered, the phone showed "reconnecting" and dropped about 25 s later. meowcaller's
    `onReject` ends an outgoing call on the first device's reject without sending a terminate, while
    WhatsApp does not stop the callee's other devices. Fix in the fork: on a peer reject, either send a
    terminate (call ends REJECTED everywhere) or keep ringing until every device rejects or one accepts;
    the second matches what the phone did, but a real decline then rings until the timeout.
    FIXED in fork `v0.2.0-elphant.6` (GOWA `v9.6.0-elphant.8`): a peer reject on an outgoing call
    now sends a terminate, so the call ends REJECTED on every device.
  - Inbound answered in the page (13:35, from the owner's second number via WhatsApp Web): media on
    both legs, `hangup_agent` after 12 s, `COMPLETED`, recording written.
  - Idle CPU after the calls: under 0.1%; per-call CPU not measured yet.
  - Each call logs `selected audio codec from voip_settings codec=mlow` followed by `audio codec: opus`;
    the fork's Opus mode wins, as in stage 1.
  - The "calls enabled" log line names every device when calls are on globally; the per-device switch
    decides handling.
- **Webhooks end to end (2026-10-15, `v9.6.0-elphant.8`):** the DDD 11 device's own webhook (the CRM)
  whitelists message events only, so `call.*` went nowhere. For the test a temporary sink service on the
  overlay network got `call.*` through `WHATSAPP_WEBHOOK`, `WHATSAPP_WEBHOOK_EVENTS` and
  `WHATSAPP_WEBHOOK_DEVICE_MERGE_GLOBAL=true`, leaving the CRM's device webhook untouched.
  - Inbound answered in the page: `call.offer` (`calls_enabled: true`, `offer_sdp`), `call.answered`,
    `call.ended` `COMPLETED` with `recording`; all signed.
  - Outbound from the page, answered on an iPhone: `call.status` ACCEPTED, `call.answered`, `call.ended`
    `COMPLETED` with `recording`. The called number's own GOWA device also sent `call.offer` with
    `calls_enabled: false` (correct: it stays on the phone).
  - Defect: no `call.status` RINGING. `v9.6.0-elphant.9` assumed a missed callback and did not fix it
    (a live call on `.9` still had no RINGING). Cause: outbound media starts on the relay allocation in
    the call ack, which moves meowcaller's phase to Connecting before the preaccept, and the preaccept
    only sets Ringing from Calling. Fixed with fork `v0.2.0-elphant.7` (`Call.OnPeerPreAccept`, one-shot,
    replayed) in GOWA `v9.6.0-elphant.10`.
  - The device 15 reject did not recur: device 15 is the owner's WhatsApp Web, which placed the inbound
    test call. The `.8` terminate-after-reject path is covered by the fork's unit test only.
  - RINGING confirmed live on `v9.6.0-elphant.10` (outbound from the page to the owner's second number:
    `call.status` RINGING, then ACCEPTED, `call.answered`, `call.ended` `COMPLETED` with `recording`).
  - The sink service and its three env lines were removed afterwards; both devices back `logged_in`.
  - **Open:** the terminate-after-reject path (`.8`) is unit-tested only, since the auto-rejecting
    device did not reject again; Chrome `jitterBufferDelay` and per-call CPU still unmeasured; the
    CRM's device webhook must add `call.offer,call.status,call.answered,call.ended` to its event list
    when the CRM driver ships.

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
