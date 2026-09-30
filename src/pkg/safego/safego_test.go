package safego

import (
	"bytes"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := logrus.StandardLogger().Out
	logrus.SetOutput(&buf)
	t.Cleanup(func() { logrus.SetOutput(previous) })
	return &buf
}

func TestGoContainsAPanicCountsItAndLogsTheName(t *testing.T) {
	buf := captureLog(t)
	before := Panics()
	var wg sync.WaitGroup
	wg.Add(1)

	Go("unit-test-goroutine", func() {
		defer wg.Done()
		panic("boom")
	})
	wg.Wait()
	// wg.Done runs before Recover (deferred later runs first); wait for the counter.
	waitForPanics(t, before+1)

	if !strings.Contains(buf.String(), "unit-test-goroutine") || !strings.Contains(buf.String(), "boom") {
		t.Fatalf("log = %q, want the name and the panic value", buf.String())
	}
}

func TestRecoverDeferredInAClosureContainsAPanic(t *testing.T) {
	captureLog(t)
	before := Panics()
	done := make(chan struct{})

	go func() {
		defer close(done)
		defer Recover("closure")
		panic("closure boom")
	}()
	<-done

	if Panics() != before+1 {
		t.Fatalf("Panics = %d, want %d", Panics(), before+1)
	}
}

func TestANormalRunCountsNothing(t *testing.T) {
	before := Panics()
	ran := make(chan struct{})

	Go("quiet", func() { close(ran) })
	<-ran

	if Panics() != before {
		t.Fatalf("Panics = %d, want %d", Panics(), before)
	}
}

func waitForPanics(t *testing.T, want int64) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if Panics() >= want {
			return
		}
		sleepBriefly()
	}
	t.Fatalf("Panics = %d, want at least %d", Panics(), want)
}

func TestLoopRestartsAfterAPanicAndStopsOnANormalReturn(t *testing.T) {
	captureLog(t)
	previous := loopDelay
	loopDelay = func(int) time.Duration { return time.Millisecond }
	t.Cleanup(func() { loopDelay = previous })

	before := Panics()
	var runs atomic.Int32
	done := make(chan struct{})

	Loop("restarting", func() {
		n := runs.Add(1)
		if n < 3 {
			panic("again")
		}
		close(done) // the third run returns normally: the loop ends
	})
	<-done
	waitForPanics(t, before+2)

	time.Sleep(20 * time.Millisecond)
	if runs.Load() != 3 {
		t.Fatalf("runs = %d, want 3 (two panics, then a normal return that stops the loop)", runs.Load())
	}
}

func TestLoopBackoffGrowsAndIsCapped(t *testing.T) {
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second}
	for i, w := range want {
		if got := loopDelay(i + 1); got != w {
			t.Errorf("delay(%d) = %v, want %v", i+1, got, w)
		}
	}
}
