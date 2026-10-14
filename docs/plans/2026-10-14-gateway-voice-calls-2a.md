# Voice calls, stage 2a (library in GOWA) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** GOWA can answer and place WhatsApp voice calls on allowed devices through REST, with
Opus audio from a test file and a recording of the peer. The feature stays behind a flag, off by
default, and the whole suite stays green.

**Architecture:**
- meowcaller (MIT) comes from our fork `eduardoelphant/meowcaller` (tag `v0.1.0-elphant.1`),
  wired by a `replace` in `src/go.mod`. The fork carries the patches and a small cgo binding to
  libopus.
- Each allowed device's `*whatsmeow.Client` gets one `CallManager`, created right after
  `whatsmeow.NewClient` and before any `Connect`. The manager is kept in a package map keyed by
  client.
- The usecase reaches the manager through the request's device context. Four REST routes expose
  answer, start, hang up and list.
- No browser leg yet (stage 2b) and no webhooks yet (stage 2c).

**Tech Stack:** Go 1.26 (`GOTOOLCHAIN=auto`), whatsmeow, meowcaller `27a3c6b` + patches,
meowcaller fork `v0.1.0-elphant.1`, libopus 1.5 through cgo (`pkg-config: opus`), Fiber v3, testify.

**Spec:** `docs/specs/2026-10-14-gateway-voice-calls-design.md` (decisions D1-D4 and D9 are in
scope here; read the spec first).

## Global Constraints

- Work in the worktree `~/Documents/whatsapp-gateway-calls`, branch `spike/meowcaller-calls`.
  Local only: no push, tag, image or PR.
- Run Go from `src/` with `GOTOOLCHAIN=auto`.
- `WHATSAPP_CALLS_ENABLED` defaults to `false`. With the flag off, behaviour must be identical to
  today, including `call.offer`, `POST /call/reject` and `WHATSAPP_AUTO_REJECT_CALL`.
- `WHATSAPP_CALLS_DEVICES`: comma-separated device ids or phone numbers. Empty means all devices
  when enabled.
- `WHATSAPP_CALLS_CODEC`: `opus` (default) or `mlow`.
- `WHATSAPP_CALLS_MAX_PER_DEVICE`: default `1`.
- `WHATSAPP_CALLS_TEST_AUDIO`: path to a `.wav`/`.mp3`/`.opus` played to the peer. Empty means
  silence.
- Recordings of the peer go to `storages/calls/<call_id>.wav`.
- Every goroutine GOWA starts must use `pkg/safego`. meowcaller is a dependency and is not
  scanned.
- meowcaller changes go to the fork (`~/Documents/meowcaller`, branch `elphant`, listed in its
  `ELPHANT.md`), never into GOWA.
- Never log full phone numbers or JIDs from call code: mask to the last 4 digits.
- Code, comments, commits and docs in English, with no attribution line in commits.

## Review Focus

1. **Answering a `call_id` that is unknown, already ended or past the ring window** must return a
   400 with a clear message, not a panic or a hang. Pinned in Task 3 (`TestAnswerUnknownCall`,
   `TestAnswerExpiredOffer`).
2. **A device that is not allowed, or with the flag off**, must get a 400 "calls are not enabled
   for this device" from every new route, and the existing `call.offer` and auto-reject must stay
   untouched. Pinned in Task 4 (`TestCallsFromContextDisabled`) and Task 5
   (`TestCallRoutesDisabledDevice`).
3. **An incoming call above `WHATSAPP_CALLS_MAX_PER_DEVICE`** must be rejected and must not be
   answerable. Pinned in Task 3 (`TestIncomingAboveCapIsRejected`).
4. **Logout and re-pair of a device** must not leave the old manager serving the new client.
   Pinned in Task 4 (`TestForgetCallsOnReset`).
5. **A call ended by the peer while it is still ringing or pending** must disappear from
   `GET /calls/active` and must not be answerable. Pinned in Task 3 (`TestEndedPendingCallIsGone`).

---

### Task 1: Point GOWA at the meowcaller fork

The fork already exists: `eduardoelphant/meowcaller`, default branch `elphant`, tag
`v0.1.0-elphant.1`. It holds upstream `27a3c6b` plus the FFT twiddle cache (upstream PR #38), the
dither fix, Opus mode (`SetCodec`) and the `opus/` libopus binding, with its own CI. See its
`ELPHANT.md`. A local clone is at `~/Documents/meowcaller`. This task only wires GOWA to it.

**Files:**
- Modify: `src/go.mod`, `src/go.sum`

**Interfaces:**
- Produces: `meowcaller.SetCodec(name string) error` (`"opus"` or `"mlow"`, called once before
  `NewClient`), plus the upstream API: `NewClient(*whatsmeow.Client, ...Option) *Client`,
  `WithLogger(zerolog.Logger)`, `(*Client).OnIncomingCall(func(*Call))`,
  `(*Client).Call(ctx, target string) (*Call, error)`, `(*Call).ID/Peer/Answer/Reject/Hangup/Play/Receive/OnReady/OnEnd`,
  `WAVFile/MP3File/OpusFile(path) (AudioSource, error)`, `WAVRecorder(path) (AudioSink, error)`.

- [ ] **Step 1: Add the replace**

```bash
cd ~/Documents/whatsapp-gateway-calls/src
GOTOOLCHAIN=auto go mod edit -replace github.com/purpshell/meowcaller=github.com/eduardoelphant/meowcaller@v0.1.0-elphant.1
```

The fork keeps `module github.com/purpshell/meowcaller` in its `go.mod` on purpose, so GOWA
imports the upstream path and nothing else changes. `go mod tidy` drops the require while nothing
imports meowcaller. Task 4 adds the import and runs `go mod tidy`, which records the require
and `go.sum`.

- [ ] **Step 2: Check the fork resolves and builds against GOWA's whatsmeow**

```bash
cd ~/Documents/whatsapp-gateway-calls/src
GOTOOLCHAIN=auto go get github.com/purpshell/meowcaller@v0.0.0-00010101000000-000000000000
GOTOOLCHAIN=auto go build github.com/purpshell/meowcaller/... && GOTOOLCHAIN=auto go build ./...
```

Expected: both builds succeed (needs `libopus` and `pkg-config`; on macOS `brew install opus pkg-config`).

- [ ] **Step 3: Commit**

```bash
git add src/go.mod src/go.sum
git commit -m "build(calls): use the eduardoelphant/meowcaller fork through a replace"
```

Changing meowcaller later: commit on `elphant` in the fork, tag `vX.Y.Z-elphant.N`, push the
branch and the tag (owner's OK), then bump the tag in the `replace`.

---

### Task 2: Configuration for calls

**Files:**
- Modify: `src/config/settings.go` (near `WhatsappAutoRejectCall`, line ~58, and the path block ~39-43)
- Create: `src/config/calls.go`, `src/config/calls_test.go`
- Modify: `src/cmd/root.go` (env block near :220, flags near :507, `CreateFolder` at :750)
- Modify: `src/.env.example` (after `WHATSAPP_AUTO_REJECT_CALL` at :51)

**Interfaces:**
- Produces: `config.WhatsappCallsEnabled bool`, `config.WhatsappCallsDevices []string`,
  `config.WhatsappCallsCodec string`, `config.WhatsappCallsMaxPerDevice int`,
  `config.WhatsappCallsTestAudio string`, `config.PathCalls = "storages/calls"`,
  `config.CallsAllowed(slotID, jidUser, jidString string) bool`.

- [ ] **Step 1: Write the failing test**

`src/config/calls_test.go`:

```go
package config

import "testing"

func TestCallsAllowed(t *testing.T) {
	origEnabled, origDevices := WhatsappCallsEnabled, WhatsappCallsDevices
	t.Cleanup(func() { WhatsappCallsEnabled, WhatsappCallsDevices = origEnabled, origDevices })

	WhatsappCallsEnabled = false
	WhatsappCallsDevices = nil
	if CallsAllowed("slot-a", "5511900000000", "5511900000000:27@s.whatsapp.net") {
		t.Fatal("flag off must deny")
	}

	WhatsappCallsEnabled = true
	if !CallsAllowed("slot-a", "5511900000000", "5511900000000:27@s.whatsapp.net") {
		t.Fatal("enabled with an empty list must allow every device")
	}

	WhatsappCallsDevices = []string{" 5511900000000 ", "slot-b"}
	cases := []struct {
		slot, user, jid string
		want            bool
	}{
		{"slot-x", "5511900000000", "5511900000000:27@s.whatsapp.net", true},
		{"slot-b", "", "", true},
		{"slot-c", "5527900000000", "5527900000000:17@s.whatsapp.net", false},
		{"5511900000000:27@s.whatsapp.net", "", "", false},
	}
	for _, c := range cases {
		if got := CallsAllowed(c.slot, c.user, c.jid); got != c.want {
			t.Errorf("CallsAllowed(%q,%q,%q) = %v, want %v", c.slot, c.user, c.jid, got, c.want)
		}
	}

	WhatsappCallsDevices = []string{"5511900000000:27@s.whatsapp.net"}
	if !CallsAllowed("slot-x", "5511900000000", "5511900000000:27@s.whatsapp.net") {
		t.Fatal("a full JID in the list must match")
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `cd src && GOTOOLCHAIN=auto go test ./config/ -run TestCallsAllowed`
Expected: FAIL to build (`undefined: WhatsappCallsEnabled`).

- [ ] **Step 3: Implement**

In `src/config/settings.go`, add next to `WhatsappAutoRejectCall`:

```go
	// Fork (elphant): voice calls through meowcaller. See docs/specs/2026-10-14-gateway-voice-calls-design.md.
	WhatsappCallsEnabled      = false    // answer and place calls on allowed devices
	WhatsappCallsDevices      []string   // device ids, phone numbers or JIDs allowed; empty = all
	WhatsappCallsCodec        = "opus"   // "opus" or "mlow"
	WhatsappCallsMaxPerDevice = 1        // simultaneous calls per device
	WhatsappCallsTestAudio    = ""       // file played to the peer (stage 2a); empty = silence
```

Add to the path block:

```go
	PathCalls     = "storages/calls"
```

`src/config/calls.go`:

```go
package config

import "strings"

// CallsAllowed reports whether calls are enabled for a device, matching the allow
// list against the slot id, the phone number (JID user) or the full JID string.
func CallsAllowed(slotID, jidUser, jidString string) bool {
	if !WhatsappCallsEnabled {
		return false
	}
	if len(WhatsappCallsDevices) == 0 {
		return true
	}
	for _, entry := range WhatsappCallsDevices {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if entry == slotID || (jidUser != "" && entry == jidUser) || (jidString != "" && entry == jidString) {
			return true
		}
	}
	return false
}
```

In `src/cmd/root.go`, add to the env block, following the pattern at :220:

```go
	if viper.IsSet("whatsapp_calls_enabled") {
		config.WhatsappCallsEnabled = viper.GetBool("whatsapp_calls_enabled")
	}
	if env := viper.GetString("whatsapp_calls_devices"); env != "" {
		config.WhatsappCallsDevices = strings.Split(env, ",")
	}
	if env := viper.GetString("whatsapp_calls_codec"); env != "" {
		config.WhatsappCallsCodec = env
	}
	if viper.IsSet("whatsapp_calls_max_per_device") {
		config.WhatsappCallsMaxPerDevice = viper.GetInt("whatsapp_calls_max_per_device")
	}
	if env := viper.GetString("whatsapp_calls_test_audio"); env != "" {
		config.WhatsappCallsTestAudio = env
	}
```

Add the flags, following the `auto-reject-call` pattern at :507:

```go
	rootCmd.PersistentFlags().BoolVarP(&config.WhatsappCallsEnabled, "calls-enabled", "", config.WhatsappCallsEnabled,
		`answer and place voice calls on allowed devices --calls-enabled <true/false> | example: --calls-enabled=true`)
	rootCmd.PersistentFlags().StringSliceVarP(&config.WhatsappCallsDevices, "calls-devices", "", config.WhatsappCallsDevices,
		`devices allowed to use calls (ids, phone numbers or JIDs); empty = all --calls-devices <list> | example: --calls-devices=5511900000000`)
	rootCmd.PersistentFlags().StringVarP(&config.WhatsappCallsCodec, "calls-codec", "", config.WhatsappCallsCodec,
		`call audio codec --calls-codec <opus|mlow> | example: --calls-codec=opus`)
	rootCmd.PersistentFlags().IntVarP(&config.WhatsappCallsMaxPerDevice, "calls-max-per-device", "", config.WhatsappCallsMaxPerDevice,
		`simultaneous calls per device --calls-max-per-device <n> | example: --calls-max-per-device=1`)
	rootCmd.PersistentFlags().StringVarP(&config.WhatsappCallsTestAudio, "calls-test-audio", "", config.WhatsappCallsTestAudio,
		`audio file played to the peer --calls-test-audio <path> | example: --calls-test-audio=storages/hello.wav`)
```

Add `config.PathCalls` to the `utils.CreateFolder(...)` call at :750.

In `src/.env.example`, after `WHATSAPP_AUTO_REJECT_CALL=false`:

```
# Voice calls (fork, off by default; ban risk for the numbers that use them)
WHATSAPP_CALLS_ENABLED=false
WHATSAPP_CALLS_DEVICES=
WHATSAPP_CALLS_CODEC=opus
WHATSAPP_CALLS_MAX_PER_DEVICE=1
WHATSAPP_CALLS_TEST_AUDIO=
```

- [ ] **Step 4: Run the tests**

Run: `cd src && gofmt -w config cmd; GOTOOLCHAIN=auto go test ./config/ ./cmd/`
Expected: no gofmt output, `ok` for both.

- [ ] **Step 5: Commit**

```bash
git add src/config src/cmd/root.go src/.env.example
git commit -m "feat(calls): configuration flags and the per-device allow list"
```

---

### Task 3: CallManager (state, cap, ring window), tested with a fake engine

**Files:**
- Create: `src/infrastructure/whatsapp/calls.go`
- Create: `src/infrastructure/whatsapp/calls_test.go`

**Interfaces:**
- Produces, used by Task 4:

```go
type callHandle interface {
	ID() string
	PeerMasked() string
	Answer() error
	Reject() error
	Hangup() error
	Play(path string) error
	Record(path string) error
	OnEnd(func(reason string))
}
type callEngine interface {
	OnIncomingCall(func(callHandle))
	Call(ctx context.Context, target string) (callHandle, error)
}
func newCallManager(engine callEngine, opts callManagerOptions) *CallManager
type callManagerOptions struct { MaxCalls int; RingWindow time.Duration; TestAudio, RecordDir string; Now func() time.Time }
```

- Produces, used by Task 5:

```go
type CallInfo struct { CallID, Direction, Peer, State string; StartedAt time.Time }
func (m *CallManager) Answer(callID string) error
func (m *CallManager) Start(ctx context.Context, target string) (CallInfo, error)
func (m *CallManager) Hangup(callID string) error
func (m *CallManager) Active() []CallInfo
var ErrCallNotFound, ErrCallLimit error // pkgError.ValidationError
```

- [ ] **Step 1: Write the failing tests**

`src/infrastructure/whatsapp/calls_test.go`:

```go
package whatsapp

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeCall struct {
	mu                                sync.Mutex
	id                                string
	answered, rejected, hungUp        bool
	played, recorded                  string
	onEnd                             func(string)
}

func (f *fakeCall) ID() string            { return f.id }
func (f *fakeCall) PeerMasked() string    { return "…0000@lid" }
func (f *fakeCall) Answer() error         { f.mu.Lock(); f.answered = true; f.mu.Unlock(); return nil }
func (f *fakeCall) Reject() error         { f.mu.Lock(); f.rejected = true; f.mu.Unlock(); return nil }
func (f *fakeCall) Hangup() error         { f.mu.Lock(); f.hungUp = true; f.mu.Unlock(); return nil }
func (f *fakeCall) Play(p string) error   { f.played = p; return nil }
func (f *fakeCall) Record(p string) error { f.recorded = p; return nil }
func (f *fakeCall) OnEnd(fn func(string)) { f.onEnd = fn }
func (f *fakeCall) end(reason string)     { if f.onEnd != nil { f.onEnd(reason) } }

type fakeEngine struct {
	incoming func(callHandle)
	placed   []*fakeCall
}

func (e *fakeEngine) OnIncomingCall(fn func(callHandle)) { e.incoming = fn }
func (e *fakeEngine) Call(_ context.Context, target string) (callHandle, error) {
	c := &fakeCall{id: "OUT-" + target}
	e.placed = append(e.placed, c)
	return c, nil
}

func newTestManager(t *testing.T, max int, now *time.Time) (*CallManager, *fakeEngine) {
	t.Helper()
	eng := &fakeEngine{}
	m := newCallManager(eng, callManagerOptions{
		MaxCalls: max, RingWindow: time.Minute, TestAudio: "hello.wav", RecordDir: t.TempDir(),
		Now: func() time.Time { return *now },
	})
	return m, eng
}

func TestAnswerUnknownCall(t *testing.T) {
	now := time.Now()
	m, _ := newTestManager(t, 1, &now)
	assert.ErrorIs(t, m.Answer("NOPE"), ErrCallNotFound)
}

func TestAnswerPendingCall(t *testing.T) {
	now := time.Now()
	m, eng := newTestManager(t, 1, &now)
	in := &fakeCall{id: "IN-1"}
	eng.incoming(in)

	require.Len(t, m.Active(), 1)
	assert.Equal(t, "ringing", m.Active()[0].State)
	require.NoError(t, m.Answer("IN-1"))
	assert.True(t, in.answered)
	assert.Equal(t, "hello.wav", in.played)
	assert.Contains(t, in.recorded, "IN-1.wav")
	assert.Equal(t, "active", m.Active()[0].State)
	assert.Equal(t, "inbound", m.Active()[0].Direction)
}

func TestAnswerExpiredOffer(t *testing.T) {
	now := time.Now()
	m, eng := newTestManager(t, 1, &now)
	eng.incoming(&fakeCall{id: "IN-1"})
	now = now.Add(2 * time.Minute)
	assert.ErrorIs(t, m.Answer("IN-1"), ErrCallNotFound)
	assert.Empty(t, m.Active())
}

func TestIncomingAboveCapIsRejected(t *testing.T) {
	now := time.Now()
	m, eng := newTestManager(t, 1, &now)
	first := &fakeCall{id: "IN-1"}
	eng.incoming(first)
	require.NoError(t, m.Answer("IN-1"))

	second := &fakeCall{id: "IN-2"}
	eng.incoming(second)
	assert.True(t, second.rejected)
	assert.ErrorIs(t, m.Answer("IN-2"), ErrCallNotFound)
	assert.Len(t, m.Active(), 1)
}

func TestStartRespectsCap(t *testing.T) {
	now := time.Now()
	m, eng := newTestManager(t, 1, &now)
	info, err := m.Start(context.Background(), "+5511900000000")
	require.NoError(t, err)
	assert.Equal(t, "outbound", info.Direction)
	assert.Equal(t, "hello.wav", eng.placed[0].played)

	_, err = m.Start(context.Background(), "+5511900000001")
	assert.ErrorIs(t, err, ErrCallLimit)
}

func TestHangupAndEnd(t *testing.T) {
	now := time.Now()
	m, eng := newTestManager(t, 1, &now)
	info, err := m.Start(context.Background(), "+5511900000000")
	require.NoError(t, err)
	require.NoError(t, m.Hangup(info.CallID))
	assert.True(t, eng.placed[0].hungUp)
	eng.placed[0].end("hangup")
	assert.Empty(t, m.Active())
	assert.ErrorIs(t, m.Hangup(info.CallID), ErrCallNotFound)
}

func TestEndedPendingCallIsGone(t *testing.T) {
	now := time.Now()
	m, eng := newTestManager(t, 1, &now)
	in := &fakeCall{id: "IN-1"}
	eng.incoming(in)
	in.end("terminate")
	assert.Empty(t, m.Active())
	assert.ErrorIs(t, m.Answer("IN-1"), ErrCallNotFound)
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `cd src && GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/ -run 'Call(Manager)?|Answer|Incoming|Start|Hangup|Ended' `
Expected: FAIL to build (`undefined: newCallManager`).

- [ ] **Step 3: Implement**

`src/infrastructure/whatsapp/calls.go`:

```go
package whatsapp

import (
	"context"
	"path/filepath"
	"sort"
	"sync"
	"time"

	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/sirupsen/logrus"
)

// Fork (elphant): voice calls on one device. See docs/specs/2026-10-14-gateway-voice-calls-design.md.

var (
	ErrCallNotFound = pkgError.ValidationError("call not found, already ended, or no longer ringing")
	ErrCallLimit    = pkgError.ValidationError("too many calls on this device")
	ErrCallsOff     = pkgError.ValidationError("calls are not enabled for this device")
)

type callHandle interface {
	ID() string
	PeerMasked() string
	Answer() error
	Reject() error
	Hangup() error
	Play(path string) error
	Record(path string) error
	OnEnd(func(reason string))
}

type callEngine interface {
	OnIncomingCall(func(callHandle))
	Call(ctx context.Context, target string) (callHandle, error)
}

type callManagerOptions struct {
	MaxCalls   int
	RingWindow time.Duration
	TestAudio  string
	RecordDir  string
	Now        func() time.Time
}

// CallInfo is the public view of one call.
type CallInfo struct {
	CallID    string    `json:"call_id"`
	Direction string    `json:"direction"`
	Peer      string    `json:"peer"`
	State     string    `json:"state"`
	StartedAt time.Time `json:"started_at"`
}

type trackedCall struct {
	handle callHandle
	info   CallInfo
}

// CallManager tracks ringing and active calls of one device client.
type CallManager struct {
	engine callEngine
	opts   callManagerOptions
	mu     sync.Mutex
	calls  map[string]*trackedCall
}

func newCallManager(engine callEngine, opts callManagerOptions) *CallManager {
	if opts.MaxCalls < 1 {
		opts.MaxCalls = 1
	}
	if opts.RingWindow <= 0 {
		opts.RingWindow = time.Minute
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	m := &CallManager{engine: engine, opts: opts, calls: map[string]*trackedCall{}}
	engine.OnIncomingCall(m.onIncoming)
	return m
}

func (m *CallManager) onIncoming(h callHandle) {
	m.mu.Lock()
	m.pruneLocked()
	if m.activeCountLocked() >= m.opts.MaxCalls {
		m.mu.Unlock()
		logrus.Infof("[CALLS] rejecting %s from %s: device at its call limit", h.ID(), h.PeerMasked())
		_ = h.Reject()
		return
	}
	m.track(h, "inbound", "ringing")
	m.mu.Unlock()
}

// track registers a call; caller holds m.mu.
func (m *CallManager) track(h callHandle, direction, state string) *trackedCall {
	tc := &trackedCall{handle: h, info: CallInfo{
		CallID: h.ID(), Direction: direction, Peer: h.PeerMasked(), State: state, StartedAt: m.opts.Now(),
	}}
	m.calls[h.ID()] = tc
	id := h.ID()
	h.OnEnd(func(reason string) {
		m.mu.Lock()
		delete(m.calls, id)
		m.mu.Unlock()
		logrus.Infof("[CALLS] call %s ended: %s", id, reason)
	})
	return tc
}

// pruneLocked drops offers that rang past the ring window.
func (m *CallManager) pruneLocked() {
	now := m.opts.Now()
	for id, tc := range m.calls {
		if tc.info.State == "ringing" && now.Sub(tc.info.StartedAt) > m.opts.RingWindow {
			delete(m.calls, id)
		}
	}
}

func (m *CallManager) activeCountLocked() int {
	n := 0
	for _, tc := range m.calls {
		if tc.info.State == "active" || tc.info.Direction == "outbound" {
			n++
		}
	}
	return n
}

func (m *CallManager) attachMedia(h callHandle) {
	if m.opts.TestAudio != "" {
		if err := h.Play(m.opts.TestAudio); err != nil {
			logrus.Warnf("[CALLS] play %s on %s: %v", m.opts.TestAudio, h.ID(), err)
		}
	}
	if m.opts.RecordDir != "" {
		if err := h.Record(filepath.Join(m.opts.RecordDir, h.ID()+".wav")); err != nil {
			logrus.Warnf("[CALLS] record %s: %v", h.ID(), err)
		}
	}
}

// Answer answers a ringing inbound call.
func (m *CallManager) Answer(callID string) error {
	m.mu.Lock()
	m.pruneLocked()
	tc, ok := m.calls[callID]
	if !ok || tc.info.State != "ringing" {
		m.mu.Unlock()
		return ErrCallNotFound
	}
	if m.activeCountLocked() >= m.opts.MaxCalls {
		m.mu.Unlock()
		return ErrCallLimit
	}
	tc.info.State = "active"
	m.mu.Unlock()

	if err := tc.handle.Answer(); err != nil {
		m.mu.Lock()
		delete(m.calls, callID)
		m.mu.Unlock()
		return err
	}
	m.attachMedia(tc.handle)
	return nil
}

// Start places an outbound call to target (phone number or JID).
func (m *CallManager) Start(ctx context.Context, target string) (CallInfo, error) {
	m.mu.Lock()
	m.pruneLocked()
	if m.activeCountLocked() >= m.opts.MaxCalls {
		m.mu.Unlock()
		return CallInfo{}, ErrCallLimit
	}
	m.mu.Unlock()

	h, err := m.engine.Call(ctx, target)
	if err != nil {
		return CallInfo{}, err
	}
	m.mu.Lock()
	tc := m.track(h, "outbound", "active")
	info := tc.info
	m.mu.Unlock()
	m.attachMedia(h)
	return info, nil
}

// Hangup ends a ringing or active call.
func (m *CallManager) Hangup(callID string) error {
	m.mu.Lock()
	tc, ok := m.calls[callID]
	m.mu.Unlock()
	if !ok {
		return ErrCallNotFound
	}
	if tc.info.State == "ringing" {
		return tc.handle.Reject()
	}
	return tc.handle.Hangup()
}

// Active lists ringing and active calls, oldest first.
func (m *CallManager) Active() []CallInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked()
	out := make([]CallInfo, 0, len(m.calls))
	for _, tc := range m.calls {
		out = append(out, tc.info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out
}
```

- [ ] **Step 4: Run the tests**

Run: `cd src && gofmt -w infrastructure/whatsapp; GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/ -run 'Answer|Incoming|Start|Hangup|Ended' -race -v`
Expected: all seven tests PASS, no race report.

- [ ] **Step 5: Commit**

```bash
git add src/infrastructure/whatsapp/calls.go src/infrastructure/whatsapp/calls_test.go
git commit -m "feat(calls): per-device call manager with a call cap and a ring window"
```

---

### Task 4: meowcaller adapter, attach to every client, find it from a request

**Files:**
- Create: `src/infrastructure/whatsapp/calls_meowcaller.go`
- Create: `src/infrastructure/whatsapp/calls_registry.go`, `src/infrastructure/whatsapp/calls_registry_test.go`
- Modify: `src/infrastructure/whatsapp/init.go:107-122` (after `NewClient`, before `registerEventHandler`)
- Modify: `src/infrastructure/whatsapp/device_manager.go:841-873` (after `NewClient`, before `SetClient`)
- Modify: `src/infrastructure/whatsapp/device_instance.go:130` (`ResetClient`)
- Modify: `src/go.mod`, `src/go.sum` (`go mod tidy`)

**Interfaces:**
- Consumes: `newCallManager`, `callManagerOptions`, `CallManager`, `ErrCallsOff` (Task 3);
  `config.CallsAllowed`, `config.WhatsappCalls*`, `config.PathCalls` (Task 2);
  `meowcaller.SetCodec`, `meowcaller.NewClient` (Task 1, from the fork).
- Produces: `attachCalls(slotID string, client *whatsmeow.Client)`,
  `forgetCalls(client *whatsmeow.Client)`,
  `CallsFromContext(ctx context.Context) (*CallManager, error)`.

- [ ] **Step 1: Write the failing tests**

`src/infrastructure/whatsapp/calls_registry_test.go`:

```go
package whatsapp

import (
	"context"
	"testing"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mau.fi/whatsmeow"
)

func TestCallsFromContextDisabled(t *testing.T) {
	orig := config.WhatsappCallsEnabled
	t.Cleanup(func() { config.WhatsappCallsEnabled = orig })
	config.WhatsappCallsEnabled = false

	client := &whatsmeow.Client{}
	attachCalls("slot-a", client)
	inst := NewDeviceInstance("slot-a", client, nil)
	_, err := CallsFromContext(ContextWithDevice(context.Background(), inst))
	assert.ErrorIs(t, err, ErrCallsOff)
}

func TestForgetCallsOnReset(t *testing.T) {
	client := &whatsmeow.Client{}
	m := newCallManager(&fakeEngine{}, callManagerOptions{})
	callsByClient.Store(client, m)

	inst := NewDeviceInstance("slot-a", client, nil)
	got, err := CallsFromContext(ContextWithDevice(context.Background(), inst))
	require.NoError(t, err)
	assert.Same(t, m, got)

	inst.ResetClient()
	_, ok := callsByClient.Load(client)
	assert.False(t, ok, "the manager of a reset client must be dropped")
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `cd src && GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/ -run 'CallsFromContext|ForgetCalls'`
Expected: FAIL to build (`undefined: attachCalls`).

- [ ] **Step 3: Implement the adapter**

`src/infrastructure/whatsapp/calls_meowcaller.go`:

```go
package whatsapp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	meowcaller "github.com/purpshell/meowcaller"
	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// Fork (elphant): adapter from meowcaller to the callEngine/callHandle seams.

type meowEngine struct{ client *meowcaller.Client }

func newMeowEngine(wa *whatsmeow.Client) *meowEngine {
	log := zerolog.New(os.Stdout).With().Timestamp().Str("component", "calls").Logger().Level(zerolog.InfoLevel)
	return &meowEngine{client: meowcaller.NewClient(wa, meowcaller.WithLogger(log))}
}

func (e *meowEngine) OnIncomingCall(fn func(callHandle)) {
	e.client.OnIncomingCall(func(c *meowcaller.Call) { fn(meowCall{c}) })
}

func (e *meowEngine) Call(ctx context.Context, target string) (callHandle, error) {
	c, err := e.client.Call(ctx, target)
	if err != nil {
		return nil, err
	}
	return meowCall{c}, nil
}

type meowCall struct{ c *meowcaller.Call }

func (m meowCall) ID() string                { return m.c.ID() }
func (m meowCall) PeerMasked() string        { return maskCallJID(m.c.Peer()) }
func (m meowCall) Answer() error             { return m.c.Answer() }
func (m meowCall) Reject() error             { return m.c.Reject() }
func (m meowCall) Hangup() error             { return m.c.Hangup() }
func (m meowCall) OnEnd(fn func(string))     { m.c.OnEnd(fn) }

func (m meowCall) Play(path string) error {
	var src meowcaller.AudioSource
	var err error
	switch strings.ToLower(filepath.Ext(path)) {
	case ".wav":
		src, err = meowcaller.WAVFile(path)
	case ".mp3":
		src, err = meowcaller.MP3File(path)
	case ".opus":
		src, err = meowcaller.OpusFile(path)
	default:
		return fmt.Errorf("unsupported audio file %q", path)
	}
	if err != nil {
		return err
	}
	m.c.OnReady(func() { m.c.Play(src) })
	return nil
}

func (m meowCall) Record(path string) error {
	sink, err := meowcaller.WAVRecorder(path)
	if err != nil {
		return err
	}
	m.c.Receive(sink)
	return nil
}

func maskCallJID(j types.JID) string {
	u := j.User
	if len(u) > 4 {
		u = "…" + u[len(u)-4:]
	}
	return u + "@" + j.Server
}
```

- [ ] **Step 4: Implement the registry**

`src/infrastructure/whatsapp/calls_registry.go`:

```go
package whatsapp

import (
	"context"
	"sync"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	meowcaller "github.com/purpshell/meowcaller"
	"github.com/sirupsen/logrus"
	"go.mau.fi/whatsmeow"
)

// callsByClient maps a *whatsmeow.Client to its CallManager. It is keyed by client,
// not by slot, because a slot gets a new client after logout and re-pair, and the
// default instance shares its client with a registry slot.
var callsByClient sync.Map

var setCodecOnce sync.Once

// attachCalls installs a call manager on client when calls are allowed for it. It must
// run before client.Connect: meowcaller installs raw call handlers that must see the
// first stanza.
func attachCalls(slotID string, client *whatsmeow.Client) {
	if client == nil {
		return
	}
	user, jid := "", ""
	if client.Store != nil && client.Store.ID != nil {
		user, jid = client.Store.ID.User, client.Store.ID.String()
	}
	if !config.CallsAllowed(slotID, user, jid) {
		return
	}
	setCodecOnce.Do(func() {
		if err := meowcaller.SetCodec(config.WhatsappCallsCodec); err != nil {
			logrus.Errorf("[CALLS] %v; using opus", err)
			_ = meowcaller.SetCodec("opus")
		}
	})
	m := newCallManager(newMeowEngine(client), callManagerOptions{
		MaxCalls:   config.WhatsappCallsMaxPerDevice,
		RingWindow: time.Minute,
		TestAudio:  config.WhatsappCallsTestAudio,
		RecordDir:  config.PathCalls,
	})
	callsByClient.Store(client, m)
	logrus.Infof("[CALLS] calls enabled for device %s (codec %s)", slotID, config.WhatsappCallsCodec)
}

// forgetCalls drops the manager of a client that is being discarded.
func forgetCalls(client *whatsmeow.Client) {
	if client != nil {
		callsByClient.Delete(client)
	}
}

// CallsFromContext returns the call manager of the request's device.
func CallsFromContext(ctx context.Context) (*CallManager, error) {
	client := ClientFromContext(ctx)
	if client == nil {
		return nil, ErrCallsOff
	}
	if v, ok := callsByClient.Load(client); ok {
		return v.(*CallManager), nil
	}
	return nil, ErrCallsOff
}
```

- [ ] **Step 5: Call attachCalls at both client creation sites, and forgetCalls on reset**

In `src/infrastructure/whatsapp/init.go`, right after `client.AutoTrustIdentity = true` (before
`deviceRepo := ...`), add:

```go
	attachCalls(instanceID, client) // Fork (elphant): voice calls; must precede Connect.
```

In `src/infrastructure/whatsapp/device_manager.go` `EnsureClient`, right after
`client.AutoTrustIdentity = true` (before `repo := inst.GetChatStorage()`), add:

```go
	attachCalls(deviceID, client) // Fork (elphant): voice calls; must precede Connect.
```

In `src/infrastructure/whatsapp/device_instance.go` `ResetClient`, as the first statements
inside the lock, before `d.client = nil`:

```go
	forgetCalls(d.client) // Fork (elphant): the call manager belongs to the discarded client.
```

- [ ] **Step 6: Tidy modules, then run the package tests**

Run:
```bash
cd src && GOTOOLCHAIN=auto go mod tidy && gofmt -w infrastructure/whatsapp
GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/ -race
```
Expected: `go.mod` has `require github.com/purpshell/meowcaller` and the `replace` to the fork. No gofmt output.
`ok` (including the two new tests and every existing test in the package).

- [ ] **Step 7: Commit**

```bash
git add src/infrastructure/whatsapp src/go.mod src/go.sum
git commit -m "feat(calls): attach a meowcaller call manager to allowed device clients"
```

---

### Task 5: Usecase and REST routes

**Files:**
- Modify: `src/domains/call/call.go`
- Modify: `src/usecase/call.go`
- Modify: `src/ui/rest/call.go`
- Create: `src/ui/rest/call_test.go`

**Interfaces:**
- Consumes: `whatsapp.CallsFromContext`, `whatsapp.CallInfo`, `(*CallManager).Answer/Start/Hangup/Active` (Tasks 3-4).
- Produces: routes `POST /call/answer` `{call_id}`, `POST /call/start` `{phone}`, `POST /call/hangup`
  `{call_id}`, `GET /calls/active`. All are device-scoped through the existing device middleware.

- [ ] **Step 1: Extend the domain**

`src/domains/call/call.go`:

```go
package call

import "context"

type ICallUsecase interface {
	RejectCall(ctx context.Context, callerJID string, callID string) error
	// Fork (elphant): voice calls.
	AnswerCall(ctx context.Context, callID string) error
	StartCall(ctx context.Context, phone string) (CallInfo, error)
	HangupCall(ctx context.Context, callID string) error
	ActiveCalls(ctx context.Context) ([]CallInfo, error)
}

type RejectCallRequest struct {
	CallerJID string `json:"caller_jid" form:"caller_jid"`
	CallID    string `json:"call_id" form:"call_id"`
}

type CallIDRequest struct {
	CallID string `json:"call_id" form:"call_id"`
}

type StartCallRequest struct {
	Phone string `json:"phone" form:"phone"`
}

type CallInfo struct {
	CallID    string `json:"call_id"`
	Direction string `json:"direction"`
	Peer      string `json:"peer"`
	State     string `json:"state"`
	StartedAt string `json:"started_at"`
}
```

- [ ] **Step 2: Write the failing REST tests**

`src/ui/rest/call_test.go`:

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

type callServiceStub struct {
	domainCall.ICallUsecase
	err           error
	answered      string
	started       string
	contextDevice *whatsapp.DeviceInstance
}

func (s *callServiceStub) AnswerCall(ctx context.Context, id string) error {
	s.answered = id
	s.contextDevice, _ = whatsapp.DeviceFromContext(ctx)
	return s.err
}
func (s *callServiceStub) StartCall(ctx context.Context, phone string) (domainCall.CallInfo, error) {
	s.started = phone
	return domainCall.CallInfo{CallID: "OUT-1", Direction: "outbound", State: "active"}, s.err
}
func (s *callServiceStub) HangupCall(ctx context.Context, id string) error { return s.err }
func (s *callServiceStub) ActiveCalls(ctx context.Context) ([]domainCall.CallInfo, error) {
	return []domainCall.CallInfo{{CallID: "IN-1", State: "ringing"}}, s.err
}

func newCallApp(service domainCall.ICallUsecase, device *whatsapp.DeviceInstance) *fiber.App {
	app := fiber.New()
	app.Use(middleware.Recovery()) // handlers panic through utils.PanicIfNeeded, as in production
	app.Use(func(c fiber.Ctx) error { c.Locals("device", device); return c.Next() })
	InitRestCall(app, service)
	return app
}

func TestCallRoutesHappyPath(t *testing.T) {
	device := whatsapp.NewDeviceInstance("device-a", nil, nil)
	service := &callServiceStub{}
	app := newCallApp(service, device)

	req := httptest.NewRequest("POST", "/call/answer", strings.NewReader(`{"call_id":"IN-1"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, "IN-1", service.answered)
	assert.Same(t, device, service.contextDevice)

	req = httptest.NewRequest("POST", "/call/start", strings.NewReader(`{"phone":"+5511900000000"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err = app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, "+5511900000000", service.started)

	resp, err = app.Test(httptest.NewRequest("GET", "/calls/active", nil))
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	var out struct{ Results []domainCall.CallInfo `json:"results"` }
	require.NoError(t, json.Unmarshal(body, &out))
	require.Len(t, out.Results, 1)
	assert.Equal(t, "IN-1", out.Results[0].CallID)
}

func TestCallRoutesDisabledDevice(t *testing.T) {
	device := whatsapp.NewDeviceInstance("device-a", nil, nil)
	app := newCallApp(&callServiceStub{err: whatsapp.ErrCallsOff}, device)
	for _, r := range []struct{ method, path, body string }{
		{"POST", "/call/answer", `{"call_id":"IN-1"}`},
		{"POST", "/call/start", `{"phone":"+5511900000000"}`},
		{"POST", "/call/hangup", `{"call_id":"IN-1"}`},
		{"GET", "/calls/active", ""},
	} {
		req := httptest.NewRequest(r.method, r.path, strings.NewReader(r.body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		require.NoError(t, err)
		assert.Equal(t, 400, resp.StatusCode, r.path)
	}
}
```

`middleware.Recovery()` (`src/ui/rest/middleware/recovery.go:13`) turns a panicked
`pkgError.GenericError` into its status code. `health_test.go` and `device_test.go` already use it
in tests. Check that `ValidationError.StatusCode()` is 400 (`src/pkg/error/validation_error.go:17`).

- [ ] **Step 3: Run them to see them fail**

Run: `cd src && GOTOOLCHAIN=auto go test ./ui/rest/ -run 'CallRoutes'`
Expected: FAIL (routes missing: 404, or the build fails on the stub's interface).

- [ ] **Step 4: Implement the usecase**

Append to `src/usecase/call.go`:

```go
// Fork (elphant): voice calls.

func toCallInfo(c whatsapp.CallInfo) domainCall.CallInfo {
	return domainCall.CallInfo{
		CallID: c.CallID, Direction: c.Direction, Peer: c.Peer, State: c.State,
		StartedAt: c.StartedAt.UTC().Format(time.RFC3339),
	}
}

func (service serviceCall) AnswerCall(ctx context.Context, callID string) error {
	if callID == "" {
		return pkgError.ValidationError("call_id is required")
	}
	m, err := whatsapp.CallsFromContext(ctx)
	if err != nil {
		return err
	}
	return m.Answer(callID)
}

func (service serviceCall) StartCall(ctx context.Context, phone string) (domainCall.CallInfo, error) {
	if phone == "" {
		return domainCall.CallInfo{}, pkgError.ValidationError("phone is required")
	}
	m, err := whatsapp.CallsFromContext(ctx)
	if err != nil {
		return domainCall.CallInfo{}, err
	}
	startCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	info, err := m.Start(startCtx, phone)
	if err != nil {
		return domainCall.CallInfo{}, err
	}
	return toCallInfo(info), nil
}

func (service serviceCall) HangupCall(ctx context.Context, callID string) error {
	if callID == "" {
		return pkgError.ValidationError("call_id is required")
	}
	m, err := whatsapp.CallsFromContext(ctx)
	if err != nil {
		return err
	}
	return m.Hangup(callID)
}

func (service serviceCall) ActiveCalls(ctx context.Context) ([]domainCall.CallInfo, error) {
	m, err := whatsapp.CallsFromContext(ctx)
	if err != nil {
		return nil, err
	}
	calls := m.Active()
	out := make([]domainCall.CallInfo, 0, len(calls))
	for _, c := range calls {
		out = append(out, toCallInfo(c))
	}
	return out, nil
}
```

Check before relying on `Start`'s context: meowcaller may tie the whole call to the context passed
to `Call`. If it does, a 30 s timeout context would end calls after 30 s. Read
`~/Documents/meowcaller/client.go` (`Call`) and `engine.go` (the outbound path) and see
whether the context is only used for signaling. If the call lives on it, pass
`context.WithoutCancel(ctx)` instead, and say so in a comment.

- [ ] **Step 5: Implement the routes**

In `src/ui/rest/call.go`, register the new routes in `InitRestCall`:

```go
	app.Post("/call/reject", rest.RejectCall)
	// Fork (elphant): voice calls.
	app.Post("/call/answer", rest.AnswerCall)
	app.Post("/call/start", rest.StartCall)
	app.Post("/call/hangup", rest.HangupCall)
	app.Get("/calls/active", rest.ActiveCalls)
```

Add the handlers, following `RejectCall`:

```go
func (controller *Call) AnswerCall(c fiber.Ctx) error {
	var request domainCall.CallIDRequest
	utils.PanicIfNeeded(c.Bind().Body(&request))
	utils.PanicIfNeeded(controller.Service.AnswerCall(whatsapp.ContextWithDevice(c.Context(), getDeviceFromCtx(c)), request.CallID))
	return c.JSON(utils.ResponseData{Status: 200, Code: "SUCCESS", Message: "Call answered"})
}

func (controller *Call) StartCall(c fiber.Ctx) error {
	var request domainCall.StartCallRequest
	utils.PanicIfNeeded(c.Bind().Body(&request))
	info, err := controller.Service.StartCall(whatsapp.ContextWithDevice(c.Context(), getDeviceFromCtx(c)), request.Phone)
	utils.PanicIfNeeded(err)
	return c.JSON(utils.ResponseData{Status: 200, Code: "SUCCESS", Message: "Call placed", Results: info})
}

func (controller *Call) HangupCall(c fiber.Ctx) error {
	var request domainCall.CallIDRequest
	utils.PanicIfNeeded(c.Bind().Body(&request))
	utils.PanicIfNeeded(controller.Service.HangupCall(whatsapp.ContextWithDevice(c.Context(), getDeviceFromCtx(c)), request.CallID))
	return c.JSON(utils.ResponseData{Status: 200, Code: "SUCCESS", Message: "Call ended"})
}

func (controller *Call) ActiveCalls(c fiber.Ctx) error {
	calls, err := controller.Service.ActiveCalls(whatsapp.ContextWithDevice(c.Context(), getDeviceFromCtx(c)))
	utils.PanicIfNeeded(err)
	return c.JSON(utils.ResponseData{Status: 200, Code: "SUCCESS", Message: "Active calls", Results: calls})
}
```

If `src/cmd/rest.go` lists device-scoped route prefixes for `DeviceMiddleware`, add `/calls/`
next to `/call/`. Grep: `grep -n '"/call' src/cmd/rest.go src/ui/rest/middleware/*.go`.

- [ ] **Step 6: Run the tests**

Run: `cd src && gofmt -w domains usecase ui; GOTOOLCHAIN=auto go test ./ui/rest/ ./usecase/ ./domains/...`
Expected: no gofmt output, `ok` everywhere.

- [ ] **Step 7: Commit**

```bash
git add src/domains/call src/usecase/call.go src/ui/rest/call.go src/ui/rest/call_test.go src/cmd/rest.go
git commit -m "feat(calls): REST routes to answer, start, hang up and list calls"
```

---

### Task 6: Build, image, CI and docs; full suite green

**Files:**
- Modify: `docker/golang.Dockerfile` (builder `apk add` and runtime `apk add`)
- Modify: `.github/workflows/elphant-ci.yml:25-34`, `.github/workflows/elphant-image.yml:51-52`
- Modify: `docs/reference/elphant-fork.md` (features list)
- Modify: `docs/elphant-debt.md` (D-17)

- [ ] **Step 1: Image**

In `docker/golang.Dockerfile`, change the builder line to
`RUN apk add --no-cache gcc musl-dev gcompat opus-dev pkgconf`, and add `opus` to the runtime
`apk add --no-cache ...` line.

- [ ] **Step 2: CI**

In both workflows, before the `go vet` step, add:

```yaml
      - name: Install libopus
        run: sudo apt-get update && sudo apt-get install -y libopus-dev pkg-config
```

meowcaller's own tests run in the fork's CI, not here.

- [ ] **Step 3: Docs**

- In `docs/reference/elphant-fork.md`, add a "Voice calls (stage 2a, off by default)" entry with
  the five `WHATSAPP_CALLS_*` variables, the four routes, the build need (`libopus` and
  `pkg-config`), and a link to the spec.
- In `docs/elphant-debt.md` D-17, mark "integration in GOWA (2a)" as done in code, with runtime
  checks pending (Task 7).

- [ ] **Step 4: Full verification**

Run:
```bash
cd ~/Documents/whatsapp-gateway-calls/src
GOTOOLCHAIN=auto go vet ./...
GOTOOLCHAIN=auto go test ./...
GOTOOLCHAIN=auto go build -o whatsapp .
docker build -f ../docker/golang.Dockerfile -t gowa-calls-local .. 2>&1 | tail -3
```

Expected: vet clean; every package `ok`, including `TestEveryBackgroundGoroutineIsProtected`, the
durable webhook tests and the watchdog tests; the binary builds; and the image builds. The image is local only and is never pushed.

- [ ] **Step 5: Commit**

```bash
git add docker .github docs
git commit -m "build(calls): libopus in the image and CI; docs for calls stage 2a"
```

---

### Task 7: Runtime checks (owner present, owner's OK for each item)

Not code. Record each result in `docs/reference/meowcaller-calls-spike.md` under a new
"Stage 2a in GOWA" section, then commit.

- [ ] **Step 1: Local run on the Mac with a fresh linked device**

1. Stop the spike prototype (`pkill -INT -f "spike-opus serve"`).
2. From `src/`, run `WHATSAPP_CALLS_ENABLED=true WHATSAPP_CALLS_TEST_AUDIO=../spikes/meowcaller/data/hello.wav GOTOOLCHAIN=auto go run . rest`
   with a throwaway `storages/` directory.
3. Pair the test number from the UI. The owner scans the QR.
4. Check the logs show `[CALLS] calls enabled for device`.

- [ ] **Step 2: Inbound and outbound through REST**

1. The owner calls the test number from WhatsApp Web on another device. Run
   `curl -u ... localhost:3000/calls/active` (it shows `ringing`), then
   `curl -u ... -X POST localhost:3000/call/answer -H 'Content-Type: application/json' -d '{"call_id":"<id>"}'`.
2. Expected: the audio plays, `storages/calls/<id>.wav` holds the owner's voice, and `call.offer`
   is still delivered to the configured webhook.
3. Then `POST /call/start` to the owner's other number, and `POST /call/hangup`.

4. With `WHATSAPP_AUTO_REJECT_CALL=true`, check a call-enabled device still rings and can be
   answered via the API.
5. Drop the network mid-call, then check `GET /calls/active` empties; if not, note it (D-17 e).

- [ ] **Step 3: Messages during a call**

During a call, send a text with `POST /send/message` and receive one from the phone. Expected:
both work, and the receive shows up in the chat storage or webhook as usual.

- [ ] **Step 4: Flag off regression**

Restart without `WHATSAPP_CALLS_ENABLED`. Expected:
- no `[CALLS]` logs;
- `GET /calls/active` returns 400 "calls are not enabled";
- an incoming call still produces `call.offer`.

- [ ] **Step 5: CPU on the gateway host (needs the owner's explicit OK for the server)**

Build the local image (Task 6). Repeat the spike's server method with the host scripts in
`/root/meowcaller-spike/`: stop `gowa_gowa`, back up its session, run the image with the flag on
for one device, then make one 3-minute call and read `docker stats`. Then restore `gowa_gowa` and
confirm `/health/devices` shows every device `logged_in`.
