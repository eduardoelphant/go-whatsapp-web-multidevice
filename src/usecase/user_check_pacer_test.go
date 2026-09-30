package usecase

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestCheckPacerSerializesAndSpacesOneKey(t *testing.T) {
	p := newCheckPacer()
	interval := 60 * time.Millisecond
	var mu sync.Mutex
	var starts []time.Time
	var running int32
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = p.run(context.Background(), "dev", interval, func() error {
				assert.Equal(t, int32(1), atomic.AddInt32(&running, 1))
				mu.Lock()
				starts = append(starts, time.Now())
				mu.Unlock()
				time.Sleep(10 * time.Millisecond)
				atomic.AddInt32(&running, -1)
				return nil
			})
		}()
	}
	wg.Wait()
	assert.Len(t, starts, 3)
	// each start is at least interval after the previous call finished (10 ms of work).
	for i := 1; i < len(starts); i++ {
		assert.GreaterOrEqual(t, starts[i].Sub(starts[i-1]), interval+10*time.Millisecond-5*time.Millisecond)
	}
}

func TestCheckPacerKeysDoNotBlockEachOther(t *testing.T) {
	p := newCheckPacer()
	release := make(chan struct{})
	started := make(chan struct{})
	go func() {
		_ = p.run(context.Background(), "a", time.Second, func() error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	done := make(chan struct{})
	go func() {
		_ = p.run(context.Background(), "b", time.Second, func() error { return nil })
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("key b was blocked by key a")
	}
	close(release)
}

func TestCheckPacerCancelledWaiterNeverRuns(t *testing.T) {
	p := newCheckPacer()
	release := make(chan struct{})
	started := make(chan struct{})
	go func() {
		_ = p.run(context.Background(), "dev", 0, func() error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	var called int32
	errCh := make(chan error, 1)
	go func() {
		errCh <- p.run(ctx, "dev", 0, func() error {
			atomic.AddInt32(&called, 1)
			return nil
		})
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	assert.ErrorIs(t, <-errCh, context.Canceled)
	close(release)
	assert.Equal(t, int32(0), atomic.LoadInt32(&called))
}

func TestCheckPacerCancelDuringIntervalWaitNeverRuns(t *testing.T) {
	p := newCheckPacer()
	assert.NoError(t, p.run(context.Background(), "dev", time.Hour, func() error { return nil }))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	var called int32
	err := p.run(ctx, "dev", time.Hour, func() error {
		atomic.AddInt32(&called, 1)
		return nil
	})
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, int32(0), atomic.LoadInt32(&called))
}

func TestCheckPacerReturnsFunctionError(t *testing.T) {
	p := newCheckPacer()
	boom := errors.New("boom")
	assert.ErrorIs(t, p.run(context.Background(), "dev", 0, func() error { return boom }), boom)
}

// D-8 2: when the slot and the cancellation are both ready, a cancelled request must not run.
func TestCheckPacerAlreadyCancelledNeverRunsEvenOnAFreeSlot(t *testing.T) {
	p := newCheckPacer()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var called int32

	for i := 0; i < 50; i++ {
		_ = p.run(ctx, "dev", 0, func() error {
			atomic.AddInt32(&called, 1)
			return nil
		})
	}

	assert.Equal(t, int32(0), atomic.LoadInt32(&called))
}
