package whatsapp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeCall struct {
	mu                         sync.Mutex
	id                         string
	answered, rejected, hungUp bool
	played, recorded           string
	onEnd                      func(string)
	syncEnd                    bool
}

func (f *fakeCall) ID() string            { return f.id }
func (f *fakeCall) PeerMasked() string    { return "…0000@lid" }
func (f *fakeCall) Answer() error         { f.mu.Lock(); f.answered = true; f.mu.Unlock(); return nil }
func (f *fakeCall) Reject() error         { f.mu.Lock(); f.rejected = true; f.mu.Unlock(); return nil }
func (f *fakeCall) Hangup() error         { f.mu.Lock(); f.hungUp = true; f.mu.Unlock(); return nil }
func (f *fakeCall) Play(p string) error   { f.mu.Lock(); f.played = p; f.mu.Unlock(); return nil }
func (f *fakeCall) Record(p string) error { f.mu.Lock(); f.recorded = p; f.mu.Unlock(); return nil }
func (f *fakeCall) OnEnd(fn func(string)) {
	f.mu.Lock()
	f.onEnd = fn
	se := f.syncEnd
	f.mu.Unlock()
	if se {
		fn("already-ended")
	}
}
func (f *fakeCall) end(reason string) {
	if f.onEnd != nil {
		f.onEnd(reason)
	}
}

type fakeEngine struct {
	mu       sync.Mutex
	incoming func(callHandle)
	placed   []*fakeCall
	err      error
	syncEnd  bool // placed calls fire OnEnd synchronously on registration
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

func newTestManager(t *testing.T, maxCalls int, now *time.Time) (*CallManager, *fakeEngine) {
	t.Helper()
	eng := &fakeEngine{}
	m := newCallManager(eng, callManagerOptions{
		MaxCalls: maxCalls, RingWindow: time.Minute, TestAudio: "hello.wav", RecordDir: t.TempDir(),
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

func TestHangupRingingRejects(t *testing.T) {
	now := time.Now()
	m, eng := newTestManager(t, 1, &now)
	in := &fakeCall{id: "IN-1"}
	eng.incoming(in)
	require.NoError(t, m.Hangup("IN-1"))
	assert.True(t, in.rejected)
	assert.False(t, in.hungUp)
}

func TestHangupRaceWithAnswer(t *testing.T) {
	now := time.Now()
	m, eng := newTestManager(t, 1, &now)
	in := &fakeCall{id: "IN-1"}
	eng.incoming(in)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = m.Answer("IN-1") }()
	go func() { defer wg.Done(); _ = m.Hangup("IN-1") }()
	wg.Wait()
	in.mu.Lock()
	defer in.mu.Unlock()
	assert.True(t, in.hungUp || in.rejected)
}

func TestConcurrentStartRespectsCap(t *testing.T) {
	now := time.Now()
	m, _ := newTestManager(t, 1, &now)
	const n = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, limited := 0, 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := m.Start(context.Background(), "+55119000000"+string(rune('a'+i)))
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
	m, eng := newTestManager(t, 1, &now)
	eng.err = errors.New("dial failed")
	_, err := m.Start(context.Background(), "+5511900000000")
	require.Error(t, err)
	_, err = m.Start(context.Background(), "+5511900000000")
	require.NoError(t, err)
}

func TestEndBeforeTrackLeavesNoEntry(t *testing.T) {
	now := time.Now()
	m, eng := newTestManager(t, 1, &now)
	eng.syncEnd = true
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = m.Start(context.Background(), "+5511900000000")
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start deadlocked")
	}
	assert.Empty(t, m.Active())
	eng.syncEnd = false
	_, err := m.Start(context.Background(), "+5511900000001")
	require.NoError(t, err)
}

func TestRecordSkipsUnsafeID(t *testing.T) {
	now := time.Now()
	m, eng := newTestManager(t, 1, &now)
	in := &fakeCall{id: "../../x"}
	eng.incoming(in)
	require.NoError(t, m.Answer("../../x"))
	assert.Empty(t, in.recorded)
	assert.True(t, in.answered)
}
