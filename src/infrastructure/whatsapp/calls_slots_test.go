package whatsapp

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCallSlotsCap(t *testing.T) {
	limit := 2
	p := newCallSlots(func() int { return limit })
	assert.True(t, p.tryAcquire())
	assert.True(t, p.tryAcquire())
	assert.False(t, p.tryAcquire(), "the third slot is over the cap")
	assert.Equal(t, 2, p.inUse())
	assert.Equal(t, 2, p.limit())

	p.release()
	assert.True(t, p.tryAcquire())

	limit = 0 // read at use time; 0 means no cap
	assert.True(t, p.tryAcquire())
	assert.Equal(t, 3, p.inUse())
	assert.Equal(t, 0, p.limit())
}

func TestCallSlotsReleaseNeverGoesNegative(t *testing.T) {
	p := newCallSlots(func() int { return 1 })
	p.release()
	assert.Equal(t, 0, p.inUse())
	assert.True(t, p.tryAcquire())
	assert.False(t, p.tryAcquire())
}

func TestNilCallSlotsNeverRefuse(t *testing.T) {
	var p *callSlots
	assert.True(t, p.tryAcquire())
	p.release()
	assert.Equal(t, 0, p.inUse())
	assert.Equal(t, 0, p.limit())
}

func TestCallSlotsConcurrentAcquire(t *testing.T) {
	p := newCallSlots(func() int { return 5 })
	var wg sync.WaitGroup
	var mu sync.Mutex
	won := 0
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if p.tryAcquire() {
				mu.Lock()
				won++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, 5, won)
	assert.Equal(t, 5, p.inUse())
}
