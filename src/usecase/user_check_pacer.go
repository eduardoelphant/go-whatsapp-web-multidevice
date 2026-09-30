package usecase

import (
	"context"
	"sync"
	"time"
)

// checkPacer runs one function at a time per key and waits a minimum interval after the
// previous function of that key finished. WhatsApp bans accounts that hammer the contact
// sync, and a broadcast audience is thousands of numbers.
type checkPacer struct {
	mu    sync.Mutex
	slots map[string]*checkSlot
}

type checkSlot struct {
	sem  chan struct{} // capacity 1: whoever holds it runs
	last time.Time     // finish time of the previous run; only touched while holding sem
}

func newCheckPacer() *checkPacer {
	return &checkPacer{slots: make(map[string]*checkSlot)}
}

func (p *checkPacer) slot(key string) *checkSlot {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.slots[key]
	if !ok {
		s = &checkSlot{sem: make(chan struct{}, 1)}
		p.slots[key] = s
	}
	return s
}

// run waits for the key's turn and for the interval, then calls fn. A cancelled ctx while
// waiting returns ctx.Err() without calling fn.
func (p *checkPacer) run(ctx context.Context, key string, interval time.Duration, fn func() error) error {
	s := p.slot(key)
	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-s.sem }()

	if wait := interval - time.Since(s.last); !s.last.IsZero() && wait > 0 {
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
	// Deferred calls run last-in first-out: last is set before the semaphore is released.
	defer func() { s.last = time.Now() }()
	return fn()
}
