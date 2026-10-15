# Voice calls, stage 2b (Cloud Calling contract, browser leg, recording) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** GOWA carries WhatsApp calls in the shape of Meta's Cloud Calling: the gateway offers SDP
on an incoming call and the browser answers, the browser offers on an outbound call and the gateway
answers. The audio is bridged between the WhatsApp leg and a WebRTC leg in GOWA, recorded in stereo
(agent left, customer right), and reported through durable `call.*` webhooks with a signed
recording link. Calls are switched per device at runtime.

**Architecture:**
- Each call has two legs that both end in GOWA. The WhatsApp leg is meowcaller (Opus 16 kHz,
  60 ms frames). The browser leg is pion WebRTC: ICE-lite, DTLS server, one UDP port for every
  call, Opus 48 kHz in 20 ms frames, non-trickle SDP.
- Between them sits a PCM bridge (`calls_bridge.go`). It resamples 16 kHz to 48 kHz and back
  (`pkg/callaudio`) and feeds the stereo Ogg Opus recorder (`pkg/callrec`). meowcaller's 60 ms
  send loop is the bridge's clock.
- `CallManager` (`calls.go`) owns the call state, the cap, the ring window and the max duration.
  It also maps end reasons to Meta's statuses and emits `CallEvent`s. It reaches the browser leg
  through a `callMedia` seam, so its tests run on fakes.
- The per-device switch lives in a fork-owned table, `elphant_device_calls`.
  `WHATSAPP_CALLS_ENABLED` stays the master switch, and `meowcaller.WithOfferFilter` reads the
  switch on every offer.
- Webhooks go through `forwardPayloadToConfiguredWebhooks`, so they are durable in
  `WHATSAPP_WEBHOOK_DELIVERY=durable` (G3). One ordered queue keeps a call's events in order.
- The recording route reuses the HMAC scheme of the signed `/statics` URLs (D-13).

**Tech Stack:**
- Go 1.26 (`GOTOOLCHAIN=auto`), whatsmeow, Fiber v3 and testify.
- meowcaller fork `v0.2.0-elphant.5` (new in Task 1), with libopus 1.5 through cgo.
- `github.com/pion/webrtc/v4` **v4.2.23**, the latest v4 on 2026-10-15: ICE UDP mux, address
  rewrite, DTLS role and `pkg/media/oggwriter`.
- `github.com/santhosh-tekuri/jsonschema/v6` for the `contract/` module.

**Spec:** `docs/specs/2026-10-14-gateway-voice-calls-2b-design.md` (B1-B12, API and webhooks).
Read it first. It is binding. Stage 2a context: `docs/specs/2026-10-14-gateway-voice-calls-design.md`
and `docs/plans/2026-10-14-gateway-voice-calls-2a.md`.

## Global Constraints

- **Workspace.** Work in the worktree `~/Documents/whatsapp-gateway-calls`, branch
  `spike/meowcaller-calls`. Local only: no push, tag push, image, PR or server change without the
  owner's explicit OK. Task 1 (fork tag push) and Task 12 (runtime) ask for it.
- **Go commands.** Run Go from `src/` with `GOTOOLCHAIN=auto`. The `contract/` module runs from
  `contract/`.
- **Master switch.** `WHATSAPP_CALLS_ENABLED` defaults to `false`. Off means behaviour identical
  to today: the `call.offer` payload, `POST /call/reject` and `WHATSAPP_AUTO_REJECT_CALL`.
- **Per-device switch.** `PUT /devices/:device_id/calls {enabled}` and
  `GET /devices/:device_id/calls` return `{enabled}`. The value is stored in the device registry.
  `WHATSAPP_CALLS_DEVICES` only seeds devices on first start.
- **Offer filter.** `meowcaller.WithOfferFilter` is consulted before pre-accept. A disabled
  device's offers are not touched, so the phone rings as it does today.
- **WebRTC port.** `WHATSAPP_CALLS_RTC_PORT` (default `3478`) is one UDP port for every call (ICE
  UDP mux). `WHATSAPP_CALLS_RTC_PUBLIC_IP` is the host's public IPv4, used as the 1:1 NAT
  address. The Swarm stack publishes the port in `mode: host`.
- **Browser leg (B1).** GOWA is **ICE-lite** and ICE-controlled, and acts as the DTLS server
  (`a=setup:passive` in answers, `actpass` in offers). Audio is Opus 48 kHz, stereo-capable, ptime
  20, with one audio SSRC. SDP is **non-trickle**, with the host candidate on the public IP, and
  every line ends in CRLF.
- **WhatsApp leg.** Opus 16 kHz in 60 ms frames (`meowcaller.SampleRate = 16000`,
  `meowcaller.FrameSamples = 960`). The browser side runs at 48 kHz in 20 ms frames.
- **Grace (B10).** `WHATSAPP_CALLS_RTC_GRACE_SECONDS` defaults to `10`. When the browser leg is
  down that long, GOWA hangs up the WhatsApp leg, and the reverse also holds. Either way the
  recording is closed and `call.ended` is sent.
- **Recording (B6).** On by default for call-enabled devices. The file is stereo Ogg Opus at
  **24 kbps a channel**, agent **left** and customer **right**, written to
  `storages/calls/<call_id>.ogg` page by page.
  `recording = {url, expires_at, bytes, duration, format: "ogg_opus_stereo", channels: {left: "agent", right: "customer"}}`.
  The URL is `GET /call/:call_id/recording?exp=&sig=` (HMAC, the D-13 scheme). It stays valid
  `WHATSAPP_CALLS_RECORDING_URL_TTL_MINUTES` minutes (default `1440`).
  `WHATSAPP_CALLS_RECORDING_RETENTION_DAYS` (default `7`) deletes old files, and the route returns
  410 after retention.
- **Max duration.** `WHATSAPP_CALLS_MAX_DURATION_MINUTES` (default `120`) hangs up and sends
  `call.ended` `FAILED`.
- **Cap (B12).** `WHATSAPP_CALLS_MAX_PER_DEVICE` (default `1`). Above it, the offer is rejected
  and `call.ended` `MISSED` is sent.
- **Webhooks.** Durable delivery (G3). Every call webhook carries `device_id`, `call_id`,
  `peer{pn, lid}`, `direction` (`USER_INITIATED`/`BUSINESS_INITIATED`) and `timestamp`.
  - `call.offer` gains `calls_enabled`, plus `offer_sdp` when calls are on for the device.
  - `call.status` has `status`: `RINGING`, `ACCEPTED` or `REJECTED`.
  - `call.answered` has `answered_by`: `api` or `elsewhere`.
  - `call.ended` has `status` (`COMPLETED`, `FAILED`, `MISSED` or `REJECTED`), `end_reason`
    (`hangup_agent`, `hangup_peer`, `rejected`, `timeout`, `answered_elsewhere` or `error`),
    `duration` in seconds, and `recording` when one exists.
- **REST.**
  - `GET /call/:call_id` returns `{call_id, state, direction, peer, offer_sdp}`.
  - `POST /call/answer {call_id, sdp}` returns `{call_id}`, 409 when already answered, and 400
    when the call is unknown or ended.
  - `POST /call/start {phone, sdp}` returns `{call_id, sdp_answer}`.
  - `POST /call/reject` is unchanged, and now also sends `call.ended` `REJECTED`.
  - `POST /call/hangup` ends both legs.
- **Masking.** Logs stay masked: no full numbers or JIDs (`MaskDigits`, `maskSlot`). REST
  responses and webhooks carry the full `pn`/`lid` (B11).
- **Fork maintenance.** Upstream GOWA files get only single lines marked `// Fork (elphant)`.
  New code goes in fork-owned files: `calls*.go`, `pkg/callaudio`, `pkg/callrec`, `pkg/callrtc`,
  `cmd/calls_flags.go`, `cmd/rest_calls.go`, `ui/rest/call_device.go`, `ui/rest/call_phone.*`
  and `pkg/error/conflict_error.go`. The 2a fork blocks may grow.
- **Goroutines.** Every goroutine GOWA starts goes through `pkg/safego`. A `time.AfterFunc`
  callback starts with `defer safego.Recover(...)`.
- **meowcaller.** Changes go to the fork (`~/Documents/meowcaller`, branch `elphant`, listed in
  `ELPHANT.md`), never into GOWA.
- **Writing.** Code, comments, commits and docs are in English, with no attribution line. Docs and
  fixtures carry no real IP, token or personal number (use `203.0.113.x` and `5511900000000`).

## Review Focus

1. **The browser never connects after answering.** Causes: the tab closes between answer and
   ICE, or UDP 3478 is blocked. Expected: the WhatsApp leg is hung up after
   `WHATSAPP_CALLS_RTC_GRACE_SECONDS`, the recording closes and `call.ended` goes out. The
   customer is never left on a silent line. Pinned in Task 7
   (`TestBridgeGraceWhenBrowserNeverConnects`, `TestManagerBridgeBrowserLostHangsUp`).
2. **WhatsApp never sends the end of a call.** Causes: a lost terminate, or an engine that
   already forgot the call. Expected: the max duration still finishes it, frees the cap and sends
   `FAILED`/`timeout`. Pinned in Task 3 (`TestMaxDurationFinishesSilentEngine`).
3. **A device is switched off in the middle of a call.** Expected: its live calls end with
   `call.ended`, and its next offers are left alone, so the phone rings. Pinned in Task 2
   (`TestSwitchOffEndsLiveCalls`, `TestSetDeviceCallsAppliesAtOnce`).
4. **A bad recording link.** That means a tampered or expired link, another call's signature, or
   a path-like `call_id`. Expected: it never serves a file without credentials, and never a file
   outside `storages/calls`. Pinned in Task 5
   (`TestRecordingRouteRejectsTamperedExpiredAndTraversal`).
5. **A browser offer with LF-only line endings, or with no Opus.** Expected: the LF offer is
   normalised and works. An offer with no Opus gets a 400 before any WhatsApp call is placed.
   Pinned in Task 6 (`TestOutboundLegAnswersBrowserOffer`,
   `TestOutboundOfferWithoutOpusIsRefused`) and Task 3 (`TestStartBadOfferPlacesNoCall`).

## Decisions taken in this plan (spec silences)

- **Answered elsewhere.** `accepted_elsewhere` sends `call.answered` with `answered_by:
  "elsewhere"`. It then sends `call.ended` with `COMPLETED` / `answered_elsewhere`, duration 0
  and no recording.
- **Status rule.**
  - `COMPLETED` means media flowed on both legs. The exception is the max duration, which always
    gives `FAILED`/`timeout`.
  - Otherwise an answered call gives `FAILED` and an unanswered one gives `MISSED`.
  - A browser-leg loss gives `end_reason` `error`.
  - A rejection by the cap gives `MISSED` / `rejected`.
- **Recording URL.** `recording.url` is relative to the server root, the same rule as
  `stable.media.url` (G4), so `APP_BASE_PATH` is included. It is signed with
  `APP_STATICS_SECRET` over the resource `call-recording:<call_id>`. Without a secret it is the
  bare path. The route always accepts Basic Auth too, so a late webhook (durable retry) can still
  fetch the file.
- **The switch's storage.** The switch is stored in a fork table, `elphant_device_calls`, created
  on first use. No upstream migration is touched.
  - `PUT` with the master switch off returns 400.
  - Switching off ends the device's live calls.
  - A device with no stored value is seeded from `WHATSAPP_CALLS_DEVICES`. The seed is not
    persisted before pairing when the list holds phone numbers.
- **Directions and peers.** `direction` uses Meta's words in REST too (`GET /calls/active`). The
  peer's `pn` and `lid` are JID strings, and `""` when unknown.
- **Removed from 2a.** `WHATSAPP_CALLS_TEST_AUDIO` and the mono `.wav` peer recording are removed.
  Retention also deletes 2a's leftover `.wav` files.
- **`call.offer` and groups.** The extra `call.offer` fields appear only with the master switch on.
  Group offers are left to the phone: the offer filter returns false for them.
- **The G4 contract.** Call events get their own closed schema, `contract/call.schema.json`, with
  fixtures in `contract/call-fixtures/` (not `payload.stable`).
- **When the max duration counts.** It counts from when the call is tracked (offer or placement).
- **Answer without an offer.** Answering before an offer exists returns 400 ("fetch offer_sdp
  first").
- **`POST /call/reject` routing.** For a call the manager tracks, it goes through the manager.
  Otherwise it uses whatsmeow, as today.
- **Recording switch.** The spec names no on/off env for recording, so recording is always on for
  call-enabled devices.

## File map

| File | Responsibility | Task |
|---|---|---|
| `~/Documents/meowcaller/opus/opus.go`, `opus_nocgo.go`, `opus_test.go`, `ELPHANT.md` | Stereo `NewEncoderChannels` | 1 |
| `src/infrastructure/chatstorage/sqlite_repository_calls.go` (+test) | `elphant_device_calls` table | 2 |
| `src/infrastructure/whatsapp/calls_switch.go` (+test) | Runtime per-device switch, seed, switch-off | 2 |
| `src/ui/rest/call_device.go` (+test) | `PUT/GET /devices/:device_id/calls` | 2 |
| `src/pkg/error/conflict_error.go` | 409 error type | 3 |
| `src/infrastructure/whatsapp/calls.go` (+test) | Call state, events, end mapping, max duration, media seam | 3, 8 |
| `src/infrastructure/whatsapp/calls_meowcaller.go` (+test) | Adapter: full peer, ringing/accept/media hooks, audio bridge hook | 3 |
| `src/cmd/calls_flags.go` (+test) | 2b env and flags | 3, 5, 7 |
| `src/pkg/callaudio/*` | Resampler 16⇄48 kHz, re-chunker, jitter FIFO | 4 |
| `src/pkg/callrec/*` | Stereo Ogg Opus recorder, retention, signed path | 5 |
| `src/cmd/rest_calls.go` (+test) | `GET /call/:call_id/recording` | 5 |
| `src/infrastructure/whatsapp/calls_services.go` | Start-up: retention job, WebRTC engine | 5, 7 |
| `src/pkg/callrtc/*` | pion leg: engine, inbound/outbound legs, SDP normalising | 6 |
| `src/infrastructure/whatsapp/calls_bridge.go` (+test) | PCM bridge, grace, recorder tap | 7 |
| `src/domains/call/call.go`, `src/usecase/call.go`, `src/ui/rest/call.go` | REST surface | 2, 3, 8 |
| `src/infrastructure/whatsapp/calls_webhook.go` (+test) | Call webhooks, ordered queue, `call.offer` fields | 9 |
| `contract/call.schema.json`, `contract/call-fixtures/*`, `contract/call_schema_test.go` | Call webhook contract | 9 |
| `src/ui/rest/call_phone.go`, `call_phone.html` (+test) | Test softphone page | 10 |
| `docs/openapi.yaml`, `docs/webhook-payload.md`, `docs/reference/elphant-fork.md`, `docs/elphant-debt.md` | Docs | 8, 9, 11 |

---

### Task 1: Stereo Opus encoder in the meowcaller fork (`v0.2.0-elphant.5`)

The recorder writes one stereo Opus stream: Ogg Opus channel mapping family 0 with 2 coupled
channels. The fork's `opus/` binding only creates mono encoders, so the fork gains
`NewEncoderChannels`. It sets `OPUS_SET_FORCE_CHANNELS` so that libopus never folds the two
speakers into mono at 48 kbps. This is a fork change with a new tag. Tasks 2, 3, 4 and 6 do not
need the tag, so they can proceed while the push waits for the owner's OK. Task 5 needs it.

**Files:**
- Modify: `~/Documents/meowcaller/opus/opus.go`, `~/Documents/meowcaller/opus/opus_nocgo.go`,
  `~/Documents/meowcaller/opus/opus_test.go`, `~/Documents/meowcaller/ELPHANT.md`
- Modify (GOWA): `src/go.mod`, `src/go.sum`

**Interfaces:**
- Produces: `opus.NewEncoderChannels(sampleRate, channels, bitrate, complexity int, dtx bool) (*opus.Encoder, error)`.
  Stereo frames passed to `(*Encoder).Encode` are interleaved L, R, L, R, so a 60 ms stereo frame
  at 16 kHz is 1920 values. `NewEncoder` keeps its signature and calls
  `NewEncoderChannels(sampleRate, 1, ...)`.

- [ ] **Step 1: Write the failing test** (append to `~/Documents/meowcaller/opus/opus_test.go`; add `"math"` to its imports)

```go
func TestStereoEncoderKeepsBothChannels(t *testing.T) {
	enc, err := NewEncoderChannels(16000, 2, 48000, 5, false)
	if err != nil {
		t.Fatal(err)
	}
	pcm := make([]float32, 2*960)
	for i := 0; i < 960; i++ {
		pcm[2*i] = 0.3 * float32(math.Sin(2*math.Pi*440*float64(i)/16000))
		pcm[2*i+1] = 0.3 * float32(math.Sin(2*math.Pi*1300*float64(i)/16000))
	}
	pkt, err := enc.Encode(pcm)
	if err != nil {
		t.Fatal(err)
	}
	if pkt[0]&0x04 == 0 {
		t.Fatalf("TOC 0x%02x: stereo flag not set", pkt[0])
	}
	if n, err := PacketSamples(pkt, 16000); err != nil || n != 960 {
		t.Fatalf("packet holds %d samples per channel (%v), want 960", n, err)
	}
}

func TestEncoderChannelsRejectsBadInput(t *testing.T) {
	if _, err := NewEncoderChannels(16000, 3, 24000, 5, false); err == nil {
		t.Fatal("3 channels must fail")
	}
	enc, err := NewEncoderChannels(16000, 2, 48000, 5, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := enc.Encode(make([]float32, 961)); err == nil {
		t.Fatal("an odd stereo frame must fail")
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `cd ~/Documents/meowcaller && GOTOOLCHAIN=auto go test ./opus/ -run 'Stereo|EncoderChannels'`
Expected: FAIL to build (`undefined: NewEncoderChannels`).

- [ ] **Step 3: Implement**

In `opus/opus.go`, add a cgo helper next to the others in the preamble:

```c
static int enc_set_force_channels(OpusEncoder *e, opus_int32 v) { return opus_encoder_ctl(e, OPUS_SET_FORCE_CHANNELS(v)); }
```

Replace the `Encoder` type, `NewEncoder` and `Encode` with:

```go
// Encoder encodes float32 frames, mono or interleaved stereo.
type Encoder struct {
	enc      *C.OpusEncoder
	out      []byte
	channels int
}

// NewEncoder creates a mono VoIP encoder. Frames passed to Encode must be a valid Opus
// frame size for sampleRate (960 samples is 60 ms at 16 kHz).
func NewEncoder(sampleRate, bitrate, complexity int, dtx bool) (*Encoder, error) {
	return NewEncoderChannels(sampleRate, 1, bitrate, complexity, dtx)
}

// NewEncoderChannels creates a VoIP encoder for 1 or 2 channels. Stereo frames are
// interleaved (L, R, L, R, ...), and stereo is forced so two independent voices stay on
// their own channels. bitrate is the total for all channels.
func NewEncoderChannels(sampleRate, channels, bitrate, complexity int, dtx bool) (*Encoder, error) {
	if channels != 1 && channels != 2 {
		return nil, fmt.Errorf("opus: %d channels, want 1 or 2", channels)
	}
	var cerr C.int
	enc := C.opus_encoder_create(C.opus_int32(sampleRate), C.int(channels), C.OPUS_APPLICATION_VOIP, &cerr)
	if cerr != C.OPUS_OK || enc == nil {
		return nil, fmt.Errorf("opus: encoder create: %d", int(cerr))
	}
	e := &Encoder{enc: enc, out: make([]byte, maxPacketBytes), channels: channels}
	runtime.SetFinalizer(e, func(e *Encoder) { C.opus_encoder_destroy(e.enc) })
	d := C.opus_int32(0)
	if dtx {
		d = 1
	}
	if C.enc_set_bitrate(enc, C.opus_int32(bitrate)) != C.OPUS_OK ||
		C.enc_set_complexity(enc, C.opus_int32(complexity)) != C.OPUS_OK ||
		C.enc_set_dtx(enc, d) != C.OPUS_OK {
		return nil, fmt.Errorf("opus: encoder ctl failed")
	}
	if channels == 2 && C.enc_set_force_channels(enc, 2) != C.OPUS_OK {
		return nil, fmt.Errorf("opus: force channels failed")
	}
	return e, nil
}

// Encode encodes one frame (interleaved when stereo) and returns a new packet slice.
func (e *Encoder) Encode(pcm []float32) ([]byte, error) {
	if len(pcm) == 0 || len(pcm)%e.channels != 0 {
		return nil, fmt.Errorf("opus: frame of %d values for %d channel(s)", len(pcm), e.channels)
	}
	n := C.opus_encode_float(e.enc, (*C.float)(unsafe.Pointer(&pcm[0])), C.int(len(pcm)/e.channels),
		(*C.uchar)(unsafe.Pointer(&e.out[0])), C.opus_int32(len(e.out)))
	runtime.KeepAlive(pcm)
	if n < 0 {
		return nil, fmt.Errorf("opus: encode: %d", int(n))
	}
	return append([]byte(nil), e.out[:n]...), nil
}
```

In `opus/opus_nocgo.go`, add:

```go
// NewEncoderChannels always returns ErrUnavailable in builds without cgo.
func NewEncoderChannels(sampleRate, channels, bitrate, complexity int, dtx bool) (*Encoder, error) {
	return nil, ErrUnavailable
}
```

In `ELPHANT.md`, add a row to "Changes on top of upstream":

```markdown
| `opus.NewEncoderChannels` (`opus/opus.go`, `opus/opus_nocgo.go`) | Stereo encoder with forced stereo, for GOWA's call recorder (agent left, customer right, one Ogg Opus stream); `NewEncoder` is now the mono case of it |
```

and under "Versions":

```markdown
- `v0.2.0-elphant.5`: `opus.NewEncoderChannels` (stereo, forced channels) for GOWA's stereo call recording.
```

- [ ] **Step 4: Run the fork's checks**

Run:
```bash
cd ~/Documents/meowcaller
gofmt -l opus
GOTOOLCHAIN=auto go vet ./... && GOTOOLCHAIN=auto go test ./...
CGO_ENABLED=0 GOTOOLCHAIN=auto go build ./...
```
Expected: no gofmt output; vet clean; every package `ok`; the CGO-free build succeeds.

- [ ] **Step 5: Commit and tag in the fork (local)**

```bash
cd ~/Documents/meowcaller
git add opus/opus.go opus/opus_nocgo.go opus/opus_test.go ELPHANT.md
git commit -m "opus: NewEncoderChannels for stereo recording"
git tag v0.2.0-elphant.5
```

- [ ] **Step 6: Push the branch and the tag (needs the owner's explicit OK)**

Ask the owner first. With the OK:

```bash
cd ~/Documents/meowcaller && git push origin elphant && git push origin v0.2.0-elphant.5
```

Without the OK, stop here and go on with Tasks 2, 3, 4 and 6. Come back before Task 5.

- [ ] **Step 7: Point GOWA at the new tag**

```bash
cd ~/Documents/whatsapp-gateway-calls/src
GOTOOLCHAIN=auto go mod edit -replace github.com/purpshell/meowcaller=github.com/eduardoelphant/meowcaller@v0.2.0-elphant.5
GOTOOLCHAIN=auto go mod tidy
GOTOOLCHAIN=auto go build ./... && GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/
```
Expected: build OK; `ok` for the package.

- [ ] **Step 8: Commit**

```bash
git add src/go.mod src/go.sum
git commit -m "build(calls): meowcaller fork v0.2.0-elphant.5 (stereo Opus encoder)"
```

---
### Task 2: Per-device calls switch at runtime (B7, B8)

**Files:**
- Create: `src/infrastructure/chatstorage/sqlite_repository_calls.go`, `src/infrastructure/chatstorage/sqlite_repository_calls_test.go`
- Create: `src/infrastructure/whatsapp/calls_switch.go`, `src/infrastructure/whatsapp/calls_switch_test.go`
- Modify: `src/infrastructure/whatsapp/calls_registry.go` (`callsAllowedNow`, `offerFilter`)
- Modify: `src/infrastructure/whatsapp/calls_registry_test.go` (`withCallsConfig`)
- Modify: `src/domains/call/call.go`, `src/usecase/call.go`
- Create: `src/ui/rest/call_device.go`, `src/ui/rest/call_device_test.go`
- Modify: `src/ui/rest/call_test.go` (stub fields)
- Modify: `src/cmd/rest.go` (one line after `rest.InitRestDevice(apiGroup, deviceUsecase)`)
- Modify: `src/.env.example`, `src/config/calls.go` (comments on `WHATSAPP_CALLS_DEVICES`)

**Interfaces:**
- Consumes: `config.CallsAllowed(slotID, jidUser, jidString string) bool`, `callsByClient`,
  `(*CallManager).closeAll()`, `(*CallManager).slotID`, `GetDeviceManager().ResolveDevice(id) (*DeviceInstance, string, error)`.
- Produces:
  - `(*chatstorage.SQLiteRepository).SetDeviceCallsEnabled(deviceID string, enabled bool) error`
  - `(*chatstorage.SQLiteRepository).GetDeviceCallsEnabled(deviceID string) (enabled, found bool, err error)`
  - `whatsapp.SetDeviceCalls(deviceID string, enabled bool) (bool, error)`, `whatsapp.DeviceCalls(deviceID string) (bool, error)`
  - `whatsapp.ErrCallsMasterOff` (400)
  - package-level test seams `callsStoreFn func() deviceCallsStore`, `resolveCallsSlot func(string) (string, *whatsmeow.Client, error)`, `resetDeviceCallsCache()`
  - `domainCall.DeviceCallsRequest{Enabled *bool}`, `domainCall.DeviceCallsResponse{Enabled bool}`
  - `ICallUsecase.SetDeviceCalls(ctx, deviceID string, enabled bool) (DeviceCallsResponse, error)`, `ICallUsecase.GetDeviceCalls(ctx, deviceID string) (DeviceCallsResponse, error)`
  - `rest.InitRestDeviceCalls(app fiber.Router, service domainCall.ICallUsecase)`

- [ ] **Step 1: Write the failing storage test**

`src/infrastructure/chatstorage/sqlite_repository_calls_test.go`:

```go
package chatstorage

import "testing"

func TestDeviceCallsEnabledRoundTrip(t *testing.T) {
	repo := newTestSQLiteRepository(t)

	on, found, err := repo.GetDeviceCallsEnabled("slot-a")
	if err != nil || found || on {
		t.Fatalf("empty table: got (%v, %v, %v), want (false, false, nil)", on, found, err)
	}
	if err := repo.SetDeviceCallsEnabled("slot-a", true); err != nil {
		t.Fatal(err)
	}
	if on, found, err = repo.GetDeviceCallsEnabled("slot-a"); err != nil || !found || !on {
		t.Fatalf("after on: got (%v, %v, %v)", on, found, err)
	}
	if err := repo.SetDeviceCallsEnabled("slot-a", false); err != nil {
		t.Fatal(err)
	}
	if on, found, err = repo.GetDeviceCallsEnabled("slot-a"); err != nil || !found || on {
		t.Fatalf("after off: got (%v, %v, %v)", on, found, err)
	}
	if err := repo.SetDeviceCallsEnabled(" ", true); err == nil {
		t.Fatal("a blank device id must fail")
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `cd src && GOTOOLCHAIN=auto go test ./infrastructure/chatstorage/ -run TestDeviceCallsEnabledRoundTrip`
Expected: FAIL to build (`repo.GetDeviceCallsEnabled undefined`).

- [ ] **Step 3: Implement the storage**

`src/infrastructure/chatstorage/sqlite_repository_calls.go`:

```go
package chatstorage

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Fork (elphant): per-device voice calls switch (stage 2b, B7). It lives in its own table,
// created on first use, so upstream's numbered migrations stay untouched.

const createDeviceCallsTable = `CREATE TABLE IF NOT EXISTS elphant_device_calls (
	device_id VARCHAR(255) PRIMARY KEY,
	enabled BOOLEAN NOT NULL,
	updated_at TIMESTAMP NOT NULL
)`

// SetDeviceCallsEnabled stores whether calls are on for a device slot.
func (r *SQLiteRepository) SetDeviceCallsEnabled(deviceID string, enabled bool) error {
	if strings.TrimSpace(deviceID) == "" {
		return fmt.Errorf("device id is required")
	}
	if _, err := r.db.Exec(createDeviceCallsTable); err != nil {
		return err
	}
	now := time.Now()
	res, err := r.db.Exec(`UPDATE elphant_device_calls SET enabled = ?, updated_at = ? WHERE device_id = ?`, enabled, now, deviceID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		_, err = r.db.Exec(`INSERT INTO elphant_device_calls (device_id, enabled, updated_at) VALUES (?, ?, ?)`, deviceID, enabled, now)
	}
	return err
}

// GetDeviceCallsEnabled reads a device slot's calls switch; found is false when the slot
// has never been set.
func (r *SQLiteRepository) GetDeviceCallsEnabled(deviceID string) (enabled, found bool, err error) {
	if _, err := r.db.Exec(createDeviceCallsTable); err != nil {
		return false, false, err
	}
	err = r.db.QueryRow(`SELECT enabled FROM elphant_device_calls WHERE device_id = ?`, deviceID).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return enabled, true, nil
}
```

- [ ] **Step 4: Run the storage test**

Run: `cd src && gofmt -l infrastructure/chatstorage; GOTOOLCHAIN=auto go test ./infrastructure/chatstorage/ -run TestDeviceCallsEnabledRoundTrip -v`
Expected: no gofmt output; PASS.

- [ ] **Step 5: Write the failing switch tests**

In `src/infrastructure/whatsapp/calls_registry_test.go`, replace `withCallsConfig` with a version
that also isolates the store and the cache. Then every existing registry test runs on the env
seed only, as in 2a.

```go
func withCallsConfig(t *testing.T, enabled bool, devices []string) {
	oe, od, of := config.WhatsappCallsEnabled, config.WhatsappCallsDevices, callsStoreFn
	resetDeviceCallsCache()
	t.Cleanup(func() {
		config.WhatsappCallsEnabled, config.WhatsappCallsDevices, callsStoreFn = oe, od, of
		resetDeviceCallsCache()
	})
	config.WhatsappCallsEnabled, config.WhatsappCallsDevices = enabled, devices
	callsStoreFn = func() deviceCallsStore { return nil }
}
```

`src/infrastructure/whatsapp/calls_switch_test.go`:

```go
package whatsapp

import (
	"sync"
	"testing"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

type fakeCallsStore struct {
	mu   sync.Mutex
	rows map[string]bool
}

func newFakeCallsStore() *fakeCallsStore { return &fakeCallsStore{rows: map[string]bool{}} }

func (s *fakeCallsStore) SetDeviceCallsEnabled(id string, on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[id] = on
	return nil
}

func (s *fakeCallsStore) GetDeviceCallsEnabled(id string) (bool, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	on, ok := s.rows[id]
	return on, ok, nil
}

func (s *fakeCallsStore) snapshot() map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]bool{}
	for k, v := range s.rows {
		out[k] = v
	}
	return out
}

// withCallsStore must run after withCallsConfig (cleanups run in reverse order).
func withCallsStore(t *testing.T, reg deviceCallsStore) {
	orig := callsStoreFn
	callsStoreFn = func() deviceCallsStore { return reg }
	resetDeviceCallsCache()
	t.Cleanup(func() { callsStoreFn = orig; resetDeviceCallsCache() })
}

func withResolvedSlot(t *testing.T, slotID string, client *whatsmeow.Client) {
	orig := resolveCallsSlot
	resolveCallsSlot = func(string) (string, *whatsmeow.Client, error) { return slotID, client, nil }
	t.Cleanup(func() { resolveCallsSlot = orig })
}

func TestDeviceCallsSeededOnceFromEnv(t *testing.T) {
	withCallsConfig(t, true, []string{"slot-a"})
	reg := newFakeCallsStore()
	withCallsStore(t, reg)

	assert.True(t, deviceCallsEnabled("slot-a", "", ""))
	assert.False(t, deviceCallsEnabled("slot-b", "5511900000001", "5511900000001:1@s.whatsapp.net"))
	assert.Equal(t, map[string]bool{"slot-a": true, "slot-b": false}, reg.snapshot())

	// A restart with another list: the stored values win.
	config.WhatsappCallsDevices = []string{"slot-b"}
	resetDeviceCallsCache()
	assert.True(t, deviceCallsEnabled("slot-a", "", ""))
	assert.False(t, deviceCallsEnabled("slot-b", "5511900000001", "5511900000001:1@s.whatsapp.net"))
}

func TestDeviceCallsNotSeededBeforePairing(t *testing.T) {
	withCallsConfig(t, true, []string{"5511987654321"})
	reg := newFakeCallsStore()
	withCallsStore(t, reg)

	assert.False(t, deviceCallsEnabled("slot-a", "", ""))
	assert.Empty(t, reg.snapshot(), "an unpaired device must not persist a seed")
	assert.True(t, deviceCallsEnabled("slot-a", "5511987654321", "5511987654321:3@s.whatsapp.net"))
	assert.Equal(t, map[string]bool{"slot-a": true}, reg.snapshot())
}

func TestDeviceCallsMasterSwitch(t *testing.T) {
	withCallsConfig(t, false, nil)
	reg := newFakeCallsStore()
	reg.rows["slot-a"] = true
	withCallsStore(t, reg)

	assert.False(t, deviceCallsEnabled("slot-a", "", ""))
	_, err := SetDeviceCalls("slot-a", true)
	assert.ErrorIs(t, err, ErrCallsMasterOff)
}

func TestSetDeviceCallsAppliesAtOnce(t *testing.T) {
	withCallsConfig(t, true, nil)
	reg := newFakeCallsStore()
	withCallsStore(t, reg)
	client := &whatsmeow.Client{Store: &store.Device{}}
	withResolvedSlot(t, "slot-a", client)

	filter := offerFilter("slot-a", client)
	assert.True(t, filter(&events.CallOffer{}))

	on, err := SetDeviceCalls("slot-a", false)
	require.NoError(t, err)
	assert.False(t, on)
	assert.False(t, filter(&events.CallOffer{}), "a disabled device's offers are left to the phone")
	got, err := DeviceCalls("slot-a")
	require.NoError(t, err)
	assert.False(t, got)
	assert.Equal(t, map[string]bool{"slot-a": false}, reg.snapshot())

	_, err = SetDeviceCalls("slot-a", true)
	require.NoError(t, err)
	assert.True(t, filter(&events.CallOffer{}))
}

func TestSwitchOffEndsLiveCalls(t *testing.T) {
	withCallsConfig(t, true, nil)
	withCallsStore(t, newFakeCallsStore())
	client := &whatsmeow.Client{}
	withResolvedSlot(t, "slot-a", client)

	eng := &fakeEngine{}
	m := newCallManager(eng, callManagerOptions{MaxCalls: 1})
	m.slotID = "slot-a"
	callsByClient.Store(client, m)
	t.Cleanup(func() { callsByClient.Delete(client) })
	in := &fakeCall{id: "IN-1"}
	eng.incoming(in)
	require.NoError(t, m.Answer("IN-1"))

	_, err := SetDeviceCalls("slot-a", false)
	require.NoError(t, err)
	assert.Eventually(t, func() bool {
		in.mu.Lock()
		defer in.mu.Unlock()
		return in.hungUp
	}, time.Second, 10*time.Millisecond)
}

func TestOfferFilterLeavesGroupCalls(t *testing.T) {
	withCallsConfig(t, true, nil)
	filter := offerFilter("slot-a", &whatsmeow.Client{})
	assert.True(t, filter(&events.CallOffer{}))
	group := &events.CallOffer{}
	group.GroupJID = types.NewJID("120363000000000000", types.GroupServer)
	assert.False(t, filter(group))
}
```

- [ ] **Step 6: Run them to see them fail**

Run: `cd src && GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/ -run 'DeviceCalls|SetDeviceCalls|SwitchOff|OfferFilter'`
Expected: FAIL to build (`undefined: deviceCallsStore`).

- [ ] **Step 7: Implement the switch**

`src/infrastructure/whatsapp/calls_switch.go`:

```go
package whatsapp

import (
	"sync"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/safego"
	"github.com/sirupsen/logrus"
	"go.mau.fi/whatsmeow"
)

// Fork (elphant): the per-device calls switch at runtime (stage 2b, B7). WHATSAPP_CALLS_ENABLED
// is the master switch. Each device slot's own value lives in the device registry
// (elphant_device_calls); WHATSAPP_CALLS_DEVICES only seeds a slot that has no value yet.

var ErrCallsMasterOff = pkgError.ValidationError("calls are disabled on this gateway (WHATSAPP_CALLS_ENABLED=false)")

type deviceCallsStore interface {
	SetDeviceCallsEnabled(deviceID string, enabled bool) error
	GetDeviceCallsEnabled(deviceID string) (enabled, found bool, err error)
}

// callsStoreFn returns the registry that keeps the switch, or nil (tests, no chat storage).
var callsStoreFn = func() deviceCallsStore {
	dm := GetDeviceManager()
	if dm == nil || dm.storage == nil {
		return nil
	}
	reg, _ := dm.storage.(deviceCallsStore)
	return reg
}

// resolveCallsSlot maps the path's device id (slot id or JID) to the slot id and its client.
var resolveCallsSlot = func(deviceID string) (string, *whatsmeow.Client, error) {
	inst, slotID, err := GetDeviceManager().ResolveDevice(deviceID)
	if err != nil {
		return "", nil, pkgError.ValidationError(err.Error())
	}
	return slotID, inst.GetClient(), nil
}

var deviceCallsCache sync.Map // slot id -> bool

func resetDeviceCallsCache() {
	deviceCallsCache.Range(func(k, _ any) bool {
		deviceCallsCache.Delete(k)
		return true
	})
}

// deviceCallsEnabled reports whether calls are on for a slot now: the master switch, then the
// stored value. A slot without one is seeded once from WHATSAPP_CALLS_DEVICES; before pairing a
// phone-number entry cannot match yet, so that seed is not stored.
func deviceCallsEnabled(slotID, jidUser, jidString string) bool {
	if !config.WhatsappCallsEnabled {
		return false
	}
	if v, ok := deviceCallsCache.Load(slotID); ok {
		return v.(bool)
	}
	seed := config.CallsAllowed(slotID, jidUser, jidString)
	reg := callsStoreFn()
	if reg == nil {
		return seed
	}
	enabled, found, err := reg.GetDeviceCallsEnabled(slotID)
	if err != nil {
		logrus.Warnf("[CALLS] read the calls switch of %s: %v", maskSlot(slotID), err)
		return seed
	}
	if !found {
		if !seed && jidUser == "" && len(config.WhatsappCallsDevices) > 0 {
			return false
		}
		enabled = seed
		if err := reg.SetDeviceCallsEnabled(slotID, enabled); err != nil {
			logrus.Warnf("[CALLS] seed the calls switch of %s: %v", maskSlot(slotID), err)
			return enabled
		}
	}
	deviceCallsCache.Store(slotID, enabled)
	return enabled
}

// SetDeviceCalls switches calls on or off for a device. Off ends the device's live calls; its
// next offers are left to the phone (the offer filter reads the switch on every offer).
func SetDeviceCalls(deviceID string, enabled bool) (bool, error) {
	if !config.WhatsappCallsEnabled {
		return false, ErrCallsMasterOff
	}
	slotID, _, err := resolveCallsSlot(deviceID)
	if err != nil {
		return false, err
	}
	reg := callsStoreFn()
	if reg == nil {
		return false, pkgError.InternalServerError("device registry unavailable")
	}
	if err := reg.SetDeviceCallsEnabled(slotID, enabled); err != nil {
		return false, err
	}
	deviceCallsCache.Store(slotID, enabled)
	if !enabled {
		endCallsOfSlot(slotID)
	}
	logrus.Infof("[CALLS] calls switched %s for device %s", onOff(enabled), maskSlot(slotID))
	return enabled, nil
}

// DeviceCalls reports whether calls are on for a device right now.
func DeviceCalls(deviceID string) (bool, error) {
	slotID, client, err := resolveCallsSlot(deviceID)
	if err != nil {
		return false, err
	}
	return callsAllowedNow(slotID, client), nil
}

func endCallsOfSlot(slotID string) {
	callsByClient.Range(func(_, v any) bool {
		if m := v.(*CallManager); m.slotID == slotID {
			safego.Go("calls_switch_off", m.closeAll)
		}
		return true
	})
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}
```

In `src/infrastructure/whatsapp/calls_registry.go`, change the last line of `callsAllowedNow` to
read the switch:

```go
	return deviceCallsEnabled(slotID, user, jid)
```

and replace `offerFilter` with:

```go
// offerFilter gates inbound offers in the call engine: a false return makes it ignore the
// offer completely, so the phone rings as usual. Group calls are out of scope and always left
// to the phone.
func offerFilter(slotID string, client *whatsmeow.Client) func(*events.CallOffer) bool {
	return func(evt *events.CallOffer) bool {
		if evt != nil && !evt.GroupJID.IsEmpty() {
			return false
		}
		return callsAllowedNow(slotID, client)
	}
}
```

- [ ] **Step 8: Run the switch tests and the registry tests**

Run: `cd src && gofmt -l infrastructure/whatsapp; GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/ -run 'DeviceCalls|SetDeviceCalls|SwitchOff|OfferFilter|CallsFromContext|AllowedAfterPairing|AttachCalls|HasCallManager|CallsAllowedNow' -race -v`
Expected: no gofmt output; all PASS, no race report.

- [ ] **Step 9: Write the failing REST test**

In `src/ui/rest/call_test.go`, add two fields to `callServiceStub`:

```go
	switchDevice string
	switchValue  bool
```

`src/ui/rest/call_device_test.go`:

```go
package rest

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	domainCall "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/call"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/ui/rest/middleware"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *callServiceStub) SetDeviceCalls(_ context.Context, deviceID string, enabled bool) (domainCall.DeviceCallsResponse, error) {
	if s.err != nil {
		return domainCall.DeviceCallsResponse{}, s.err
	}
	s.switchDevice, s.switchValue = deviceID, enabled
	return domainCall.DeviceCallsResponse{Enabled: enabled}, nil
}

func (s *callServiceStub) GetDeviceCalls(_ context.Context, deviceID string) (domainCall.DeviceCallsResponse, error) {
	s.switchDevice = deviceID
	return domainCall.DeviceCallsResponse{Enabled: s.switchValue}, s.err
}

func TestDeviceCallsRoutes(t *testing.T) {
	service := &callServiceStub{}
	app := fiber.New()
	app.Use(middleware.Recovery())
	InitRestDeviceCalls(app, service)

	put := func(body string) int {
		req := httptest.NewRequest("PUT", "/devices/slot-a/calls", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		require.NoError(t, err)
		return resp.StatusCode
	}

	assert.Equal(t, 200, put(`{"enabled":true}`))
	assert.Equal(t, "slot-a", service.switchDevice)
	assert.True(t, service.switchValue)
	assert.Equal(t, 400, put(`{}`), "enabled is required")

	resp, err := app.Test(httptest.NewRequest("GET", "/devices/slot-a/calls", nil))
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	var out struct {
		Results domainCall.DeviceCallsResponse `json:"results"`
	}
	require.NoError(t, json.Unmarshal(body, &out))
	assert.True(t, out.Results.Enabled)

	service.err = whatsapp.ErrCallsMasterOff
	assert.Equal(t, 400, put(`{"enabled":true}`))
}
```

- [ ] **Step 10: Run it to see it fail**

Run: `cd src && GOTOOLCHAIN=auto go test ./ui/rest/ -run TestDeviceCallsRoutes`
Expected: FAIL to build (`undefined: domainCall.DeviceCallsResponse`).

- [ ] **Step 11: Implement domain, usecase and routes**

In `src/domains/call/call.go`, add to `ICallUsecase` (after `ActiveCalls`):

```go
	SetDeviceCalls(ctx context.Context, deviceID string, enabled bool) (DeviceCallsResponse, error)
	GetDeviceCalls(ctx context.Context, deviceID string) (DeviceCallsResponse, error)
```

and the types:

```go
type DeviceCallsRequest struct {
	Enabled *bool `json:"enabled" form:"enabled"`
}

type DeviceCallsResponse struct {
	Enabled bool `json:"enabled"`
}
```

In `src/usecase/call.go` (fork section), add:

```go
func (service serviceCall) SetDeviceCalls(_ context.Context, deviceID string, enabled bool) (domainCall.DeviceCallsResponse, error) {
	on, err := whatsapp.SetDeviceCalls(deviceID, enabled)
	if err != nil {
		return domainCall.DeviceCallsResponse{}, err
	}
	return domainCall.DeviceCallsResponse{Enabled: on}, nil
}

func (service serviceCall) GetDeviceCalls(_ context.Context, deviceID string) (domainCall.DeviceCallsResponse, error) {
	on, err := whatsapp.DeviceCalls(deviceID)
	if err != nil {
		return domainCall.DeviceCallsResponse{}, err
	}
	return domainCall.DeviceCallsResponse{Enabled: on}, nil
}
```

`src/ui/rest/call_device.go`:

```go
package rest

import (
	domainCall "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/call"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"github.com/gofiber/fiber/v3"
)

// Fork (elphant): the per-device voice calls switch (stage 2b, B7). The routes sit with the
// other /devices/:device_id routes, before the header-scoped device group.

func InitRestDeviceCalls(app fiber.Router, service domainCall.ICallUsecase) {
	handler := Call{Service: service}
	app.Put("/devices/:device_id/calls", handler.SetDeviceCalls)
	app.Get("/devices/:device_id/calls", handler.GetDeviceCalls)
}

func (controller *Call) SetDeviceCalls(c fiber.Ctx) error {
	var request domainCall.DeviceCallsRequest
	utils.PanicIfNeeded(c.Bind().Body(&request))
	if request.Enabled == nil {
		utils.PanicIfNeeded(pkgError.ValidationError("enabled is required"))
	}
	res, err := controller.Service.SetDeviceCalls(c.Context(), pathDeviceID(c), *request.Enabled)
	utils.PanicIfNeeded(err)
	return c.JSON(utils.ResponseData{Status: 200, Code: "SUCCESS", Message: "Calls switch updated", Results: res})
}

func (controller *Call) GetDeviceCalls(c fiber.Ctx) error {
	res, err := controller.Service.GetDeviceCalls(c.Context(), pathDeviceID(c))
	utils.PanicIfNeeded(err)
	return c.JSON(utils.ResponseData{Status: 200, Code: "SUCCESS", Message: "Calls switch", Results: res})
}
```

In `src/cmd/rest.go`, right after `rest.InitRestDevice(apiGroup, deviceUsecase)`:

```go
	rest.InitRestDeviceCalls(apiGroup, callUsecase) // Fork (elphant): per-device voice calls switch (stage 2b).
```

In `src/config/calls.go`, change the `WhatsappCallsDevices` comment to:

```go
	WhatsappCallsDevices      []string // seeds the per-device switch of a device that has none yet (stage 2b); later changes go through PUT /devices/:device_id/calls
```

In `src/.env.example`, replace the comment above `WHATSAPP_CALLS_DEVICES=` with:

```
# Devices seeded with calls on at their first start (phone numbers, JIDs or device ids; empty = all).
# After that, switch calls per device with PUT /devices/:device_id/calls {"enabled": true|false}.
```

- [ ] **Step 12: Run the tests**

Run: `cd src && gofmt -l domains usecase ui cmd infrastructure; GOTOOLCHAIN=auto go test ./ui/rest/ ./usecase/ ./cmd/ ./infrastructure/whatsapp/ ./infrastructure/chatstorage/`
Expected: no gofmt output; `ok` everywhere (including `TestDeviceGroupIsRegisteredLast`).

- [ ] **Step 13: Commit**

```bash
git add src/infrastructure/chatstorage/sqlite_repository_calls.go src/infrastructure/chatstorage/sqlite_repository_calls_test.go \
  src/infrastructure/whatsapp/calls_switch.go src/infrastructure/whatsapp/calls_switch_test.go \
  src/infrastructure/whatsapp/calls_registry.go src/infrastructure/whatsapp/calls_registry_test.go \
  src/domains/call/call.go src/usecase/call.go src/ui/rest/call_device.go src/ui/rest/call_device_test.go \
  src/ui/rest/call_test.go src/cmd/rest.go src/config/calls.go src/.env.example
git commit -m "feat(calls): per-device calls switch at runtime"
```

---
### Task 3: Call model for 2b: full peer, Meta's directions, end mapping, events, max duration, media seam

This task rewrites `CallManager` around the 2b contract and defines the `callMedia` seam the
bridge implements in Task 7. Until Task 7, production managers have no media factory
(`Media == nil`), so answered calls carry no audio. 2a's test-audio player and `.wav` recorder are
removed.

**Files:**
- Create: `src/pkg/error/conflict_error.go`
- Rewrite: `src/infrastructure/whatsapp/calls.go`, `src/infrastructure/whatsapp/calls_test.go`
- Rewrite: `src/infrastructure/whatsapp/calls_meowcaller.go`
- Modify: `src/infrastructure/whatsapp/calls_meowcaller_test.go` (add tests)
- Modify: `src/infrastructure/whatsapp/calls_registry.go` (`attachCalls` options)
- Modify: `src/infrastructure/whatsapp/calls_switch_test.go` (`m.Answer("IN-1")` becomes `m.Answer("IN-1", "")`)
- Modify: `src/config/calls.go`, `src/config/calls_test.go`
- Create: `src/cmd/calls_flags.go`, `src/cmd/calls_flags_test.go`
- Modify: `src/cmd/root.go` (drop the 2a test-audio env and flag lines; add two single lines)
- Modify: `src/.env.example`
- Modify: `src/domains/call/call.go`, `src/usecase/call.go`, `src/ui/rest/call_test.go`

**Interfaces:**
- Consumes: `deviceCallsEnabled` (Task 2), `MaskDigits(string) string`, `maskSlot(string) string`.
- Produces (package `whatsapp`, used by Tasks 7-10):

```go
const DirectionUser = "USER_INITIATED"; const DirectionBusiness = "BUSINESS_INITIATED"
const EventCallStatus = "call.status"; const EventCallAnswered = "call.answered"; const EventCallEnded = "call.ended"
var ErrCallNotFound, ErrCallLimit, ErrCallsOff, ErrCallsNoMedia, ErrCallNoOffer error // 400
var ErrCallAnswered error                                                          // 409 (pkgError.ConflictError)
type CallPeer struct{ PN, LID string }            // json "pn", "lid"
type CallInfo struct{ CallID, Direction string; Peer CallPeer; State string; StartedAt time.Time }
type RecordingInfo struct{ Path string; Bytes int64; Duration time.Duration }
type CallEvent struct{ Type string; Call CallInfo; Status, AnsweredBy, EndReason string; Duration int; Recording *RecordingInfo; At time.Time }
type pcmSource interface{ ReadFrame() ([]float32, error); Close() error }
type pcmSink interface{ WriteFrame(frame []float32) error; Close() error }
type callHandle interface{ ID() string; Peer() CallPeer; Answer() error; Reject() error; Hangup() error;
	OnEnd(func(reason string)); OnRinging(func()); OnAccept(func()); OnMedia(func()); Bridge(src pcmSource, sink pcmSink) }
type mediaEvents struct{ Flowing func(); Lost func() }
type callMedia interface{ LocalSDP() string; Accept(answerSDP string) error; Start(h callHandle, ev mediaEvents); WhatsAppReady(); Close() *RecordingInfo }
type mediaFactory interface{ Inbound() (callMedia, error); Outbound(offerSDP string) (callMedia, error) }
type callManagerOptions struct{ MaxCalls int; RingWindow, MaxDuration, OfferWait time.Duration; Now func() time.Time; Emit func(CallEvent); Media mediaFactory }
func (m *CallManager) OfferSDP(ctx context.Context, callID string) (string, error)
func (m *CallManager) Answer(callID, answerSDP string) error
func (m *CallManager) Start(ctx context.Context, target, offerSDP string) (CallInfo, string, error) // string = SDP answer
func (m *CallManager) Reject(callID string) error
func (m *CallManager) Hangup(callID string) error
func (m *CallManager) Get(callID string) (CallInfo, bool)
func (m *CallManager) Active() []CallInfo
func resolvePeer(wa *whatsmeow.Client, peer types.JID, pnHint string) CallPeer
func maskPeer(p CallPeer) string
```

- Produces: `config.WhatsappCallsMaxDurationMinutes = 120`, `cmd.initCallsEnv()`,
  `cmd.initCallsFlags()`, `pkgError.ConflictError`, and
  `domainCall.CallPeer{PN, LID string}`. `domainCall.CallInfo.Peer` becomes a `CallPeer`.

- [ ] **Step 1: Add the 409 error type**

`src/pkg/error/conflict_error.go`:

```go
package error

import "net/http"

// Fork (elphant): ConflictError is a 409, for example a voice call answered twice (stage 2b).
type ConflictError string

func (e ConflictError) Error() string { return string(e) }

// ErrCode will return the error code based on the error data type
func (e ConflictError) ErrCode() string { return "CONFLICT" }

// StatusCode will return the HTTP status code based on the error data type
func (e ConflictError) StatusCode() int { return http.StatusConflict }
```

- [ ] **Step 2: Write the failing manager tests**

Replace `src/infrastructure/whatsapp/calls_test.go` with:

```go
package whatsapp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeCall mirrors meowcaller: Reject and Hangup fire OnEnd synchronously, unless silent (an
// engine that already forgot the call).
type fakeCall struct {
	mu                           sync.Mutex
	id                           string
	peer                         CallPeer
	answered, rejected, hungUp   bool
	answerErr                    error
	silent                       bool
	syncEnd                      bool
	onEnd                        func(string)
	onRinging, onAccept, onMedia func()
	src                          pcmSource
	sink                         pcmSink
}

func (f *fakeCall) ID() string { return f.id }
func (f *fakeCall) Peer() CallPeer {
	if f.peer == (CallPeer{}) {
		return CallPeer{PN: "5511900000000@s.whatsapp.net", LID: "123456789012345@lid"}
	}
	return f.peer
}
func (f *fakeCall) Answer() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.answerErr != nil {
		return f.answerErr
	}
	f.answered = true
	return nil
}
func (f *fakeCall) Reject() error {
	f.mu.Lock()
	f.rejected = true
	f.mu.Unlock()
	f.fireEnd("rejected")
	return nil
}
func (f *fakeCall) Hangup() error {
	f.mu.Lock()
	f.hungUp = true
	f.mu.Unlock()
	f.fireEnd("hangup")
	return nil
}
func (f *fakeCall) fireEnd(reason string) {
	f.mu.Lock()
	fn, silent := f.onEnd, f.silent
	f.mu.Unlock()
	if fn != nil && !silent {
		fn(reason)
	}
}
func (f *fakeCall) OnEnd(fn func(string)) {
	f.mu.Lock()
	f.onEnd = fn
	se := f.syncEnd
	f.mu.Unlock()
	if se {
		fn("already-ended")
	}
}
func (f *fakeCall) OnRinging(fn func()) { f.mu.Lock(); f.onRinging = fn; f.mu.Unlock() }
func (f *fakeCall) OnAccept(fn func())  { f.mu.Lock(); f.onAccept = fn; f.mu.Unlock() }
func (f *fakeCall) OnMedia(fn func())   { f.mu.Lock(); f.onMedia = fn; f.mu.Unlock() }
func (f *fakeCall) Bridge(src pcmSource, sink pcmSink) {
	f.mu.Lock()
	f.src, f.sink = src, sink
	f.mu.Unlock()
}

// end, ring, accept and media play the engine's side.
func (f *fakeCall) end(reason string) {
	f.mu.Lock()
	fn := f.onEnd
	f.mu.Unlock()
	if fn != nil {
		fn(reason)
	}
}
func (f *fakeCall) fire(pick func(*fakeCall) func()) {
	f.mu.Lock()
	fn := pick(f)
	f.mu.Unlock()
	if fn != nil {
		fn()
	}
}
func (f *fakeCall) ring()   { f.fire(func(c *fakeCall) func() { return c.onRinging }) }
func (f *fakeCall) accept() { f.fire(func(c *fakeCall) func() { return c.onAccept }) }
func (f *fakeCall) media()  { f.fire(func(c *fakeCall) func() { return c.onMedia }) }

type fakeEngine struct {
	mu       sync.Mutex
	incoming func(callHandle)
	placed   []*fakeCall
	err      error
	syncEnd  bool
}

func (e *fakeEngine) OnIncomingCall(fn func(callHandle)) { e.incoming = fn }
func (e *fakeEngine) Call(_ context.Context, target string) (callHandle, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil {
		err := e.err
		e.err = nil
		return nil, err
	}
	c := &fakeCall{id: "OUT-" + target, syncEnd: e.syncEnd}
	e.placed = append(e.placed, c)
	return c, nil
}
func (e *fakeEngine) placedCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.placed)
}

type fakeMedia struct {
	mu        sync.Mutex
	sdp       string
	acceptErr error
	accepted  string
	started   bool
	ev        mediaEvents
	waReady   int
	closed    int
	rec       *RecordingInfo
}

func (f *fakeMedia) LocalSDP() string { return f.sdp }
func (f *fakeMedia) Accept(s string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.acceptErr != nil {
		return f.acceptErr
	}
	f.accepted = s
	return nil
}
func (f *fakeMedia) Start(_ callHandle, ev mediaEvents) {
	f.mu.Lock()
	f.started, f.ev = true, ev
	f.mu.Unlock()
}
func (f *fakeMedia) WhatsAppReady() { f.mu.Lock(); f.waReady++; f.mu.Unlock() }
func (f *fakeMedia) Close() *RecordingInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed++
	return f.rec
}

type fakeMediaFactory struct {
	mu     sync.Mutex
	made   []*fakeMedia
	offers []string
	err    error
}

func (f *fakeMediaFactory) Inbound() (callMedia, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	m := &fakeMedia{sdp: "v=0 gowa-offer", rec: &RecordingInfo{Path: "x.ogg", Bytes: 10}}
	f.made = append(f.made, m)
	return m, nil
}
func (f *fakeMediaFactory) Outbound(offer string) (callMedia, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	f.offers = append(f.offers, offer)
	m := &fakeMedia{sdp: "v=0 gowa-answer"}
	f.made = append(f.made, m)
	return m, nil
}
func (f *fakeMediaFactory) last() *fakeMedia {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.made[len(f.made)-1]
}

type eventLog struct {
	mu     sync.Mutex
	events []CallEvent
}

func (l *eventLog) add(ev CallEvent) { l.mu.Lock(); l.events = append(l.events, ev); l.mu.Unlock() }
func (l *eventLog) all() []CallEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]CallEvent(nil), l.events...)
}
func (l *eventLog) last(typ string) (CallEvent, bool) {
	evs := l.all()
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Type == typ {
			return evs[i], true
		}
	}
	return CallEvent{}, false
}
func (l *eventLog) summary() []string {
	var out []string
	for _, ev := range l.all() {
		s := ev.Type + ":" + ev.Status + ev.AnsweredBy
		if ev.EndReason != "" {
			s += "/" + ev.EndReason
		}
		out = append(out, s)
	}
	return out
}

func newTestManager(t *testing.T, maxCalls int, now *time.Time, media mediaFactory) (*CallManager, *fakeEngine, *eventLog) {
	t.Helper()
	eng := &fakeEngine{}
	log := &eventLog{}
	m := newCallManager(eng, callManagerOptions{
		MaxCalls: maxCalls, RingWindow: time.Minute, OfferWait: 200 * time.Millisecond,
		Now: func() time.Time { return *now }, Emit: log.add, Media: media,
	})
	return m, eng, log
}

func TestAnswerUnknownCall(t *testing.T) {
	now := time.Now()
	m, _, _ := newTestManager(t, 1, &now, nil)
	assert.ErrorIs(t, m.Answer("NOPE", ""), ErrCallNotFound)
}

func TestAnswerPendingCall(t *testing.T) {
	now := time.Now()
	m, eng, _ := newTestManager(t, 1, &now, nil)
	in := &fakeCall{id: "IN-1"}
	eng.incoming(in)

	active := m.Active()
	require.Len(t, active, 1)
	assert.Equal(t, "ringing", active[0].State)
	assert.Equal(t, DirectionUser, active[0].Direction)
	assert.Equal(t, CallPeer{PN: "5511900000000@s.whatsapp.net", LID: "123456789012345@lid"}, active[0].Peer)

	require.NoError(t, m.Answer("IN-1", ""))
	assert.True(t, in.answered)
	assert.Equal(t, "active", m.Active()[0].State)
}

func TestAnswerTwiceConflicts(t *testing.T) {
	now := time.Now()
	m, eng, _ := newTestManager(t, 1, &now, nil)
	eng.incoming(&fakeCall{id: "IN-1"})
	require.NoError(t, m.Answer("IN-1", ""))
	err := m.Answer("IN-1", "")
	assert.ErrorIs(t, err, ErrCallAnswered)
	var ge pkgError.GenericError
	require.ErrorAs(t, err, &ge)
	assert.Equal(t, 409, ge.StatusCode())
}

func TestAnswerExpiredOffer(t *testing.T) {
	now := time.Now()
	m, eng, log := newTestManager(t, 1, &now, nil)
	in := &fakeCall{id: "IN-1"}
	eng.incoming(in)
	now = now.Add(2 * time.Minute)
	assert.ErrorIs(t, m.Answer("IN-1", ""), ErrCallNotFound)
	assert.Empty(t, m.Active())
	assert.True(t, in.rejected, "an offer past the ring window is rejected")
	assert.Equal(t, []string{"call.ended:MISSED/timeout"}, log.summary())
}

func TestIncomingAboveCapIsRejectedAndMissed(t *testing.T) {
	now := time.Now()
	m, eng, log := newTestManager(t, 1, &now, nil)
	eng.incoming(&fakeCall{id: "IN-1"})
	require.NoError(t, m.Answer("IN-1", ""))

	second := &fakeCall{id: "IN-2"}
	eng.incoming(second)
	assert.True(t, second.rejected)
	assert.ErrorIs(t, m.Answer("IN-2", ""), ErrCallNotFound)
	assert.Len(t, m.Active(), 1)
	ended, ok := log.last(EventCallEnded)
	require.True(t, ok)
	assert.Equal(t, "IN-2", ended.Call.CallID)
	assert.Equal(t, "MISSED", ended.Status)
	assert.Equal(t, "rejected", ended.EndReason)
}

func TestStartRespectsCap(t *testing.T) {
	now := time.Now()
	m, _, _ := newTestManager(t, 1, &now, nil)
	info, sdp, err := m.Start(context.Background(), "+5511900000000", "")
	require.NoError(t, err)
	assert.Equal(t, DirectionBusiness, info.Direction)
	assert.Equal(t, "ringing", info.State)
	assert.Empty(t, sdp)

	_, _, err = m.Start(context.Background(), "+5511900000001", "")
	assert.ErrorIs(t, err, ErrCallLimit)
}

func TestHangupAndEnd(t *testing.T) {
	now := time.Now()
	m, eng, log := newTestManager(t, 1, &now, nil)
	info, _, err := m.Start(context.Background(), "+5511900000000", "")
	require.NoError(t, err)
	require.NoError(t, m.Hangup(info.CallID))
	assert.True(t, eng.placed[0].hungUp)
	assert.Empty(t, m.Active())
	assert.ErrorIs(t, m.Hangup(info.CallID), ErrCallNotFound)
	assert.Equal(t, []string{"call.ended:MISSED/hangup_agent"}, log.summary())
}

func TestEndedPendingCallIsGone(t *testing.T) {
	now := time.Now()
	m, eng, log := newTestManager(t, 1, &now, nil)
	in := &fakeCall{id: "IN-1"}
	eng.incoming(in)
	in.end("")
	assert.Empty(t, m.Active())
	assert.ErrorIs(t, m.Answer("IN-1", ""), ErrCallNotFound)
	assert.Equal(t, []string{"call.ended:MISSED/hangup_peer"}, log.summary())
}

func TestHangupRingingRejects(t *testing.T) {
	now := time.Now()
	m, eng, log := newTestManager(t, 1, &now, nil)
	in := &fakeCall{id: "IN-1"}
	eng.incoming(in)
	require.NoError(t, m.Hangup("IN-1"))
	assert.True(t, in.rejected)
	assert.False(t, in.hungUp)
	assert.Equal(t, []string{"call.ended:REJECTED/rejected"}, log.summary())
}

func TestRejectRingingOnly(t *testing.T) {
	now := time.Now()
	m, eng, log := newTestManager(t, 1, &now, nil)
	assert.ErrorIs(t, m.Reject("NOPE"), ErrCallNotFound)
	in := &fakeCall{id: "IN-1"}
	eng.incoming(in)
	require.NoError(t, m.Reject("IN-1"))
	assert.True(t, in.rejected)
	assert.Equal(t, []string{"call.ended:REJECTED/rejected"}, log.summary())
}

func TestHangupRaceWithAnswer(t *testing.T) {
	now := time.Now()
	m, eng, _ := newTestManager(t, 1, &now, nil)
	in := &fakeCall{id: "IN-1"}
	eng.incoming(in)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = m.Answer("IN-1", "") }()
	go func() { defer wg.Done(); _ = m.Hangup("IN-1") }()
	wg.Wait()
	in.mu.Lock()
	defer in.mu.Unlock()
	assert.True(t, in.hungUp || in.rejected)
	assert.Empty(t, m.Active())
}

func TestConcurrentStartRespectsCap(t *testing.T) {
	now := time.Now()
	m, _, _ := newTestManager(t, 1, &now, nil)
	const n = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, limited := 0, 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := m.Start(context.Background(), "+55119000000"+string(rune('a'+i)), "")
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				ok++
			} else if errors.Is(err, ErrCallLimit) {
				limited++
			}
		}(i)
	}
	wg.Wait()
	assert.Equal(t, 1, ok)
	assert.Equal(t, n-1, limited)
}

func TestStartEngineErrorFreesSlot(t *testing.T) {
	now := time.Now()
	m, eng, _ := newTestManager(t, 1, &now, nil)
	eng.err = errors.New("dial failed")
	_, _, err := m.Start(context.Background(), "+5511900000000", "")
	require.Error(t, err)
	_, _, err = m.Start(context.Background(), "+5511900000000", "")
	require.NoError(t, err)
}

func TestEndBeforeTrackLeavesNoEntry(t *testing.T) {
	now := time.Now()
	m, eng, log := newTestManager(t, 1, &now, nil)
	eng.syncEnd = true
	done := make(chan error, 1)
	go func() {
		_, _, err := m.Start(context.Background(), "+5511900000000", "")
		done <- err
	}()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, ErrCallNotFound)
	case <-time.After(2 * time.Second):
		t.Fatal("Start deadlocked")
	}
	assert.Empty(t, m.Active())
	assert.Len(t, log.all(), 1, "the call that ended during setup still gets its call.ended")
	eng.syncEnd = false
	_, _, err := m.Start(context.Background(), "+5511900000001", "")
	require.NoError(t, err)
}

func TestPeerHangupAfterMediaCompletes(t *testing.T) {
	now := time.Now()
	m, eng, log := newTestManager(t, 1, &now, nil)
	in := &fakeCall{id: "IN-1"}
	eng.incoming(in)
	require.NoError(t, m.Answer("IN-1", ""))
	in.media()
	now = now.Add(64 * time.Second)
	in.end("") // the peer hung up: meowcaller gives an empty reason
	assert.Equal(t, []string{"call.answered:api", "call.ended:COMPLETED/hangup_peer"}, log.summary())
	ended, _ := log.last(EventCallEnded)
	assert.Equal(t, 64, ended.Duration)
}

func TestMediaBeforeAnswerWaitsForAnswer(t *testing.T) {
	now := time.Now()
	m, eng, log := newTestManager(t, 1, &now, nil)
	in := &fakeCall{id: "IN-1"}
	eng.incoming(in)
	in.media() // early media "listen": the relay may report media while it rings
	assert.Empty(t, log.all())
	require.NoError(t, m.Answer("IN-1", ""))
	assert.Equal(t, []string{"call.answered:api"}, log.summary())
}

func TestOutboundStatusEvents(t *testing.T) {
	now := time.Now()
	m, eng, log := newTestManager(t, 1, &now, nil)
	_, _, err := m.Start(context.Background(), "+5511900000000", "")
	require.NoError(t, err)
	a := eng.placed[0]
	a.ring()
	a.accept()
	assert.Equal(t, "active", m.Active()[0].State)
	a.media()
	now = now.Add(10 * time.Second)
	a.end("")

	_, _, err = m.Start(context.Background(), "+5511900000001", "")
	require.NoError(t, err)
	b := eng.placed[1]
	b.ring()
	b.end("rejected")

	assert.Equal(t, []string{
		"call.status:RINGING", "call.status:ACCEPTED", "call.answered:api", "call.ended:COMPLETED/hangup_peer",
		"call.status:RINGING", "call.status:REJECTED", "call.ended:REJECTED/rejected",
	}, log.summary())
}

func TestAnsweredElsewhere(t *testing.T) {
	now := time.Now()
	m, eng, log := newTestManager(t, 1, &now, nil)
	in := &fakeCall{id: "IN-1"}
	eng.incoming(in)
	in.end("accepted_elsewhere")
	assert.Equal(t, []string{"call.answered:elsewhere", "call.ended:COMPLETED/answered_elsewhere"}, log.summary())
	assert.Empty(t, m.Active())
}

func TestClassifyEnd(t *testing.T) {
	cases := []struct {
		in             endInput
		status, reason string
	}{
		{endInput{Local: localMaxDuration, MediaFlowed: true}, "FAILED", "timeout"},
		{endInput{Local: localTimeout}, "MISSED", "timeout"},
		{endInput{Local: localReject}, "REJECTED", "rejected"},
		{endInput{Local: localRTCFailed, Answered: true, MediaFlowed: true}, "COMPLETED", "error"},
		{endInput{Local: localRTCFailed, Answered: true}, "FAILED", "error"},
		{endInput{Local: localHangup, Answered: true, MediaFlowed: true}, "COMPLETED", "hangup_agent"},
		{endInput{Local: localHangup, Answered: true}, "FAILED", "hangup_agent"},
		{endInput{Local: localHangup}, "MISSED", "hangup_agent"},
		{endInput{Engine: "accepted_elsewhere"}, "COMPLETED", "answered_elsewhere"},
		{endInput{Engine: "rejected"}, "REJECTED", "rejected"},
		{endInput{Engine: "rejected_elsewhere"}, "REJECTED", "rejected"},
		{endInput{Engine: "timeout"}, "MISSED", "timeout"},
		{endInput{Engine: "server:500"}, "FAILED", "error"},
		{endInput{Engine: "offer_failed"}, "FAILED", "error"},
		{endInput{Engine: "", Answered: true, MediaFlowed: true}, "COMPLETED", "hangup_peer"},
		{endInput{Engine: ""}, "MISSED", "hangup_peer"},
		{endInput{Engine: "something_new", Answered: true}, "FAILED", "hangup_peer"},
	}
	for _, c := range cases {
		status, reason := classifyEnd(c.in)
		assert.Equal(t, c.status, status, "%+v", c.in)
		assert.Equal(t, c.reason, reason, "%+v", c.in)
	}
}

// Review Focus 2: an engine that never reports the end must not hold the cap forever.
func TestMaxDurationFinishesSilentEngine(t *testing.T) {
	eng := &fakeEngine{}
	log := &eventLog{}
	m := newCallManager(eng, callManagerOptions{MaxCalls: 1, MaxDuration: 30 * time.Millisecond, Emit: log.add})
	_, _, err := m.Start(context.Background(), "+5511900000000", "")
	require.NoError(t, err)
	placed := eng.placed[0]
	placed.mu.Lock()
	placed.silent = true
	placed.mu.Unlock()

	require.Eventually(t, func() bool { return len(m.Active()) == 0 }, 2*time.Second, 10*time.Millisecond)
	placed.mu.Lock()
	assert.True(t, placed.hungUp)
	placed.mu.Unlock()
	ended, ok := log.last(EventCallEnded)
	require.True(t, ok)
	assert.Equal(t, "FAILED", ended.Status)
	assert.Equal(t, "timeout", ended.EndReason)
	_, _, err = m.Start(context.Background(), "+5511900000001", "")
	assert.NoError(t, err, "the cap is free again")
}

func TestOfferSDPWaitsForTracking(t *testing.T) {
	now := time.Now()
	media := &fakeMediaFactory{}
	m, eng, _ := newTestManager(t, 1, &now, media)
	go func() {
		time.Sleep(50 * time.Millisecond)
		eng.incoming(&fakeCall{id: "IN-1"})
	}()
	sdp, err := m.OfferSDP(context.Background(), "IN-1")
	require.NoError(t, err)
	assert.Equal(t, "v=0 gowa-offer", sdp)
	again, err := m.OfferSDP(context.Background(), "IN-1")
	require.NoError(t, err)
	assert.Equal(t, sdp, again)
	assert.Len(t, media.made, 1, "one browser leg per call")
}

func TestOfferSDPUnknownAndWithoutMedia(t *testing.T) {
	now := time.Now()
	m, _, _ := newTestManager(t, 1, &now, &fakeMediaFactory{})
	_, err := m.OfferSDP(context.Background(), "NOPE")
	assert.ErrorIs(t, err, ErrCallNotFound)

	bare, _, _ := newTestManager(t, 1, &now, nil)
	_, err = bare.OfferSDP(context.Background(), "IN-1")
	assert.ErrorIs(t, err, ErrCallsNoMedia)
	assert.ErrorIs(t, bare.Answer("IN-1", "v=0"), ErrCallsNoMedia)
}

func TestAnswerWithMediaFlow(t *testing.T) {
	now := time.Now()
	media := &fakeMediaFactory{}
	m, eng, log := newTestManager(t, 1, &now, media)
	in := &fakeCall{id: "IN-1"}
	eng.incoming(in)
	assert.ErrorIs(t, m.Answer("IN-1", "v=0 browser-answer"), ErrCallNoOffer)

	_, err := m.OfferSDP(context.Background(), "IN-1")
	require.NoError(t, err)
	require.NoError(t, m.Answer("IN-1", "v=0 browser-answer"))
	fm := media.last()
	assert.Equal(t, "v=0 browser-answer", fm.accepted)
	assert.True(t, fm.started)

	in.media()
	assert.Equal(t, 1, fm.waReady)
	assert.Empty(t, log.all(), "call.answered waits for the browser leg")
	fm.ev.Flowing()
	assert.Equal(t, []string{"call.answered:api"}, log.summary())

	fm.ev.Lost() // the browser leg is gone: hang up the WhatsApp leg (B10)
	assert.True(t, in.hungUp)
	ended, ok := log.last(EventCallEnded)
	require.True(t, ok)
	assert.Equal(t, "COMPLETED", ended.Status)
	assert.Equal(t, "error", ended.EndReason)
	assert.Equal(t, fm.rec, ended.Recording)
	assert.Equal(t, 1, fm.closed)
}

func TestAnswerBadSDPKeepsCallRinging(t *testing.T) {
	now := time.Now()
	media := &fakeMediaFactory{}
	m, eng, _ := newTestManager(t, 1, &now, media)
	eng.incoming(&fakeCall{id: "IN-1"})
	_, err := m.OfferSDP(context.Background(), "IN-1")
	require.NoError(t, err)
	media.last().acceptErr = errors.New("bad sdp")

	err = m.Answer("IN-1", "garbage")
	var ge pkgError.GenericError
	require.ErrorAs(t, err, &ge)
	assert.Equal(t, 400, ge.StatusCode())
	assert.Equal(t, "ringing", m.Active()[0].State)

	media.last().acceptErr = nil
	require.NoError(t, m.Answer("IN-1", "v=0 browser-answer"))
}

// Review Focus 5 (manager side): a bad browser offer places no WhatsApp call.
func TestStartBadOfferPlacesNoCall(t *testing.T) {
	now := time.Now()
	media := &fakeMediaFactory{err: errors.New("no opus")}
	m, eng, _ := newTestManager(t, 1, &now, media)
	_, _, err := m.Start(context.Background(), "+5511900000000", "v=0 no-opus")
	var ge pkgError.GenericError
	require.ErrorAs(t, err, &ge)
	assert.Equal(t, 400, ge.StatusCode())
	assert.Equal(t, 0, eng.placedCount())

	media.err = nil
	info, answer, err := m.Start(context.Background(), "+5511900000000", "v=0 browser-offer")
	require.NoError(t, err, "the cap slot was released")
	assert.Equal(t, "v=0 gowa-answer", answer)
	assert.Equal(t, []string{"v=0 browser-offer"}, media.offers)
	assert.True(t, media.last().started)
	assert.Equal(t, DirectionBusiness, info.Direction)
}
```

Add to `src/infrastructure/whatsapp/calls_meowcaller_test.go` (add imports `"github.com/stretchr/testify/assert"` and `"go.mau.fi/whatsmeow/types"`):

```go
func TestTargetPN(t *testing.T) {
	assert.Equal(t, "5511900000000@s.whatsapp.net", targetPN("+55 (11) 90000-0000"))
	assert.Equal(t, "5511900000000@s.whatsapp.net", targetPN("5511900000000@s.whatsapp.net"))
	assert.Equal(t, "", targetPN("123456789012345@lid"))
	assert.Equal(t, "", targetPN(""))
}

func TestResolvePeerWithoutStore(t *testing.T) {
	lid := types.NewJID("123456789012345", types.HiddenUserServer)
	assert.Equal(t, CallPeer{LID: "123456789012345@lid"}, resolvePeer(nil, lid, ""))
	assert.Equal(t, CallPeer{PN: "5511900000000@s.whatsapp.net", LID: "123456789012345@lid"},
		resolvePeer(nil, lid, "5511900000000@s.whatsapp.net"))
	pn := types.NewJID("5511900000000", types.DefaultUserServer)
	assert.Equal(t, CallPeer{PN: "5511900000000@s.whatsapp.net"}, resolvePeer(nil, pn, ""))
}

func TestMaskPeer(t *testing.T) {
	assert.Equal(t, "…2345@lid", maskPeer(CallPeer{PN: "5511900000000@s.whatsapp.net", LID: "123456789012345@lid"}))
	assert.Equal(t, "…0000@s.whatsapp.net", maskPeer(CallPeer{PN: "5511900000000@s.whatsapp.net"}))
	assert.Equal(t, "unknown", maskPeer(CallPeer{}))
}
```

- [ ] **Step 3: Run them to see them fail**

Run: `cd src && GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/ -run 'Answer|Incoming|Start|Hangup|Ended|Reject|Peer|Media|Outbound|Elsewhere|Classify|MaxDuration|OfferSDP|TargetPN|MaskPeer'`
Expected: FAIL to build (`undefined: CallPeer`, `undefined: endInput`, …).

- [ ] **Step 4: Implement the manager**

Replace `src/infrastructure/whatsapp/calls.go` with:

```go
package whatsapp

import (
	"context"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/safego"
	"github.com/sirupsen/logrus"
)

// Fork (elphant): voice calls on one device. Stage 2a: docs/specs/2026-10-14-gateway-voice-calls-design.md;
// stage 2b (browser leg, events, recording): docs/specs/2026-10-14-gateway-voice-calls-2b-design.md.

var (
	ErrCallNotFound = pkgError.ValidationError("call not found, already ended, or no longer ringing")
	ErrCallLimit    = pkgError.ValidationError("too many calls on this device")
	ErrCallsOff     = pkgError.ValidationError("calls are not enabled for this device")
	ErrCallAnswered = pkgError.ConflictError("call already answered")
	ErrCallsNoMedia = pkgError.ValidationError("browser audio is unavailable on this gateway (check WHATSAPP_CALLS_RTC_PORT and the start log)")
	ErrCallNoOffer  = pkgError.ValidationError("no offer was made for this call yet; fetch offer_sdp with GET /call/:call_id first")
)

// Meta's words for the direction of a call (B5).
const (
	DirectionUser     = "USER_INITIATED"     // the customer called the device
	DirectionBusiness = "BUSINESS_INITIATED" // the device called the customer
)

const (
	stateRinging = "ringing"
	stateActive  = "active"
)

// Call webhook events (stage 2b).
const (
	EventCallStatus   = "call.status"
	EventCallAnswered = "call.answered"
	EventCallEnded    = "call.ended"
)

// Why GOWA ended a call itself; empty when the engine reported the end.
const (
	localHangup      = "hangup"
	localReject      = "reject"
	localTimeout     = "timeout"
	localMaxDuration = "max_duration"
	localRTCFailed   = "rtc_failed"
	localError       = "error"
)

// CallPeer is the other party, as JID strings ("" when unknown). API responses and webhooks
// carry it in full; logs use maskPeer.
type CallPeer struct {
	PN  string `json:"pn"`
	LID string `json:"lid"`
}

// CallInfo is the public view of one call.
type CallInfo struct {
	CallID    string    `json:"call_id"`
	Direction string    `json:"direction"`
	Peer      CallPeer  `json:"peer"`
	State     string    `json:"state"`
	StartedAt time.Time `json:"started_at"`
}

// RecordingInfo describes a finished recording file.
type RecordingInfo struct {
	Path     string
	Bytes    int64
	Duration time.Duration
}

// CallEvent is one call.status, call.answered or call.ended event.
type CallEvent struct {
	Type       string
	Call       CallInfo
	Status     string // call.status: RINGING, ACCEPTED, REJECTED; call.ended: COMPLETED, FAILED, MISSED, REJECTED
	AnsweredBy string // call.answered: api or elsewhere
	EndReason  string // call.ended
	Duration   int    // call.ended, seconds of media on both legs
	Recording  *RecordingInfo
	At         time.Time
}

// pcmSource and pcmSink have the method sets of meowcaller's AudioSource and AudioSink:
// 16 kHz mono frames of meowcaller.FrameSamples.
type pcmSource interface {
	ReadFrame() ([]float32, error)
	Close() error
}

type pcmSink interface {
	WriteFrame(frame []float32) error
	Close() error
}

// callHandle is one WhatsApp call as the manager sees it.
type callHandle interface {
	ID() string
	Peer() CallPeer
	Answer() error
	Reject() error
	Hangup() error
	OnEnd(func(reason string))
	OnRinging(func()) // outbound: the peer's device rings
	OnAccept(func())  // outbound: the peer answered
	OnMedia(func())   // the WhatsApp leg carries media
	Bridge(src pcmSource, sink pcmSink)
}

type callEngine interface {
	OnIncomingCall(func(callHandle))
	Call(ctx context.Context, target string) (callHandle, error)
}

// mediaEvents are the browser leg's signals back to the manager.
type mediaEvents struct {
	Flowing func() // both legs carry audio
	Lost    func() // the browser leg was down longer than the grace period
}

// callMedia is the browser side of one call: its SDP, its audio bridge and its recording.
type callMedia interface {
	LocalSDP() string                  // inbound: GOWA's offer; outbound: GOWA's answer
	Accept(answerSDP string) error     // inbound: the browser's answer
	Start(h callHandle, ev mediaEvents) // attach audio to the WhatsApp leg and watch the browser leg
	WhatsAppReady()                    // the WhatsApp leg carries media
	Close() *RecordingInfo             // end the browser leg and the recording; idempotent
}

type mediaFactory interface {
	Inbound() (callMedia, error)
	Outbound(offerSDP string) (callMedia, error)
}

type callManagerOptions struct {
	MaxCalls    int
	RingWindow  time.Duration // an inbound offer older than this is rejected as missed
	MaxDuration time.Duration // a tracked call older than this is hung up (0: no limit)
	OfferWait   time.Duration // how long OfferSDP waits for the engine to report an offer
	Now         func() time.Time
	Emit        func(CallEvent) // nil: no events
	Media       mediaFactory    // nil: no browser leg (calls carry no audio)
}

type trackedCall struct {
	handle    callHandle
	info      CallInfo
	media     callMedia
	answering bool
	answered  bool // answered here (inbound) or by the peer (outbound)
	waReady   bool
	ended     bool
	local     string
	mediaAt   time.Time // both legs flowing
	timer     *time.Timer
}

// CallManager tracks the ringing and active calls of one device client. All fields of
// trackedCall are guarded by mu.
type CallManager struct {
	engine   callEngine
	opts     callManagerOptions
	mu       sync.Mutex
	calls    map[string]*trackedCall
	gone     map[string]time.Time // recently ended or refused call ids
	waiters  map[string][]chan struct{}
	reserved int    // outbound slots claimed before dialing
	slotID   string // slot the manager was attached with
}

func newCallManager(engine callEngine, opts callManagerOptions) *CallManager {
	if opts.MaxCalls < 1 {
		opts.MaxCalls = 1
	}
	if opts.RingWindow <= 0 {
		opts.RingWindow = time.Minute
	}
	if opts.OfferWait <= 0 {
		opts.OfferWait = 3 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	m := &CallManager{
		engine: engine, opts: opts,
		calls: map[string]*trackedCall{}, gone: map[string]time.Time{}, waiters: map[string][]chan struct{}{},
	}
	engine.OnIncomingCall(m.onIncoming)
	return m
}

func (m *CallManager) emit(ev CallEvent) {
	if m.opts.Emit != nil {
		m.opts.Emit(ev)
	}
}

func (m *CallManager) onIncoming(h callHandle) {
	m.prune()
	id := h.ID()
	m.mu.Lock()
	full := m.activeCountLocked() >= m.opts.MaxCalls
	if full {
		m.gone[id] = m.opts.Now()
		m.notifyLocked(id)
	}
	m.mu.Unlock()
	if full {
		info := CallInfo{CallID: id, Direction: DirectionUser, Peer: h.Peer(), State: stateRinging, StartedAt: m.opts.Now()}
		logrus.Infof("[CALLS] rejecting %s from %s: device at its call limit", id, maskPeer(info.Peer))
		_ = h.Reject()
		m.emit(CallEvent{Type: EventCallEnded, Call: info, Status: "MISSED", EndReason: "rejected", At: m.opts.Now()})
		return
	}
	tc := m.newTracked(h, DirectionUser, stateRinging, nil)
	m.mu.Lock()
	m.insertLocked(tc)
	m.mu.Unlock()
}

// newTracked builds a tracked call and wires the engine callbacks. It must be called without
// m.mu held: a handle may fire a callback (even OnEnd) synchronously.
func (m *CallManager) newTracked(h callHandle, direction, state string, media callMedia) *trackedCall {
	tc := &trackedCall{handle: h, media: media, info: CallInfo{
		CallID: h.ID(), Direction: direction, Peer: h.Peer(), State: state, StartedAt: m.opts.Now(),
	}}
	h.OnRinging(func() { m.onPeerRinging(tc) })
	h.OnAccept(func() { m.onPeerAccept(tc) })
	h.OnMedia(func() { m.onWhatsAppMedia(tc) })
	h.OnEnd(func(reason string) { m.finish(tc, reason) })
	return tc
}

// insertLocked registers a call unless it already ended, and arms its max duration.
func (m *CallManager) insertLocked(tc *trackedCall) {
	id := tc.info.CallID
	if !tc.ended {
		m.calls[id] = tc
		if m.opts.MaxDuration > 0 {
			tc.timer = time.AfterFunc(m.opts.MaxDuration, func() {
				defer safego.Recover("calls_max_duration")
				logrus.Warnf("[CALLS] call %s reached the maximum duration; hanging up", id)
				_ = m.endLocal(id, localMaxDuration)
			})
		}
	}
	m.notifyLocked(id)
}

func (m *CallManager) notifyLocked(id string) {
	for _, ch := range m.waiters[id] {
		close(ch)
	}
	delete(m.waiters, id)
}

// prune rejects inbound offers that rang past the ring window and forgets old ended ids.
// It takes m.mu itself; callers must not hold it.
func (m *CallManager) prune() {
	now := m.opts.Now()
	var expired []string
	m.mu.Lock()
	for id, tc := range m.calls {
		if tc.info.Direction == DirectionUser && tc.info.State == stateRinging && !tc.answering &&
			now.Sub(tc.info.StartedAt) > m.opts.RingWindow {
			expired = append(expired, id)
		}
	}
	for id, at := range m.gone {
		if now.Sub(at) > m.opts.RingWindow {
			delete(m.gone, id)
		}
	}
	m.mu.Unlock()
	for _, id := range expired {
		_ = m.endLocal(id, localTimeout)
	}
}

func (m *CallManager) activeCountLocked() int {
	n := m.reserved
	for _, tc := range m.calls {
		if tc.info.State == stateActive || tc.answering || tc.info.Direction == DirectionBusiness {
			n++
		}
	}
	return n
}

// waitTracked returns a tracked call, waiting up to OfferWait for the engine to report it:
// the call.offer webhook can run before meowcaller has seen the same offer.
func (m *CallManager) waitTracked(ctx context.Context, callID string) (*trackedCall, error) {
	deadline := time.NewTimer(m.opts.OfferWait)
	defer deadline.Stop()
	for {
		m.mu.Lock()
		if tc, ok := m.calls[callID]; ok {
			m.mu.Unlock()
			return tc, nil
		}
		if _, gone := m.gone[callID]; gone {
			m.mu.Unlock()
			return nil, ErrCallNotFound
		}
		ch := make(chan struct{})
		m.waiters[callID] = append(m.waiters[callID], ch)
		m.mu.Unlock()
		select {
		case <-ch:
		case <-deadline.C:
			m.mu.Lock()
			delete(m.waiters, callID)
			m.mu.Unlock()
			return nil, ErrCallNotFound
		case <-ctx.Done():
			return nil, ErrCallNotFound
		}
	}
}

// OfferSDP returns GOWA's SDP offer for a ringing inbound call, creating the browser leg on
// first use. The call.offer webhook and GET /call/:call_id both use it.
func (m *CallManager) OfferSDP(ctx context.Context, callID string) (string, error) {
	if m.opts.Media == nil {
		return "", ErrCallsNoMedia
	}
	tc, err := m.waitTracked(ctx, callID)
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	if tc.info.Direction != DirectionUser || tc.ended {
		m.mu.Unlock()
		return "", ErrCallNotFound
	}
	if tc.media != nil {
		sdp := tc.media.LocalSDP()
		m.mu.Unlock()
		return sdp, nil
	}
	m.mu.Unlock()

	media, err := m.opts.Media.Inbound() // outside the lock: ICE gathering takes a moment
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	if tc.ended || tc.media != nil {
		existing, ended := tc.media, tc.ended
		m.mu.Unlock()
		media.Close()
		if ended {
			return "", ErrCallNotFound
		}
		return existing.LocalSDP(), nil
	}
	tc.media = media
	m.mu.Unlock()
	return media.LocalSDP(), nil
}

// Answer answers a ringing inbound call with the browser's SDP answer (B3).
func (m *CallManager) Answer(callID, answerSDP string) error {
	if m.opts.Media == nil && answerSDP != "" {
		return ErrCallsNoMedia
	}
	m.prune()
	m.mu.Lock()
	tc, ok := m.calls[callID]
	if !ok || tc.info.Direction != DirectionUser {
		m.mu.Unlock()
		return ErrCallNotFound
	}
	if tc.answering || tc.answered {
		m.mu.Unlock()
		return ErrCallAnswered
	}
	if m.activeCountLocked() >= m.opts.MaxCalls {
		m.mu.Unlock()
		return ErrCallLimit
	}
	media := tc.media
	if m.opts.Media != nil && media == nil {
		m.mu.Unlock()
		return ErrCallNoOffer
	}
	tc.answering = true
	m.mu.Unlock()

	if media != nil {
		if err := media.Accept(answerSDP); err != nil {
			m.mu.Lock()
			tc.answering = false
			m.mu.Unlock()
			return pkgError.ValidationError("invalid sdp: " + err.Error())
		}
	}
	if err := tc.handle.Answer(); err != nil {
		m.mu.Lock()
		if tc.local == "" {
			tc.local = localError
		}
		m.mu.Unlock()
		_ = tc.handle.Reject()
		m.finish(tc, "")
		return err
	}
	m.mu.Lock()
	if tc.ended {
		m.mu.Unlock()
		return ErrCallNotFound
	}
	tc.answering, tc.answered, tc.info.State = false, true, stateActive
	ready := tc.waReady
	m.mu.Unlock()
	if media != nil {
		media.Start(tc.handle, m.mediaEvents(tc))
	}
	if ready {
		m.whatsAppReady(tc, media)
	}
	return nil
}

// Start places an outbound call to target (phone number or JID) with the browser's SDP offer
// and returns GOWA's SDP answer at once (B4). A bad offer places no call.
func (m *CallManager) Start(ctx context.Context, target, offerSDP string) (CallInfo, string, error) {
	if m.opts.Media == nil && offerSDP != "" {
		return CallInfo{}, "", ErrCallsNoMedia
	}
	m.prune()
	m.mu.Lock()
	if m.activeCountLocked() >= m.opts.MaxCalls {
		m.mu.Unlock()
		return CallInfo{}, "", ErrCallLimit
	}
	m.reserved++
	m.mu.Unlock()
	release := func() {
		m.mu.Lock()
		m.reserved--
		m.mu.Unlock()
	}

	var media callMedia
	if m.opts.Media != nil {
		var err error
		if media, err = m.opts.Media.Outbound(offerSDP); err != nil {
			release()
			return CallInfo{}, "", pkgError.ValidationError("invalid sdp: " + err.Error())
		}
	}
	h, err := m.engine.Call(ctx, target)
	if err != nil {
		release()
		if media != nil {
			media.Close()
		}
		return CallInfo{}, "", err
	}
	tc := m.newTracked(h, DirectionBusiness, stateRinging, media)
	m.mu.Lock()
	m.reserved--
	m.insertLocked(tc)
	ended, info := tc.ended, tc.info
	m.mu.Unlock()
	if ended {
		return CallInfo{}, "", ErrCallNotFound
	}
	if media == nil {
		return info, "", nil
	}
	media.Start(h, m.mediaEvents(tc))
	return info, media.LocalSDP(), nil
}

// Reject declines a ringing inbound call.
func (m *CallManager) Reject(callID string) error {
	m.mu.Lock()
	tc, ok := m.calls[callID]
	ringing := ok && tc.info.Direction == DirectionUser && tc.info.State == stateRinging && !tc.answering
	m.mu.Unlock()
	if !ringing {
		return ErrCallNotFound
	}
	return m.endLocal(callID, localReject)
}

// Hangup ends a call: a ringing inbound call is rejected, anything else is hung up.
func (m *CallManager) Hangup(callID string) error { return m.endLocal(callID, localHangup) }

// endLocal ends a call on GOWA's initiative and records why. It finishes the call itself when
// the engine does not report the end (an engine that already forgot the call).
func (m *CallManager) endLocal(callID, why string) error {
	m.mu.Lock()
	tc, ok := m.calls[callID]
	if !ok {
		m.mu.Unlock()
		return ErrCallNotFound
	}
	ringingIn := tc.info.Direction == DirectionUser && tc.info.State == stateRinging && !tc.answering
	if tc.local == "" {
		tc.local = why
		if why == localHangup && ringingIn {
			tc.local = localReject
		}
	}
	h := tc.handle
	m.mu.Unlock()
	var err error
	if ringingIn {
		err = h.Reject()
	} else {
		err = h.Hangup()
	}
	m.finish(tc, "")
	return err
}

// closeAll ends every call of the manager (client discarded, or calls switched off).
func (m *CallManager) closeAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.calls))
	for id := range m.calls {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		_ = m.endLocal(id, localHangup)
	}
}

func (m *CallManager) onPeerRinging(tc *trackedCall) {
	m.mu.Lock()
	skip := tc.ended || tc.answered || tc.info.Direction != DirectionBusiness
	info := tc.info
	m.mu.Unlock()
	if !skip {
		m.emit(CallEvent{Type: EventCallStatus, Call: info, Status: "RINGING", At: m.opts.Now()})
	}
}

func (m *CallManager) onPeerAccept(tc *trackedCall) {
	m.mu.Lock()
	if tc.ended || tc.answered || tc.info.Direction != DirectionBusiness {
		m.mu.Unlock()
		return
	}
	tc.answered, tc.info.State = true, stateActive
	info, ready, media := tc.info, tc.waReady, tc.media
	m.mu.Unlock()
	m.emit(CallEvent{Type: EventCallStatus, Call: info, Status: "ACCEPTED", At: m.opts.Now()})
	if ready {
		m.whatsAppReady(tc, media)
	}
}

// onWhatsAppMedia records that the WhatsApp leg carries media. With early media the relay can
// report it while the call still rings, so it only counts once the call is answered.
func (m *CallManager) onWhatsAppMedia(tc *trackedCall) {
	m.mu.Lock()
	if tc.ended || tc.waReady {
		m.mu.Unlock()
		return
	}
	tc.waReady = true
	answered, media := tc.answered, tc.media
	m.mu.Unlock()
	if answered {
		m.whatsAppReady(tc, media)
	}
}

func (m *CallManager) whatsAppReady(tc *trackedCall, media callMedia) {
	if media != nil {
		media.WhatsAppReady()
		return
	}
	m.markFlowing(tc)
}

// markFlowing records that media flows on both legs and sends call.answered (answered_by api).
func (m *CallManager) markFlowing(tc *trackedCall) {
	m.mu.Lock()
	if tc.ended || !tc.mediaAt.IsZero() {
		m.mu.Unlock()
		return
	}
	tc.mediaAt = m.opts.Now()
	info := tc.info
	m.mu.Unlock()
	logrus.Infof("[CALLS] call %s: media flowing on both legs", info.CallID)
	m.emit(CallEvent{Type: EventCallAnswered, Call: info, AnsweredBy: "api", At: m.opts.Now()})
}

func (m *CallManager) mediaEvents(tc *trackedCall) mediaEvents {
	id := tc.info.CallID
	return mediaEvents{
		Flowing: func() { m.markFlowing(tc) },
		Lost: func() {
			logrus.Warnf("[CALLS] call %s: browser leg lost; hanging up", id)
			_ = m.endLocal(id, localRTCFailed)
		},
	}
}

// finish ends a call once: it drops it, stops its timer, closes the browser leg and the
// recording, and sends the end events.
func (m *CallManager) finish(tc *trackedCall, engineReason string) {
	m.mu.Lock()
	if tc.ended {
		m.mu.Unlock()
		return
	}
	tc.ended = true
	id := tc.info.CallID
	if m.calls[id] == tc {
		delete(m.calls, id)
	}
	now := m.opts.Now()
	m.gone[id] = now
	m.notifyLocked(id)
	info, local, answered, mediaAt, media, timer := tc.info, tc.local, tc.answered, tc.mediaAt, tc.media, tc.timer
	m.mu.Unlock()

	if timer != nil {
		timer.Stop()
	}
	var rec *RecordingInfo
	if media != nil {
		rec = media.Close()
	}
	status, reason := classifyEnd(endInput{Engine: engineReason, Local: local, Answered: answered, MediaFlowed: !mediaAt.IsZero()})
	duration := 0
	if !mediaAt.IsZero() {
		duration = int(math.Round(now.Sub(mediaAt).Seconds()))
	}
	if reason == "answered_elsewhere" {
		m.emit(CallEvent{Type: EventCallAnswered, Call: info, AnsweredBy: "elsewhere", At: now})
	}
	if info.Direction == DirectionBusiness && status == "REJECTED" && local == "" {
		m.emit(CallEvent{Type: EventCallStatus, Call: info, Status: "REJECTED", At: now})
	}
	logrus.Infof("[CALLS] call %s with %s ended: %s (%s, engine %q, %ds)", id, maskPeer(info.Peer), reason, status, MaskDigits(engineReason), duration)
	m.emit(CallEvent{Type: EventCallEnded, Call: info, Status: status, EndReason: reason, Duration: duration, Recording: rec, At: now})
}

type endInput struct {
	Engine      string // meowcaller's reason; "" when the peer hung up
	Local       string // why GOWA ended it, or ""
	Answered    bool
	MediaFlowed bool
}

// classifyEnd maps an end to call.ended's status and end_reason (B5).
func classifyEnd(in endInput) (status, reason string) {
	switch in.Local {
	case localMaxDuration:
		return "FAILED", "timeout"
	case localTimeout:
		return "MISSED", "timeout"
	case localReject:
		return "REJECTED", "rejected"
	case localRTCFailed, localError:
		if in.MediaFlowed {
			return "COMPLETED", "error"
		}
		return "FAILED", "error"
	case localHangup:
		return afterMedia(in), "hangup_agent"
	}
	switch {
	case in.Engine == "accepted_elsewhere":
		return "COMPLETED", "answered_elsewhere"
	case in.Engine == "rejected" || in.Engine == "rejected_elsewhere":
		return "REJECTED", "rejected"
	case in.Engine == "timeout":
		return "MISSED", "timeout"
	case in.Engine == "offer_failed" || strings.HasPrefix(in.Engine, "server:"):
		if in.MediaFlowed {
			return "COMPLETED", "error"
		}
		return "FAILED", "error"
	}
	return afterMedia(in), "hangup_peer"
}

func afterMedia(in endInput) string {
	switch {
	case in.MediaFlowed:
		return "COMPLETED"
	case in.Answered:
		return "FAILED"
	default:
		return "MISSED"
	}
}

// Get returns a tracked call.
func (m *CallManager) Get(callID string) (CallInfo, bool) {
	m.prune()
	m.mu.Lock()
	defer m.mu.Unlock()
	tc, ok := m.calls[callID]
	if !ok {
		return CallInfo{}, false
	}
	return tc.info, true
}

// Active lists ringing and active calls, oldest first.
func (m *CallManager) Active() []CallInfo {
	m.prune()
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]CallInfo, 0, len(m.calls))
	for _, tc := range m.calls {
		out = append(out, tc.info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out
}

// maskPeer is the peer for logs: the last 4 digits only.
func maskPeer(p CallPeer) string {
	switch {
	case p.LID != "":
		return MaskDigits(p.LID)
	case p.PN != "":
		return MaskDigits(p.PN)
	default:
		return "unknown"
	}
}
```

- [ ] **Step 5: Rewrite the meowcaller adapter**

Replace `src/infrastructure/whatsapp/calls_meowcaller.go` with:

```go
package whatsapp

import (
	"context"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	meowcaller "github.com/purpshell/meowcaller"
	"github.com/rs/zerolog"
	"github.com/sirupsen/logrus"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// Fork (elphant): adapter from meowcaller to the callEngine/callHandle seams.

type meowEngine struct {
	client *meowcaller.Client
	wa     *whatsmeow.Client
}

// parseCallsLogLevel maps the configured level to zerolog; anything else is info.
func parseCallsLogLevel(s string) zerolog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return zerolog.DebugLevel
	case "warn":
		return zerolog.WarnLevel
	case "error":
		return zerolog.ErrorLevel
	}
	return zerolog.InfoLevel
}

// parseCallsEarlyMedia maps the configured mode to meowcaller; anything else is off.
func parseCallsEarlyMedia(s string) meowcaller.EarlyMediaMode {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "off":
		return meowcaller.EarlyMediaOff
	case "listen":
		return meowcaller.EarlyMediaListen
	case "full":
		return meowcaller.EarlyMediaFull
	}
	logrus.Warnf("[CALLS] invalid WHATSAPP_CALLS_EARLY_MEDIA %q; using off", s)
	return meowcaller.EarlyMediaOff
}

func newMeowEngine(wa *whatsmeow.Client, filter func(*events.CallOffer) bool) *meowEngine {
	log := zerolog.New(maskingWriter{os.Stdout}).With().Timestamp().Str("component", "calls").Logger().Level(parseCallsLogLevel(config.WhatsappCallsLogLevel))
	return &meowEngine{wa: wa, client: meowcaller.NewClient(wa, meowcaller.WithLogger(log), meowcaller.WithOfferFilter(filter), meowcaller.WithEarlyMedia(parseCallsEarlyMedia(config.WhatsappCallsEarlyMedia)))}
}

func (e *meowEngine) OnIncomingCall(fn func(callHandle)) {
	e.client.OnIncomingCall(func(c *meowcaller.Call) {
		fn(meowCall{c: c, peer: resolvePeer(e.wa, c.Peer(), "")})
	})
}

func (e *meowEngine) Call(ctx context.Context, target string) (callHandle, error) {
	c, err := e.client.Call(ctx, target)
	if err != nil {
		return nil, err
	}
	return meowCall{c: c, peer: resolvePeer(e.wa, c.Peer(), targetPN(target))}, nil
}

type meowCall struct {
	c    *meowcaller.Call
	peer CallPeer
}

func (m meowCall) ID() string     { return m.c.ID() }
func (m meowCall) Peer() CallPeer { return m.peer }
func (m meowCall) Answer() error  { return m.c.Answer() }
func (m meowCall) Reject() error  { return m.c.Reject() }
func (m meowCall) Hangup() error  { return m.c.Hangup() }

// OnEnd registers fn. meowcaller does not replay an end to a callback registered
// after the call ended, so check the phase after registering; the Once guard stops a
// double call when the end fires concurrently with that check.
func (m meowCall) OnEnd(fn func(string)) {
	var once sync.Once
	wrapped := func(reason string) { once.Do(func() { fn(reason) }) }
	m.c.OnEnd(wrapped)
	if m.c.State() == meowcaller.CallPhaseEnded {
		wrapped("ended")
	}
}

// OnRinging fires when an outbound call's peer device pre-accepts (the phone rings).
func (m meowCall) OnRinging(fn func()) {
	m.c.OnStateChange(func(p meowcaller.CallPhase) {
		if p == meowcaller.CallPhaseRinging {
			fn()
		}
	})
}

func (m meowCall) OnAccept(fn func()) { m.c.OnPeerAccept(fn) }
func (m meowCall) OnMedia(fn func())  { m.c.OnReady(fn) }

// Bridge attaches the call's audio. sink receives the peer's decoded 16 kHz frames; src is
// pulled every 60 ms by the send loop once media runs (it is not wrapped in OnReady: that is a
// single slot that may already have fired).
func (m meowCall) Bridge(src pcmSource, sink pcmSink) {
	m.c.Receive(sink)
	m.c.Play(src)
}

// resolvePeer builds the peer's phone-number JID and LID from what the engine knows plus the
// device's LID store. Either may stay "".
func resolvePeer(wa *whatsmeow.Client, peer types.JID, pnHint string) CallPeer {
	var out CallPeer
	peer = peer.ToNonAD()
	switch peer.Server {
	case types.HiddenUserServer:
		out.LID = peer.String()
	case types.DefaultUserServer:
		out.PN = peer.String()
	}
	if pnHint != "" {
		out.PN = pnHint
	}
	if wa == nil || wa.Store == nil || wa.Store.LIDs == nil {
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if out.PN == "" && out.LID != "" {
		if pn, err := wa.Store.LIDs.GetPNForLID(ctx, peer); err == nil && !pn.IsEmpty() {
			out.PN = pn.ToNonAD().String()
		}
	}
	if out.LID == "" && out.PN != "" {
		if pnJID, err := types.ParseJID(out.PN); err == nil {
			if lid, err := wa.Store.LIDs.GetLIDForPN(ctx, pnJID); err == nil && !lid.IsEmpty() {
				out.LID = lid.ToNonAD().String()
			}
		}
	}
	return out
}

var nonDigits = regexp.MustCompile(`[^0-9]`)

// targetPN is the phone-number JID of a call target given as digits, "+55 ..." or a JID.
func targetPN(target string) string {
	if strings.Contains(target, "@") {
		j, err := types.ParseJID(target)
		if err != nil || j.Server != types.DefaultUserServer {
			return ""
		}
		return j.ToNonAD().String()
	}
	digits := nonDigits.ReplaceAllString(target, "")
	if digits == "" {
		return ""
	}
	return digits + "@" + types.DefaultUserServer
}

var jidDigits = regexp.MustCompile(`([0-9]{8,})((?::[0-9]+)?@)`)

// maskSlot masks the phone number in a slot id that is a JID.
func maskSlot(s string) string { return string(maskJIDDigits([]byte(s))) }

func maskJIDDigits(p []byte) []byte {
	return jidDigits.ReplaceAllFunc(p, func(b []byte) []byte {
		m := jidDigits.FindSubmatch(b)
		out := append([]byte("…"), m[1][len(m[1])-4:]...)
		return append(out, m[2]...)
	})
}

// maskingWriter hides phone numbers in meowcaller's logs: every run of 8 or more
// digits in a JID (before "@" or ":device@") is cut to an ellipsis plus its last 4 digits.
type maskingWriter struct{ w io.Writer }

func (mw maskingWriter) Write(p []byte) (int, error) {
	masked := maskJIDDigits(p)
	if _, err := mw.w.Write(masked); err != nil {
		return 0, err
	}
	return len(p), nil
}

var digitRun = regexp.MustCompile(`[0-9]{8,}`)

// MaskDigits replaces every run of 8 or more digits with an ellipsis plus its last 4,
// so error text from the call engine can be returned to API clients without phone numbers.
func MaskDigits(s string) string {
	return digitRun.ReplaceAllStringFunc(s, func(r string) string { return "…" + r[len(r)-4:] })
}
```

- [ ] **Step 6: Settings (max duration), drop the 2a test audio**

In `src/config/calls.go`, delete the `WhatsappCallsTestAudio` line and add inside the `var` block:

```go
	WhatsappCallsMaxDurationMinutes = 120 // stage 2b: hang up a call after this many minutes and report it FAILED (0: no limit)
```

Append to `src/config/calls_test.go`:

```go
func TestCallsStage2bDefaults(t *testing.T) {
	if WhatsappCallsMaxDurationMinutes != 120 {
		t.Fatalf("WhatsappCallsMaxDurationMinutes = %d, want 120", WhatsappCallsMaxDurationMinutes)
	}
}
```

`src/cmd/calls_flags.go`:

```go
package cmd

import (
	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/spf13/viper"
)

// Fork (elphant): settings of voice calls stage 2b, kept out of root.go.
// See docs/specs/2026-10-14-gateway-voice-calls-2b-design.md.

func initCallsEnv() {
	if viper.IsSet("whatsapp_calls_max_duration_minutes") {
		config.WhatsappCallsMaxDurationMinutes = viper.GetInt("whatsapp_calls_max_duration_minutes")
	}
}

func initCallsFlags() {
	flags := rootCmd.PersistentFlags()
	flags.IntVarP(&config.WhatsappCallsMaxDurationMinutes, "calls-max-duration-minutes", "", config.WhatsappCallsMaxDurationMinutes,
		`hang up a call after this many minutes (0: no limit) --calls-max-duration-minutes <n> | example: --calls-max-duration-minutes=120`)
}
```

`src/cmd/calls_flags_test.go`:

```go
package cmd

import (
	"testing"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/spf13/viper"
)

func TestInitCallsEnv(t *testing.T) {
	orig := config.WhatsappCallsMaxDurationMinutes
	t.Cleanup(func() { config.WhatsappCallsMaxDurationMinutes = orig })
	viper.Set("whatsapp_calls_max_duration_minutes", 30)
	t.Cleanup(func() { viper.Set("whatsapp_calls_max_duration_minutes", orig) })

	initCallsEnv()
	if config.WhatsappCallsMaxDurationMinutes != 30 {
		t.Fatalf("got %d, want 30", config.WhatsappCallsMaxDurationMinutes)
	}
	if rootCmd.PersistentFlags().Lookup("calls-max-duration-minutes") == nil {
		t.Fatal("flag not registered")
	}
}
```

In `src/cmd/root.go`:
- In the `// Fork (elphant): voice calls` env block, delete the three `whatsapp_calls_test_audio` lines.
  After the block's last `if` (`whatsapp_calls_early_media`), add:

```go
	initCallsEnv() // Fork (elphant): voice calls stage 2b settings (cmd/calls_flags.go).
```

- In the flags block, delete the two `calls-test-audio` lines. After the `calls-early-media` flag, add:

```go
	initCallsFlags() // Fork (elphant): voice calls stage 2b flags (cmd/calls_flags.go).
```

In `src/.env.example`, replace `WHATSAPP_CALLS_TEST_AUDIO=` with:

```
# Hang up a call after this many minutes and report it FAILED (0: no limit)
WHATSAPP_CALLS_MAX_DURATION_MINUTES=120
```

In `src/infrastructure/whatsapp/calls_registry.go`, replace the `newCallManager(...)` call in
`attachCalls` with:

```go
	m := newCallManager(newMeowEngine(client, offerFilter(slotID, client)), callManagerOptions{
		MaxCalls:    config.WhatsappCallsMaxPerDevice,
		RingWindow:  time.Minute,
		MaxDuration: time.Duration(config.WhatsappCallsMaxDurationMinutes) * time.Minute,
	})
```

In `src/infrastructure/whatsapp/calls_switch_test.go`, change `m.Answer("IN-1")` to `m.Answer("IN-1", "")`.

- [ ] **Step 7: Domain, usecase and REST stub follow the new model**

In `src/domains/call/call.go`, replace `CallInfo` with:

```go
type CallPeer struct {
	PN  string `json:"pn"`
	LID string `json:"lid"`
}

type CallInfo struct {
	CallID    string   `json:"call_id"`
	Direction string   `json:"direction"`
	Peer      CallPeer `json:"peer"`
	State     string   `json:"state"`
	StartedAt string   `json:"started_at"`
}
```

In `src/usecase/call.go`, replace `toCallInfo` and the two manager calls:

```go
func toCallInfo(c whatsapp.CallInfo) domainCall.CallInfo {
	return domainCall.CallInfo{
		CallID: c.CallID, Direction: c.Direction, Peer: domainCall.CallPeer{PN: c.Peer.PN, LID: c.Peer.LID},
		State: c.State, StartedAt: c.StartedAt.UTC().Format(time.RFC3339),
	}
}
```

In `AnswerCall`: `return mapCallError(m.Answer(callID, ""), "call operation failed: ")`. In
`StartCall`: `info, _, err := m.Start(startCtx, phone, "")`. Task 8 passes the SDPs.

In `src/ui/rest/call_test.go`, change the stub's `StartCall` result to
`domainCall.CallInfo{CallID: "OUT-1", Direction: "BUSINESS_INITIATED", State: "ringing"}`.

- [ ] **Step 8: Run the package tests with the race detector**

Run: `cd src && gofmt -l infrastructure config cmd pkg domains usecase ui; GOTOOLCHAIN=auto go vet ./infrastructure/whatsapp/ ./cmd/ && GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/ ./config/ ./cmd/ ./usecase/ ./ui/rest/ -race`
Expected: no gofmt output; vet clean; `ok` everywhere, no race report.

- [ ] **Step 9: Commit**

```bash
git add src/pkg/error/conflict_error.go src/infrastructure/whatsapp/calls.go src/infrastructure/whatsapp/calls_test.go \
  src/infrastructure/whatsapp/calls_meowcaller.go src/infrastructure/whatsapp/calls_meowcaller_test.go \
  src/infrastructure/whatsapp/calls_registry.go src/infrastructure/whatsapp/calls_switch_test.go \
  src/config/calls.go src/config/calls_test.go src/cmd/calls_flags.go src/cmd/calls_flags_test.go src/cmd/root.go \
  src/.env.example src/domains/call/call.go src/usecase/call.go src/ui/rest/call_test.go
git commit -m "feat(calls): 2b call model with full peer, Meta statuses, events and max duration"
```

---
### Task 4: Audio plumbing: resampler 16 ⇄ 48 kHz, re-chunker, jitter FIFO

The package is pure Go with no dependencies, and mono float32 PCM in [-1, 1]. One 48-tap
Blackman-windowed sinc low-pass (cutoff 7.2 kHz at 48 kHz, unit gain at DC) does both jobs: the
anti-imaging filter of the upsampler and the anti-aliasing filter of the downsampler. Both keep
their filter history across calls, so chunked processing equals processing in one go.

**Files:**
- Create: `src/pkg/callaudio/resample.go`, `src/pkg/callaudio/chunk.go`, `src/pkg/callaudio/fifo.go`
- Create: `src/pkg/callaudio/callaudio_test.go`

**Interfaces:**
- Produces (used by Task 7):

```go
const Ratio = 3
func NewUpsampler() *Upsampler;     func (u *Upsampler) Process(in []float32) []float32   // 16 kHz in, 3x samples at 48 kHz out
func NewDownsampler() *Downsampler; func (d *Downsampler) Process(in []float32) []float32 // 48 kHz in, 16 kHz out
func NewRechunker(size int) *Rechunker; func (r *Rechunker) Push(in []float32) [][]float32
func NewFIFO(prefill, max int) *FIFO; func (f *FIFO) Write(pcm []float32); func (f *FIFO) Read(n int) []float32; func (f *FIFO) Stats() (underruns, dropped int)
```

- [ ] **Step 1: Write the failing tests**

`src/pkg/callaudio/callaudio_test.go`:

```go
package callaudio

import (
	"math"
	"testing"
)

func sine(n, rate int, hz, amp float64) []float32 {
	out := make([]float32, n)
	for i := range out {
		out[i] = float32(amp * math.Sin(2*math.Pi*hz*float64(i)/float64(rate)))
	}
	return out
}

func rms(p []float32) float64 {
	var s float64
	for _, v := range p {
		s += float64(v) * float64(v)
	}
	return math.Sqrt(s / float64(len(p)))
}

func zeroCrossings(p []float32) int {
	n := 0
	for i := 1; i < len(p); i++ {
		if (p[i-1] < 0) != (p[i] < 0) {
			n++
		}
	}
	return n
}

func TestUpsampleKeepsToneAndLevel(t *testing.T) {
	in := sine(16000, 16000, 1000, 0.5) // 1 s at 16 kHz
	out := NewUpsampler().Process(in)
	if len(out) != 48000 {
		t.Fatalf("len %d, want 48000", len(out))
	}
	steady := out[480:]
	if r := rms(steady) / rms(in); r < 0.97 || r > 1.03 {
		t.Fatalf("level ratio %.3f, want about 1", r)
	}
	if z := zeroCrossings(steady); z < 1960 || z > 2000 {
		t.Fatalf("%d zero crossings, want about 1970 for 1 kHz", z)
	}
}

func TestDownsampleKeepsToneAndRejectsAliases(t *testing.T) {
	in := sine(48000, 48000, 1000, 0.5)
	out := NewDownsampler().Process(in)
	if len(out) != 16000 {
		t.Fatalf("len %d, want 16000", len(out))
	}
	steady := out[160:]
	if r := rms(steady) / rms(in); r < 0.97 || r > 1.03 {
		t.Fatalf("level ratio %.3f, want about 1", r)
	}
	if z := zeroCrossings(steady); z < 1960 || z > 2000 {
		t.Fatalf("%d zero crossings, want about 1970", z)
	}
	alias := NewDownsampler().Process(sine(48000, 48000, 12000, 0.5))
	if r := rms(alias[160:]) / 0.3536; r > 0.05 {
		t.Fatalf("12 kHz leaks through at %.3f of its level, want under 0.05", r)
	}
}

func TestResamplersAreChunkInvariant(t *testing.T) {
	in16 := sine(4800, 16000, 440, 0.4)
	whole := NewUpsampler().Process(in16)
	up := NewUpsampler()
	var parts []float32
	for i := 0; i < len(in16); i += 960 {
		parts = append(parts, up.Process(in16[i:i+960])...)
	}
	assertClose(t, whole, parts)

	in48 := sine(14400, 48000, 440, 0.4)
	wholeDown := NewDownsampler().Process(in48)
	down := NewDownsampler()
	var partsDown []float32
	for i := 0; i < len(in48); i += 1000 { // not a multiple of 3: the phase must carry over
		end := min(i+1000, len(in48))
		partsDown = append(partsDown, down.Process(in48[i:end])...)
	}
	assertClose(t, wholeDown, partsDown)
}

func assertClose(t *testing.T, a, b []float32) {
	t.Helper()
	if len(a) != len(b) {
		t.Fatalf("lengths %d and %d differ", len(a), len(b))
	}
	for i := range a {
		if math.Abs(float64(a[i]-b[i])) > 1e-5 {
			t.Fatalf("sample %d: %f vs %f", i, a[i], b[i])
		}
	}
}

func TestRechunker(t *testing.T) {
	r := NewRechunker(960)
	if frames := r.Push(make([]float32, 2880)); len(frames) != 3 || len(frames[2]) != 960 {
		t.Fatalf("2880 samples gave %d frames, want 3 of 960", len(frames))
	}
	if frames := r.Push(make([]float32, 500)); len(frames) != 0 {
		t.Fatal("500 samples must not make a frame")
	}
	in := sine(500, 48000, 440, 0.5)
	frames := r.Push(in)
	if len(frames) != 1 {
		t.Fatalf("1000 samples gave %d frames, want 1", len(frames))
	}
	if frames[0][500] != in[0] {
		t.Fatal("samples must keep their order across pushes")
	}
}

func TestFIFOPrefillUnderrunAndOverflow(t *testing.T) {
	f := NewFIFO(960, 3840)
	f.Write(sine(500, 16000, 440, 0.5))
	if rms(f.Read(960)) != 0 {
		t.Fatal("before prefill the reader gets silence")
	}
	f.Write(sine(1460, 16000, 440, 0.5)) // 1960 buffered
	if rms(f.Read(960)) == 0 {
		t.Fatal("after prefill the reader gets audio")
	}
	f.Read(960) // 40 left
	f.Read(960) // underrun
	if u, _ := f.Stats(); u != 1 {
		t.Fatalf("underruns %d, want 1", u)
	}
	f.Write(sine(500, 16000, 440, 0.5))
	if rms(f.Read(960)) != 0 {
		t.Fatal("after an underrun it waits for prefill again")
	}
	f.Write(make([]float32, 5000))
	if _, dropped := f.Stats(); dropped == 0 {
		t.Fatal("samples beyond max must be dropped")
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `cd src && GOTOOLCHAIN=auto go test ./pkg/callaudio/`
Expected: FAIL to build (`undefined: NewUpsampler`).

- [ ] **Step 3: Implement**

`src/pkg/callaudio/resample.go`:

```go
// Package callaudio converts call audio between WhatsApp's 16 kHz / 60 ms frames and WebRTC's
// 48 kHz / 20 ms frames (fork: elphant, voice calls stage 2b, B9). Mono float32 PCM in [-1, 1].
package callaudio

import "math"

const (
	// Ratio is 48 kHz over 16 kHz.
	Ratio = 3
	taps  = 48
)

// lowpass is a Blackman-windowed sinc at 48 kHz, cutoff 7.2 kHz (under the 8 kHz Nyquist of
// the 16 kHz side), normalised to unit gain at DC.
var lowpass = designLowpass(taps, 7200.0/48000.0)

func designLowpass(n int, fc float64) []float32 {
	h := make([]float64, n)
	mid := float64(n-1) / 2
	var sum float64
	for i := range h {
		x := float64(i) - mid
		s := 2 * fc
		if x != 0 {
			s = math.Sin(2*math.Pi*fc*x) / (math.Pi * x)
		}
		w := 0.42 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(n-1)) + 0.08*math.Cos(4*math.Pi*float64(i)/float64(n-1))
		h[i] = s * w
		sum += h[i]
	}
	out := make([]float32, n)
	for i := range h {
		out[i] = float32(h[i] / sum)
	}
	return out
}

// fir filters one window of taps samples (the filter is symmetric).
func fir(window []float32) float32 {
	var acc float32
	for k, c := range lowpass {
		acc += window[k] * c
	}
	return acc
}

// Upsampler turns 16 kHz into 48 kHz: zero-stuffing, then the low-pass removes the images.
type Upsampler struct{ hist []float32 }

func NewUpsampler() *Upsampler { return &Upsampler{hist: make([]float32, taps-1)} }

func (u *Upsampler) Process(in []float32) []float32 {
	buf := make([]float32, len(u.hist)+len(in)*Ratio)
	copy(buf, u.hist)
	for i, s := range in {
		buf[len(u.hist)+i*Ratio] = s * Ratio
	}
	out := make([]float32, len(in)*Ratio)
	for i := range out {
		out[i] = fir(buf[i : i+taps])
	}
	copy(u.hist, buf[len(buf)-len(u.hist):])
	return out
}

// Downsampler turns 48 kHz into 16 kHz: the low-pass removes what would alias, then it keeps
// every third sample. Input of any length works; the phase carries over.
type Downsampler struct {
	hist  []float32
	phase int // index, in the next input, of the first sample that ends an output window
}

func NewDownsampler() *Downsampler { return &Downsampler{hist: make([]float32, taps-1)} }

func (d *Downsampler) Process(in []float32) []float32 {
	buf := make([]float32, len(d.hist)+len(in))
	copy(buf, d.hist)
	copy(buf[len(d.hist):], in)
	out := make([]float32, 0, len(in)/Ratio+1)
	p := d.phase
	for ; p < len(in); p += Ratio {
		out = append(out, fir(buf[p:p+taps]))
	}
	d.phase = p - len(in)
	copy(d.hist, buf[len(buf)-len(d.hist):])
	return out
}
```

`src/pkg/callaudio/chunk.go`:

```go
package callaudio

// Rechunker regroups a stream of samples into frames of a fixed size.
type Rechunker struct {
	size int
	buf  []float32
}

func NewRechunker(size int) *Rechunker { return &Rechunker{size: size} }

// Push appends samples and returns every complete frame, in order.
func (r *Rechunker) Push(in []float32) [][]float32 {
	r.buf = append(r.buf, in...)
	var out [][]float32
	for len(r.buf) >= r.size {
		frame := make([]float32, r.size)
		copy(frame, r.buf[:r.size])
		out = append(out, frame)
		r.buf = r.buf[r.size:]
	}
	r.buf = append([]float32(nil), r.buf...) // do not pin the consumed prefix
	return out
}
```

`src/pkg/callaudio/fifo.go`:

```go
package callaudio

import "sync"

// FIFO is the jitter buffer on the WebRTC side: the network writes samples as packets arrive,
// and the reader pulls fixed frames on its own clock. It plays silence until it holds prefill
// samples, again after an underrun, and drops the oldest samples beyond max.
type FIFO struct {
	mu        sync.Mutex
	buf       []float32
	prefill   int
	max       int
	playing   bool
	underruns int
	dropped   int
}

func NewFIFO(prefill, max int) *FIFO { return &FIFO{prefill: prefill, max: max} }

func (f *FIFO) Write(pcm []float32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.buf = append(f.buf, pcm...)
	if over := len(f.buf) - f.max; over > 0 {
		f.buf = append([]float32(nil), f.buf[over:]...)
		f.dropped += over
	}
}

// Read returns exactly n samples, padded with silence when the buffer runs short.
func (f *FIFO) Read(n int) []float32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]float32, n)
	if !f.playing {
		if len(f.buf) < f.prefill {
			return out
		}
		f.playing = true
	}
	if len(f.buf) < n {
		copy(out, f.buf)
		f.buf = f.buf[:0]
		f.playing = false
		f.underruns++
		return out
	}
	copy(out, f.buf[:n])
	f.buf = append(f.buf[:0], f.buf[n:]...)
	return out
}

// Stats reports underruns and dropped samples since the FIFO was made.
func (f *FIFO) Stats() (underruns, dropped int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.underruns, f.dropped
}
```

- [ ] **Step 4: Run the tests**

Run: `cd src && gofmt -l pkg/callaudio; GOTOOLCHAIN=auto go vet ./pkg/callaudio/ && GOTOOLCHAIN=auto go test ./pkg/callaudio/ -race -v`
Expected: no gofmt output; vet clean; all five tests PASS. If a level or crossing bound fails
by a hair, print the measured value and check the filter before widening a bound. The 12 kHz
alias bound stays at 0.05.

- [ ] **Step 5: Commit**

```bash
git add src/pkg/callaudio
git commit -m "feat(calls): PCM resampler 16/48 kHz, re-chunker and jitter FIFO"
```

---
### Task 5: Stereo recorder, retention and the signed recording route (B6)

Needs Task 1's tag in `go.mod`. The recorder writes one stereo Opus stream at 16 kHz, 48 kbps in
total (24 kbps a channel), in 60 ms packets: the WhatsApp leg's own frame. The Ogg container
comes from pion's `oggwriter`. It writes one page per packet straight to the file, computes
granule positions from each packet's TOC, and on `Close` rewrites the last page with the
end-of-stream flag. A crash therefore keeps every page written before it.

**Files:**
- Modify: `src/go.mod`, `src/go.sum` (add `github.com/pion/webrtc/v4 v4.2.23`; `github.com/pion/rtp` comes with it)
- Create: `src/pkg/callrec/recorder.go`, `src/pkg/callrec/retention.go`, `src/pkg/callrec/url.go`
- Create: `src/pkg/callrec/recorder_test.go`, `src/pkg/callrec/retention_test.go`, `src/pkg/callrec/url_test.go`
- Create: `src/cmd/rest_calls.go`, `src/cmd/rest_calls_test.go`
- Modify: `src/cmd/rest.go` (one line after `mountStatics(...)`)
- Create: `src/infrastructure/whatsapp/calls_services.go`
- Modify: `src/cmd/root.go` (one line after `whatsapp.StartDurableWebhooks`)
- Modify: `src/config/calls.go`, `src/config/calls_test.go`, `src/cmd/calls_flags.go`, `src/.env.example`

**Interfaces:**
- Consumes: `opus.NewEncoderChannels` (Task 1), `staticurl.Sign/Verify` (D-13), `safego.Loop`,
  `newBasicAuthMiddleware(accounts)` (in `cmd/rest.go`).
- Produces (used by Tasks 7 and 9):

```go
package callrec
const SampleRate = 16000; const FrameSamples = 960; const Bitrate = 48000; const Format = "ogg_opus_stereo"
type Encoder interface{ Encode(pcm []float32) ([]byte, error) }
type Info struct{ Path string; Bytes int64; Duration time.Duration }
func ValidCallID(id string) bool
func Create(path string) (*Recorder, error)            // libopus stereo encoder
func New(path string, enc Encoder) (*Recorder, error)
func (r *Recorder) Write(left, right []float32) error   // left = agent, right = customer, FrameSamples each
func (r *Recorder) Close() (Info, error)                // idempotent; an empty recording is removed (Bytes 0)
func Cleanup(dir string, maxAge time.Duration, now time.Time) (int, error)
func StartRetention(ctx context.Context, dir string, maxAge time.Duration)
func URLPath(basePath, callID string, secret []byte, expires time.Time) string
func Verify(secret []byte, callID, exp, sig string, now time.Time) bool
```

- Produces: `whatsapp.StartCallsServices(ctx context.Context)`,
  `config.WhatsappCallsRecordingURLTTLMinutes = 1440` and
  `config.WhatsappCallsRecordingRetentionDays = 7`.

- [ ] **Step 1: Add pion**

```bash
cd ~/Documents/whatsapp-gateway-calls/src
GOTOOLCHAIN=auto go get github.com/pion/webrtc/v4@v4.2.23 github.com/pion/rtp
```

`go mod tidy` runs in Step 6, once code imports the packages.

- [ ] **Step 2: Write the failing recorder, retention and URL tests**

`src/pkg/callrec/recorder_test.go`:

```go
package callrec

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/purpshell/meowcaller/opus"
)

// tocEncoder returns a minimal valid Opus packet: TOC 0x5C is SILK wideband, 60 ms, stereo,
// one frame. It keeps the last frame it got.
type tocEncoder struct{ last []float32 }

func (e *tocEncoder) Encode(pcm []float32) ([]byte, error) {
	if len(pcm) != 2*FrameSamples {
		return nil, fmt.Errorf("got %d values, want %d", len(pcm), 2*FrameSamples)
	}
	e.last = append([]float32(nil), pcm...)
	return []byte{0x5C, 0x01, 0x02}, nil
}

type oggPage struct {
	headerType byte
	granule    uint64
	body       []byte
}

func readPages(t *testing.T, path string) []oggPage {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var pages []oggPage
	for len(data) >= 27 {
		if string(data[:4]) != "OggS" {
			t.Fatalf("no OggS capture pattern at a page start")
		}
		nseg := int(data[26])
		size := 0
		for _, s := range data[27 : 27+nseg] {
			size += int(s)
		}
		pages = append(pages, oggPage{
			headerType: data[5],
			granule:    binary.LittleEndian.Uint64(data[6:14]),
			body:       data[27+nseg : 27+nseg+size],
		})
		data = data[27+nseg+size:]
	}
	return pages
}

func TestRecorderWritesStereoOggOpus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "CALL1.ogg")
	r, err := New(path, &tocEncoder{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := r.Write(make([]float32, FrameSamples), make([]float32, FrameSamples)); err != nil {
			t.Fatal(err)
		}
	}
	info, err := r.Close()
	if err != nil {
		t.Fatal(err)
	}
	pages := readPages(t, path)
	if len(pages) != 7 {
		t.Fatalf("%d pages, want 2 headers + 5 audio", len(pages))
	}
	head := pages[0].body
	if string(head[:8]) != "OpusHead" || head[9] != 2 || binary.LittleEndian.Uint32(head[12:16]) != SampleRate {
		t.Fatalf("OpusHead: channels %d, rate %d", head[9], binary.LittleEndian.Uint32(head[12:16]))
	}
	if string(pages[1].body[:8]) != "OpusTags" {
		t.Fatal("second page must be OpusTags")
	}
	last := pages[len(pages)-1]
	if last.headerType&0x04 == 0 {
		t.Fatal("last page must carry the end-of-stream flag")
	}
	if last.granule != 5*2880 {
		t.Fatalf("granule %d, want %d (5 x 60 ms at 48 kHz)", last.granule, 5*2880)
	}
	fi, _ := os.Stat(path)
	if info.Bytes != fi.Size() || info.Duration != 300*time.Millisecond || info.Path != path {
		t.Fatalf("info %+v, file %d bytes", info, fi.Size())
	}
}

func TestRecorderKeepsPagesWrittenBeforeACrash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "CALL2.ogg")
	r, err := New(path, &tocEncoder{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := r.Write(nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	// No Close: the process died. Every packet is already a page on disk.
	if pages := readPages(t, path); len(pages) != 5 {
		t.Fatalf("%d pages on disk, want 2 headers + 3 audio", len(pages))
	}
}

func TestRecorderPutsAgentLeftAndCustomerRight(t *testing.T) {
	enc := &tocEncoder{}
	r, err := New(filepath.Join(t.TempDir(), "CALL3.ogg"), enc)
	if err != nil {
		t.Fatal(err)
	}
	left := make([]float32, FrameSamples)
	left[0], left[1] = 0.5, 2.0
	right := []float32{-0.25} // short: padded with silence
	if err := r.Write(left, right); err != nil {
		t.Fatal(err)
	}
	if enc.last[0] != 0.5 || enc.last[1] != -0.25 || enc.last[2] != 1 || enc.last[3] != 0 {
		t.Fatalf("interleaved %v, want L0=0.5 R0=-0.25 L1=1 (clamped) R1=0", enc.last[:4])
	}
}

func TestRecorderEmptyIsRemovedAndCloseIsFinal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "CALL4.ogg")
	r, err := New(path, &tocEncoder{})
	if err != nil {
		t.Fatal(err)
	}
	info, err := r.Close()
	if err != nil || info.Bytes != 0 {
		t.Fatalf("empty close: %+v, %v", info, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("an empty recording must be removed")
	}
	if _, err := r.Close(); err != nil {
		t.Fatal("Close must be idempotent")
	}
	if err := r.Write(nil, nil); err == nil {
		t.Fatal("Write after Close must fail")
	}
}

func TestCreateEncodesRealStereoOpus(t *testing.T) {
	if !opus.Available {
		t.Skip("libopus not linked (CGO_ENABLED=0)")
	}
	path := filepath.Join(t.TempDir(), "CALL5.ogg")
	r, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	left, right := make([]float32, FrameSamples), make([]float32, FrameSamples)
	for i := range left {
		left[i] = float32(0.3 * math.Sin(2*math.Pi*440*float64(i)/SampleRate))
		right[i] = float32(0.3 * math.Sin(2*math.Pi*1300*float64(i)/SampleRate))
	}
	for i := 0; i < 10; i++ {
		if err := r.Write(left, right); err != nil {
			t.Fatal(err)
		}
	}
	info, err := r.Close()
	if err != nil {
		t.Fatal(err)
	}
	pages := readPages(t, path)
	if len(pages) != 12 || pages[2].body[0]&0x04 == 0 {
		t.Fatalf("%d pages; first audio TOC 0x%02x must be stereo", len(pages), pages[2].body[0])
	}
	if info.Duration != 600*time.Millisecond {
		t.Fatalf("duration %v, want 600ms", info.Duration)
	}
}

func TestValidCallID(t *testing.T) {
	for _, ok := range []string{"A1B2C3D4E5F60718293A4B5C6D7E8F90", "abc_DEF-1"} {
		if !ValidCallID(ok) {
			t.Errorf("%q must be valid", ok)
		}
	}
	for _, bad := range []string{"", "../x", "a/b", "a.ogg", "a b"} {
		if ValidCallID(bad) {
			t.Errorf("%q must be invalid", bad)
		}
	}
}
```

`src/pkg/callrec/retention_test.go`:

```go
package callrec

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanupRemovesOldRecordingsOnly(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	old := now.Add(-8 * 24 * time.Hour)
	for name, mtime := range map[string]time.Time{"old.ogg": old, "old.wav": old, "new.ogg": now, "notes.txt": old} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	n, err := Cleanup(dir, 7*24*time.Hour, now)
	if err != nil || n != 2 {
		t.Fatalf("removed %d (%v), want 2", n, err)
	}
	for name, want := range map[string]bool{"old.ogg": false, "old.wav": false, "new.ogg": true, "notes.txt": true} {
		_, err := os.Stat(filepath.Join(dir, name))
		if (err == nil) != want {
			t.Errorf("%s exists=%v, want %v", name, err == nil, want)
		}
	}
	if n, err := Cleanup(filepath.Join(dir, "missing"), time.Hour, now); n != 0 || err != nil {
		t.Fatalf("missing dir: %d, %v", n, err)
	}
}
```

`src/pkg/callrec/url_test.go`:

```go
package callrec

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestURLPathSignsOneCall(t *testing.T) {
	secret := []byte("s3cret")
	now := time.Now()
	link := URLPath("/gowa", "CALL1", secret, now.Add(time.Hour))
	if !strings.HasPrefix(link, "/gowa/call/CALL1/recording?exp=") {
		t.Fatalf("link %q", link)
	}
	u, _ := url.Parse(link)
	exp, sig := u.Query().Get("exp"), u.Query().Get("sig")
	if !Verify(secret, "CALL1", exp, sig, now) {
		t.Fatal("a fresh signature must verify")
	}
	if Verify(secret, "CALL2", exp, sig, now) {
		t.Fatal("a signature opens one call only")
	}
	if Verify(secret, "CALL1", exp, sig, now.Add(2*time.Hour)) {
		t.Fatal("an expired signature must fail")
	}
	if Verify([]byte("other"), "CALL1", exp, sig, now) {
		t.Fatal("another secret must fail")
	}
	if got := URLPath("", "CALL1", nil, now); got != "/call/CALL1/recording" {
		t.Fatalf("without a secret: %q", got)
	}
}
```

- [ ] **Step 3: Run them to see them fail**

Run: `cd src && GOTOOLCHAIN=auto go test ./pkg/callrec/`
Expected: FAIL to build (`undefined: New`).

- [ ] **Step 4: Implement the recorder, retention and URL**

`src/pkg/callrec/recorder.go`:

```go
// Package callrec writes the stereo recording of a call, cleans old recordings and signs their
// download link (fork: elphant, voice calls stage 2b, B6).
package callrec

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"sync"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4/pkg/media/oggwriter"
	"github.com/purpshell/meowcaller/opus"
)

const (
	SampleRate   = 16000             // the WhatsApp leg's rate
	FrameSamples = 960               // per channel: 60 ms at 16 kHz
	Bitrate      = 48000             // total: 24 kbps a channel
	Format       = "ogg_opus_stereo" // recording.format in call.ended
)

var validCallID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// ValidCallID reports whether id is safe as a file name and a URL path segment.
func ValidCallID(id string) bool { return validCallID.MatchString(id) }

// Encoder encodes one interleaved stereo frame into one Opus packet.
type Encoder interface {
	Encode(pcm []float32) ([]byte, error)
}

// Info describes a closed recording.
type Info struct {
	Path     string
	Bytes    int64
	Duration time.Duration
}

var errClosed = errors.New("callrec: recorder closed")

// Recorder writes agent (left) and customer (right) into one Ogg Opus file as the call runs.
type Recorder struct {
	mu     sync.Mutex
	ow     *oggwriter.OggWriter
	enc    Encoder
	path   string
	pcm    []float32
	frames int
	seq    uint16
	ts     uint32
	closed bool
	info   Info
}

// Create starts a recording at path with libopus in forced stereo.
func Create(path string) (*Recorder, error) {
	enc, err := opus.NewEncoderChannels(SampleRate, 2, Bitrate, 5, false)
	if err != nil {
		return nil, fmt.Errorf("callrec: stereo encoder: %w", err)
	}
	return New(path, enc)
}

// New starts a recording at path with enc.
func New(path string, enc Encoder) (*Recorder, error) {
	ow, err := oggwriter.New(path, SampleRate, 2)
	if err != nil {
		return nil, err
	}
	return &Recorder{ow: ow, enc: enc, path: path, pcm: make([]float32, 2*FrameSamples)}, nil
}

// Write encodes one 60 ms frame (left = agent, right = customer) and appends it as its own Ogg
// page. A short or nil channel is padded with silence; samples are clamped to [-1, 1].
func (r *Recorder) Write(left, right []float32) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errClosed
	}
	for i := 0; i < FrameSamples; i++ {
		r.pcm[2*i] = sampleAt(left, i)
		r.pcm[2*i+1] = sampleAt(right, i)
	}
	pkt, err := r.enc.Encode(r.pcm)
	if err != nil {
		return err
	}
	r.seq++
	r.ts += FrameSamples * 3 // the Opus RTP clock is 48 kHz
	if err := r.ow.WriteRTP(&rtp.Packet{
		Header:  rtp.Header{Version: 2, PayloadType: 111, SequenceNumber: r.seq, Timestamp: r.ts},
		Payload: pkt,
	}); err != nil {
		return err
	}
	r.frames++
	return nil
}

func sampleAt(s []float32, i int) float32 {
	if i >= len(s) {
		return 0
	}
	switch v := s[i]; {
	case v > 1:
		return 1
	case v < -1:
		return -1
	default:
		return v
	}
}

// Close marks the end of stream and reports the file. A recording without frames is removed
// and reported with zero Bytes. Safe to call more than once.
func (r *Recorder) Close() (Info, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return r.info, nil
	}
	r.closed = true
	err := r.ow.Close()
	r.info = Info{Path: r.path, Duration: time.Duration(r.frames) * 60 * time.Millisecond}
	if r.frames == 0 {
		_ = os.Remove(r.path)
		return r.info, nil
	}
	if fi, statErr := os.Stat(r.path); statErr == nil {
		r.info.Bytes = fi.Size()
	}
	return r.info, err
}
```

`src/pkg/callrec/retention.go`:

```go
package callrec

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/safego"
	"github.com/sirupsen/logrus"
)

// Cleanup deletes the recordings in dir (.ogg, plus stage 2a's .wav) last written before
// now - maxAge. A recording in progress is written every 60 ms, so it is never old.
func Cleanup(dir string, maxAge time.Duration, now time.Time) (int, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, e := range entries {
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if e.IsDir() || (ext != ".ogg" && ext != ".wav") {
			continue
		}
		fi, err := e.Info()
		if err != nil || now.Sub(fi.ModTime()) <= maxAge {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err == nil {
			removed++
		}
	}
	return removed, nil
}

// StartRetention runs Cleanup at once and then every hour until ctx ends.
func StartRetention(ctx context.Context, dir string, maxAge time.Duration) {
	if maxAge <= 0 {
		return
	}
	safego.Loop("calls-recording-retention", func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			if n, err := Cleanup(dir, maxAge, time.Now()); err != nil {
				logrus.Warnf("[CALLS] recording retention: %v", err)
			} else if n > 0 {
				logrus.Infof("[CALLS] removed %d recording(s) past retention", n)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})
}
```

`src/pkg/callrec/url.go`:

```go
package callrec

import (
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/staticurl"
)

// resource is what a recording link signs. The "call-recording:" prefix keeps it apart from
// every path a /statics signature can open.
func resource(callID string) string { return "call-recording:" + callID }

// URLPath is the recording's path below the server root (with APP_BASE_PATH), signed until
// expires when secret is set. Without a secret the route needs Basic Auth.
func URLPath(basePath, callID string, secret []byte, expires time.Time) string {
	p := basePath + "/call/" + callID + "/recording"
	if len(secret) == 0 {
		return p
	}
	return p + "?" + staticurl.Sign(secret, resource(callID), expires)
}

// Verify reports whether exp and sig open the recording of callID at now.
func Verify(secret []byte, callID, exp, sig string, now time.Time) bool {
	return staticurl.Verify(secret, resource(callID), exp, sig, now)
}
```

- [ ] **Step 5: Write the failing route test**

`src/cmd/rest_calls_test.go` (pins Review Focus 4):

```go
package cmd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/callrec"
	"github.com/gofiber/fiber/v3"
)

func recordingsApp(t *testing.T, accounts map[string]string, secret []byte) *fiber.App {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ABC123.ogg"), []byte("OggS-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "OTHER.ogg"), []byte("other"), 0o644); err != nil {
		t.Fatal(err)
	}
	app := fiber.New()
	mountCallRecordings(app, "", dir, accounts, secret)
	return app
}

func TestRecordingSignedURLOpensWithoutCredentials(t *testing.T) {
	secret := []byte("s3cret")
	app := recordingsApp(t, map[string]string{"admin": "pw"}, secret)
	link := callrec.URLPath("", "ABC123", secret, time.Now().Add(time.Hour))
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, link, nil))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "audio/ogg" || string(body) != "OggS-bytes" {
		t.Fatalf("status %d, type %q, body %q", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
}

func TestRecordingRouteRejectsTamperedExpiredAndTraversal(t *testing.T) {
	secret := []byte("s3cret")
	app := recordingsApp(t, map[string]string{"admin": "pw"}, secret)
	now := time.Now()

	expired := callrec.URLPath("", "ABC123", secret, now.Add(-time.Minute))
	if got := statusOf(t, app, expired, "", ""); got != http.StatusUnauthorized {
		t.Fatalf("expired link: %d, want 401", got)
	}
	tampered := callrec.URLPath("", "ABC123", secret, now.Add(time.Hour)) + "0"
	if got := statusOf(t, app, tampered, "", ""); got != http.StatusUnauthorized {
		t.Fatalf("tampered link: %d, want 401", got)
	}
	other := callrec.URLPath("", "OTHER", secret, now.Add(time.Hour))
	borrowed := "/call/ABC123/recording" + other[len("/call/OTHER/recording"):]
	if got := statusOf(t, app, borrowed, "", ""); got != http.StatusUnauthorized {
		t.Fatalf("another call's signature: %d, want 401", got)
	}
	for _, path := range []string{"/call/..%2F..%2Fetc%2Fpasswd/recording", "/call/a.b/recording", "/call/%2E%2E/recording"} {
		if got := statusOf(t, app, path, "admin", "pw"); got == http.StatusOK {
			t.Fatalf("%s served a file", path)
		}
	}
	if got := statusOf(t, app, "/call/ABC123/recording", "admin", "pw"); got != http.StatusOK {
		t.Fatalf("Basic Auth: %d, want 200", got)
	}
	if got := statusOf(t, app, "/call/GONE/recording", "admin", "pw"); got != http.StatusGone {
		t.Fatalf("missing recording: %d, want 410", got)
	}
}
```

(`statusOf` already exists in `rest_statics_test.go`.)

- [ ] **Step 6: Implement the route, the start-up hook and the settings**

`src/cmd/rest_calls.go`:

```go
package cmd

import (
	"os"
	"path/filepath"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/callrec"
	"github.com/gofiber/fiber/v3"
)

// Fork (elphant): GET <base>/call/:call_id/recording serves a call's stereo recording (voice
// calls stage 2b, B6). A valid signature (exp, sig) from the call.ended webhook opens it without
// credentials; otherwise it needs Basic Auth when Basic Auth is configured. It is mounted before
// the request timeout, like /statics, so a long download is not cut.
func mountCallRecordings(app *fiber.App, basePath, dir string, accounts map[string]string, secret []byte) {
	var basicAuth fiber.Handler
	if len(accounts) > 0 {
		basicAuth = newBasicAuthMiddleware(accounts)
	}
	gate := func(c fiber.Ctx) error {
		id := c.Params("call_id")
		if !callrec.ValidCallID(id) {
			return c.Status(fiber.StatusBadRequest).SendString("invalid call id")
		}
		if basicAuth == nil || callrec.Verify(secret, id, c.Query("exp"), c.Query("sig"), time.Now()) {
			return c.Next()
		}
		return basicAuth(c)
	}
	app.Get(basePath+"/call/:call_id/recording", gate, func(c fiber.Ctx) error {
		f, err := os.Open(filepath.Join(dir, c.Params("call_id")+".ogg"))
		if err != nil {
			return c.Status(fiber.StatusGone).SendString("recording expired or not found")
		}
		fi, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return c.Status(fiber.StatusGone).SendString("recording expired or not found")
		}
		c.Set(fiber.HeaderContentType, "audio/ogg")
		return c.SendStream(f, int(fi.Size()))
	})
}
```

In `src/cmd/rest.go`, right after the `mountStatics(...)` line:

```go
	mountCallRecordings(app, config.AppBasePath, config.PathCalls, accounts, []byte(config.AppStaticsSecret)) // Fork (elphant): signed call recordings (voice calls 2b).
```

`src/infrastructure/whatsapp/calls_services.go`:

```go
package whatsapp

import (
	"context"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/callrec"
)

// Fork (elphant): what voice calls need besides the clients (stage 2b).

// StartCallsServices runs once at start, before the first client is created. With calls off
// it does nothing.
func StartCallsServices(ctx context.Context) {
	if !config.WhatsappCallsEnabled {
		return
	}
	retention := time.Duration(config.WhatsappCallsRecordingRetentionDays) * 24 * time.Hour
	callrec.StartRetention(ctx, config.PathCalls, retention)
}
```

In `src/cmd/root.go`, right after the `whatsapp.StartDurableWebhooks` block:

```go
	whatsapp.StartCallsServices(ctx) // Fork (elphant): voice calls 2b (recording retention, WebRTC port); before the first client.
```

In `src/config/calls.go`, add to the `var` block:

```go
	WhatsappCallsRecordingURLTTLMinutes = 1440 // stage 2b: validity of the signed recording link in call.ended
	WhatsappCallsRecordingRetentionDays = 7    // stage 2b: recordings older than this are deleted (0: keep)
```

In `src/config/calls_test.go`, extend `TestCallsStage2bDefaults`:

```go
	if WhatsappCallsRecordingURLTTLMinutes != 1440 || WhatsappCallsRecordingRetentionDays != 7 {
		t.Fatalf("recording defaults %d min / %d days, want 1440 / 7", WhatsappCallsRecordingURLTTLMinutes, WhatsappCallsRecordingRetentionDays)
	}
```

In `src/cmd/calls_flags.go`, add to `initCallsEnv`:

```go
	if viper.IsSet("whatsapp_calls_recording_url_ttl_minutes") {
		config.WhatsappCallsRecordingURLTTLMinutes = viper.GetInt("whatsapp_calls_recording_url_ttl_minutes")
	}
	if viper.IsSet("whatsapp_calls_recording_retention_days") {
		config.WhatsappCallsRecordingRetentionDays = viper.GetInt("whatsapp_calls_recording_retention_days")
	}
```

and to `initCallsFlags`:

```go
	flags.IntVarP(&config.WhatsappCallsRecordingURLTTLMinutes, "calls-recording-url-ttl-minutes", "", config.WhatsappCallsRecordingURLTTLMinutes,
		`validity of the signed recording link --calls-recording-url-ttl-minutes <n> | example: --calls-recording-url-ttl-minutes=1440`)
	flags.IntVarP(&config.WhatsappCallsRecordingRetentionDays, "calls-recording-retention-days", "", config.WhatsappCallsRecordingRetentionDays,
		`delete call recordings older than this (0: keep) --calls-recording-retention-days <n> | example: --calls-recording-retention-days=7`)
```

In `src/.env.example`, after `WHATSAPP_CALLS_MAX_DURATION_MINUTES=120`:

```
# Stereo call recordings (agent left, customer right) in storages/calls; the link in call.ended is
# signed with APP_STATICS_SECRET and also opens with Basic Auth
WHATSAPP_CALLS_RECORDING_URL_TTL_MINUTES=1440
WHATSAPP_CALLS_RECORDING_RETENTION_DAYS=7
```

Then tidy:

```bash
cd ~/Documents/whatsapp-gateway-calls/src && GOTOOLCHAIN=auto go mod tidy
```

- [ ] **Step 7: Run the tests**

Run: `cd src && gofmt -l pkg cmd config infrastructure; GOTOOLCHAIN=auto go vet ./pkg/callrec/ ./cmd/ && GOTOOLCHAIN=auto go test ./pkg/callrec/ ./cmd/ ./config/ -race -v -run 'Recorder|Create|ValidCallID|Cleanup|URLPath|Recording|Stage2b|InitCallsEnv|DeviceGroup'`
Expected: no gofmt output; vet clean; all PASS. `TestCreateEncodesRealStereoOpus` runs, because
cgo builds link libopus. `go.mod` lists `github.com/pion/webrtc/v4 v4.2.23`.

- [ ] **Step 8: Commit**

```bash
git add src/go.mod src/go.sum src/pkg/callrec src/cmd/rest_calls.go src/cmd/rest_calls_test.go src/cmd/rest.go \
  src/cmd/root.go src/cmd/calls_flags.go src/infrastructure/whatsapp/calls_services.go \
  src/config/calls.go src/config/calls_test.go src/.env.example
git commit -m "feat(calls): stereo Ogg Opus recorder, retention and signed recording route"
```

---
### Task 6: The browser leg on pion (B1, B2)

One `Engine` per process holds the UDP socket on `WHATSAPP_CALLS_RTC_PORT`, the ICE UDP mux and
a pion API configured as Meta's side of Cloud Calling:
- ICE-lite, so it is ICE-controlled;
- DTLS server when answering (`a=setup:passive`); offers carry pion's `actpass`;
- host candidates rewritten to `WHATSAPP_CALLS_RTC_PUBLIC_IP`;
- Opus 48 kHz only, payload type 111.

A `Leg` is one `RTCPeerConnection` with one local Opus track (one SSRC). It encodes and decodes
mono Opus at 48 kHz in 20 ms frames with the fork's binding. The libopus mono decoder downmixes a
stereo stream from the browser. Gathering always completes before the SDP is handed out
(non-trickle), and every SDP passes through `NormalizeSDP` (CRLF).

pion does not write `a=ptime`; GOWA sends 20 ms frames, which is the browser's default.

**Files:**
- Create: `src/pkg/callrtc/rtc.go`, `src/pkg/callrtc/sdp.go`, `src/pkg/callrtc/rtc_test.go`
- Modify: `src/go.mod`, `src/go.sum` (`go mod tidy`; when this task runs before Task 5, first run `GOTOOLCHAIN=auto go get github.com/pion/webrtc/v4@v4.2.23`)

**Interfaces:**
- Consumes: `opus.NewEncoder(48000, ...)`, `opus.NewDecoder(48000)`, `safego.Go`.
- Produces (used by Task 7):

```go
package callrtc
const SampleRate = 48000; const FrameSamples = 960
type Config struct{ Port int; PublicIP, ListenIP string; IncludeLoopback bool }
func NewEngine(cfg Config) (*Engine, error)
func (e *Engine) Addr() net.Addr
func (e *Engine) Close() error
func (e *Engine) NewInbound() (*Leg, error)            // GOWA offers (LocalSDP is the offer)
func (e *Engine) NewOutbound(offer string) (*Leg, error) // GOWA answers (LocalSDP is the answer)
func (l *Leg) LocalSDP() string
func (l *Leg) SetAnswer(sdp string) error
func (l *Leg) WriteFrame(pcm []float32) error          // 960 samples, 48 kHz mono
func (l *Leg) OnFrame(fn func(pcm []float32))          // decoded browser audio, 48 kHz mono
func (l *Leg) OnConnected(fn func(up bool))            // replays the current state when it is up
func (l *Leg) Close() error
func NormalizeSDP(s string) string
```

- [ ] **Step 1: Write the failing tests**

`src/pkg/callrtc/rtc_test.go`:

```go
package callrtc

import (
	"math"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/purpshell/meowcaller/opus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func requireOpus(t *testing.T) {
	t.Helper()
	if !opus.Available {
		t.Skip("libopus not linked (CGO_ENABLED=0)")
	}
}

func newTestEngine(t *testing.T, publicIP string) *Engine {
	t.Helper()
	e, err := NewEngine(Config{Port: 0, ListenIP: "127.0.0.1", PublicIP: publicIP, IncludeLoopback: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = e.Close() })
	return e
}

func sineFrame(hz float64) []float32 {
	out := make([]float32, FrameSamples)
	for i := range out {
		out[i] = float32(0.4 * math.Sin(2*math.Pi*hz*float64(i)/SampleRate))
	}
	return out
}

func rms(p []float32) float64 {
	var s float64
	for _, v := range p {
		s += float64(v) * float64(v)
	}
	return math.Sqrt(s / float64(len(p)))
}

// browser is a full (not lite) pion peer playing the agent's browser.
type browser struct {
	pc    *webrtc.PeerConnection
	track *webrtc.TrackLocalStaticSample
	got   atomic.Int64 // RTP packets received from GOWA
}

func newBrowser(t *testing.T) *browser {
	t.Helper()
	se := webrtc.SettingEngine{}
	se.SetIncludeLoopbackCandidate(true)
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	me := &webrtc.MediaEngine{}
	require.NoError(t, me.RegisterDefaultCodecs())
	pc, err := webrtc.NewAPI(webrtc.WithMediaEngine(me), webrtc.WithSettingEngine(se)).NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	track, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, "mic", "browser")
	require.NoError(t, err)
	_, err = pc.AddTrack(track)
	require.NoError(t, err)
	b := &browser{pc: pc, track: track}
	pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		go func() {
			for {
				if _, _, err := tr.ReadRTP(); err != nil {
					return
				}
				b.got.Add(1)
			}
		}()
	})
	t.Cleanup(func() { _ = pc.Close() })
	return b
}

// gather sets the local description and waits for ICE gathering: the browser side of non-trickle.
func gather(t *testing.T, pc *webrtc.PeerConnection, desc webrtc.SessionDescription) string {
	t.Helper()
	done := webrtc.GatheringCompletePromise(pc)
	require.NoError(t, pc.SetLocalDescription(desc))
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("browser gathering timed out")
	}
	return pc.LocalDescription().SDP
}

// speak sends a 440 Hz tone from the browser every 20 ms until the test ends.
func (b *browser) speak(t *testing.T) {
	enc, err := opus.NewEncoder(SampleRate, 32000, 5, false)
	require.NoError(t, err)
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				pkt, err := enc.Encode(sineFrame(440))
				if err == nil {
					_ = b.track.WriteSample(media.Sample{Data: pkt, Duration: 20 * time.Millisecond})
				}
			}
		}
	}()
}

func ssrcs(sdp string) map[string]bool {
	out := map[string]bool{}
	for _, line := range strings.Split(sdp, "\r\n") {
		if strings.HasPrefix(line, "a=ssrc:") {
			out[strings.Fields(strings.TrimPrefix(line, "a=ssrc:"))[0]] = true
		}
	}
	return out
}

func assertGOWASDP(t *testing.T, sdp, setup, ip string) {
	t.Helper()
	assert.NotContains(t, strings.ReplaceAll(sdp, "\r\n", ""), "\n", "every line ends in CRLF")
	assert.True(t, strings.HasSuffix(sdp, "\r\n"))
	assert.Contains(t, sdp, "a=ice-lite")
	assert.Contains(t, sdp, "a=setup:"+setup)
	assert.Contains(t, sdp, "opus/48000/2")
	assert.Contains(t, sdp, "a=candidate:")
	assert.Contains(t, sdp, " "+ip+" ", "host candidate on the public address")
	assert.Len(t, ssrcs(sdp), 1, "one audio SSRC")
}

func waitUp(t *testing.T, leg *Leg) {
	t.Helper()
	up := make(chan struct{}, 1)
	leg.OnConnected(func(ok bool) {
		if ok {
			select {
			case up <- struct{}{}:
			default:
			}
		}
	})
	select {
	case <-up:
	case <-time.After(10 * time.Second):
		t.Fatal("ICE did not connect")
	}
}

// exchangeAudio sends GOWA's tone to the browser and counts the browser's tone at GOWA.
func exchangeAudio(t *testing.T, leg *Leg, b *browser) {
	t.Helper()
	var heard atomic.Int64
	leg.OnFrame(func(pcm []float32) {
		if rms(pcm) > 0.01 {
			heard.Add(1)
		}
	})
	b.speak(t)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && (heard.Load() < 10 || b.got.Load() < 10) {
		require.NoError(t, leg.WriteFrame(sineFrame(880)))
		time.Sleep(20 * time.Millisecond)
	}
	assert.GreaterOrEqual(t, heard.Load(), int64(10), "decoded browser frames at GOWA")
	assert.GreaterOrEqual(t, b.got.Load(), int64(10), "RTP packets from GOWA at the browser")
}

func TestInboundLegConnectsAndCarriesAudio(t *testing.T) {
	requireOpus(t)
	eng := newTestEngine(t, "127.0.0.1")
	leg, err := eng.NewInbound()
	require.NoError(t, err)
	t.Cleanup(func() { _ = leg.Close() })
	assertGOWASDP(t, leg.LocalSDP(), "actpass", "127.0.0.1")

	b := newBrowser(t)
	require.NoError(t, b.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: leg.LocalSDP()}))
	answer, err := b.pc.CreateAnswer(nil)
	require.NoError(t, err)
	answerSDP := gather(t, b.pc, answer)
	assert.Contains(t, answerSDP, "a=setup:active", "the browser is the DTLS client")
	require.NoError(t, leg.SetAnswer(answerSDP))

	waitUp(t, leg)
	exchangeAudio(t, leg, b)
}

// Review Focus 5: an LF-only browser offer still works.
func TestOutboundLegAnswersBrowserOffer(t *testing.T) {
	requireOpus(t)
	eng := newTestEngine(t, "127.0.0.1")
	b := newBrowser(t)
	offer, err := b.pc.CreateOffer(nil)
	require.NoError(t, err)
	offerSDP := gather(t, b.pc, offer)

	leg, err := eng.NewOutbound(strings.ReplaceAll(offerSDP, "\r\n", "\n"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = leg.Close() })
	assertGOWASDP(t, leg.LocalSDP(), "passive", "127.0.0.1")

	require.NoError(t, b.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: leg.LocalSDP()}))
	waitUp(t, leg)
	exchangeAudio(t, leg, b)
}

// Review Focus 5: an offer without Opus is refused before anything else happens.
func TestOutboundOfferWithoutOpusIsRefused(t *testing.T) {
	requireOpus(t)
	eng := newTestEngine(t, "127.0.0.1")
	pcmu := "v=0\r\no=- 1 1 IN IP4 0.0.0.0\r\ns=-\r\nt=0 0\r\nm=audio 9 UDP/TLS/RTP/SAVPF 0\r\na=rtpmap:0 PCMU/8000\r\n"
	_, err := eng.NewOutbound(pcmu)
	assert.ErrorIs(t, err, errNoOpus)
	_, err = eng.NewOutbound("not an sdp")
	assert.Error(t, err)
}

func TestPublicIPReplacesHostCandidates(t *testing.T) {
	requireOpus(t)
	eng := newTestEngine(t, "203.0.113.7")
	leg, err := eng.NewInbound()
	require.NoError(t, err)
	t.Cleanup(func() { _ = leg.Close() })
	assert.Contains(t, leg.LocalSDP(), " 203.0.113.7 ")
	assert.NotContains(t, leg.LocalSDP(), " 127.0.0.1 ")
}

func TestEngineRefusesABusyPort(t *testing.T) {
	eng := newTestEngine(t, "")
	port := eng.Addr().(*net.UDPAddr).Port
	_, err := NewEngine(Config{Port: port, ListenIP: "127.0.0.1"})
	assert.Error(t, err)
}

func TestNormalizeSDP(t *testing.T) {
	assert.Equal(t, "v=0\r\na=x\r\n", NormalizeSDP("v=0\na=x"))
	assert.Equal(t, "v=0\r\na=x\r\n", NormalizeSDP("v=0\r\na=x\r\n"))
	assert.Equal(t, "v=0\r\na=x\r\n", NormalizeSDP("v=0\r\na=x\n\n"))
	assert.Equal(t, "", NormalizeSDP(""))
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `cd src && GOTOOLCHAIN=auto go test ./pkg/callrtc/`
Expected: FAIL to build (`undefined: NewEngine`).

- [ ] **Step 3: Implement**

`src/pkg/callrtc/sdp.go`:

```go
package callrtc

import "strings"

// NormalizeSDP gives every line a CRLF ending, as Meta's and the browsers' SDP has; some
// clients hand over LF-only text, and the CRM normalises the same way.
func NormalizeSDP(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return ""
	}
	return strings.ReplaceAll(s, "\n", "\r\n") + "\r\n"
}
```

`src/pkg/callrtc/rtc.go`:

```go
// Package callrtc is the browser leg of a WhatsApp call (fork: elphant, voice calls stage 2b,
// B1/B2): pion WebRTC as an ICE-lite, DTLS-server endpoint on one UDP port, carrying mono Opus
// at 48 kHz in 20 ms frames, with non-trickle SDP.
package callrtc

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/safego"
	"github.com/pion/logging"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/purpshell/meowcaller/opus"
)

const (
	SampleRate    = 48000
	FrameSamples  = 960 // 20 ms at 48 kHz
	opusBitrate   = 32000
	gatherTimeout = 5 * time.Second
)

var errNoOpus = errors.New("callrtc: the offer has no Opus 48 kHz audio")

var opusCapability = webrtc.RTPCodecCapability{
	MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2, SDPFmtpLine: "minptime=10;useinbandfec=1",
}

// Config of the WebRTC endpoint.
type Config struct {
	Port            int    // UDP port shared by every call (ICE UDP mux)
	PublicIP        string // public IPv4 put in the host candidates (1:1 NAT); empty = local addresses
	ListenIP        string // address to bind; empty = all IPv4 addresses
	IncludeLoopback bool   // tests only
}

// Engine is the process-wide WebRTC endpoint.
type Engine struct {
	api  *webrtc.API
	conn *net.UDPConn
	mux  interface{ Close() error }
}

func NewEngine(cfg Config) (*Engine, error) {
	ip := net.IPv4zero
	if cfg.ListenIP != "" {
		if ip = net.ParseIP(cfg.ListenIP); ip == nil {
			return nil, fmt.Errorf("callrtc: bad listen address %q", cfg.ListenIP)
		}
	}
	if cfg.PublicIP != "" && net.ParseIP(cfg.PublicIP) == nil {
		return nil, fmt.Errorf("callrtc: bad public address %q", cfg.PublicIP)
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ip, Port: cfg.Port})
	if err != nil {
		return nil, fmt.Errorf("callrtc: listen on UDP %d: %w", cfg.Port, err)
	}
	mux := webrtc.NewICEUDPMux(logging.NewDefaultLoggerFactory().NewLogger("calls-rtc"), conn)

	se := webrtc.SettingEngine{}
	se.SetLite(true)
	se.SetICEUDPMux(mux)
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	se.SetIncludeLoopbackCandidate(cfg.IncludeLoopback)
	fail := func(err error) (*Engine, error) {
		_ = mux.Close()
		_ = conn.Close()
		return nil, err
	}
	if err := se.SetAnsweringDTLSRole(webrtc.DTLSRoleServer); err != nil {
		return fail(err)
	}
	if cfg.PublicIP != "" {
		if err := se.SetICEAddressRewriteRules(webrtc.ICEAddressRewriteRule{
			External: []string{cfg.PublicIP}, AsCandidateType: webrtc.ICECandidateTypeHost, Mode: webrtc.ICEAddressRewriteReplace,
		}); err != nil {
			return fail(err)
		}
	}
	me := &webrtc.MediaEngine{}
	if err := me.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: opusCapability, PayloadType: 111}, webrtc.RTPCodecTypeAudio); err != nil {
		return fail(err)
	}
	return &Engine{api: webrtc.NewAPI(webrtc.WithMediaEngine(me), webrtc.WithSettingEngine(se)), conn: conn, mux: mux}, nil
}

// Addr is the bound UDP address.
func (e *Engine) Addr() net.Addr { return e.conn.LocalAddr() }

func (e *Engine) Close() error {
	err := e.mux.Close()
	_ = e.conn.Close()
	return err
}

// Leg is one call's peer connection with the agent's browser.
type Leg struct {
	pc    *webrtc.PeerConnection
	track *webrtc.TrackLocalStaticSample
	enc   *opus.Encoder
	dec   *opus.Decoder
	local string

	mu          sync.Mutex
	onFrame     func([]float32)
	onConnected func(bool)
	connected   bool
	closed      bool
}

func (e *Engine) newLeg() (*Leg, error) {
	enc, err := opus.NewEncoder(SampleRate, opusBitrate, 5, false)
	if err != nil {
		return nil, fmt.Errorf("callrtc: opus encoder: %w", err)
	}
	dec, err := opus.NewDecoder(SampleRate)
	if err != nil {
		return nil, fmt.Errorf("callrtc: opus decoder: %w", err)
	}
	pc, err := e.api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, err
	}
	track, err := webrtc.NewTrackLocalStaticSample(opusCapability, "audio", "gowa")
	if err != nil {
		_ = pc.Close()
		return nil, err
	}
	l := &Leg{pc: pc, track: track, enc: enc, dec: dec}
	pc.OnICEConnectionStateChange(l.onICEState)
	pc.OnTrack(l.onTrack)
	return l, nil
}

// NewInbound makes a leg whose LocalSDP is GOWA's offer (the incoming-call flow, B3).
func (e *Engine) NewInbound() (*Leg, error) {
	l, err := e.newLeg()
	if err != nil {
		return nil, err
	}
	sender, err := l.pc.AddTrack(l.track)
	if err != nil {
		_ = l.Close()
		return nil, err
	}
	l.drainRTCP(sender)
	offer, err := l.pc.CreateOffer(nil)
	if err == nil {
		err = l.finishLocal(offer)
	}
	if err != nil {
		_ = l.Close()
		return nil, err
	}
	return l, nil
}

// NewOutbound makes a leg that answers the browser's offer (the outbound-call flow, B4).
func (e *Engine) NewOutbound(remoteOffer string) (*Leg, error) {
	sdp := NormalizeSDP(remoteOffer)
	if !strings.Contains(strings.ToLower(sdp), "opus/48000") {
		return nil, errNoOpus
	}
	l, err := e.newLeg()
	if err != nil {
		return nil, err
	}
	if err := l.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: sdp}); err != nil {
		_ = l.Close()
		return nil, fmt.Errorf("callrtc: browser offer: %w", err)
	}
	sender, err := l.pc.AddTrack(l.track)
	if err != nil {
		_ = l.Close()
		return nil, err
	}
	l.drainRTCP(sender)
	answer, err := l.pc.CreateAnswer(nil)
	if err == nil {
		err = l.finishLocal(answer)
	}
	if err != nil {
		_ = l.Close()
		return nil, err
	}
	return l, nil
}

// finishLocal applies desc and waits for ICE gathering, so the SDP carries every candidate.
func (l *Leg) finishLocal(desc webrtc.SessionDescription) error {
	gathered := webrtc.GatheringCompletePromise(l.pc)
	if err := l.pc.SetLocalDescription(desc); err != nil {
		return err
	}
	select {
	case <-gathered:
	case <-time.After(gatherTimeout):
		return errors.New("callrtc: ICE gathering timed out")
	}
	l.local = NormalizeSDP(l.pc.LocalDescription().SDP)
	return nil
}

func (l *Leg) LocalSDP() string { return l.local }

// SetAnswer applies the browser's answer to GOWA's offer.
func (l *Leg) SetAnswer(answer string) error {
	return l.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: NormalizeSDP(answer)})
}

// WriteFrame encodes one 20 ms frame and sends it to the browser.
func (l *Leg) WriteFrame(pcm []float32) error {
	pkt, err := l.enc.Encode(pcm)
	if err != nil {
		return err
	}
	return l.track.WriteSample(media.Sample{Data: pkt, Duration: time.Duration(len(pcm)) * time.Second / SampleRate})
}

func (l *Leg) OnFrame(fn func([]float32)) {
	l.mu.Lock()
	l.onFrame = fn
	l.mu.Unlock()
}

// OnConnected registers fn for ICE up/down changes and replays "up" when already connected.
func (l *Leg) OnConnected(fn func(bool)) {
	l.mu.Lock()
	l.onConnected = fn
	up := l.connected
	l.mu.Unlock()
	if up {
		fn(true)
	}
}

func (l *Leg) onICEState(s webrtc.ICEConnectionState) {
	var up bool
	switch s {
	case webrtc.ICEConnectionStateConnected, webrtc.ICEConnectionStateCompleted:
		up = true
	case webrtc.ICEConnectionStateDisconnected, webrtc.ICEConnectionStateFailed, webrtc.ICEConnectionStateClosed:
		up = false
	default:
		return
	}
	l.mu.Lock()
	if l.connected == up {
		l.mu.Unlock()
		return
	}
	l.connected = up
	fn := l.onConnected
	l.mu.Unlock()
	if fn != nil {
		fn(up)
	}
}

func (l *Leg) onTrack(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
	if track.Kind() != webrtc.RTPCodecTypeAudio {
		return
	}
	safego.Go("calls_rtc_read", func() {
		for {
			pkt, _, err := track.ReadRTP()
			if err != nil {
				return
			}
			pcm := l.dec.Decode(pkt.Payload)
			if len(pcm) == 0 {
				continue
			}
			l.mu.Lock()
			fn := l.onFrame
			l.mu.Unlock()
			if fn != nil {
				fn(pcm)
			}
		}
	})
}

// drainRTCP reads the sender's RTCP so pion's interceptors keep running.
func (l *Leg) drainRTCP(sender *webrtc.RTPSender) {
	safego.Go("calls_rtc_rtcp", func() {
		buf := make([]byte, 1500)
		for {
			if _, _, err := sender.Read(buf); err != nil {
				return
			}
		}
	})
}

// Close ends the peer connection; safe to call more than once.
func (l *Leg) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	l.mu.Unlock()
	return l.pc.Close()
}
```

- [ ] **Step 4: Run the tests**

Run: `cd src && gofmt -l pkg/callrtc; GOTOOLCHAIN=auto go mod tidy && GOTOOLCHAIN=auto go vet ./pkg/callrtc/ && GOTOOLCHAIN=auto go test ./pkg/callrtc/ -race -v -count=1`
Expected: no gofmt output; vet clean; all six tests PASS within about 15 s. If pion keeps a
loopback candidate out of the lite side's SDP, check that `IncludeLoopback` reaches
`SetIncludeLoopbackCandidate` before changing anything else. The connection tests must run over
loopback, not be skipped.

- [ ] **Step 5: Commit**

```bash
git add src/pkg/callrtc src/go.mod src/go.sum
git commit -m "feat(calls): ICE-lite WebRTC leg on pion with one UDP port"
```

---
### Task 7: The bridge and its wiring (B9, B10, B6)

`callBridge` implements `callMedia` (Task 3) on top of an `rtcLeg` (Task 6) and a `recorder`
(Task 5).

**Customer to agent.**
1. meowcaller's sink gets 16 kHz frames of 60 ms.
2. They are upsampled to 48 kHz and cut into three 20 ms frames.
3. The frames are written to the browser leg. All three go out at once: the browser's jitter
   buffer absorbs the 60 ms burst.
4. A copy of each frame is queued for the recorder.

**Agent to customer.**
1. Decoded 48 kHz browser frames are downsampled to 16 kHz.
2. They go into the jitter FIFO, with a 60 ms prefill and a 240 ms maximum.

**Clock.** meowcaller pulls one 60 ms source frame every 60 ms, and that pull is the bridge's
clock. On each pull the bridge:
1. takes the agent's frame from the FIFO;
2. takes the oldest queued customer frame, or silence when there is none;
3. writes the pair to the recorder (agent left, customer right);
4. hands the agent's frame to WhatsApp.

**Grace.** The browser leg's connection state drives the grace timer of B10. The timer is armed
from `Start` until the first ICE connect, and again on every disconnect.

**Files:**
- Create: `src/infrastructure/whatsapp/calls_bridge.go`, `src/infrastructure/whatsapp/calls_bridge_test.go`
- Modify: `src/infrastructure/whatsapp/calls_services.go` (WebRTC engine, `callsMedia`)
- Modify: `src/infrastructure/whatsapp/calls_registry.go` (`Media: callsMedia`)
- Modify: `src/config/calls.go`, `src/config/calls_test.go`, `src/cmd/calls_flags.go`, `src/.env.example`

**Interfaces:**
- Consumes: `callMedia`, `mediaFactory`, `mediaEvents`, `callHandle`, `RecordingInfo` (Task 3);
  `callaudio.*` (Task 4); `callrec.Create`, `callrec.ValidCallID`, `callrec.Info` (Task 5);
  `callrtc.NewEngine`, `*callrtc.Leg` (Task 6).
- Produces:

```go
type rtcLeg interface{ LocalSDP() string; SetAnswer(sdp string) error; WriteFrame(pcm []float32) error; OnFrame(func(pcm []float32)); OnConnected(func(up bool)); Close() error }
type rtcEngine interface{ NewInbound() (rtcLeg, error); NewOutbound(offer string) (rtcLeg, error) }
type recorder interface{ Write(left, right []float32) error; Close() (callrec.Info, error) }
type bridgeFactory struct{ rtc rtcEngine; recordDir string; grace time.Duration; newRecorder func(path string) (recorder, error) } // implements mediaFactory
var callsMedia mediaFactory // set by StartCallsServices when the WebRTC port opens
```

- Produces: `config.WhatsappCallsRTCPort = 3478`, `config.WhatsappCallsRTCPublicIP = ""`,
  `config.WhatsappCallsRTCGraceSeconds = 10`.

- [ ] **Step 1: Write the failing tests**

`src/infrastructure/whatsapp/calls_bridge_test.go`:

```go
package whatsapp

import (
	"context"
	"io"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/callrec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeLeg struct {
	mu          sync.Mutex
	sdp         string
	answer      string
	written     [][]float32
	onFrame     func([]float32)
	onConnected func(bool)
	closed      int
}

func (l *fakeLeg) LocalSDP() string { return l.sdp }
func (l *fakeLeg) SetAnswer(s string) error {
	l.mu.Lock()
	l.answer = s
	l.mu.Unlock()
	return nil
}
func (l *fakeLeg) WriteFrame(p []float32) error {
	l.mu.Lock()
	l.written = append(l.written, append([]float32(nil), p...))
	l.mu.Unlock()
	return nil
}
func (l *fakeLeg) OnFrame(fn func([]float32))  { l.mu.Lock(); l.onFrame = fn; l.mu.Unlock() }
func (l *fakeLeg) OnConnected(fn func(bool))   { l.mu.Lock(); l.onConnected = fn; l.mu.Unlock() }
func (l *fakeLeg) Close() error                { l.mu.Lock(); l.closed++; l.mu.Unlock(); return nil }
func (l *fakeLeg) connect(up bool) {
	l.mu.Lock()
	fn := l.onConnected
	l.mu.Unlock()
	fn(up)
}
func (l *fakeLeg) send(pcm []float32) {
	l.mu.Lock()
	fn := l.onFrame
	l.mu.Unlock()
	fn(pcm)
}
func (l *fakeLeg) frames() [][]float32 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([][]float32(nil), l.written...)
}

type fakeRTC struct {
	mu   sync.Mutex
	legs []*fakeLeg
}

func (r *fakeRTC) NewInbound() (rtcLeg, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	l := &fakeLeg{sdp: "v=0 gowa-offer"}
	r.legs = append(r.legs, l)
	return l, nil
}
func (r *fakeRTC) NewOutbound(string) (rtcLeg, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	l := &fakeLeg{sdp: "v=0 gowa-answer"}
	r.legs = append(r.legs, l)
	return l, nil
}

type fakeRecorder struct {
	mu     sync.Mutex
	frames [][2][]float32
	closed int
	bytes  int64
}

func (r *fakeRecorder) Write(left, right []float32) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frames = append(r.frames, [2][]float32{append([]float32(nil), left...), append([]float32(nil), right...)})
	return nil
}
func (r *fakeRecorder) Close() (callrec.Info, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed++
	return callrec.Info{Path: "x.ogg", Bytes: r.bytes, Duration: time.Duration(len(r.frames)) * 60 * time.Millisecond}, nil
}

type bridgeEvents struct{ flows, losts atomic.Int32 }

func (e *bridgeEvents) events() mediaEvents {
	return mediaEvents{Flowing: func() { e.flows.Add(1) }, Lost: func() { e.losts.Add(1) }}
}

func testFactory(t *testing.T, grace time.Duration, rec *fakeRecorder, made *atomic.Int32) (*bridgeFactory, *fakeRTC) {
	rtc := &fakeRTC{}
	return &bridgeFactory{rtc: rtc, recordDir: t.TempDir(), grace: grace, newRecorder: func(string) (recorder, error) {
		if made != nil {
			made.Add(1)
		}
		return rec, nil
	}}, rtc
}

func startBridge(t *testing.T, grace time.Duration, id string) (*callBridge, *fakeLeg, *fakeRecorder, *fakeCall, *bridgeEvents) {
	t.Helper()
	rec := &fakeRecorder{bytes: 1234}
	f, rtc := testFactory(t, grace, rec, nil)
	media, err := f.Inbound()
	require.NoError(t, err)
	b := media.(*callBridge)
	h := &fakeCall{id: id}
	ev := &bridgeEvents{}
	b.Start(h, ev.events())
	return b, rtc.legs[0], rec, h, ev
}

func tone(n, rate int, hz float64) []float32 {
	out := make([]float32, n)
	for i := range out {
		out[i] = float32(0.5 * math.Sin(2*math.Pi*hz*float64(i)/float64(rate)))
	}
	return out
}

func level(p []float32) float64 {
	var s float64
	for _, v := range p {
		s += float64(v) * float64(v)
	}
	return math.Sqrt(s / float64(len(p)))
}

func TestBridgeCustomerToBrowser(t *testing.T) {
	_, leg, _, h, _ := startBridge(t, time.Second, "IN-1")
	require.NoError(t, h.sink.WriteFrame(tone(960, 16000, 440)))
	frames := leg.frames()
	require.Len(t, frames, 3, "60 ms at 16 kHz becomes three 20 ms frames at 48 kHz")
	for _, f := range frames {
		assert.Len(t, f, 960)
	}
	assert.Greater(t, level(frames[2]), 0.2)
}

func TestBridgeAgentToWhatsAppAndRecorder(t *testing.T) {
	_, leg, rec, h, _ := startBridge(t, time.Second, "IN-1")
	require.NoError(t, h.sink.WriteFrame(tone(960, 16000, 300))) // customer
	for i := 0; i < 3; i++ {
		leg.send(tone(960, 48000, 440)) // 60 ms of agent audio
	}
	agent, err := h.src.ReadFrame()
	require.NoError(t, err)
	assert.Len(t, agent, 960)
	assert.Greater(t, level(agent), 0.2)

	silent, err := h.src.ReadFrame()
	require.NoError(t, err)
	assert.Zero(t, level(silent), "an empty jitter buffer plays silence")

	rec.mu.Lock()
	defer rec.mu.Unlock()
	require.Len(t, rec.frames, 2)
	assert.Greater(t, level(rec.frames[0][0]), 0.2, "agent on the left")
	assert.Greater(t, level(rec.frames[0][1]), 0.2, "customer on the right")
	assert.Empty(t, rec.frames[1][1], "no customer frame queued: the recorder pads silence")
}

func TestBridgeFlowingNeedsBothLegs(t *testing.T) {
	b, leg, _, _, ev := startBridge(t, time.Second, "IN-1")
	b.WhatsAppReady()
	assert.Zero(t, ev.flows.Load())
	leg.connect(true)
	assert.EqualValues(t, 1, ev.flows.Load())
	leg.connect(false)
	leg.connect(true)
	assert.EqualValues(t, 1, ev.flows.Load(), "Flowing fires once")
}

// Review Focus 1: a browser that never connects must not leave the customer on a silent line.
func TestBridgeGraceWhenBrowserNeverConnects(t *testing.T) {
	_, _, _, _, ev := startBridge(t, 30*time.Millisecond, "IN-1")
	assert.Eventually(t, func() bool { return ev.losts.Load() == 1 }, time.Second, 5*time.Millisecond)
}

func TestBridgeGraceCancelledByReconnect(t *testing.T) {
	_, leg, _, _, ev := startBridge(t, 80*time.Millisecond, "IN-1")
	leg.connect(true)
	leg.connect(false)
	leg.connect(true)
	time.Sleep(150 * time.Millisecond)
	assert.Zero(t, ev.losts.Load())
}

func TestBridgeCloseIsFinal(t *testing.T) {
	b, leg, rec, h, ev := startBridge(t, 30*time.Millisecond, "IN-1")
	got := b.Close()
	require.NotNil(t, got)
	assert.EqualValues(t, 1234, got.Bytes)
	assert.Same(t, got, b.Close(), "Close is idempotent")
	assert.Equal(t, 1, leg.closed)
	assert.Equal(t, 1, rec.closed)
	_, err := h.src.ReadFrame()
	assert.ErrorIs(t, err, io.EOF)
	time.Sleep(80 * time.Millisecond)
	assert.Zero(t, ev.losts.Load(), "no grace hangup after Close")
}

func TestBridgeSkipsRecordingForUnsafeID(t *testing.T) {
	var made atomic.Int32
	f, _ := testFactory(t, time.Second, &fakeRecorder{}, &made)
	media, err := f.Inbound()
	require.NoError(t, err)
	media.Start(&fakeCall{id: "../x"}, (&bridgeEvents{}).events())
	assert.Zero(t, made.Load())
	assert.Nil(t, media.Close())
}

func TestManagerBridgeInboundEndToEnd(t *testing.T) {
	now := time.Now()
	rec := &fakeRecorder{bytes: 99}
	f, rtc := testFactory(t, time.Second, rec, nil)
	m, eng, log := newTestManager(t, 1, &now, f)
	in := &fakeCall{id: "IN-1"}
	eng.incoming(in)

	sdp, err := m.OfferSDP(context.Background(), "IN-1")
	require.NoError(t, err)
	assert.Equal(t, "v=0 gowa-offer", sdp)
	require.NoError(t, m.Answer("IN-1", "v=0 browser-answer"))
	leg := rtc.legs[0]
	assert.Equal(t, "v=0 browser-answer", leg.answer)

	in.media()
	leg.connect(true)
	answered, ok := log.last(EventCallAnswered)
	require.True(t, ok)
	assert.Equal(t, "api", answered.AnsweredBy)

	now = now.Add(30 * time.Second)
	in.end("")
	ended, ok := log.last(EventCallEnded)
	require.True(t, ok)
	assert.Equal(t, "COMPLETED", ended.Status)
	assert.Equal(t, "hangup_peer", ended.EndReason)
	assert.Equal(t, 30, ended.Duration)
	require.NotNil(t, ended.Recording)
	assert.EqualValues(t, 99, ended.Recording.Bytes)
	assert.Equal(t, 1, leg.closed)
}

// Review Focus 1, end to end: losing the browser hangs up the WhatsApp leg and reports the end.
func TestManagerBridgeBrowserLostHangsUp(t *testing.T) {
	now := time.Now()
	f, rtc := testFactory(t, 30*time.Millisecond, &fakeRecorder{bytes: 5}, nil)
	m, eng, log := newTestManager(t, 1, &now, f)
	in := &fakeCall{id: "IN-1"}
	eng.incoming(in)
	_, err := m.OfferSDP(context.Background(), "IN-1")
	require.NoError(t, err)
	require.NoError(t, m.Answer("IN-1", "v=0 browser-answer"))
	in.media()
	rtc.legs[0].connect(true)
	rtc.legs[0].connect(false)

	assert.Eventually(t, func() bool {
		in.mu.Lock()
		defer in.mu.Unlock()
		return in.hungUp
	}, time.Second, 5*time.Millisecond)
	ended, ok := log.last(EventCallEnded)
	require.True(t, ok)
	assert.Equal(t, "COMPLETED", ended.Status)
	assert.Equal(t, "error", ended.EndReason)
	assert.Empty(t, m.Active())
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `cd src && GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/ -run 'Bridge'`
Expected: FAIL to build (`undefined: bridgeFactory`).

- [ ] **Step 3: Implement the bridge**

`src/infrastructure/whatsapp/calls_bridge.go`:

```go
package whatsapp

import (
	"io"
	"path/filepath"
	"sync"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/callaudio"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/callrec"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/callrtc"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/safego"
	meowcaller "github.com/purpshell/meowcaller"
	"github.com/sirupsen/logrus"
)

// Fork (elphant): the audio bridge between the WhatsApp leg (meowcaller, 16 kHz / 60 ms) and
// the browser leg (pion, 48 kHz / 20 ms), with the stereo recorder tapping both directions
// (stage 2b: B9, B10, B6).

type rtcLeg interface {
	LocalSDP() string
	SetAnswer(sdp string) error
	WriteFrame(pcm []float32) error
	OnFrame(func(pcm []float32))
	OnConnected(func(up bool))
	Close() error
}

type rtcEngine interface {
	NewInbound() (rtcLeg, error)
	NewOutbound(offer string) (rtcLeg, error)
}

type recorder interface {
	Write(left, right []float32) error
	Close() (callrec.Info, error)
}

// pionEngine adapts *callrtc.Engine to rtcEngine.
type pionEngine struct{ e *callrtc.Engine }

func (p pionEngine) NewInbound() (rtcLeg, error) {
	l, err := p.e.NewInbound()
	if err != nil {
		return nil, err
	}
	return l, nil
}

func (p pionEngine) NewOutbound(offer string) (rtcLeg, error) {
	l, err := p.e.NewOutbound(offer)
	if err != nil {
		return nil, err
	}
	return l, nil
}

const (
	waFrame             = meowcaller.FrameSamples // 60 ms at 16 kHz
	rtcFrame            = callrtc.FrameSamples    // 20 ms at 48 kHz
	customerQueueFrames = 4
)

// bridgeFactory makes one callBridge per call; it is the manager's mediaFactory.
type bridgeFactory struct {
	rtc         rtcEngine
	recordDir   string // empty: no recording
	grace       time.Duration
	newRecorder func(path string) (recorder, error)
}

func (f *bridgeFactory) Inbound() (callMedia, error) {
	leg, err := f.rtc.NewInbound()
	if err != nil {
		return nil, err
	}
	return newCallBridge(f, leg), nil
}

func (f *bridgeFactory) Outbound(offer string) (callMedia, error) {
	leg, err := f.rtc.NewOutbound(offer)
	if err != nil {
		return nil, err
	}
	return newCallBridge(f, leg), nil
}

type callBridge struct {
	f     *bridgeFactory
	leg   rtcLeg
	up    *callaudio.Upsampler   // used only by the sink (meowcaller's receive goroutine)
	chunk *callaudio.Rechunker   // idem
	down  *callaudio.Downsampler // used only by the browser leg's read goroutine
	agent *callaudio.FIFO

	mu       sync.Mutex
	customer [][]float32
	rec      recorder
	ev       mediaEvents
	started  bool
	waReady  bool
	rtcUp    bool
	flowing  bool
	closed   bool
	grace    *time.Timer
	result   *RecordingInfo
}

func newCallBridge(f *bridgeFactory, leg rtcLeg) *callBridge {
	b := &callBridge{
		f: f, leg: leg,
		up: callaudio.NewUpsampler(), chunk: callaudio.NewRechunker(rtcFrame),
		down: callaudio.NewDownsampler(), agent: callaudio.NewFIFO(waFrame, 4*waFrame),
	}
	// Registered before the answer is applied, so an early ICE "up" is never missed.
	leg.OnFrame(b.fromBrowser)
	leg.OnConnected(b.onBrowser)
	return b
}

func (b *callBridge) LocalSDP() string            { return b.leg.LocalSDP() }
func (b *callBridge) Accept(answerSDP string) error { return b.leg.SetAnswer(answerSDP) }

func (b *callBridge) Start(h callHandle, ev mediaEvents) {
	var rec recorder
	id := h.ID()
	if b.f.recordDir != "" && b.f.newRecorder != nil {
		if !callrec.ValidCallID(id) {
			logrus.Warnf("[CALLS] not recording call from %s: unsafe call id", maskPeer(h.Peer()))
		} else if r, err := b.f.newRecorder(filepath.Join(b.f.recordDir, id+".ogg")); err != nil {
			logrus.Warnf("[CALLS] record %s: %v", id, err)
		} else {
			rec = r
		}
	}
	b.mu.Lock()
	if b.closed || b.started {
		b.mu.Unlock()
		if rec != nil {
			_, _ = rec.Close()
		}
		return
	}
	b.started, b.ev, b.rec = true, ev, rec
	up := b.rtcUp
	b.mu.Unlock()
	h.Bridge(bridgeSource{b}, bridgeSink{b})
	if up {
		b.maybeFlowing()
	} else {
		b.armGrace()
	}
}

func (b *callBridge) WhatsAppReady() {
	b.mu.Lock()
	b.waReady = true
	b.mu.Unlock()
	b.maybeFlowing()
}

func (b *callBridge) onBrowser(up bool) {
	b.mu.Lock()
	b.rtcUp = up
	started := b.started
	b.mu.Unlock()
	if !started {
		return
	}
	if up {
		b.stopGrace()
		b.maybeFlowing()
	} else {
		b.armGrace()
	}
}

func (b *callBridge) maybeFlowing() {
	b.mu.Lock()
	fire := b.started && !b.closed && b.waReady && b.rtcUp && !b.flowing
	if fire {
		b.flowing = true
	}
	fn := b.ev.Flowing
	b.mu.Unlock()
	if fire && fn != nil {
		fn()
	}
}

// armGrace starts the B10 timer unless it already runs: when it fires with the browser leg
// still down, the manager hangs up the WhatsApp leg.
func (b *callBridge) armGrace() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.grace != nil || b.f.grace <= 0 {
		return
	}
	var timer *time.Timer
	timer = time.AfterFunc(b.f.grace, func() {
		defer safego.Recover("calls_rtc_grace")
		b.mu.Lock()
		if b.grace == timer {
			b.grace = nil
		}
		lost := !b.closed && !b.rtcUp
		fn := b.ev.Lost
		b.mu.Unlock()
		if lost && fn != nil {
			fn()
		}
	})
	b.grace = timer
}

func (b *callBridge) stopGrace() {
	b.mu.Lock()
	if b.grace != nil {
		b.grace.Stop()
		b.grace = nil
	}
	b.mu.Unlock()
}

// fromBrowser takes the agent's decoded audio (48 kHz) into the jitter buffer (16 kHz).
func (b *callBridge) fromBrowser(pcm48 []float32) { b.agent.Write(b.down.Process(pcm48)) }

// bridgeSource is what meowcaller plays to the customer. Its ReadFrame is pulled every 60 ms
// by the send loop: it is the bridge's clock, so it also writes the recorder.
type bridgeSource struct{ b *callBridge }

func (s bridgeSource) ReadFrame() ([]float32, error) {
	b := s.b
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, io.EOF
	}
	var customer []float32
	if len(b.customer) > 0 {
		customer, b.customer = b.customer[0], b.customer[1:]
	}
	rec := b.rec
	b.mu.Unlock()
	agent := b.agent.Read(waFrame)
	if rec != nil {
		if err := rec.Write(agent, customer); err != nil {
			logrus.Debugf("[CALLS] recorder: %v", err)
		}
	}
	return agent, nil
}

func (s bridgeSource) Close() error { return nil }

// bridgeSink receives the customer's decoded audio from meowcaller (16 kHz, 60 ms).
type bridgeSink struct{ b *callBridge }

func (s bridgeSink) WriteFrame(frame []float32) error {
	b := s.b
	pcm := append([]float32(nil), frame...)
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.customer = append(b.customer, pcm)
	if len(b.customer) > customerQueueFrames {
		b.customer = b.customer[len(b.customer)-customerQueueFrames:]
	}
	b.mu.Unlock()
	for _, f := range b.chunk.Push(b.up.Process(pcm)) {
		if err := b.leg.WriteFrame(f); err != nil {
			logrus.Debugf("[CALLS] browser write: %v", err)
			break
		}
	}
	return nil
}

func (s bridgeSink) Close() error { return nil }

// Close ends the browser leg and the recording (B10: whichever leg ended first). Idempotent.
func (b *callBridge) Close() *RecordingInfo {
	b.mu.Lock()
	if b.closed {
		r := b.result
		b.mu.Unlock()
		return r
	}
	b.closed = true
	if b.grace != nil {
		b.grace.Stop()
		b.grace = nil
	}
	rec := b.rec
	b.mu.Unlock()

	_ = b.leg.Close()
	var result *RecordingInfo
	if rec != nil {
		info, err := rec.Close()
		if err != nil {
			logrus.Warnf("[CALLS] close recording: %v", err)
		}
		if info.Bytes > 0 {
			result = &RecordingInfo{Path: info.Path, Bytes: info.Bytes, Duration: info.Duration}
		}
	}
	b.mu.Lock()
	b.result = result
	b.mu.Unlock()
	return result
}
```

- [ ] **Step 4: Wire the engine at start-up and into every manager**

Replace `src/infrastructure/whatsapp/calls_services.go` with:

```go
package whatsapp

import (
	"context"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/callrec"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/callrtc"
	"github.com/sirupsen/logrus"
)

// Fork (elphant): what voice calls need besides the clients (stage 2b).

// callsMedia is the browser-leg factory every call manager uses; nil until StartCallsServices
// opens the WebRTC port (then calls report ErrCallsNoMedia).
var callsMedia mediaFactory

// StartCallsServices runs once at start, before the first client is created: the recording
// retention job and the WebRTC port. With calls off it does nothing.
func StartCallsServices(ctx context.Context) {
	if !config.WhatsappCallsEnabled {
		return
	}
	retention := time.Duration(config.WhatsappCallsRecordingRetentionDays) * 24 * time.Hour
	callrec.StartRetention(ctx, config.PathCalls, retention)

	eng, err := callrtc.NewEngine(callrtc.Config{Port: config.WhatsappCallsRTCPort, PublicIP: config.WhatsappCallsRTCPublicIP})
	if err != nil {
		logrus.Errorf("[CALLS] %v; calls cannot carry browser audio", err)
		return
	}
	if config.WhatsappCallsRTCPublicIP == "" {
		logrus.Warn("[CALLS] WHATSAPP_CALLS_RTC_PUBLIC_IP is empty: the SDP carries local addresses, reachable only from the same network")
	}
	callsMedia = newBridgeFactory(pionEngine{eng})
	logrus.Infof("[CALLS] WebRTC on UDP %d", config.WhatsappCallsRTCPort)
}

func newBridgeFactory(rtc rtcEngine) *bridgeFactory {
	return &bridgeFactory{
		rtc:       rtc,
		recordDir: config.PathCalls,
		grace:     time.Duration(config.WhatsappCallsRTCGraceSeconds) * time.Second,
		newRecorder: func(path string) (recorder, error) {
			r, err := callrec.Create(path)
			if err != nil {
				return nil, err
			}
			return r, nil
		},
	}
}
```

In `src/infrastructure/whatsapp/calls_registry.go`, add `Media: callsMedia,` to the
`callManagerOptions` in `attachCalls`.

In `src/config/calls.go`, add to the `var` block:

```go
	WhatsappCallsRTCPort         = 3478 // stage 2b: one UDP port for every browser leg (ICE UDP mux); publish it in mode: host
	WhatsappCallsRTCPublicIP     = ""   // stage 2b: the host's public IPv4, put in the SDP's host candidate (1:1 NAT)
	WhatsappCallsRTCGraceSeconds = 10   // stage 2b: hang up the WhatsApp leg after the browser leg is down this long
```

Extend `TestCallsStage2bDefaults` in `src/config/calls_test.go`:

```go
	if WhatsappCallsRTCPort != 3478 || WhatsappCallsRTCPublicIP != "" || WhatsappCallsRTCGraceSeconds != 10 {
		t.Fatalf("RTC defaults %d %q %d, want 3478 \"\" 10", WhatsappCallsRTCPort, WhatsappCallsRTCPublicIP, WhatsappCallsRTCGraceSeconds)
	}
```

In `src/cmd/calls_flags.go`, add to `initCallsEnv`:

```go
	if viper.IsSet("whatsapp_calls_rtc_port") {
		config.WhatsappCallsRTCPort = viper.GetInt("whatsapp_calls_rtc_port")
	}
	if env := viper.GetString("whatsapp_calls_rtc_public_ip"); env != "" {
		config.WhatsappCallsRTCPublicIP = env
	}
	if viper.IsSet("whatsapp_calls_rtc_grace_seconds") {
		config.WhatsappCallsRTCGraceSeconds = viper.GetInt("whatsapp_calls_rtc_grace_seconds")
	}
```

and to `initCallsFlags`:

```go
	flags.IntVarP(&config.WhatsappCallsRTCPort, "calls-rtc-port", "", config.WhatsappCallsRTCPort,
		`UDP port of the browser leg --calls-rtc-port <n> | example: --calls-rtc-port=3478`)
	flags.StringVarP(&config.WhatsappCallsRTCPublicIP, "calls-rtc-public-ip", "", config.WhatsappCallsRTCPublicIP,
		`public IPv4 announced to browsers --calls-rtc-public-ip <ip> | example: --calls-rtc-public-ip=203.0.113.7`)
	flags.IntVarP(&config.WhatsappCallsRTCGraceSeconds, "calls-rtc-grace-seconds", "", config.WhatsappCallsRTCGraceSeconds,
		`hang up after the browser leg is down this long --calls-rtc-grace-seconds <n> | example: --calls-rtc-grace-seconds=10`)
```

In `src/.env.example`, after the recording lines:

```
# Browser leg (WebRTC): one UDP port for every call, published in mode: host, and the host's public IPv4
WHATSAPP_CALLS_RTC_PORT=3478
WHATSAPP_CALLS_RTC_PUBLIC_IP=
# Hang up the WhatsApp leg after the browser leg is down this many seconds
WHATSAPP_CALLS_RTC_GRACE_SECONDS=10
```

- [ ] **Step 5: Run the tests**

Run: `cd src && gofmt -l infrastructure config cmd; GOTOOLCHAIN=auto go vet ./infrastructure/whatsapp/ && GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/ ./config/ ./cmd/ -race`
Expected: no gofmt output; vet clean; `ok` everywhere, no race report.

- [ ] **Step 6: Commit**

```bash
git add src/infrastructure/whatsapp/calls_bridge.go src/infrastructure/whatsapp/calls_bridge_test.go \
  src/infrastructure/whatsapp/calls_services.go src/infrastructure/whatsapp/calls_registry.go \
  src/config/calls.go src/config/calls_test.go src/cmd/calls_flags.go src/.env.example
git commit -m "feat(calls): bridge the WhatsApp and browser legs with the stereo recorder"
```

---
### Task 8: REST surface: answer and start with SDP, `GET /call/:call_id`, reject through the manager

**Files:**
- Modify: `src/infrastructure/whatsapp/calls.go` (add `Detail`), `src/infrastructure/whatsapp/calls_test.go` (add a test)
- Modify: `src/domains/call/call.go`, `src/usecase/call.go`
- Create: `src/usecase/call_test.go`
- Modify: `src/ui/rest/call.go`, `src/ui/rest/call_test.go`
- Modify: `docs/openapi.yaml`

**Interfaces:**
- Consumes: `(*CallManager).Answer/Start/Get/Reject/OfferSDP`, `ErrCallAnswered`, `DirectionUser` (Task 3).
- Produces:

```go
func (m *CallManager) Detail(ctx context.Context, callID string) (CallInfo, string, error) // string: offer SDP while an inbound call rings
// domains/call
type ICallUsecase interface {
	RejectCall(ctx context.Context, callerJID string, callID string) error
	AnswerCall(ctx context.Context, request AnswerCallRequest) (AnswerCallResponse, error)
	StartCall(ctx context.Context, request StartCallRequest) (StartCallResponse, error)
	HangupCall(ctx context.Context, callID string) error
	ActiveCalls(ctx context.Context) ([]CallInfo, error)
	GetCall(ctx context.Context, callID string) (CallDetail, error)
	SetDeviceCalls(ctx context.Context, deviceID string, enabled bool) (DeviceCallsResponse, error)
	GetDeviceCalls(ctx context.Context, deviceID string) (DeviceCallsResponse, error)
}
type AnswerCallRequest struct{ CallID, SDP string }      // json call_id, sdp
type AnswerCallResponse struct{ CallID string }          // json call_id
type StartCallRequest struct{ Phone, SDP string }        // json phone, sdp
type StartCallResponse struct{ CallID, SDPAnswer string } // json call_id, sdp_answer
type CallDetail struct{ CallID, State, Direction string; Peer CallPeer; OfferSDP string } // json offer_sdp,omitempty
```

- [ ] **Step 1: Write the failing tests**

Append to `src/infrastructure/whatsapp/calls_test.go`:

```go
func TestDetailCarriesOfferWhileRinging(t *testing.T) {
	now := time.Now()
	m, eng, _ := newTestManager(t, 1, &now, &fakeMediaFactory{})
	eng.incoming(&fakeCall{id: "IN-1"})
	info, sdp, err := m.Detail(context.Background(), "IN-1")
	require.NoError(t, err)
	assert.Equal(t, "ringing", info.State)
	assert.Equal(t, "v=0 gowa-offer", sdp)

	require.NoError(t, m.Answer("IN-1", "v=0 browser-answer"))
	_, sdp, err = m.Detail(context.Background(), "IN-1")
	require.NoError(t, err)
	assert.Empty(t, sdp, "no offer once answered")

	_, _, err = m.Detail(context.Background(), "NOPE")
	assert.ErrorIs(t, err, ErrCallNotFound)
}
```

`src/usecase/call_test.go`:

```go
package usecase

import (
	"context"
	"testing"

	domainCall "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/call"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCallRequestsNeedTheirSDP(t *testing.T) {
	svc := serviceCall{}
	ctx := context.Background()
	cases := map[string]error{}
	_, cases["answer without call_id"] = svc.AnswerCall(ctx, domainCall.AnswerCallRequest{SDP: "v=0"})
	_, cases["answer without sdp"] = svc.AnswerCall(ctx, domainCall.AnswerCallRequest{CallID: "IN-1"})
	_, cases["start without phone"] = svc.StartCall(ctx, domainCall.StartCallRequest{SDP: "v=0"})
	_, cases["start without sdp"] = svc.StartCall(ctx, domainCall.StartCallRequest{Phone: "+5511900000000"})
	_, cases["get without call_id"] = svc.GetCall(ctx, "")
	for name, err := range cases {
		var ge pkgError.GenericError
		require.ErrorAs(t, err, &ge, name)
		assert.Equal(t, 400, ge.StatusCode(), name)
	}
}

func TestRejectTrackedCallFallsThroughWithoutCalls(t *testing.T) {
	handled, err := rejectTrackedCall(context.Background(), "IN-1")
	assert.False(t, handled)
	assert.NoError(t, err)
}
```

In `src/ui/rest/call_test.go`, replace the stub's `AnswerCall` and `StartCall` and add `GetCall`:

```go
func (s *callServiceStub) AnswerCall(ctx context.Context, request domainCall.AnswerCallRequest) (domainCall.AnswerCallResponse, error) {
	s.answered, s.answeredSDP = request.CallID, request.SDP
	s.contextDevice, _ = whatsapp.DeviceFromContext(ctx)
	return domainCall.AnswerCallResponse{CallID: request.CallID}, s.err
}
func (s *callServiceStub) StartCall(ctx context.Context, request domainCall.StartCallRequest) (domainCall.StartCallResponse, error) {
	s.started = request.Phone
	return domainCall.StartCallResponse{CallID: "OUT-1", SDPAnswer: "v=0 gowa-answer"}, s.err
}
func (s *callServiceStub) GetCall(ctx context.Context, callID string) (domainCall.CallDetail, error) {
	return domainCall.CallDetail{CallID: callID, State: "ringing", Direction: "USER_INITIATED", OfferSDP: "v=0 gowa-offer"}, s.err
}
```

Add the field `answeredSDP string` to `callServiceStub`. Replace `TestCallRoutesHappyPath` and
`TestCallRoutesDisabledDevice`, and add the 409 test:

```go
func TestCallRoutesHappyPath(t *testing.T) {
	device := whatsapp.NewDeviceInstance("device-a", nil, nil)
	service := &callServiceStub{}
	app := newCallApp(service, device)

	post := func(path, body string) map[string]any {
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		require.NoError(t, err)
		require.Equal(t, 200, resp.StatusCode, path)
		var out struct {
			Results map[string]any `json:"results"`
		}
		raw, _ := io.ReadAll(resp.Body)
		require.NoError(t, json.Unmarshal(raw, &out))
		return out.Results
	}

	res := post("/call/answer", `{"call_id":"IN-1","sdp":"v=0 browser-answer"}`)
	assert.Equal(t, "IN-1", res["call_id"])
	assert.Equal(t, "v=0 browser-answer", service.answeredSDP)
	assert.Same(t, device, service.contextDevice)

	res = post("/call/start", `{"phone":"+5511900000000","sdp":"v=0 browser-offer"}`)
	assert.Equal(t, "OUT-1", res["call_id"])
	assert.Equal(t, "v=0 gowa-answer", res["sdp_answer"])
	assert.Equal(t, "+5511900000000", service.started)

	resp, err := app.Test(httptest.NewRequest("GET", "/call/IN-1", nil))
	require.NoError(t, err)
	raw, _ := io.ReadAll(resp.Body)
	var detail struct {
		Results domainCall.CallDetail `json:"results"`
	}
	require.NoError(t, json.Unmarshal(raw, &detail))
	assert.Equal(t, "v=0 gowa-offer", detail.Results.OfferSDP)

	resp, err = app.Test(httptest.NewRequest("GET", "/calls/active", nil))
	require.NoError(t, err)
	raw, _ = io.ReadAll(resp.Body)
	var active struct {
		Results []domainCall.CallInfo `json:"results"`
	}
	require.NoError(t, json.Unmarshal(raw, &active))
	require.Len(t, active.Results, 1)
	assert.Equal(t, "IN-1", active.Results[0].CallID)
}

func TestAnswerTwiceIs409(t *testing.T) {
	app := newCallApp(&callServiceStub{err: whatsapp.ErrCallAnswered}, whatsapp.NewDeviceInstance("device-a", nil, nil))
	req := httptest.NewRequest("POST", "/call/answer", strings.NewReader(`{"call_id":"IN-1","sdp":"v=0"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 409, resp.StatusCode)
}

func TestCallRoutesDisabledDevice(t *testing.T) {
	device := whatsapp.NewDeviceInstance("device-a", nil, nil)
	app := newCallApp(&callServiceStub{err: whatsapp.ErrCallsOff}, device)
	for _, r := range []struct{ method, path, body string }{
		{"POST", "/call/answer", `{"call_id":"IN-1","sdp":"v=0"}`},
		{"POST", "/call/start", `{"phone":"+5511900000000","sdp":"v=0"}`},
		{"POST", "/call/hangup", `{"call_id":"IN-1"}`},
		{"GET", "/calls/active", ""},
		{"GET", "/call/IN-1", ""},
	} {
		req := httptest.NewRequest(r.method, r.path, strings.NewReader(r.body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		require.NoError(t, err)
		assert.Equal(t, 400, resp.StatusCode, r.path)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `cd src && GOTOOLCHAIN=auto go test ./ui/rest/ ./usecase/ ./infrastructure/whatsapp/ -run 'CallRoutes|AnswerTwiceIs409|CallRequestsNeed|RejectTracked|DetailCarries'`
Expected: FAIL to build (`undefined: domainCall.AnswerCallRequest`, `m.Detail undefined`).

- [ ] **Step 3: Implement**

Append to `src/infrastructure/whatsapp/calls.go`:

```go
// Detail returns a tracked call and, while an inbound call rings, GOWA's offer SDP (made on
// first use), so the browser can fetch the offer again (GET /call/:call_id).
func (m *CallManager) Detail(ctx context.Context, callID string) (CallInfo, string, error) {
	info, ok := m.Get(callID)
	if !ok {
		return CallInfo{}, "", ErrCallNotFound
	}
	if info.Direction != DirectionUser || info.State != stateRinging || m.opts.Media == nil {
		return info, "", nil
	}
	sdp, err := m.OfferSDP(ctx, callID)
	if err != nil {
		return CallInfo{}, "", err
	}
	return info, sdp, nil
}
```

In `src/domains/call/call.go`, replace the fork part of `ICallUsecase` and the request types:

```go
type ICallUsecase interface {
	RejectCall(ctx context.Context, callerJID string, callID string) error
	// Fork (elphant): voice calls.
	AnswerCall(ctx context.Context, request AnswerCallRequest) (AnswerCallResponse, error)
	StartCall(ctx context.Context, request StartCallRequest) (StartCallResponse, error)
	HangupCall(ctx context.Context, callID string) error
	ActiveCalls(ctx context.Context) ([]CallInfo, error)
	GetCall(ctx context.Context, callID string) (CallDetail, error)
	SetDeviceCalls(ctx context.Context, deviceID string, enabled bool) (DeviceCallsResponse, error)
	GetDeviceCalls(ctx context.Context, deviceID string) (DeviceCallsResponse, error)
}
```

```go
// AnswerCallRequest carries the browser's SDP answer to the call's offer_sdp.
type AnswerCallRequest struct {
	CallID string `json:"call_id" form:"call_id"`
	SDP    string `json:"sdp" form:"sdp"`
}

type AnswerCallResponse struct {
	CallID string `json:"call_id"`
}

// StartCallRequest carries the browser's SDP offer.
type StartCallRequest struct {
	Phone string `json:"phone" form:"phone"`
	SDP   string `json:"sdp" form:"sdp"`
}

type StartCallResponse struct {
	CallID    string `json:"call_id"`
	SDPAnswer string `json:"sdp_answer"`
}

type CallDetail struct {
	CallID    string   `json:"call_id"`
	State     string   `json:"state"`
	Direction string   `json:"direction"`
	Peer      CallPeer `json:"peer"`
	OfferSDP  string   `json:"offer_sdp,omitempty"`
}
```

In `src/usecase/call.go` (add `"strings"` to the imports):
- In the upstream `RejectCall`, add one line right after the
  `ValidateRejectCall` check:

```go
	if handled, err := rejectTrackedCall(ctx, callID); handled { // Fork (elphant): a call GOWA tracks is rejected through its manager (call.ended REJECTED).
		return err
	}
```

- In the fork section, replace `AnswerCall` and `StartCall`, and add `GetCall` and
  `rejectTrackedCall`:

```go
// rejectTrackedCall rejects a call the device's call manager tracks; handled is false when
// calls are off for the device or the manager does not know the call (whatsmeow rejects it).
func rejectTrackedCall(ctx context.Context, callID string) (handled bool, err error) {
	m, err := whatsapp.CallsFromContext(ctx)
	if err != nil {
		return false, nil
	}
	if _, ok := m.Get(callID); !ok {
		return false, nil
	}
	return true, mapCallError(m.Reject(callID), "call operation failed: ")
}

func (service serviceCall) AnswerCall(ctx context.Context, request domainCall.AnswerCallRequest) (domainCall.AnswerCallResponse, error) {
	if request.CallID == "" {
		return domainCall.AnswerCallResponse{}, pkgError.ValidationError("call_id is required")
	}
	if strings.TrimSpace(request.SDP) == "" {
		return domainCall.AnswerCallResponse{}, pkgError.ValidationError("sdp is required (the browser's answer to offer_sdp)")
	}
	m, err := whatsapp.CallsFromContext(ctx)
	if err != nil {
		return domainCall.AnswerCallResponse{}, err
	}
	if err := m.Answer(request.CallID, request.SDP); err != nil {
		return domainCall.AnswerCallResponse{}, mapCallError(err, "call operation failed: ")
	}
	return domainCall.AnswerCallResponse{CallID: request.CallID}, nil
}

func (service serviceCall) StartCall(ctx context.Context, request domainCall.StartCallRequest) (domainCall.StartCallResponse, error) {
	if request.Phone == "" {
		return domainCall.StartCallResponse{}, pkgError.ValidationError("phone is required")
	}
	if strings.TrimSpace(request.SDP) == "" {
		return domainCall.StartCallResponse{}, pkgError.ValidationError("sdp is required (the browser's offer)")
	}
	m, err := whatsapp.CallsFromContext(ctx)
	if err != nil {
		return domainCall.StartCallResponse{}, err
	}
	// meowcaller uses this ctx only for signaling (peer resolution, device discovery,
	// offer send in engine.go placeCall); the Call does not keep it, so a timeout is safe.
	startCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	info, answer, err := m.Start(startCtx, request.Phone, request.SDP)
	if err != nil {
		return domainCall.StartCallResponse{}, mapCallError(err, "could not place the call: ")
	}
	return domainCall.StartCallResponse{CallID: info.CallID, SDPAnswer: answer}, nil
}

func (service serviceCall) GetCall(ctx context.Context, callID string) (domainCall.CallDetail, error) {
	if callID == "" {
		return domainCall.CallDetail{}, pkgError.ValidationError("call_id is required")
	}
	m, err := whatsapp.CallsFromContext(ctx)
	if err != nil {
		return domainCall.CallDetail{}, err
	}
	info, offer, err := m.Detail(ctx, callID)
	if err != nil {
		return domainCall.CallDetail{}, mapCallError(err, "call operation failed: ")
	}
	return domainCall.CallDetail{
		CallID: info.CallID, State: info.State, Direction: info.Direction,
		Peer: domainCall.CallPeer{PN: info.Peer.PN, LID: info.Peer.LID}, OfferSDP: offer,
	}, nil
}
```

In `src/ui/rest/call.go`, register `app.Get("/call/:call_id", rest.GetCall)` after
`app.Get("/calls/active", rest.ActiveCalls)`, and replace `AnswerCall` and `StartCall`:

```go
func (controller *Call) AnswerCall(c fiber.Ctx) error {
	var request domainCall.AnswerCallRequest
	utils.PanicIfNeeded(c.Bind().Body(&request))
	res, err := controller.Service.AnswerCall(whatsapp.ContextWithDevice(c.Context(), getDeviceFromCtx(c)), request)
	utils.PanicIfNeeded(err)
	return c.JSON(utils.ResponseData{Status: 200, Code: "SUCCESS", Message: "Call answered", Results: res})
}

func (controller *Call) StartCall(c fiber.Ctx) error {
	var request domainCall.StartCallRequest
	utils.PanicIfNeeded(c.Bind().Body(&request))
	res, err := controller.Service.StartCall(whatsapp.ContextWithDevice(c.Context(), getDeviceFromCtx(c)), request)
	utils.PanicIfNeeded(err)
	return c.JSON(utils.ResponseData{Status: 200, Code: "SUCCESS", Message: "Call placed", Results: res})
}

func (controller *Call) GetCall(c fiber.Ctx) error {
	res, err := controller.Service.GetCall(whatsapp.ContextWithDevice(c.Context(), getDeviceFromCtx(c)), c.Params("call_id"))
	utils.PanicIfNeeded(err)
	return c.JSON(utils.ResponseData{Status: 200, Code: "SUCCESS", Message: "Call", Results: res})
}
```

- [ ] **Step 4: Run the tests**

Run: `cd src && gofmt -l domains usecase ui infrastructure; GOTOOLCHAIN=auto go vet ./... && GOTOOLCHAIN=auto go test ./ui/rest/ ./usecase/ ./infrastructure/whatsapp/ ./domains/...`
Expected: no gofmt output; vet clean; `ok` everywhere.

- [ ] **Step 5: Update the OpenAPI document**

In `docs/openapi.yaml`:

Replace the `/call/answer` request schema and description with:

```yaml
      summary: Answer an incoming call with the browser's SDP
      description: |
        Answer a ringing call. `sdp` is the browser's answer to the call's `offer_sdp` (from the
        `call.offer` webhook or `GET /call/{call_id}`). GOWA answers on WhatsApp and bridges the
        audio. `results` has `call_id`. Requires calls on for the device.
```

```yaml
              required:
                - call_id
                - sdp
              properties:
                call_id:
                  type: string
                  description: Call id from the `call.offer` webhook (`payload.call_id`)
                sdp:
                  type: string
                  description: The browser's SDP answer (non-trickle, CRLF line endings)
```

and add a `'409'` response:

```yaml
        '409':
          description: The call was already answered
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/ErrorBadRequest'
```

Replace the `/call/start` description and schema with:

```yaml
      summary: Place an outgoing call with the browser's SDP offer
      description: |
        Place a voice call. `sdp` is the browser's offer; `results` has `call_id` and
        `sdp_answer` (GOWA's answer, apply it at once). Progress arrives as `call.status` webhooks.
```

```yaml
              required:
                - phone
                - sdp
              properties:
                phone:
                  type: string
                  description: Phone number in international format, e.g. +5511999999999, or digits only
                sdp:
                  type: string
                  description: The browser's SDP offer (non-trickle, CRLF line endings)
```

In the `/call/reject` description, add the line
`For a call GOWA tracks (calls on for the device) it also sends call.ended with status REJECTED.`

After `/calls/active`, add:

```yaml
  /call/{call_id}:
    get:
      operationId: getCall
      tags:
        - call
      summary: Get a call and its offer
      description: |
        `results` has `call_id`, `state`, `direction`, `peer` (`pn`, `lid`) and, while an incoming
        call rings, `offer_sdp`, so the browser can fetch the offer again.
      parameters:
        - $ref: '#/components/parameters/DeviceIdHeader'
        - name: call_id
          in: path
          required: true
          schema:
            type: string
      responses:
        '200':
          description: The call
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/GenericResponse'
        '400':
          description: Bad Request (calls not enabled for this device, or unknown or ended call)
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/ErrorBadRequest'

  /call/{call_id}/recording:
    get:
      operationId: getCallRecording
      tags:
        - call
      summary: Download a call recording
      description: |
        The stereo Ogg Opus recording (agent left, customer right). The signed link from
        `call.ended` (`exp`, `sig`) opens it without credentials; Basic Auth also works.
      parameters:
        - name: call_id
          in: path
          required: true
          schema:
            type: string
        - name: exp
          in: query
          schema:
            type: string
        - name: sig
          in: query
          schema:
            type: string
      responses:
        '200':
          description: The recording
          content:
            audio/ogg:
              schema:
                type: string
                format: binary
        '400':
          description: Invalid call id
        '401':
          description: Neither a valid signature nor Basic Auth
        '410':
          description: Deleted by retention, or never recorded

  /devices/{device_id}/calls:
    get:
      operationId: getDeviceCalls
      tags:
        - call
      summary: Whether calls are on for a device
      parameters:
        - name: device_id
          in: path
          required: true
          schema:
            type: string
      responses:
        '200':
          description: '`results.enabled`'
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/GenericResponse'
    put:
      operationId: setDeviceCalls
      tags:
        - call
      summary: Switch calls on or off for a device
      description: |
        Stored in the device registry and applied at once: off ends the device's live calls and
        leaves its next offers to the phone. Needs `WHATSAPP_CALLS_ENABLED=true` (400 otherwise).
        Calls carry a ban risk for the number.
      parameters:
        - name: device_id
          in: path
          required: true
          schema:
            type: string
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              required:
                - enabled
              properties:
                enabled:
                  type: boolean
      responses:
        '200':
          description: '`results.enabled`'
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/GenericResponse'
        '400':
          description: Bad Request (missing `enabled`, unknown device, or calls off on the gateway)
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/ErrorBadRequest'
```

Check the YAML parses:

```bash
cd ~/Documents/whatsapp-gateway-calls && python3 -c "import yaml,sys; yaml.safe_load(open('docs/openapi.yaml')); print('ok')"
```

Expected: `ok`. Without PyYAML, check the indentation by reading the diff.

- [ ] **Step 6: Commit**

```bash
git add src/infrastructure/whatsapp/calls.go src/infrastructure/whatsapp/calls_test.go src/domains/call/call.go \
  src/usecase/call.go src/usecase/call_test.go src/ui/rest/call.go src/ui/rest/call_test.go docs/openapi.yaml
git commit -m "feat(calls): answer and start with SDP, GET /call/:call_id, reject through the manager"
```

---
### Task 9: Call webhooks, the call contract and the payload docs

The manager's events become `call.status`, `call.answered` and `call.ended` bodies. They go
through `forwardPayloadToConfiguredWebhooks`, so in `WHATSAPP_WEBHOOK_DELIVERY=durable` they are
queued in the SQLite outbox (G3) and retried for 72 h. One process-wide queue with one worker
keeps the order in which they were emitted (status, answered, ended), and the engine never waits
on a webhook. `call.offer` (upstream event) gains its 2b fields through one marked line.

Known limit, documented below: a call refused by the cap can send `call.ended` before
`call.offer`, because `call.offer` leaves from upstream's own goroutine. Consumers match by
`call_id`.

**Files:**
- Create: `src/infrastructure/whatsapp/calls_webhook.go`, `src/infrastructure/whatsapp/calls_webhook_test.go`
- Modify: `src/infrastructure/whatsapp/event_call.go` (one line in `createCallOfferPayload`)
- Modify: `src/infrastructure/whatsapp/calls_registry.go` (`Emit: callEventSender(client)`)
- Create: `contract/call.schema.json`, `contract/call_schema_test.go`
- Create: `contract/call-fixtures/call-offer.json`, `call-status.json`, `call-answered.json`, `call-ended.json`, `call-ended-missed.json`
- Modify: `docs/webhook-payload.md`

**Interfaces:**
- Consumes: `CallEvent`, `EventCall*`, `DirectionUser` (Task 3); `(*CallManager).OfferSDP`
  (Task 3); `callrec.URLPath`, `callrec.Format` (Task 5); `forwardPayloadToConfiguredWebhooks`
  (G3); `resolvePeer`, `hasCallManager`, `callsByClient`.
- Produces: `callEventBody(deviceID string, ev CallEvent) map[string]any`,
  `callEventSender(client *whatsmeow.Client) func(CallEvent)`,
  `addCallOfferFields(ctx context.Context, payload map[string]any, evt *events.CallOffer, client *whatsmeow.Client)`,
  the test seam `forwardCallWebhook`, and the contract `contract/call.schema.json`.

- [ ] **Step 1: Write the contract fixtures**

The signature in `call-ended.json` is the real HMAC-SHA256 of
`statics|call-recording:A1B2C3D4E5F60718293A4B5C6D7E8F90|1792152000` with the secret
`test-secret`. 1792152000 is 2026-10-16T12:00:00Z, which is the event time plus the default
1440 minutes.

`contract/call-fixtures/call-status.json`:

```json
{
  "event": "call.status",
  "timestamp": "2026-10-15T12:00:00Z",
  "device_id": "5511900000000@s.whatsapp.net",
  "payload": {
    "call_id": "A1B2C3D4E5F60718293A4B5C6D7E8F90",
    "peer": { "pn": "5511900000001@s.whatsapp.net", "lid": "123456789012345@lid" },
    "direction": "BUSINESS_INITIATED",
    "status": "RINGING"
  }
}
```

`contract/call-fixtures/call-answered.json`:

```json
{
  "event": "call.answered",
  "timestamp": "2026-10-15T12:00:00Z",
  "device_id": "5511900000000@s.whatsapp.net",
  "payload": {
    "call_id": "A1B2C3D4E5F60718293A4B5C6D7E8F90",
    "peer": { "pn": "5511900000001@s.whatsapp.net", "lid": "123456789012345@lid" },
    "direction": "USER_INITIATED",
    "answered_by": "api"
  }
}
```

`contract/call-fixtures/call-ended.json`:

```json
{
  "event": "call.ended",
  "timestamp": "2026-10-15T12:00:00Z",
  "device_id": "5511900000000@s.whatsapp.net",
  "payload": {
    "call_id": "A1B2C3D4E5F60718293A4B5C6D7E8F90",
    "peer": { "pn": "5511900000001@s.whatsapp.net", "lid": "123456789012345@lid" },
    "direction": "USER_INITIATED",
    "status": "COMPLETED",
    "end_reason": "hangup_peer",
    "duration": 64,
    "recording": {
      "url": "/call/A1B2C3D4E5F60718293A4B5C6D7E8F90/recording?exp=1792152000&sig=0d3139625594f1833be6825b4a939f2eda423a8d6cdcc90057838b9d4e913fbf",
      "expires_at": "2026-10-16T12:00:00Z",
      "bytes": 190464,
      "duration": 64,
      "format": "ogg_opus_stereo",
      "channels": { "left": "agent", "right": "customer" }
    }
  }
}
```

`contract/call-fixtures/call-ended-missed.json`:

```json
{
  "event": "call.ended",
  "timestamp": "2026-10-15T12:00:00Z",
  "device_id": "5511900000000@s.whatsapp.net",
  "payload": {
    "call_id": "A1B2C3D4E5F60718293A4B5C6D7E8F90",
    "peer": { "pn": "", "lid": "123456789012345@lid" },
    "direction": "USER_INITIATED",
    "status": "MISSED",
    "end_reason": "timeout",
    "duration": 0
  }
}
```

`contract/call-fixtures/call-offer.json` (schema only; the upstream keys stay open):

```json
{
  "event": "call.offer",
  "timestamp": "2026-10-15T12:00:00Z",
  "device_id": "5511900000000@s.whatsapp.net",
  "payload": {
    "call_id": "A1B2C3D4E5F60718293A4B5C6D7E8F90",
    "from": "5511900000001@s.whatsapp.net",
    "from_lid": "123456789012345@lid",
    "auto_rejected": false,
    "remote_platform": "android",
    "calls_enabled": true,
    "direction": "USER_INITIATED",
    "peer": { "pn": "5511900000001@s.whatsapp.net", "lid": "123456789012345@lid" },
    "offer_sdp": "v=0\r\no=- 1 2 IN IP4 203.0.113.7\r\ns=-\r\nt=0 0\r\na=ice-lite\r\n"
  }
}
```

- [ ] **Step 2: Write the schema and its failing test**

`contract/call.schema.json`:

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "GOWA fork call webhooks, schema 1 (voice calls stage 2b)",
  "type": "object",
  "required": ["event", "timestamp", "device_id", "payload"],
  "additionalProperties": false,
  "properties": {
    "event": { "enum": ["call.offer", "call.status", "call.answered", "call.ended"] },
    "timestamp": { "type": "string" },
    "device_id": { "type": "string" },
    "session_id": { "type": "string" },
    "payload": { "type": "object" }
  },
  "allOf": [
    { "if": { "properties": { "event": { "const": "call.offer" } } }, "then": { "properties": { "payload": { "$ref": "#/$defs/offer" } } } },
    { "if": { "properties": { "event": { "const": "call.status" } } }, "then": { "properties": { "payload": { "$ref": "#/$defs/status" } } } },
    { "if": { "properties": { "event": { "const": "call.answered" } } }, "then": { "properties": { "payload": { "$ref": "#/$defs/answered" } } } },
    { "if": { "properties": { "event": { "const": "call.ended" } } }, "then": { "properties": { "payload": { "$ref": "#/$defs/ended" } } } }
  ],
  "$defs": {
    "peer": {
      "type": "object",
      "required": ["pn", "lid"],
      "additionalProperties": false,
      "properties": {
        "pn": { "type": "string", "pattern": "^$|^[0-9]+@s\\.whatsapp\\.net$" },
        "lid": { "type": "string", "pattern": "^$|^[0-9]+@lid$" }
      }
    },
    "common": {
      "required": ["call_id", "peer", "direction"],
      "properties": {
        "call_id": { "type": "string", "minLength": 1 },
        "peer": { "$ref": "#/$defs/peer" },
        "direction": { "enum": ["USER_INITIATED", "BUSINESS_INITIATED"] }
      }
    },
    "offer": {
      "type": "object",
      "allOf": [{ "$ref": "#/$defs/common" }],
      "required": ["calls_enabled"],
      "properties": {
        "calls_enabled": { "type": "boolean" },
        "direction": { "const": "USER_INITIATED" },
        "offer_sdp": { "type": "string", "pattern": "^v=0\\r\\n" }
      }
    },
    "status": {
      "type": "object",
      "allOf": [{ "$ref": "#/$defs/common" }],
      "required": ["status"],
      "properties": { "status": { "enum": ["RINGING", "ACCEPTED", "REJECTED"] } },
      "unevaluatedProperties": false
    },
    "answered": {
      "type": "object",
      "allOf": [{ "$ref": "#/$defs/common" }],
      "required": ["answered_by"],
      "properties": { "answered_by": { "enum": ["api", "elsewhere"] } },
      "unevaluatedProperties": false
    },
    "ended": {
      "type": "object",
      "allOf": [{ "$ref": "#/$defs/common" }],
      "required": ["status", "end_reason", "duration"],
      "properties": {
        "status": { "enum": ["COMPLETED", "FAILED", "MISSED", "REJECTED"] },
        "end_reason": { "enum": ["hangup_agent", "hangup_peer", "rejected", "timeout", "answered_elsewhere", "error"] },
        "duration": { "type": "integer", "minimum": 0 },
        "recording": { "$ref": "#/$defs/recording" }
      },
      "unevaluatedProperties": false
    },
    "recording": {
      "type": "object",
      "required": ["url", "expires_at", "bytes", "duration", "format", "channels"],
      "additionalProperties": false,
      "properties": {
        "url": { "type": "string", "pattern": "^(/[^?]*)?/call/[A-Za-z0-9_-]+/recording(\\?exp=[0-9]+&sig=[0-9a-f]{64})?$" },
        "expires_at": { "type": "string" },
        "bytes": { "type": "integer", "minimum": 1 },
        "duration": { "type": "integer", "minimum": 0 },
        "format": { "const": "ogg_opus_stereo" },
        "channels": {
          "type": "object",
          "required": ["left", "right"],
          "additionalProperties": false,
          "properties": { "left": { "const": "agent" }, "right": { "const": "customer" } }
        }
      }
    }
  }
}
```

`contract/call_schema_test.go`:

```go
package contract

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func compileCallSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	schema, err := jsonschema.NewCompiler().Compile("call.schema.json")
	if err != nil {
		t.Fatalf("compile call schema: %v", err)
	}
	return schema
}

func TestCallFixturesMatchSchema(t *testing.T) {
	schema := compileCallSchema(t)
	files, _ := filepath.Glob(filepath.Join("call-fixtures", "*.json"))
	if len(files) < 5 {
		t.Fatalf("found %d call fixtures, want 5", len(files))
	}
	for _, f := range files {
		t.Run(f, func(t *testing.T) {
			if err := schema.Validate(loadJSON(t, f)); err != nil {
				t.Errorf("%v", err)
			}
		})
	}
}

func TestCallSchemaRejectsViolations(t *testing.T) {
	schema := compileCallSchema(t)
	mutate := func(fn func(p map[string]any)) any {
		doc := loadJSON(t, filepath.Join("call-fixtures", "call-ended.json")).(map[string]any)
		raw, _ := json.Marshal(doc)
		var out map[string]any
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		_ = dec.Decode(&out)
		fn(out["payload"].(map[string]any))
		return out
	}
	cases := map[string]any{
		"unknown status":     mutate(func(p map[string]any) { p["status"] = "DONE" }),
		"unknown reason":     mutate(func(p map[string]any) { p["end_reason"] = "bye" }),
		"2a direction":       mutate(func(p map[string]any) { p["direction"] = "inbound" }),
		"extra key":          mutate(func(p map[string]any) { p["reason"] = "x" }),
		"masked peer":        mutate(func(p map[string]any) { p["peer"].(map[string]any)["pn"] = "…7777@s.whatsapp.net" }),
		"missing lid":        mutate(func(p map[string]any) { delete(p["peer"].(map[string]any), "lid") }),
		"wav recording":      mutate(func(p map[string]any) { p["recording"].(map[string]any)["format"] = "wav" }),
		"absolute url":       mutate(func(p map[string]any) { p["recording"].(map[string]any)["url"] = "https://x/call/A/recording" }),
		"channels swapped":   mutate(func(p map[string]any) { p["recording"].(map[string]any)["channels"] = map[string]any{"left": "customer", "right": "agent"} }),
		"negative duration":  mutate(func(p map[string]any) { p["duration"] = json.Number("-1") }),
	}
	for name, doc := range cases {
		if err := schema.Validate(doc); err == nil {
			t.Errorf("%s: the schema accepted it", name)
		}
	}
}
```

Run: `cd ~/Documents/whatsapp-gateway-calls/contract && GOTOOLCHAIN=auto go test ./... -run 'Call'`
Expected: PASS. The schema and fixtures come together. The existing `TestFixturesMatchSchema`
does not see `call-fixtures/`, because it walks `fixtures/` only. If a violation case passes,
fix the schema, not the case.

- [ ] **Step 3: Write the failing GOWA tests**

`src/infrastructure/whatsapp/calls_webhook_test.go`:

```go
package whatsapp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func TestCallEventBodiesMatchTheContractFixtures(t *testing.T) {
	secret, base, ttl := config.AppStaticsSecret, config.AppBasePath, config.WhatsappCallsRecordingURLTTLMinutes
	t.Cleanup(func() {
		config.AppStaticsSecret, config.AppBasePath, config.WhatsappCallsRecordingURLTTLMinutes = secret, base, ttl
	})
	config.AppStaticsSecret, config.AppBasePath, config.WhatsappCallsRecordingURLTTLMinutes = "test-secret", "", 1440

	at := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	peer := CallPeer{PN: "5511900000001@s.whatsapp.net", LID: "123456789012345@lid"}
	in := CallInfo{CallID: "A1B2C3D4E5F60718293A4B5C6D7E8F90", Direction: DirectionUser, Peer: peer}
	out := in
	out.Direction = DirectionBusiness
	missed := in
	missed.Peer = CallPeer{LID: "123456789012345@lid"}

	cases := map[string]CallEvent{
		"call-status.json":   {Type: EventCallStatus, Call: out, Status: "RINGING", At: at},
		"call-answered.json": {Type: EventCallAnswered, Call: in, AnsweredBy: "api", At: at},
		"call-ended.json": {Type: EventCallEnded, Call: in, Status: "COMPLETED", EndReason: "hangup_peer", Duration: 64, At: at,
			Recording: &RecordingInfo{Path: "storages/calls/A1B2C3D4E5F60718293A4B5C6D7E8F90.ogg", Bytes: 190464, Duration: 64 * time.Second}},
		"call-ended-missed.json": {Type: EventCallEnded, Call: missed, Status: "MISSED", EndReason: "timeout", At: at},
	}
	for name, ev := range cases {
		raw, err := os.ReadFile(filepath.Join("..", "..", "..", "contract", "call-fixtures", name))
		require.NoError(t, err)
		got, err := json.Marshal(callEventBody("5511900000000@s.whatsapp.net", ev))
		require.NoError(t, err)
		assert.JSONEq(t, string(raw), string(got), name)
	}
}

func TestCallWebhooksKeepOrder(t *testing.T) {
	var mu sync.Mutex
	var got []string
	orig := forwardCallWebhook
	forwardCallWebhook = func(_ context.Context, body map[string]any, event string) error {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, event+":"+body["payload"].(map[string]any)["call_id"].(string))
		return nil
	}
	t.Cleanup(func() { forwardCallWebhook = orig })

	send := callEventSender(nil)
	for _, typ := range []string{EventCallStatus, EventCallAnswered, EventCallEnded} {
		send(CallEvent{Type: typ, Call: CallInfo{CallID: "ORDER-1"}, At: time.Now()})
	}
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) == 3
	}, 2*time.Second, 5*time.Millisecond)
	assert.Equal(t, []string{"call.status:ORDER-1", "call.answered:ORDER-1", "call.ended:ORDER-1"}, got)
}

func TestCallOfferFields(t *testing.T) {
	withCallsConfig(t, false, nil)
	evt := &events.CallOffer{}
	evt.CallID = "IN-1"
	evt.CallCreator = types.NewJID("123456789012345", types.HiddenUserServer)
	client := &whatsmeow.Client{}

	payload := map[string]any{}
	addCallOfferFields(context.Background(), payload, evt, client)
	assert.Empty(t, payload, "flag off: call.offer stays as upstream builds it")

	config.WhatsappCallsEnabled = true
	eng := &fakeEngine{}
	m := newCallManager(eng, callManagerOptions{Media: &fakeMediaFactory{}, OfferWait: 200 * time.Millisecond})
	callsByClient.Store(client, m)
	t.Cleanup(func() { callsByClient.Delete(client) })
	eng.incoming(&fakeCall{id: "IN-1"})

	addCallOfferFields(context.Background(), payload, evt, client)
	assert.Equal(t, true, payload["calls_enabled"])
	assert.Equal(t, DirectionUser, payload["direction"])
	assert.Equal(t, map[string]any{"pn": "", "lid": "123456789012345@lid"}, payload["peer"])
	assert.Equal(t, "v=0 gowa-offer", payload["offer_sdp"])

	config.WhatsappCallsDevices = []string{"another-device"}
	resetDeviceCallsCache()
	off := map[string]any{}
	addCallOfferFields(context.Background(), off, evt, client)
	assert.Equal(t, false, off["calls_enabled"])
	assert.NotContains(t, off, "offer_sdp")
}
```

Run: `cd src && GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/ -run 'CallEventBodies|CallWebhooks|CallOfferFields'`
Expected: FAIL to build (`undefined: callEventBody`).

- [ ] **Step 4: Implement**

`src/infrastructure/whatsapp/calls_webhook.go`:

```go
package whatsapp

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/callrec"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/safego"
	"github.com/sirupsen/logrus"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types/events"
)

// Fork (elphant): call webhooks of voice calls stage 2b. Contract: contract/call.schema.json;
// docs/webhook-payload.md. Delivery is forwardPayloadToConfiguredWebhooks, so durable mode (G3)
// applies.

// forwardCallWebhook is the delivery function; tests replace it.
var forwardCallWebhook = forwardPayloadToConfiguredWebhooks

type callWebhookJob struct {
	deviceID string
	ev       CallEvent
}

var (
	callWebhookQueue = make(chan callWebhookJob, 1024)
	callWebhookOnce  sync.Once
)

// callEventSender is a call manager's Emit: events join one ordered queue and leave from one
// worker, so a call's status, answered and ended arrive in that order and a slow endpoint never
// blocks the call engine.
func callEventSender(client *whatsmeow.Client) func(CallEvent) {
	return func(ev CallEvent) {
		callWebhookOnce.Do(func() { safego.Loop("calls-webhooks", runCallWebhooks) })
		select {
		case callWebhookQueue <- callWebhookJob{deviceID: deviceJIDOf(client), ev: ev}:
		default:
			logrus.Errorf("[CALLS] webhook queue full; dropped %s of call %s", ev.Type, ev.Call.CallID)
		}
	}
}

func runCallWebhooks() {
	for job := range callWebhookQueue {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := forwardCallWebhook(ctx, callEventBody(job.deviceID, job.ev), job.ev.Type); err != nil {
			logrus.Errorf("[CALLS] webhook %s of call %s: %v", job.ev.Type, job.ev.Call.CallID, err)
		}
		cancel()
	}
}

func deviceJIDOf(client *whatsmeow.Client) string {
	if client == nil || client.Store == nil || client.Store.ID == nil {
		return ""
	}
	return client.Store.ID.ToNonAD().String()
}

func peerPayload(p CallPeer) map[string]any { return map[string]any{"pn": p.PN, "lid": p.LID} }

// callEventBody builds the webhook body of a call.status, call.answered or call.ended event.
func callEventBody(deviceID string, ev CallEvent) map[string]any {
	payload := map[string]any{
		"call_id":   ev.Call.CallID,
		"peer":      peerPayload(ev.Call.Peer),
		"direction": ev.Call.Direction,
	}
	switch ev.Type {
	case EventCallStatus:
		payload["status"] = ev.Status
	case EventCallAnswered:
		payload["answered_by"] = ev.AnsweredBy
	case EventCallEnded:
		payload["status"] = ev.Status
		payload["end_reason"] = ev.EndReason
		payload["duration"] = ev.Duration
		if ev.Recording != nil && ev.Recording.Bytes > 0 {
			payload["recording"] = recordingPayload(ev.Call.CallID, ev.Recording, ev.At)
		}
	}
	body := map[string]any{"event": ev.Type, "timestamp": ev.At.UTC().Format(time.RFC3339), "payload": payload}
	if deviceID != "" {
		body["device_id"] = deviceID
	}
	return body
}

// recordingPayload is call.ended's recording object (B6). The URL is relative to the server
// root, like stable.media.url.
func recordingPayload(callID string, rec *RecordingInfo, now time.Time) map[string]any {
	expires := now.Add(time.Duration(max(1, config.WhatsappCallsRecordingURLTTLMinutes)) * time.Minute)
	return map[string]any{
		"url":        callrec.URLPath(config.AppBasePath, callID, []byte(config.AppStaticsSecret), expires),
		"expires_at": expires.UTC().Format(time.RFC3339),
		"bytes":      rec.Bytes,
		"duration":   int(math.Round(rec.Duration.Seconds())),
		"format":     callrec.Format,
		"channels":   map[string]any{"left": "agent", "right": "customer"},
	}
}

// addCallOfferFields adds stage 2b's fields to call.offer: calls_enabled, direction, peer and,
// for a device with calls on, offer_sdp (GOWA's SDP offer, B3). With the master switch off the
// payload stays as upstream builds it.
func addCallOfferFields(ctx context.Context, payload map[string]any, evt *events.CallOffer, client *whatsmeow.Client) {
	if !config.WhatsappCallsEnabled || evt == nil {
		return
	}
	var m *CallManager
	if v, ok := callsByClient.Load(client); ok && client != nil {
		m = v.(*CallManager)
	}
	enabled := m != nil && hasCallManager(client) && evt.GroupJID.IsEmpty()
	payload["calls_enabled"] = enabled
	payload["direction"] = DirectionUser
	payload["peer"] = peerPayload(resolvePeer(client, evt.CallCreator, ""))
	if !enabled {
		return
	}
	sdp, err := m.OfferSDP(ctx, evt.CallID)
	if err != nil {
		logrus.Warnf("[CALLS] call.offer %s without offer_sdp: %v", evt.CallID, MaskDigits(err.Error()))
		return
	}
	payload["offer_sdp"] = sdp
}
```

In `src/infrastructure/whatsapp/event_call.go`, in `createCallOfferPayload`, right after
`payload["auto_rejected"] = autoRejected`:

```go
	addCallOfferFields(ctx, payload, evt, client) // Fork (elphant): calls_enabled, direction, peer and offer_sdp (voice calls 2b).
```

In `src/infrastructure/whatsapp/calls_registry.go`, add `Emit: callEventSender(client),` to the
`callManagerOptions` in `attachCalls`.

- [ ] **Step 5: Run the tests**

Run: `cd src && gofmt -l infrastructure; GOTOOLCHAIN=auto go vet ./infrastructure/whatsapp/ && GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/ -race`
Expected: no gofmt output; vet clean; `ok`, including the existing `call.offer` webhook tests.
Those run with the flag off, so their payloads are unchanged.

- [ ] **Step 6: Document the events**

In `docs/webhook-payload.md`:
- In the event table near the top, after the `call.offer` row, add:

```markdown
| `call.status`        | Outbound call progress (fork, voice calls; `WHATSAPP_CALLS_ENABLED=true`) |
| `call.answered`      | Media flows on both legs of a call (fork, voice calls)  |
| `call.ended`         | A call is over, with its recording (fork, voice calls)  |
```

- Add `call.status`, `call.answered` and `call.ended` to the `event` enumeration in the common
  fields table.
- At the end of the call events section, after "Rejecting calls programmatically", add:

````markdown
### Voice call events (fork, stage 2b)

With `WHATSAPP_CALLS_ENABLED=true` (and calls on for the device, `PUT /devices/:device_id/calls`)
GOWA carries calls itself, in the shape of Meta's Cloud Calling. The contract is
`contract/call.schema.json` (fixtures in `contract/call-fixtures/`). Every call event has
`device_id`, `timestamp`, and in `payload`: `call_id`, `peer` (`pn` and `lid`, full JIDs, `""`
when unknown) and `direction` (`USER_INITIATED` for an incoming call, `BUSINESS_INITIATED` for an
outbound one). They use the durable delivery when `WHATSAPP_WEBHOOK_DELIVERY=durable`.

**`call.offer`** keeps its fields and gains, with the master switch on: `calls_enabled`,
`direction`, `peer`, and `offer_sdp` when calls are on for the device. `offer_sdp` is GOWA's SDP
offer: non-trickle, CRLF lines, ICE-lite, DTLS `actpass`, Opus 48 kHz. The browser answers it
and sends the answer with `POST /call/answer {call_id, sdp}`. `GET /call/{call_id}` returns the
offer again while the call rings. Group calls have `calls_enabled: false` and ring on the phone.

**`call.status`** (outbound only): `status` is `RINGING` (the peer's phone rings), `ACCEPTED` or
`REJECTED`.

**`call.answered`**: media flows on both legs. `answered_by` is `api` (answered through GOWA) or
`elsewhere` (the number's phone or another linked device took the call; GOWA carries no audio).

**`call.ended`**:

| Field | Values |
|---|---|
| `status` | `COMPLETED` (media flowed on both legs, or answered elsewhere), `FAILED`, `MISSED` (rang out, cancelled, or refused by `WHATSAPP_CALLS_MAX_PER_DEVICE`), `REJECTED` |
| `end_reason` | `hangup_agent`, `hangup_peer`, `rejected`, `timeout` (ring window or `WHATSAPP_CALLS_MAX_DURATION_MINUTES`), `answered_elsewhere`, `error` (the browser leg was lost for `WHATSAPP_CALLS_RTC_GRACE_SECONDS`, or a signalling error) |
| `duration` | Seconds of media on both legs (0 when none) |
| `recording` | Present when audio was recorded: `url`, `expires_at`, `bytes`, `duration`, `format` (`ogg_opus_stereo`), `channels` (`left: agent`, `right: customer`) |

```json
{
  "event": "call.ended",
  "timestamp": "2026-10-15T12:00:00Z",
  "device_id": "5511900000000@s.whatsapp.net",
  "payload": {
    "call_id": "A1B2C3D4E5F60718293A4B5C6D7E8F90",
    "peer": { "pn": "5511900000001@s.whatsapp.net", "lid": "123456789012345@lid" },
    "direction": "USER_INITIATED",
    "status": "COMPLETED",
    "end_reason": "hangup_peer",
    "duration": 64,
    "recording": {
      "url": "/call/A1B2C3D4E5F60718293A4B5C6D7E8F90/recording?exp=1792152000&sig=0d31…",
      "expires_at": "2026-10-16T12:00:00Z",
      "bytes": 190464,
      "duration": 64,
      "format": "ogg_opus_stereo",
      "channels": { "left": "agent", "right": "customer" }
    }
  }
}
```

- `recording.url` is relative to the server root (`APP_BASE_PATH` included), signed with
  `APP_STATICS_SECRET` and valid `WHATSAPP_CALLS_RECORDING_URL_TTL_MINUTES` (default 1440). Without
  a secret it is the bare path. The route also accepts Basic Auth, so a webhook delivered late by
  the durable retry can still fetch the file. Files are deleted after
  `WHATSAPP_CALLS_RECORDING_RETENTION_DAYS` (default 7); the route then answers 410.
- A call's events arrive in order (status, answered, ended). `call.offer` leaves from its own
  path: for a call refused by the cap, `call.ended` can arrive before `call.offer`. Match by
  `call_id`.
- `POST /call/reject` on a call GOWA tracks also sends `call.ended` (`REJECTED`, `rejected`).
````

- [ ] **Step 7: Commit**

```bash
git add src/infrastructure/whatsapp/calls_webhook.go src/infrastructure/whatsapp/calls_webhook_test.go \
  src/infrastructure/whatsapp/event_call.go src/infrastructure/whatsapp/calls_registry.go \
  contract/call.schema.json contract/call_schema_test.go contract/call-fixtures docs/webhook-payload.md
git commit -m "feat(calls): call.status, call.answered and call.ended webhooks with the call contract"
```

---
### Task 10: Test softphone page

The upstream Vue views are gone from this tree, because the dashboard is the separate `gowa-ui`
download. The test page is therefore one static HTML file embedded in the binary, served at
`GET <base>/calls/phone` behind Basic Auth, and 404 when calls are off. It makes the same
`RTCPeerConnection` calls as the CRM's `useWebRtc.js`:
- non-trickle: it waits for ICE gathering to complete, or 5 s;
- it normalises SDP to CRLF;
- inbound, it takes the gateway's offer and answers it;
- outbound, it offers and applies `sdp_answer`.

That lets 2b be tested without the CRM. `getUserMedia` needs a secure context: `localhost` on
the Mac, HTTPS through Traefik on the host.

**Files:**
- Create: `src/ui/rest/call_phone.go`, `src/ui/rest/call_phone.html`, `src/ui/rest/call_phone_test.go`
- Modify: `src/cmd/rest.go` (one line after `rest.InitRestDeviceCalls(...)`)

**Interfaces:**
- Consumes: REST routes from Tasks 2 and 8 (`GET /calls/active`, `GET /call/:call_id`,
  `POST /call/answer|start|hangup|reject`).
- Produces: `rest.InitRestCallPhone(app fiber.Router)`.

- [ ] **Step 1: Write the failing test**

`src/ui/rest/call_phone_test.go`:

```go
package rest

import (
	"io"
	"net/http/httptest"
	"testing"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCallPhonePage(t *testing.T) {
	orig := config.WhatsappCallsEnabled
	t.Cleanup(func() { config.WhatsappCallsEnabled = orig })
	app := fiber.New()
	InitRestCallPhone(app)

	config.WhatsappCallsEnabled = false
	resp, err := app.Test(httptest.NewRequest("GET", "/calls/phone", nil))
	require.NoError(t, err)
	assert.Equal(t, 404, resp.StatusCode)

	config.WhatsappCallsEnabled = true
	resp, err = app.Test(httptest.NewRequest("GET", "/calls/phone", nil))
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "text/html")
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{"new RTCPeerConnection", "waitIceComplete", `replace(/\r?\n/g, '\r\n')`,
		"/call/answer", "/call/start", "/call/hangup", "offer_sdp", "sdp_answer", "X-Device-Id"} {
		assert.Contains(t, string(body), want)
	}
}
```

Run: `cd src && GOTOOLCHAIN=auto go test ./ui/rest/ -run TestCallPhonePage`
Expected: FAIL to build (`undefined: InitRestCallPhone`).

- [ ] **Step 2: Implement the route and the page**

`src/ui/rest/call_phone.go`:

```go
package rest

import (
	_ "embed"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/gofiber/fiber/v3"
)

// Fork (elphant): a bare test softphone for voice calls stage 2b, to try the browser leg without
// the CRM. It makes the same RTCPeerConnection calls as the CRM's useWebRtc.js.

//go:embed call_phone.html
var callPhoneHTML []byte

func InitRestCallPhone(app fiber.Router) {
	app.Get("/calls/phone", func(c fiber.Ctx) error {
		if !config.WhatsappCallsEnabled {
			return c.SendStatus(fiber.StatusNotFound)
		}
		c.Set(fiber.HeaderCacheControl, "no-store")
		c.Type("html")
		return c.Send(callPhoneHTML)
	})
}
```

`src/ui/rest/call_phone.html`:

```html
<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>GOWA test phone</title>
<style>
  body { font-family: system-ui, sans-serif; max-width: 640px; margin: 2rem auto; padding: 0 1rem; }
  fieldset { margin-bottom: 1rem; }
  input { padding: .3rem; }
  button { margin: .25rem 0; }
  #log { font-family: monospace; font-size: 12px; white-space: pre-wrap; background: #f4f4f4; padding: .5rem; max-height: 240px; overflow: auto; }
</style>
</head>
<body>
<h1>GOWA test phone</h1>
<p>Voice calls stage 2b test page. Same WebRTC steps as the CRM: non-trickle SDP with CRLF lines; the gateway is ICE-lite and the DTLS server.</p>
<fieldset><legend>Device</legend><label>X-Device-Id <input id="device" placeholder="device id"></label></fieldset>
<fieldset><legend>Incoming</legend><div id="ringing">No calls.</div></fieldset>
<fieldset><legend>Outgoing</legend><input id="phone" placeholder="+5511999999999"> <button id="call">Call</button></fieldset>
<fieldset><legend>Current call</legend><span id="current">none</span> <button id="hangup" disabled>Hang up</button> <label><input type="checkbox" id="mute"> mute</label></fieldset>
<audio id="remote" autoplay></audio>
<div id="log"></div>
<script>
const base = location.pathname.replace(/\/calls\/phone\/?$/, '');
const $ = (id) => document.getElementById(id);
let pc = null;
let mic = null;
let currentId = null;

function log(msg) {
  $('log').textContent = new Date().toISOString().slice(11, 19) + ' ' + msg + '\n' + $('log').textContent;
}

// The gateway and Meta use CRLF line endings; some browsers hand back LF.
function normalizeSdp(sdp) {
  const s = sdp.replace(/\r?\n/g, '\r\n');
  return s.endsWith('\r\n') ? s : s + '\r\n';
}

async function api(method, path, body) {
  const res = await fetch(base + path, {
    method,
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json', 'X-Device-Id': $('device').value.trim() },
    body: body ? JSON.stringify(body) : undefined,
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(res.status + ' ' + (data.message || res.statusText));
  return data.results;
}

// Non-trickle: wait until ICE gathering completes (or 5 s), then read the whole SDP.
function waitIceComplete(peer) {
  if (peer.iceGatheringState === 'complete') return Promise.resolve();
  return new Promise((resolve) => {
    let done = false;
    const finish = () => {
      if (done) return;
      done = true;
      peer.removeEventListener('icegatheringstatechange', check);
      resolve();
    };
    const check = () => { if (peer.iceGatheringState === 'complete') finish(); };
    peer.addEventListener('icegatheringstatechange', check);
    setTimeout(finish, 5000);
  });
}

async function newPeer() {
  mic = await navigator.mediaDevices.getUserMedia({ audio: { echoCancellation: true, noiseSuppression: true }, video: false });
  pc = new RTCPeerConnection({ iceServers: [] });
  mic.getTracks().forEach((t) => pc.addTrack(t, mic));
  pc.ontrack = (e) => { $('remote').srcObject = e.streams[0] || new MediaStream([e.track]); };
  pc.oniceconnectionstatechange = () => log('ICE ' + pc.iceConnectionState);
  pc.onconnectionstatechange = () => log('connection ' + pc.connectionState);
}

function setCurrent(id) {
  currentId = id;
  $('current').textContent = id || 'none';
  $('hangup').disabled = !id;
}

function cleanup() {
  if (pc) pc.close();
  pc = null;
  if (mic) mic.getTracks().forEach((t) => t.stop());
  mic = null;
  setCurrent(null);
}

async function answer(callId) {
  try {
    const call = await api('GET', '/call/' + encodeURIComponent(callId));
    if (!call.offer_sdp) throw new Error('the call has no offer_sdp');
    await newPeer();
    await pc.setRemoteDescription({ type: 'offer', sdp: normalizeSdp(call.offer_sdp) });
    await pc.setLocalDescription(await pc.createAnswer());
    await waitIceComplete(pc);
    await api('POST', '/call/answer', { call_id: callId, sdp: normalizeSdp(pc.localDescription.sdp) });
    setCurrent(callId);
    log('answered ' + callId);
  } catch (err) {
    log('answer failed: ' + err.message);
    cleanup();
  }
}

async function reject(call) {
  try {
    await api('POST', '/call/reject', { call_id: call.call_id, caller_jid: call.peer.lid || call.peer.pn });
    log('rejected ' + call.call_id);
  } catch (err) {
    log('reject failed: ' + err.message);
  }
}

$('call').onclick = async () => {
  try {
    await newPeer();
    await pc.setLocalDescription(await pc.createOffer());
    await waitIceComplete(pc);
    const res = await api('POST', '/call/start', { phone: $('phone').value.trim(), sdp: normalizeSdp(pc.localDescription.sdp) });
    await pc.setRemoteDescription({ type: 'answer', sdp: normalizeSdp(res.sdp_answer) });
    setCurrent(res.call_id);
    log('calling ' + res.call_id);
  } catch (err) {
    log('call failed: ' + err.message);
    cleanup();
  }
};

$('hangup').onclick = async () => {
  const id = currentId;
  try {
    await api('POST', '/call/hangup', { call_id: id });
    log('hung up ' + id);
  } catch (err) {
    log('hangup: ' + err.message);
  }
  cleanup();
};

$('mute').onchange = () => {
  if (mic) mic.getAudioTracks().forEach((t) => { t.enabled = !$('mute').checked; });
};

async function poll() {
  if ($('device').value.trim()) {
    try {
      const calls = (await api('GET', '/calls/active')) || [];
      const box = $('ringing');
      box.textContent = '';
      calls.forEach((c) => {
        if (c.direction !== 'USER_INITIATED' || c.state !== 'ringing' || c.call_id === currentId) return;
        const row = document.createElement('div');
        row.textContent = (c.peer.pn || c.peer.lid) + ' ';
        const a = document.createElement('button');
        a.textContent = 'Answer';
        a.onclick = () => answer(c.call_id);
        const r = document.createElement('button');
        r.textContent = 'Reject';
        r.onclick = () => reject(c);
        row.append(a, ' ', r);
        box.append(row);
      });
      if (!box.childElementCount) box.textContent = 'No calls.';
      if (currentId && !calls.some((c) => c.call_id === currentId)) {
        log('call ' + currentId + ' ended');
        cleanup();
      }
    } catch (err) {
      $('ringing').textContent = err.message;
    }
  }
  setTimeout(poll, 2000);
}

try { $('device').value = localStorage.getItem('gowa-phone-device') || ''; } catch (e) { /* storage blocked */ }
$('device').onchange = () => { try { localStorage.setItem('gowa-phone-device', $('device').value.trim()); } catch (e) { /* ignore */ } };
poll();
</script>
</body>
</html>
```

In `src/cmd/rest.go`, right after the `rest.InitRestDeviceCalls(...)` line:

```go
	rest.InitRestCallPhone(apiGroup) // Fork (elphant): test softphone for voice calls (stage 2b); 404 unless calls are on.
```

- [ ] **Step 3: Run the tests**

Run: `cd src && gofmt -l ui cmd; GOTOOLCHAIN=auto go test ./ui/rest/ ./cmd/ -run 'CallPhone|DeviceGroup|Statics|Recording'`
Expected: no gofmt output; PASS. `TestDeviceGroupIsRegisteredLast` still passes.

- [ ] **Step 4: Commit**

```bash
git add src/ui/rest/call_phone.go src/ui/rest/call_phone.html src/ui/rest/call_phone_test.go src/cmd/rest.go
git commit -m "feat(calls): test softphone page for the browser leg"
```

---

### Task 11: Docs, debt and full verification

**Files:**
- Modify: `docs/reference/elphant-fork.md` (the "Voice calls" section)
- Modify: `docs/elphant-debt.md` (D-17)

**Interfaces:** none (docs and checks only).

- [ ] **Step 1: Rewrite the voice calls section of the fork page**

In `docs/reference/elphant-fork.md`, rename the heading to
`## Voice calls (stages 2a and 2b, off by default)` and replace its body with:

```markdown
GOWA carries WhatsApp voice calls through the fork
[eduardoelphant/meowcaller](https://github.com/eduardoelphant/meowcaller) (`v0.2.0-elphant.5`),
in the shape of Meta's Cloud Calling: the gateway offers SDP on an incoming call and the
browser answers; the browser offers on an outbound call and the gateway answers. Designs:
[2a](../specs/2026-10-14-gateway-voice-calls-design.md),
[2b](../specs/2026-10-14-gateway-voice-calls-2b-design.md).

Settings:

- `WHATSAPP_CALLS_ENABLED` (default `false`) is the master switch. Off means no change: `call.offer`,
  `POST /call/reject` and `WHATSAPP_AUTO_REJECT_CALL` behave as upstream.
- Per device: `PUT /devices/:device_id/calls {"enabled": true|false}` and
  `GET /devices/:device_id/calls`, stored in the chat storage database (table
  `elphant_device_calls`) and applied at once: off ends the device's live calls and leaves its
  next offers to the phone (`meowcaller.WithOfferFilter`). `WHATSAPP_CALLS_DEVICES` only seeds a
  device that has no stored value yet. Calls carry a ban risk for the number.
- `WHATSAPP_CALLS_CODEC` (`opus`), `WHATSAPP_CALLS_LOG_LEVEL` (`info`),
  `WHATSAPP_CALLS_EARLY_MEDIA` (`off`; `listen` for the CRM, where a human answers late),
  `WHATSAPP_CALLS_MAX_PER_DEVICE` (`1`; above it the offer is rejected and `call.ended` is `MISSED`),
  `WHATSAPP_CALLS_MAX_DURATION_MINUTES` (`120`; then GOWA hangs up and reports `FAILED`).
- Browser leg (pion, ICE-lite, DTLS server, Opus 48 kHz, non-trickle SDP): `WHATSAPP_CALLS_RTC_PORT`
  (`3478`, one UDP port for every call), `WHATSAPP_CALLS_RTC_PUBLIC_IP` (the host's public IPv4),
  `WHATSAPP_CALLS_RTC_GRACE_SECONDS` (`10`: a browser leg down that long hangs up the call).
- Recording: every answered call, stereo Ogg Opus (agent left, customer right, 24 kbps a
  channel) in `storages/calls/<call_id>.ogg`, written as the call runs. The link in `call.ended`
  is signed with `APP_STATICS_SECRET` for `WHATSAPP_CALLS_RECORDING_URL_TTL_MINUTES` (`1440`) and
  also opens with Basic Auth. Files are deleted after `WHATSAPP_CALLS_RECORDING_RETENTION_DAYS` (`7`).

Routes: `GET /call/:call_id` (with `offer_sdp` while it rings), `POST /call/answer {call_id, sdp}`
(409 when already answered), `POST /call/start {phone, sdp}` (`{call_id, sdp_answer}`),
`POST /call/hangup`, `POST /call/reject`, `GET /calls/active`, `GET /call/:call_id/recording`.
Webhooks: `call.offer` (extended), `call.status`, `call.answered`, `call.ended`; see
[webhook-payload.md](../webhook-payload.md) and `contract/call.schema.json`.

Test page: `GET /calls/phone` (Basic Auth, 404 with calls off) answers and places calls from a
browser with the same WebRTC steps as the CRM. `getUserMedia` needs HTTPS or `localhost`.

Infrastructure (gateway host, owner's OK each time):

- Publish UDP `3478` in `mode: host` in the Swarm stack (`ports: - target: 3478, published: 3478,
  protocol: udp, mode: host`) and open it in the host firewall. Traefik does not carry it.
- Set `WHATSAPP_CALLS_RTC_PUBLIC_IP` to the host's public IPv4 and `APP_STATICS_SECRET`.
- Outbound UDP from the container to Meta's relays (already working in 2a).
- No TURN: an agent behind a network that blocks UDP to the host gets no audio; add coturn only
  if that happens.
- CPU: 2a measured 10% to 25% of a core per call with one leg; the bridge adds a 48 kHz encode,
  a decode and resampling. Measure before raising `WHATSAPP_CALLS_MAX_PER_DEVICE`.

Build: meowcaller's `opus/` uses cgo (libopus and `pkg-config`; `opus-dev pkgconf` on Alpine,
already in the Dockerfile and CI). pion is pure Go. `CGO_ENABLED=0 -tags purego` builds still
compile; there calls stay on MLow and the browser leg reports itself unavailable.
```

- [ ] **Step 2: Update D-17**

In `docs/elphant-debt.md`, under D-17 "Stage 2a … Open items":
- Mark (d) closed: "CLOSED in 2b: REST and webhooks carry the full `pn`/`lid`; logs stay masked."
- Mark (e) closed: "CLOSED in 2b: `WHATSAPP_CALLS_MAX_DURATION_MINUTES` finishes a call even
  when the engine never reports its end."
- Mark (f) closed: "CLOSED in 2b: retention deletes recordings (and 2a's `.wav` files)."
- In the deferred minors, mark these as closed in 2b: "An expired offer is dropped without
  Reject; a failed Answer is not followed by Reject" and "Start returns State active even if the
  call ended during setup".
- In "Still open", replace "The end reason is empty when the peer hangs up" with "Mapped to
  `hangup_peer` in 2b".

Then add a new bullet list "Stage 2b, open after the code":

```markdown
- **Stage 2b, open after the code.**
  - Runtime checks on the gateway host (plan 2026-10-15, Task 12) need the owner's OK.
  - For a call refused by the cap, `call.ended` can reach the webhook before `call.offer`
    (different paths); consumers match by `call_id`.
  - The bridge sends the three 20 ms browser packets of each 60 ms WhatsApp frame at once; pace
    them if a browser reports jitter.
  - No TURN; RTP from the browser is not reordered before decoding.
  - pion adds no `a=ptime:20`; GOWA sends 20 ms frames anyway.
```

- [ ] **Step 3: Full verification**

Run:

```bash
cd ~/Documents/whatsapp-gateway-calls/src
gofmt -l . | grep -v '^vendor/' ; echo "gofmt done"
GOTOOLCHAIN=auto go vet ./...
GOTOOLCHAIN=auto go test ./...
GOTOOLCHAIN=auto go test -race ./infrastructure/whatsapp/ ./pkg/callaudio/ ./pkg/callrec/ ./pkg/callrtc/ ./pkg/webhookoutbox/ ./ui/rest/ ./usecase/ ./cmd/
CGO_ENABLED=0 GOTOOLCHAIN=auto go build -tags purego -o /dev/null .
GOTOOLCHAIN=auto go build -o whatsapp . && rm whatsapp
cd ../contract && GOTOOLCHAIN=auto go test ./...
```

Expected:
- gofmt lists nothing before "gofmt done", and vet is clean.
- Every package is `ok`, including `TestEveryBackgroundGoroutineIsProtected`, the durable
  webhook tests and the watchdog tests.
- The race run is clean.
- The CGO-free build and the default build succeed.
- The contract tests pass.

The `pkg/callrtc` tests skip only in the CGO-free build. Fix any failure here before going on.

- [ ] **Step 4: Check the diff of upstream files**

Run: `cd ~/Documents/whatsapp-gateway-calls && git diff 33d1cbc --stat -- src ':!src/infrastructure/whatsapp/calls*' ':!src/pkg/callaudio' ':!src/pkg/callrec' ':!src/pkg/callrtc' ':!src/cmd/calls_flags*' ':!src/cmd/rest_calls*' ':!src/ui/rest/call_device*' ':!src/ui/rest/call_phone*'`
Expected: in upstream files, only single lines marked `// Fork (elphant)` were added, plus
edits inside existing 2a fork blocks: `cmd/root.go`, `config/calls.go`, `ui/rest/call.go`,
`domains/call/call.go` and `usecase/call.go`. Move anything bigger into a fork-owned file.

- [ ] **Step 5: Commit**

```bash
git add docs/reference/elphant-fork.md docs/elphant-debt.md
git commit -m "docs(calls): stage 2b settings, routes, infrastructure and debt"
```

---

### Task 12: Runtime checks (owner present; the owner's explicit OK for each server step)

Nothing here runs without the owner. The Mac steps use a dedicated test number (see the
calls-spike notes), not a customer number.

- [ ] **Step 1: Local run on the Mac**

```bash
cd ~/Documents/whatsapp-gateway-calls/src
WHATSAPP_CALLS_ENABLED=true WHATSAPP_CALLS_EARLY_MEDIA=listen APP_STATICS_SECRET=local-test \
  GOTOOLCHAIN=auto go run . rest
```

Pair the test device, then run `PUT /devices/<id>/calls {"enabled":true}`. Open
`http://localhost:3000/calls/phone`, set the device id, and check each item:
- An incoming call from another phone shows up in the page. Answer it, and audio flows both
  ways.
- `call.offer` (with `offer_sdp`), `call.answered` and `call.ended` (with `recording`) arrive at
  a local webhook receiver.
- The recording link plays in a browser without credentials. `ffprobe` shows 2 channels, Opus,
  with the agent on the left.
- An outbound call from the page rings (`call.status RINGING`), connects (`ACCEPTED`) and hangs
  up from the page (`hangup_agent`).
- Closing the tab mid-call: about `WHATSAPP_CALLS_RTC_GRACE_SECONDS` later the phone's call ends,
  and `call.ended` has `end_reason: error`.
- `PUT /devices/<id>/calls {"enabled":false}`: the next call rings on the phone only, and
  `call.offer` has `calls_enabled: false`.

- [ ] **Step 2: Image and stack (needs the owner's explicit OK)**

With the OK:
1. Build the image from this branch on the gateway host, as for `.4` to `.6`, because Actions is
   disabled on the gateway repository.
2. Add to the `gowa` stack: the UDP 3478 port in `mode: host`, `WHATSAPP_CALLS_RTC_PUBLIC_IP`,
   `APP_STATICS_SECRET` (if it is not set yet) and `WHATSAPP_CALLS_EARLY_MEDIA=listen`.
3. Open UDP 3478 in the host firewall.
4. Calls stay on only for the DDD 11 device.

- [ ] **Step 3: Live calls through the test page on the host (owner present)**

Repeat Step 1's list over HTTPS. Run inbound from Android and from WhatsApp Web, three calls
each, and also an outbound call. For each call, record:
- whether there is audio both ways;
- the recording's channels and length against the call;
- CPU per call (`docker stats`), compared with 2a's 10% to 25%;
- whether messages still flow during the call.

- [ ] **Step 4: Record the results**

Add the results to D-17 in `docs/elphant-debt.md`. Use no number, IP or token. Then commit:

```bash
git add docs/elphant-debt.md
git commit -m "docs(calls): stage 2b runtime results on the gateway host"
```
