# meowcaller calls spike: measured results

Stage 1 of [the calls handoff](../handoff/2026-10-13-meowcaller-calls.md), run on 2026-10-13/14 with
the prototype in `spikes/meowcaller/`. Everything here was measured, not estimated, unless it says
otherwise.

## Setup

- meowcaller pinned at `27a3c6b` (last commit before the `polymorfa/hypermeow` switch), built
  against GOWA's own `go.mau.fi/whatsmeow` version. It builds, and meowcaller's whole test suite
  passes on that whatsmeow version.
- Account: a WhatsApp Business number in coexistence with the Cloud API (its device list carries a
  `:99@hosted.lid` device). The owner accepted the ban risk for this number.
- Runs: first on the gateway host, reusing the session of the GOWA device (gateway stopped during
  the test, then restored with both devices `logged_in`); then on a Mac with a linked device paired
  by the prototype itself.

## Results

| Test | Result |
|---|---|
| Outbound, 5 min | Answered, media ready in 2.6 s, ran the full 300 s, ended by our own limit. Owner heard the audio clean. |
| Inbound, 15 min | Answered in 68 ms, media ready in 1.1 s, ran the full 900 s, ended by our own limit. No error, no logout, no ban. |
| Inbound, short | Answered in 64 ms, media ready in 1.1 s. |
| Outbound, 3 min (with PR #38) | Media ready in 12.4 s on this run, then the full 180 s. |

The reported "drops after about 20 seconds" did not happen in any call.

## Findings

- **A call placed from the same phone never reaches linked devices.** Both test numbers live on
  one phone. A call from that phone rang only the phone itself; no `<call>` offer reached any
  linked device (the GOWA device, or two devices paired by the prototype), only the `<reject>`
  broadcast when it was declined. The same call placed from WhatsApp Web on another device
  arrived and was answered. This cost most of the session; test inbound calls from a separate
  device.
- **Coexistence did not block inbound calls** in these tests, unlike meowcaller issue #35
  (`uncallable` reject from the hosted device). Not proven for every account.
- **Recording gaps.** The WAV of the peer's audio covers 50% to 85% of the call time, with speech
  present to the end. The recorder seems to write only decoded frames, so silences are missing.
  This is a recorder defect, not lost call audio, but it is not confirmed by listening yet.
- **CPU is the main cost.** One call takes about 45% to 50% of one core on the Mac (Intel),
  steady for the whole call. Memory stays around 40 to 70 MB. Bandwidth is about 16 to 23 kbps
  each way.
  - 84% of that CPU is the MLow encoder (`MlowEncoder.Encode`, mostly CELP search and FFT).
  - meowcaller PR #38 (FFT twiddle cache, MIT, open) makes the encoder benchmark 2.2x faster on
    the Mac (5.9 ms to 2.7 ms per 60 ms frame) and passes the `mlow` tests, but did **not** lower
    in-call CPU (47.6% with it against 48.6% without).
  - The same encoder, paced at 60 ms in a test, costs 3.4 ms per frame and 6.8% of a core. In the
    call it costs about 24 ms per frame.
  - **Cause of that gap: subnormal floats.** When speech turns to silence, the encoder's
    high-pass filter state (`hpState`) and history buffers decay into the subnormal float range,
    which is several times slower on x86, and the frame no longer takes the cheap silent branch.
    Measured on the Mac: 5.3 ms per frame for silence on a fresh encoder, 6.1 ms for speech, 17.7
    to 21.9 ms for silence after speech, 6.0 ms with 1-LSB dither.
  - **Fix measured in a live call:** dither before `Encode` plus PR #38 brought a 3-minute
    outbound call from about 48% to **about 11.5% of one core** (7.7% to 12.8% over the call).
    Patches in `spikes/meowcaller/patches/`.

## Not done yet

- CPU on the gateway host's hardware.
- Text messages sent and received during a call (stage 2 item 4).
- Stage 2: running inside GOWA behind a flag.
