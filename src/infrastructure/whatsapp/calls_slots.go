package whatsapp

import (
	"sync"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/sirupsen/logrus"
)

// Fork (elphant): the gateway-wide call slot pool (concurrent calls, D-20, D-21). See
// docs/specs/2026-11-14-gateway-concurrent-calls-design.md.

// callSlots counts the call slots held by every CallManager of the process. Its mutex is a
// leaf: a manager takes it inside m.mu, and the pool never calls out while holding it. A nil
// pool never refuses and counts nothing.
type callSlots struct {
	mu   sync.Mutex
	held int
	max  func() int // the cap, read at use time; <= 0: no cap
}

func newCallSlots(max func() int) *callSlots { return &callSlots{max: max} }

// gatewayCallSlots is the pool shared by every manager that attachCalls creates.
var gatewayCallSlots = newCallSlots(config.CallsMaxTotal)

// tryAcquire takes one slot unless the cap is reached.
func (p *callSlots) tryAcquire() bool {
	if p == nil {
		return true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if limit := p.limitLocked(); limit > 0 && p.held >= limit {
		return false
	}
	p.held++
	return true
}

// release gives one slot back. A release with nothing held is a bug elsewhere: it is logged
// and ignored, so the count never goes negative.
func (p *callSlots) release() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.held == 0 {
		logrus.Warn("[CALLS] released a gateway call slot that was not held")
		return
	}
	p.held--
}

func (p *callSlots) inUse() int {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.held
}

// limit is the cap in force (0: none).
func (p *callSlots) limit() int {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.limitLocked()
}

func (p *callSlots) limitLocked() int {
	if p.max == nil {
		return 0
	}
	if v := p.max(); v > 0 {
		return v
	}
	return 0
}
