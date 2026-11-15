package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Fork (elphant): concurrent calls (docs/specs/2026-11-14-gateway-concurrent-calls-design.md).

func newPooledManager(t *testing.T, ceiling int, limit func() int, pool *callSlots, now *time.Time, media mediaFactory) (*CallManager, *fakeEngine, *eventLog) {
	t.Helper()
	eng := &fakeEngine{}
	log := &eventLog{}
	m := newCallManager(eng, callManagerOptions{
		MaxCalls: ceiling, Limit: limit, Pool: pool,
		RingWindow: time.Minute, OfferWait: 200 * time.Millisecond,
		Now: func() time.Time { return *now }, Emit: log.add, Media: media,
	})
	return m, eng, log
}

func heldOf(m *CallManager) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.heldLocked()
}

func TestRingingInboundHoldsTheDeviceSlot(t *testing.T) {
	now := time.Now()
	m, eng, log := newTestManager(t, 1, &now, nil)
	eng.incoming(&fakeCall{id: "IN-1"}) // rings, not answered

	second := &fakeCall{id: "IN-2"}
	eng.incoming(second)
	assert.True(t, second.rejected, "a ringing call holds the only slot")
	ended, ok := log.last(EventCallEnded)
	require.True(t, ok)
	assert.Equal(t, "IN-2", ended.Call.CallID)
	assert.Equal(t, "MISSED", ended.Status)
	assert.Equal(t, "device_limit", ended.EndReason)

	_, _, err := m.Start(context.Background(), "+5511900000000", "")
	assert.ErrorIs(t, err, ErrCallLimit)
	require.NoError(t, m.Answer("IN-1", ""), "the admitted call is still answered")
}

func TestDeviceLimitFollowsTheLimitFunc(t *testing.T) {
	now := time.Now()
	limit := 2
	m, _, _ := newPooledManager(t, 3, func() int { return limit }, nil, &now, nil)

	first, _, err := m.Start(context.Background(), "+5511900000001", "")
	require.NoError(t, err)
	_, _, err = m.Start(context.Background(), "+5511900000002", "")
	require.NoError(t, err)
	_, _, err = m.Start(context.Background(), "+5511900000003", "")
	assert.ErrorIs(t, err, ErrCallLimit)

	limit = 3
	_, _, err = m.Start(context.Background(), "+5511900000003", "")
	require.NoError(t, err)

	limit = 1 // lowering never ends live calls (D-24)
	assert.Len(t, m.Active(), 3)
	require.NoError(t, m.Hangup(first.CallID))
	_, _, err = m.Start(context.Background(), "+5511900000004", "")
	assert.ErrorIs(t, err, ErrCallLimit, "still above the lowered limit")

	limit = 9 // capped by the ceiling
	_, _, err = m.Start(context.Background(), "+5511900000004", "")
	require.NoError(t, err)
	_, _, err = m.Start(context.Background(), "+5511900000005", "")
	assert.ErrorIs(t, err, ErrCallLimit)

	limit = 0 // a bad stored value reads as 1, and 3 calls are live
	_, _, err = m.Start(context.Background(), "+5511900000006", "")
	assert.ErrorIs(t, err, ErrCallLimit)
}

func TestGatewayPoolIsSharedAcrossManagers(t *testing.T) {
	now := time.Now()
	pool := newCallSlots(func() int { return 2 })
	a, _, _ := newPooledManager(t, 3, nil, pool, &now, nil)
	b, engB, logB := newPooledManager(t, 3, nil, pool, &now, nil)

	callA, _, err := a.Start(context.Background(), "+5511900000001", "")
	require.NoError(t, err)
	_, _, err = b.Start(context.Background(), "+5511900000002", "")
	require.NoError(t, err)
	assert.Equal(t, 2, pool.inUse())

	_, _, err = a.Start(context.Background(), "+5511900000003", "")
	assert.ErrorIs(t, err, ErrGatewayCallLimit)
	assert.NotContains(t, err.Error(), "too many calls", "the CRM matches substrings")
	assert.Equal(t, "call capacity of this gateway reached", err.Error())

	in := &fakeCall{id: "IN-B"}
	engB.incoming(in)
	assert.True(t, in.rejected)
	ended, ok := logB.last(EventCallEnded)
	require.True(t, ok)
	assert.Equal(t, "MISSED", ended.Status)
	assert.Equal(t, "gateway_limit", ended.EndReason)

	require.NoError(t, a.Hangup(callA.CallID))
	assert.Equal(t, 1, pool.inUse())
	_, _, err = b.Start(context.Background(), "+5511900000003", "")
	require.NoError(t, err)
}

func TestDeviceLimitIsCheckedBeforeTheGatewayCap(t *testing.T) {
	now := time.Now()
	pool := newCallSlots(func() int { return 2 })
	a, _, _ := newPooledManager(t, 1, nil, pool, &now, nil)
	b, engB, logB := newPooledManager(t, 1, nil, pool, &now, nil)
	_, _, err := a.Start(context.Background(), "+5511900000001", "")
	require.NoError(t, err)
	_, _, err = b.Start(context.Background(), "+5511900000002", "")
	require.NoError(t, err)

	_, _, err = b.Start(context.Background(), "+5511900000003", "")
	assert.ErrorIs(t, err, ErrCallLimit, "device and gateway are both full: the device wins")
	engB.incoming(&fakeCall{id: "IN-B"})
	ended, _ := logB.last(EventCallEnded)
	assert.Equal(t, "device_limit", ended.EndReason)
}

func TestConcurrentStartsAcrossManagersRespectTheGatewayCap(t *testing.T) {
	now := time.Now()
	pool := newCallSlots(func() int { return 3 })
	a, _, _ := newPooledManager(t, 3, nil, pool, &now, nil)
	b, _, _ := newPooledManager(t, 3, nil, pool, &now, nil)
	const n = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, limited := 0, 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			m := a
			if i%2 == 1 {
				m = b
			}
			_, _, err := m.Start(context.Background(), fmt.Sprintf("+55119000000%02d", i), "")
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, ErrCallLimit), errors.Is(err, ErrGatewayCallLimit):
				limited++
			}
		}(i)
	}
	wg.Wait()
	assert.Equal(t, 3, ok)
	assert.Equal(t, n-3, limited)
	assert.Equal(t, 3, pool.inUse())
	assert.Equal(t, 3, heldOf(a)+heldOf(b))
}

func TestAnswerIgnoresALoweredLimit(t *testing.T) {
	now := time.Now()
	limit := 2
	m, eng, _ := newPooledManager(t, 2, func() int { return limit }, nil, &now, nil)
	eng.incoming(&fakeCall{id: "IN-1"})
	eng.incoming(&fakeCall{id: "IN-2"})
	limit = 1
	require.NoError(t, m.Answer("IN-1", ""))
	require.NoError(t, m.Answer("IN-2", ""), "an admitted call can be answered (D-27)")
}

// Every way a call can end, or fail to start, gives back its device slot and its gateway slot
// exactly once.
func TestEveryEndPathReleasesTheGatewaySlot(t *testing.T) {
	now := time.Now()
	pool := newCallSlots(func() int { return 0 }) // no cap: the pool still counts
	media := &fakeMediaFactory{}
	m, eng, _ := newPooledManager(t, 8, nil, pool, &now, media)
	free := func(path string) {
		t.Helper()
		assert.Equal(t, 0, pool.inUse(), path)
		assert.Equal(t, 0, heldOf(m), path)
	}
	ctx := context.Background()

	// Outbound: bad SDP, engine error, synchronous end during tracking.
	media.err = errors.New("bad sdp")
	_, _, err := m.Start(ctx, "+5511900000001", "v=0 browser")
	require.Error(t, err)
	media.err = nil
	free("bad sdp")
	eng.err = errors.New("dial failed")
	_, _, err = m.Start(ctx, "+5511900000001", "v=0 browser")
	require.Error(t, err)
	free("engine error")
	eng.syncEnd = true
	_, _, err = m.Start(ctx, "+5511900000001", "v=0 browser")
	require.ErrorIs(t, err, ErrCallNotFound)
	eng.syncEnd = false
	free("outbound synchronous end")

	// Outbound: agent hangup and peer end.
	info, _, err := m.Start(ctx, "+5511900000002", "v=0 browser")
	require.NoError(t, err)
	require.NoError(t, m.Hangup(info.CallID))
	free("hangup")
	_, _, err = m.Start(ctx, "+5511900000003", "v=0 browser")
	require.NoError(t, err)
	eng.mu.Lock()
	out := eng.placed[len(eng.placed)-1]
	eng.mu.Unlock()
	out.end("")
	free("peer end")

	// Inbound: synchronous end during tracking, reject, ring timeout, failed answer, browser loss.
	eng.incoming(&fakeCall{id: "IN-SYNC", syncEnd: true})
	free("inbound synchronous end")
	eng.incoming(&fakeCall{id: "IN-REJ"})
	require.NoError(t, m.Reject("IN-REJ"))
	free("reject")
	eng.incoming(&fakeCall{id: "IN-TIMEOUT"})
	now = now.Add(2 * time.Minute)
	m.Active() // prune rejects the expired offer
	free("ring timeout")
	eng.incoming(&fakeCall{id: "IN-FAIL", answerErr: errors.New("answer failed")})
	_, err = m.OfferSDP(ctx, "IN-FAIL")
	require.NoError(t, err)
	require.Error(t, m.Answer("IN-FAIL", "v=0 answer"))
	free("failed answer")
	eng.incoming(&fakeCall{id: "IN-LOST"})
	_, err = m.OfferSDP(ctx, "IN-LOST")
	require.NoError(t, err)
	require.NoError(t, m.Answer("IN-LOST", "v=0 answer"))
	media.last().ev.Lost()
	free("browser leg lost")

	// Several live calls, then closeAll (switch off, client discarded, shutdown).
	eng.incoming(&fakeCall{id: "IN-A"})
	_, _, err = m.Start(ctx, "+5511900000004", "v=0 browser")
	require.NoError(t, err)
	assert.Equal(t, 2, pool.inUse())
	m.closeAll()
	free("closeAll")
}

func TestRefusalLogNamesTheReason(t *testing.T) {
	now := time.Now()
	pool := newCallSlots(func() int { return 1 })
	a, _, _ := newPooledManager(t, 1, nil, pool, &now, nil)
	assert.True(t, strings.Contains(a.refusalText(refusalDevice, 1), "device at its call limit (1)"))
	assert.True(t, strings.Contains(a.refusalText(refusalGateway, 1), "gateway at its call capacity (1)"))
}

func TestDuplicateInboundOfferHoldsOneSlot(t *testing.T) {
	now := time.Now()
	pool := newCallSlots(func() int { return 0 })
	m, eng, log := newPooledManager(t, 4, nil, pool, &now, nil)
	first := &fakeCall{id: "IN-DUP"}
	eng.incoming(first)
	eng.incoming(&fakeCall{id: "IN-DUP"})
	assert.Len(t, m.Active(), 1)
	assert.Equal(t, 1, pool.inUse())
	assert.Equal(t, 1, heldOf(m))
	assert.False(t, first.rejected, "the live call is not rejected")
	_, ended := log.last(EventCallEnded)
	assert.False(t, ended, "a duplicate offer emits nothing")
	require.NoError(t, m.Reject("IN-DUP"))
	assert.Equal(t, 0, pool.inUse())
	assert.Equal(t, 0, heldOf(m))
}

func TestMaxDurationReleasesTheGatewaySlot(t *testing.T) {
	pool := newCallSlots(func() int { return 0 })
	eng := &fakeEngine{}
	m := newCallManager(eng, callManagerOptions{
		MaxCalls: 2, Pool: pool, MaxDuration: 20 * time.Millisecond, Now: time.Now,
	})
	eng.incoming(&fakeCall{id: "IN-LONG"})
	require.Equal(t, 1, pool.inUse())
	require.Eventually(t, func() bool { return pool.inUse() == 0 && heldOf(m) == 0 }, 2*time.Second, 10*time.Millisecond)
}
