package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/callrec"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/callrtc"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/purpshell/meowcaller/opus"
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
func (l *fakeLeg) OnFrame(fn func([]float32)) { l.mu.Lock(); l.onFrame = fn; l.mu.Unlock() }
func (l *fakeLeg) OnConnected(fn func(bool))  { l.mu.Lock(); l.onConnected = fn; l.mu.Unlock() }
func (l *fakeLeg) Close() error               { l.mu.Lock(); l.closed++; l.mu.Unlock(); return nil }
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
func (l *fakeLeg) closes() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.closed
}

type fakeRTC struct {
	mu   sync.Mutex
	legs []*fakeLeg
	err  error
}

func (r *fakeRTC) NewInbound() (rtcLeg, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return nil, r.err
	}
	l := &fakeLeg{sdp: "v=0 gowa-offer"}
	r.legs = append(r.legs, l)
	return l, nil
}
func (r *fakeRTC) NewOutbound(string) (rtcLeg, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return nil, r.err
	}
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
func (r *fakeRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.frames)
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
	if len(p) == 0 {
		return 0
	}
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
	b, leg, rec, h, _ := startBridge(t, time.Second, "IN-1")
	b.WhatsAppReady()
	require.NoError(t, h.sink.WriteFrame(tone(960, 16000, 300))) // customer
	leg.send(tone(960, 48000, 440))
	silent, err := h.src.ReadFrame()
	require.NoError(t, err)
	assert.Zero(t, level(silent), "below the prefill the jitter buffer plays silence")

	for i := 0; i < 4; i++ {
		leg.send(tone(960, 48000, 440)) // 100 ms of agent audio in all: the prefill
	}
	agent, err := h.src.ReadFrame()
	require.NoError(t, err)
	assert.Len(t, agent, 960)
	assert.Greater(t, level(agent), 0.2)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	require.Len(t, rec.frames, 2)
	assert.Zero(t, level(rec.frames[0][0]), "prefill: silence on the left")
	assert.Greater(t, level(rec.frames[0][1]), 0.2, "customer on the right")
	assert.Greater(t, level(rec.frames[1][0]), 0.2, "agent on the left")
	assert.Zero(t, level(rec.frames[1][1]), "no customer audio queued: silence on the right")
}

// The recording starts when the WhatsApp leg is answered and carries media: ringing (early
// media) is not recorded, so an unanswered call leaves no file behind.
func TestBridgeRecordsOnlyAfterWhatsAppReady(t *testing.T) {
	b, _, rec, h, _ := startBridge(t, time.Second, "OUT-1")
	require.NoError(t, h.sink.WriteFrame(tone(960, 16000, 300)))
	_, err := h.src.ReadFrame()
	require.NoError(t, err)
	assert.Zero(t, rec.count(), "nothing recorded before WhatsAppReady")

	b.WhatsAppReady()
	_, err = h.src.ReadFrame()
	require.NoError(t, err)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	require.Len(t, rec.frames, 1)
	assert.Zero(t, level(rec.frames[0][1]), "the customer audio from before the answer was consumed, not kept")
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

func TestBridgeBrowserUpBeforeStart(t *testing.T) {
	f, rtc := testFactory(t, 30*time.Millisecond, &fakeRecorder{}, nil)
	media, err := f.Inbound()
	require.NoError(t, err)
	rtc.legs[0].connect(true) // ICE and DTLS done before the WhatsApp answer
	ev := &bridgeEvents{}
	media.Start(&fakeCall{id: "IN-1"}, ev.events())
	media.WhatsAppReady()
	assert.EqualValues(t, 1, ev.flows.Load())
	time.Sleep(80 * time.Millisecond)
	assert.Zero(t, ev.losts.Load(), "no grace timer for a leg that is already up")
}

// Review Focus 1: a browser that never connects must not leave the customer on a silent line.
func TestBridgeGraceWhenBrowserNeverConnects(t *testing.T) {
	_, _, _, _, ev := startBridge(t, 30*time.Millisecond, "IN-1")
	assert.Eventually(t, func() bool { return ev.losts.Load() == 1 }, time.Second, 5*time.Millisecond)
	time.Sleep(60 * time.Millisecond)
	assert.EqualValues(t, 1, ev.losts.Load(), "Lost fires once per outage")
}

func TestBridgeGraceCancelledByReconnect(t *testing.T) {
	_, leg, _, _, ev := startBridge(t, 150*time.Millisecond, "IN-1")
	leg.connect(true)
	leg.connect(false)
	leg.connect(true)
	time.Sleep(300 * time.Millisecond)
	assert.Zero(t, ev.losts.Load())
}

// A grace timer that already fired but lost the race with a reconnect must not count against a
// later outage: only the current outage's timer may hang up.
func TestBridgeGraceStaleTimerIgnored(t *testing.T) {
	b, leg, _, _, ev := startBridge(t, 200*time.Millisecond, "IN-1")
	b.mu.Lock()
	stale := b.grace
	b.mu.Unlock()
	require.NotNil(t, stale)
	leg.connect(true)
	leg.connect(false) // a new outage with its own timer
	b.graceFired(stale)
	assert.Zero(t, ev.losts.Load(), "the stale timer is ignored")
	assert.Eventually(t, func() bool { return ev.losts.Load() == 1 }, time.Second, 5*time.Millisecond)
}

func TestBridgeCloseIsFinal(t *testing.T) {
	b, leg, rec, h, ev := startBridge(t, 100*time.Millisecond, "IN-1")
	got := b.Close()
	require.NotNil(t, got)
	assert.EqualValues(t, 1234, got.Bytes)
	assert.Same(t, got, b.Close(), "Close is idempotent")
	assert.Equal(t, 1, leg.closes())
	assert.Equal(t, 1, rec.closed)
	_, err := h.src.ReadFrame()
	assert.ErrorIs(t, err, io.EOF)
	time.Sleep(200 * time.Millisecond)
	assert.Zero(t, ev.losts.Load(), "no grace hangup after Close")

	b.Start(&fakeCall{id: "IN-1"}, ev.events())
	b.WhatsAppReady()
	leg.connect(true)
	assert.Zero(t, ev.flows.Load(), "Start, WhatsAppReady and a late up after Close do nothing")
}

func TestBridgeConcurrentCloseSameRecording(t *testing.T) {
	b, _, rec, _, _ := startBridge(t, time.Second, "IN-1")
	var wg sync.WaitGroup
	got := make([]*RecordingInfo, 8)
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i] = b.Close()
		}(i)
	}
	wg.Wait()
	for _, r := range got {
		require.NotNil(t, r)
		assert.Same(t, got[0], r)
	}
	assert.Equal(t, 1, rec.closed)
}

func TestBridgeCloseBeforeStartMakesNoRecording(t *testing.T) {
	var made atomic.Int32
	f, _ := testFactory(t, time.Second, &fakeRecorder{bytes: 7}, &made)
	media, err := f.Inbound()
	require.NoError(t, err)
	assert.Nil(t, media.Close(), "an unanswered call has no recording")
	media.Start(&fakeCall{id: "IN-1"}, (&bridgeEvents{}).events())
	assert.Zero(t, made.Load())
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

// A bad browser offer is the client's fault (400); an engine failure is the server's (500).
func TestBridgeFactoryErrors(t *testing.T) {
	f, rtc := testFactory(t, time.Second, &fakeRecorder{}, nil)
	rtc.err = fmt.Errorf("callrtc: %w", callrtc.ErrBadOffer)
	_, err := f.Outbound("v=0 no-opus")
	assert.ErrorIs(t, err, callrtc.ErrBadOffer)
	var ge pkgError.GenericError
	assert.False(t, errors.As(err, &ge), "the manager turns a bad offer into a 400")

	rtc.err = errors.New("ice gathering timed out")
	_, err = f.Outbound("v=0 offer")
	require.ErrorAs(t, err, &ge)
	assert.Equal(t, 500, ge.StatusCode())
	_, err = f.Inbound()
	require.ErrorAs(t, err, &ge)
	assert.Equal(t, 500, ge.StatusCode())
}

func TestManagerStartEngineErrorIsServerError(t *testing.T) {
	now := time.Now()
	f, rtc := testFactory(t, time.Second, &fakeRecorder{}, nil)
	rtc.err = errors.New("udp port closed")
	m, eng, _ := newTestManager(t, 1, &now, f)
	_, _, err := m.Start(context.Background(), "+5511900000000", "v=0 browser-offer")
	var ge pkgError.GenericError
	require.ErrorAs(t, err, &ge)
	assert.Equal(t, 500, ge.StatusCode())
	assert.Equal(t, 0, eng.placedCount())

	rtc.err = fmt.Errorf("callrtc: %w", callrtc.ErrBadOffer)
	_, _, err = m.Start(context.Background(), "+5511900000000", "v=0 no-opus")
	require.ErrorAs(t, err, &ge)
	assert.Equal(t, 400, ge.StatusCode())
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
	assert.Equal(t, 1, leg.closes())
}

// Review Focus 1, end to end: losing the browser hangs up the WhatsApp leg and reports the end.
func TestManagerBridgeBrowserLostHangsUp(t *testing.T) {
	now := time.Now()
	// The grace leaves time for Answer and the first connect even on a loaded machine.
	f, rtc := testFactory(t, 150*time.Millisecond, &fakeRecorder{bytes: 5}, nil)
	m, eng, log := newTestManager(t, 1, &now, f)
	in := &fakeCall{id: "IN-1"}
	eng.incoming(in)
	_, err := m.OfferSDP(context.Background(), "IN-1")
	require.NoError(t, err)
	require.NoError(t, m.Answer("IN-1", "v=0 browser-answer"))
	in.media()
	rtc.legs[0].connect(true)
	_, ok := log.last(EventCallAnswered)
	require.True(t, ok, "media flowed on both legs")
	rtc.legs[0].connect(false)

	assert.Eventually(t, func() bool {
		_, ok := log.last(EventCallEnded)
		return ok
	}, 2*time.Second, 5*time.Millisecond)
	in.mu.Lock()
	assert.True(t, in.hungUp, "the WhatsApp leg was hung up")
	in.mu.Unlock()
	ended, _ := log.last(EventCallEnded)
	assert.Equal(t, "COMPLETED", ended.Status)
	assert.Equal(t, "error", ended.EndReason)
	require.NotNil(t, ended.Recording)
	assert.Empty(t, m.Active())
	assert.Equal(t, 1, rtc.legs[0].closes())
}

// Review Focus 1: the browser never connects after the answer; the WhatsApp leg is hung up after
// the grace period with FAILED / error (no media ever flowed on both legs).
func TestManagerBridgeBrowserNeverConnects(t *testing.T) {
	now := time.Now()
	f, rtc := testFactory(t, 30*time.Millisecond, &fakeRecorder{}, nil)
	m, eng, log := newTestManager(t, 1, &now, f)
	in := &fakeCall{id: "IN-1"}
	eng.incoming(in)
	_, err := m.OfferSDP(context.Background(), "IN-1")
	require.NoError(t, err)
	require.NoError(t, m.Answer("IN-1", "v=0 browser-answer"))
	in.media()

	assert.Eventually(t, func() bool {
		_, ok := log.last(EventCallEnded)
		return ok
	}, time.Second, 5*time.Millisecond)
	ended, _ := log.last(EventCallEnded)
	assert.Equal(t, "FAILED", ended.Status)
	assert.Equal(t, "error", ended.EndReason)
	assert.Nil(t, ended.Recording, "an empty recording is not reported")
	in.mu.Lock()
	assert.True(t, in.hungUp)
	in.mu.Unlock()
	assert.Equal(t, 1, rtc.legs[0].closes())
	assert.Empty(t, m.Active())
}

// The production recorder through the bridge: a real stereo Ogg file in the record dir.
func TestBridgeRealRecorder(t *testing.T) {
	if !opus.Available {
		t.Skip("libopus not linked (CGO_ENABLED=0)")
	}
	f := newBridgeFactory(&fakeRTC{})
	f.recordDir = t.TempDir()
	media, err := f.Inbound()
	require.NoError(t, err)
	h := &fakeCall{id: "IN-REC"}
	media.Start(h, (&bridgeEvents{}).events())
	media.WhatsAppReady()
	for i := 0; i < 5; i++ {
		require.NoError(t, h.sink.WriteFrame(tone(960, 16000, 300)))
		_, err := h.src.ReadFrame()
		require.NoError(t, err)
	}
	rec := media.Close()
	require.NotNil(t, rec)
	assert.Equal(t, filepath.Join(f.recordDir, "IN-REC.ogg"), rec.Path)
	assert.Equal(t, 300*time.Millisecond, rec.Duration)
	fi, err := os.Stat(rec.Path)
	require.NoError(t, err)
	assert.Equal(t, fi.Size(), rec.Bytes)
}

// Fix round 1: the agent's 20 ms browser frames arrive with network jitter against meowcaller's
// 60 ms pulls; the jitter buffer's cushion must absorb it with no underrun once playing. Two
// patterns: alternating 0/30 ms delays, and seeded random delays of 0 to 30 ms.
func TestBridgeAgentJitterNoUnderrun(t *testing.T) {
	rnd := rand.New(rand.NewSource(7))
	patterns := map[string]func(i int) int{
		"alternating": func(i int) int { return 30 * (i % 2) },
		"random":      func(int) int { return rnd.Intn(31) },
	}
	for name, delay := range patterns {
		t.Run(name, func(t *testing.T) { agentJitter(t, delay) })
	}
}

func agentJitter(t *testing.T, delay func(i int) int) {
	b, leg, _, h, _ := startBridge(t, time.Second, "IN-1")
	type event struct {
		at   int // ms
		pull bool
	}
	var evs []event
	for i := 0; i < 300; i++ { // 6 s of browser audio
		evs = append(evs, event{at: 20*i + delay(i)})
	}
	for k := 0; k < 100; k++ {
		evs = append(evs, event{at: 60*k + 5, pull: true})
	}
	sort.SliceStable(evs, func(i, j int) bool { return evs[i].at < evs[j].at })
	played := false
	for _, e := range evs {
		if !e.pull {
			leg.send(tone(960, 48000, 440))
			continue
		}
		frame, err := h.src.ReadFrame()
		require.NoError(t, err)
		if !played && level(frame) > 0 {
			played = true
			under, _ := b.agent.Stats()
			require.Zero(t, under, "an underrun before the first play is the prefill, not a hole")
		}
	}
	require.True(t, played)
	under, dropped := b.agent.Stats()
	assert.Zero(t, under, "no underrun after the first play")
	assert.Zero(t, dropped)
}

// Fix round 1: customer frames that are not 960 samples are kept sample by sample, neither
// truncated nor padded.
func TestBridgeCustomerVariableFrames(t *testing.T) {
	b, _, rec, h, _ := startBridge(t, time.Second, "IN-1")
	b.WhatsAppReady()
	ramp := func(n int, start float32) []float32 {
		out := make([]float32, n)
		for i := range out {
			out[i] = start + float32(i)/10000
		}
		return out
	}
	require.NoError(t, h.sink.WriteFrame(ramp(640, 0.1)))
	require.NoError(t, h.sink.WriteFrame(ramp(640, 0.2)))
	for i := 0; i < 2; i++ {
		_, err := h.src.ReadFrame()
		require.NoError(t, err)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	require.Len(t, rec.frames, 2)
	right := append(append([]float32(nil), rec.frames[0][1]...), rec.frames[1][1]...)
	require.Equal(t, 1920, len(right))
	want := append(ramp(640, 0.1), ramp(640, 0.2)...)
	assert.Equal(t, want, right[:1280], "every customer sample, in order")
	assert.Zero(t, level(right[1280:]), "then silence")
}
