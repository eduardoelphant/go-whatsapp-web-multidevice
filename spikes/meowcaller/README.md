# meowcaller spike

Stage 1 of [the calls handoff](../../docs/handoff/2026-10-13-meowcaller-calls.md): a standalone
prototype, its own Go module, so `src/go.mod` stays untouched.

It pins meowcaller at `27a3c6b` (the last commit before the switch to the `polymorfa/hypermeow`
fork) and uses GOWA's own `go.mau.fi/whatsmeow` version. meowcaller's test suite passes against
that whatsmeow version.

Everything under `data/` is git-ignored: the session database, recordings, the binary and the
test audio.

```sh
cd spikes/meowcaller
GOTOOLCHAIN=auto go build -o data/spike .
say -o data/hello.wav --data-format=LEI16@16000 "test call"   # test audio, 16 kHz mono

data/spike pair                                  # QR opens from data/qr.png, deleted after pairing
data/spike answer -play data/hello.wav -max 5m   # auto-answer incoming calls
data/spike call -play data/hello.wav -max 5m +55XXXXXXXXXXX
```

The peer's audio goes to `data/rec/<call-id>.wav`. The log prints one `call step` line per step
(offer, answered or peer_accepted, media_ready, ended), a `call summary` line per call, and a
`stats` line every 10 seconds (CPU, RSS, bytes in and out from `nettop`). Peers are logged with
only the last four digits. `LOGGED OUT`, `STREAM REPLACED` and `TEMPORARY BAN` are logged as
errors: stop at the first one.

Set `SPIKE_LOG_LEVEL=debug` (or `trace`) for meowcaller's media logs.

meowcaller is MIT licensed (Copyright purpshell). This prototype only calls its public API; the
login boilerplate follows its `examples/cli`.
