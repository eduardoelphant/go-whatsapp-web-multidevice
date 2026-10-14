# Voice calls, stage 2b: the Cloud Calling contract on GOWA, with server-side recording

> **Status:** draft for the owner's review (2026-10-14). It builds on stage 2a (shipped in
> `v9.6.0-elphant.3`, see `docs/specs/2026-10-14-gateway-voice-calls-design.md`). It replaces
> that spec's REST shapes (D5, D6) and merges its stage 2c (recording and webhooks) into this
> stage.

## Problem

ElphantCRM v3 already runs WhatsApp **Cloud API Calling** end to end:
- the incoming-call banner;
- routing to the conversation's assignee or department;
- browser WebRTC (`useWebRtc.js`);
- the `calls` table and the call bubble in the conversation.

Unofficial numbers, which are coexistence numbers or WhatsApp-Web-only numbers, cannot use Cloud
Calling: Meta does not support calling on coexistence numbers, and Web-only numbers are not on the
Cloud API at all. GOWA can carry those calls (stage 2a proved it on the gateway host), but its API
does not match what the CRM's calling flow expects.

The owner wants the recording made **on the gateway**, not in the browser (decision of
2026-10-14). The CRM records in the browser only because the Cloud API offers no recording. That
recording is lost when the agent reloads the tab or loses the connection.

## Goal

1. GOWA exposes calls in the **same shape as Meta's Cloud Calling**: the remote side offers SDP
   on an incoming call, the browser answers; the browser offers on an outbound call, the gateway
   answers. The CRM's `whatsapp_gowa` driver can then plug into the existing calling flow and UI.
2. GOWA records every answered call, stereo with the agent on the left and the customer on the
   right, and hands the CRM a signed link when the call ends.
3. Calls can be switched on and off per device **at runtime** through the API. No env list and
   no restart.
4. The intermittent "caller's audio never arrives" defect seen on the host is fixed before the
   CRM beta (tracked separately; see "Prerequisite").

Out of scope: group calls, video, the CRM changes themselves (summarised below for the v3 spec),
TURN.

## Shape

```
customer phone ⇄ Meta relay ⇄ GOWA [meowcaller leg: Opus 16 kHz / 60 ms]
                                  ⇅ PCM bridge (resample 16 ⇄ 48 kHz) + stereo recorder
                               GOWA [WebRTC leg, pion: Opus 48 kHz / 20 ms, ICE-lite, DTLS-SRTP]
                                  ⇄ agent's browser (CRM useWebRtc.js, unchanged)
```

Both legs end in GOWA. Audio is decoded to PCM in the middle. That is where the recorder taps
both directions, and where the 16 kHz / 60 ms WhatsApp frames are converted to the browser's
48 kHz / 20 ms frames.

## Decisions

| # | Decision | Alternative rejected |
|---|---|---|
| B1 | GOWA plays Meta's role in WebRTC: **ICE-lite**, ICE-controlled, DTLS server (`a=setup:passive` in answers, `actpass` in offers), Opus 48 kHz stereo-capable with ptime 20, one audio SSRC, **non-trickle** SDP with the host candidate on the public IP. The browser side (`useWebRtc.js`) needs no change. | A new browser flow for GOWA. |
| B2 | WebRTC through **pion** with one UDP port for every call (ICE UDP mux): `WHATSAPP_CALLS_RTC_PORT` (default `3478`) and `WHATSAPP_CALLS_RTC_PUBLIC_IP` (the host's public IPv4, used as the 1:1 NAT address). The Swarm stack publishes that port in `mode: host`. | A port range. TURN (add only if agents behind strict networks fail). |
| B3 | Incoming call: `call.offer` carries `offer_sdp`, created by GOWA before the webhook is sent, for devices with calls on. `POST /call/answer {call_id, sdp}` takes the browser's answer, answers on WhatsApp and starts the bridge. CRM-side `pre_accept` maps to nothing; the CRM driver calls answer once. | Answer on WhatsApp before the browser is attached. |
| B4 | Outbound call: `POST /call/start {phone, sdp}` takes the browser's offer, places the WhatsApp call and returns `{call_id, sdp_answer}` at once (GOWA builds the answer locally). Progress then arrives as `call.status` webhooks: `RINGING`, `ACCEPTED`, `REJECTED`. | Sending the answer only by webhook (Meta does that because its answer comes from the far side; ours does not). |
| B5 | `call.ended` webhook: `status` (`COMPLETED` when media flowed, `FAILED`, `MISSED` (rang out or answered nowhere), `REJECTED`), `end_reason` (`hangup_agent`, `hangup_peer`, `rejected`, `timeout`, `answered_elsewhere`, `error`), `duration` in seconds, `direction` (`USER_INITIATED`/`BUSINESS_INITIATED`, Meta's words), and `recording` when one exists. | Reusing 2a's free-text end reasons. |
| B6 | Recording: on by default for call-enabled devices. Stereo Ogg Opus, agent left and customer right, 24 kbps a channel, written to `storages/calls/<call_id>.ogg` as the call runs (page by page, so a crash keeps what was written). `recording = {url, expires_at, bytes, duration, format: "ogg_opus_stereo", channels: {left: "agent", right: "customer"}}`. The URL is `GET /call/:call_id/recording?exp=&sig=` (HMAC, same scheme as the signed `/statics` URLs of D-13), valid `WHATSAPP_CALLS_RECORDING_URL_TTL_MINUTES` (default `1440`). Files are deleted after `WHATSAPP_CALLS_RECORDING_RETENTION_DAYS` (default `7`). | Browser recording (lost on reload). WAV (about 10x larger). The file inside the webhook. |
| B7 | Per-device switch at runtime: `PUT /devices/:device_id/calls {enabled: bool}` and `GET /devices/:device_id/calls`, stored in the device registry. The env `WHATSAPP_CALLS_ENABLED` becomes the master switch, and `WHATSAPP_CALLS_DEVICES` only seeds devices on first start. | Env list plus restart (2a). |
| B8 | B7 needs meowcaller on every client while the master switch is on, but meowcaller pre-accepts every offer. The fork gains an **offer filter** option, `meowcaller.WithOfferFilter(func(*events.CallOffer) bool)`, consulted before pre-accept. GOWA passes "is this device enabled now". A disabled device's offers are not touched, so the phone rings as it does today. | Recreating the client to switch calls on (drops the connection). |
| B9 | Audio bridge per call: meowcaller's PCM sink → resample 16→48 kHz → pion Opus 20 ms track; pion inbound Opus → decode → resample 48→16 kHz → meowcaller source. One jitter buffer on each side (meowcaller already has one inbound; the WebRTC side uses a 60 ms buffer). | Re-packetising Opus without decoding (frame sizes and clocks differ, and the recorder needs PCM anyway). |
| B10 | When the browser leg drops (ICE failed or disconnected for `WHATSAPP_CALLS_RTC_GRACE_SECONDS`, default `10`), GOWA hangs up the WhatsApp leg, and the reverse. The recording is closed and `call.ended` is sent either way. | Leaving the customer on a silent line. |
| B11 | REST responses carry the peer's full `pn`/`lid` (the CRM needs them to match the contact). Logs stay masked. | Masked peer in responses (2a). |
| B12 | The cap `WHATSAPP_CALLS_MAX_PER_DEVICE` stays, default `1` until concurrency is measured. Above it, the offer is rejected and `call.ended` `MISSED` is sent. | |

## API (device-scoped with `X-Device-Id`, Basic Auth as today)

| Method and path | Body | Result |
|---|---|---|
| `PUT /devices/:device_id/calls` | `{enabled}` | `{enabled}` |
| `GET /devices/:device_id/calls` | none | `{enabled}` |
| `GET /calls/active` | none | Calls with `call_id`, `direction`, `peer{pn,lid}`, `state`, `started_at` |
| `GET /call/:call_id` | none | `{call_id, state, direction, peer, offer_sdp}`, so the browser can fetch the offer again (mirrors the CRM's `GET calls/{call}`) |
| `POST /call/answer` | `{call_id, sdp}` | `{call_id}`. 409 when the call is already answered, 400 when it is unknown or ended |
| `POST /call/start` | `{phone, sdp}` | `{call_id, sdp_answer}` |
| `POST /call/reject` | `{call_id, caller_jid}` | Unchanged. It now also sends `call.ended` `REJECTED` |
| `POST /call/hangup` | `{call_id}` | Ends both legs |
| `GET /call/:call_id/recording` | signed query | `audio/ogg`. 410 after retention |

## Webhooks (durable delivery, G3)

All carry `device_id`, `call_id`, `peer{pn, lid}`, `direction`, `timestamp`.

| Event | When | Extra fields |
|---|---|---|
| `call.offer` | Incoming call. Existing event, extended | `calls_enabled`, and `offer_sdp` when calls are on for the device |
| `call.status` | Outbound progress | `status`: `RINGING`, `ACCEPTED`, `REJECTED` |
| `call.answered` | Media flowing on both legs | `answered_by`: `api` or `elsewhere` (the phone or another linked device took it) |
| `call.ended` | Call over | B5 fields plus `recording` |

`docs/webhook-payload.md` and the `stable` contract (G4) gain these.

## CRM v3 side (summary for the CRM spec; nothing here touches the CRM)

The v3 calling code is hard-wired to Cloud:
- `WhatsAppCloudService` is injected into `CallController`, `CallSettingsController` and
  `ProcessIncomingEventJob:2124`;
- `initiate` hardcodes `provider: 'whatsapp_cloud'`;
- the calling flag is read only from `meta.whatsapp_cloud.calling`.

The CRM work:
1. A **calls provider interface** (`answer`, `reject`, `terminate`, `initiate`), implemented by the
   Cloud service and by a new GOWA one over `GowaService`. Pick it by the channel's driver.
2. **`WhatsAppGowaDriver::normalize`** maps `call.offer`, `call.status` and `call.ended` to the
   same `raw` keys the Cloud driver emits (`_call_event` connect|terminate|status_update,
   `_call_sdp`, `_call_status`, `_call_duration`, `_call_direction`), so `handleCall` stays as it
   is.
3. **Calling switch for GOWA channels**: `meta.whatsapp_gowa.calling.status`, a toggle in the
   channel panel with the ban-risk warning, calling `PUT /devices/:id/calls`. `ChannelSerializer`
   exposes `calling_enabled` for GOWA channels too.
4. **Recording**: on `call.ended` with `recording`, a job downloads the file into the CRM storage
   and fills `calls.meta.recording`, the same way the browser upload path does. It then
   dispatches `TranscribeCallRecordingJob` (stereo gives speaker labels). The browser recorder is
   skipped when the call's provider is GOWA.
5. `OutboundCallSdpAnswer` is broadcast right after `initiate` returns, because GOWA's answer is
   synchronous.

## Prerequisite: the intermittent inbound-audio defect

On the gateway host, one of two inbound calls never received the caller's audio:
- the relay was connected and peer SRTCP arrived, but there was no peer RTP;
- the next call worked;
- a packet capture ruled out Docker NAT.

The fix lands in the meowcaller fork before this stage goes to the CRM beta. The investigation is
in progress; see D-17.

## Rollout

1. Fork: offer filter (B8), the inbound-audio fix, and a `//go:build !cgo` stub for `opus/`. Tag
   `v0.2.0-elphant.1`.
2. GOWA: the WebRTC leg, the bridge, the recorder, signed URLs, retention, the REST changes and
   the webhooks.
   - Test it with a minimal test page in the embedded UI that uses the same `RTCPeerConnection`
     calls as `useWebRtc.js`.
   - Test on the gateway host with one device, owner present.
3. Stack: publish UDP `3478` in `mode: host`, set the public IP, open the host firewall.
4. CRM v3: the five items above, in the CRM's own spec and worktree. Test on the CRM beta with the
   DDD 11 channel.

## Risks and open questions

- **Ban risk** on every number with calls on. The CRM toggle must say so.
- **Agents behind strict firewalls** may get no audio without TURN. Add coturn only if it happens.
- **CPU:**
  - 2a measured about 10% to 25% of a core per call on the host, with one leg.
  - The bridge adds a 48 kHz Opus encode and decode plus resampling, so expect roughly double.
  - Measure before raising the cap above 1.
- **iOS and WhatsApp Desktop callers** are still untested in Opus mode.
- **Concurrency:** several calls on one number at once is still unknown.
- **LGPD:** recording consent wording and retention in the CRM are the owner's legal decisions.
- **Stale calls:** if WhatsApp never sends the end, a call can hold the cap. Add a hard
  `WHATSAPP_CALLS_MAX_DURATION_MINUTES` (default `120`) that hangs up and sends `call.ended`
  `FAILED`.
