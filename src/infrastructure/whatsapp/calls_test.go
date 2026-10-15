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
