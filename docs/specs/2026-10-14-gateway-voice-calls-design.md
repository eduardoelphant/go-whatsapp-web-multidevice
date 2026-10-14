# Voice calls in GOWA, bridged to the CRM agent's browser

> **Status:** draft for the owner's review (2026-10-14). Nothing here is implemented. It builds on
> the stage 1 spike: `docs/handoff/2026-10-13-meowcaller-calls.md` and the measured results in
> `docs/reference/meowcaller-calls-spike.md`.

## Problem

The CRM wants WhatsApp voice calls on the unofficial channels. An agent should receive and place
calls from the CRM screen, and every call should land in the conversation with its recording, a
transcript and a summary.

GOWA today only sees calls (`call.offer` webhook) and rejects them (`POST /call/reject`,
`WHATSAPP_AUTO_REJECT_CALL`). whatsmeow has the signaling but no media.

The spike proved the media layer works on top of GOWA's own whatsmeow version, through meowcaller
(MIT) with three local patches:
- inbound and outbound calls work, both up to 15 minutes without a drop;
- with standard Opus instead of MLow, a call costs about 4% of one core on the Mac;
- tested peers so far are Android and WhatsApp Web.

The official Cloud API Calling is not an option for these numbers: Meta does not support calling
on coexistence numbers, and moving a number fully to the Cloud API ends its unofficial session.

## Goal

1. An agent answers an incoming call in the CRM and talks to the customer from the browser.
2. An agent places a call from a contact or conversation in the CRM.
3. When a call ends, the CRM gets its metadata and a stereo recording (agent on one channel,
   customer on the other). The CRM then shows it in the conversation and sends it to
   transcription.
4. Everything is off by default and enabled per device, because calls carry a ban risk for the
   number that uses them.

Out of scope: group calls, video, an AI voice agent (the same bridge could carry one later), PSTN,
and voicemail.

## Shape

```
customer (WhatsApp) ⇄ Meta relay ⇄ GOWA [meowcaller, Opus] ⇄ WebRTC [pion] ⇄ agent's browser (CRM)
                                      └─ stereo recorder
```

- GOWA terminates both legs. It decodes the WhatsApp leg's Opus and re-encodes it for WebRTC, and
  does the same in the other direction.
- Both legs are Opus, so the cost stays low. Only frame size and clock differ: WhatsApp uses 16 kHz
  with 60 ms frames, and the browser uses 48 kHz with 20 ms frames.
- The CRM backend relays the browser's SDP to GOWA. The browser then sends media straight to
  GOWA's WebRTC port, never through the CRM.

## Decisions

| # | Decision | Alternative rejected |
|---|---|---|
| D1 | meowcaller lives in the repository at `src/third_party/meowcaller` (pinned `27a3c6b`, its LICENSE kept, our patches applied and listed in a README), wired with a `replace` in `src/go.mod`. It imports the official `go.mau.fi/whatsmeow`. | A separate fork repository (one more thing to publish and sync). Upstream `main` (it moved to the `hypermeow` fork of whatsmeow). |
| D2 | Opus is the codec: we clear capability 31 (`use_mlow_codec_v1`) and send `voip_settings` that turn MLow off when accepting. MLow, with the dither fix, stays as a fallback behind `WHATSAPP_CALLS_CODEC=mlow`. | MLow by default (about 3x the CPU even after the fix). |
| D3 | libopus through cgo (`gopkg.in/hraban/opus.v2`, MIT, build tag `nolibopusfile`). The image installs `opus` / `opus-dev` on Alpine. GOWA already builds with cgo. | A pure-Go Opus encoder (young, unproven). |
| D4 | Off by default: `WHATSAPP_CALLS_ENABLED=false`. `WHATSAPP_CALLS_DEVICES` lists the device ids allowed to use calls (empty means all, when enabled). A device not allowed keeps today's behaviour. | Global on/off only (no way to limit the ban risk to chosen numbers). |
| D5 | GOWA does not answer the WhatsApp call until a browser is attached. `POST /call/answer` carries the browser's SDP offer. GOWA sets up the WebRTC leg, answers on WhatsApp and returns the SDP answer. | Answer first and attach later (the customer would hear silence). |
| D6 | WebRTC through pion with one UDP port for all calls (ICE UDP mux, `WHATSAPP_CALLS_RTC_PORT`, default `3478`), published by the Swarm stack in `mode: host`, with the host's public IP set as the 1:1 NAT address. TURN is not used at first. | A port range (harder to publish in Swarm). A TURN server from the start (one more service; add it only if agents behind strict networks fail). |
| D7 | Recording on by default for allowed devices (`WHATSAPP_CALLS_RECORD=true`): stereo Ogg Opus, agent left and customer right, kept under `storages/calls/` for `WHATSAPP_CALLS_RECORDING_RETENTION_DAYS` (default `7`). The CRM fetches it through a signed URL (same model as the G4 media endpoint). | WAV (about 10x larger). Pushing the file in the webhook (large payloads, retries). |
| D8 | Optional announcement played to the customer when the call connects (`WHATSAPP_CALLS_ANNOUNCE_FILE`, for example "this call may be recorded"). Whether to use it is the CRM's legal decision (LGPD). | Hard-coding it. |
| D9 | `WHATSAPP_CALLS_MAX_PER_DEVICE` (default `1`) caps simultaneous calls per number until concurrency is tested. Above it, an incoming call is rejected and reported as missed. | No cap. |
| D10 | Call webhooks use the durable delivery (G3), like messages. | Fire and forget. |

## REST API (device-scoped with `X-Device-Id`, Basic Auth as today)

| Method and path | Body | Result |
|---|---|---|
| `POST /call/answer` | `call_id`, `sdp` (browser offer) | `sdp` (answer). The WhatsApp call is answered. |
| `POST /call/start` | `phone` or `jid`, `sdp` (browser offer) | `call_id`, `sdp` (answer). The WhatsApp call rings. |
| `POST /call/hangup` | `call_id` | Ends the call on both legs. |
| `POST /call/reject` | `call_id`, `caller_jid` | Unchanged. |
| `GET /calls/active` | none | Active calls on the device: id, direction, peer, state, start time. |
| `GET /call/:call_id/recording` | signed query (`exp`, `sig`) | The Ogg Opus file. 410 after retention. |

Rules:
- If the browser leg drops, GOWA hangs up the WhatsApp leg after a grace period (`WHATSAPP_CALLS_RTC_GRACE_SECONDS`, default `10`). The reverse also holds.
- Mute stays in the browser (a disabled track); GOWA needs no endpoint for it.

## Webhooks

All carry `device_id`, `call_id`, `direction` (`inbound` or `outbound`), `peer` (`pn` and `lid`
where known), and a timestamp.

| Event | When | Extra fields |
|---|---|---|
| `call.offer` | Incoming call (exists today) | Unchanged. It also gains `calls_enabled` so the CRM knows whether it can be answered. |
| `call.ringing` | Outbound call placed and ringing | none |
| `call.answered` | Media flowing on both legs | `answered_by` (`agent`, or `phone` when the number's own phone took it) |
| `call.ended` | Call over | `reason` (`hangup_agent`, `hangup_peer`, `rejected`, `timeout`, `error`), `duration_ms`, `media_ms`, `recording` (`url`, `expires_at`, `bytes`, `format: ogg_opus_stereo`, `channels: {left: agent, right: customer}`) |
| `call.missed` | Incoming call not answered in the CRM, or rejected by the cap | `reason` |

`docs/webhook-payload.md` gains these events. The `stable` contract (G4) gains a `call` block.

## CRM side (summary for the CRM v3 team; their spec owns the details)

- **Real-time call banner** for agents of the channel on `call.offer`, with the contact and
  conversation. Answering creates an `RTCPeerConnection`, sends its offer through the CRM backend
  to `POST /call/answer`, and applies the answer.
- **Softphone widget** (Materio team): answer, reject, hang up, mute, dial from a contact,
  call timer, and audio device choice.
- **Permissions:** who may answer and place calls, per channel.
- **On `call.ended`:** download the recording, store it in the CRM storage, add a "call" item to
  the conversation timeline with a player, then run transcription (stereo gives speaker labels for
  free) and an optional AI summary, both attached to the item.
- **Missed calls** appear in the timeline and in reports.
- **Reports:** calls per agent, missed calls, duration.

## Rollout

1. **Stage 2a: library in GOWA.**
   - Vendor meowcaller with its patches. Add the flag, answer and start with a file source and a
     recording, without the browser.
   - Proves cohesion: `go vet ./...` and `go test ./...` green, including
     `TestEveryBackgroundGoroutineIsProtected`, the durable webhook tests and the watchdog tests.
   - Messages must keep flowing during a call.
   - Measure CPU per call on the gateway host.
2. **Stage 2b: browser bridge.** pion leg, the REST endpoints above, and a test softphone page in
   GOWA's embedded UI (Vue), so the bridge is tested before the CRM exists.
3. **Stage 2c: recording and webhooks.** Stereo recorder, signed recording URL, retention, and the
   five events. This is the contract handed to the CRM.
4. **CRM v3:** banner, softphone, timeline item, transcription. Their spec, their worktrees.
5. **Production:** one allowed device first, then more, owner's call each time.

## Infrastructure needs (for the stack, not done here)

- Outbound UDP from the container to Meta's relays. This already worked from the host in the spike.
- One inbound UDP port (D6) published in `mode: host`, and the host firewall opened for it.
- `opus` libraries in the image. CPU headroom of about 5% of a core per concurrent call. This was
  measured on the Mac and must be re-measured on the host.
- Disk for recordings: Opus at about 24 kbps a channel is roughly 20 MB per stereo hour, and
  retention (D7) cleans it.

## Risks and open questions

- **Ban risk** on every number with calls enabled (unofficial path). The owner chooses the numbers
  (D4).
- **Untested peers:** iOS and WhatsApp Desktop in Opus mode. A peer that keeps sending MLow would
  give one-way silence; GOWA should detect "no decodable audio for N seconds" and log it.
- **Concurrency:** several calls on one number at once is unknown (D9 caps it at 1).
- **A call placed from the number's own phone** never reaches the linked devices (seen in the
  spike). The CRM will not see those calls.
- **Agents' networks:** without TURN, an agent behind a strict firewall may get no audio. TURN is
  the fix if it happens.
- **Library upkeep:** our meowcaller copy diverges from upstream (Opus mode, dither, FFT patch).
  Re-check upstream on each GOWA sync and send the dither fix upstream.
- **LGPD:** recording consent and retention in the CRM are legal decisions (D8).
