package whatsapp

import (
	"sync"
	"time"
)

// Fork (elphant): per-device reach-out timelock state (spec 2026-09-30-gateway-g6).
// In memory: a restart forgets it until the next event or 463.
const (
	ReachoutSourceEvent   = "event"
	ReachoutSourceSend463 = "send_463"
)

type ReachoutSnapshot struct {
	Active          bool
	Source          string
	EnforcementType string
	EndsAt          *time.Time // nil when WhatsApp gave no end
}

type reachoutState struct {
	mu        sync.Mutex
	active    bool
	source    string
	typ       string
	until     time.Time // when the state expires
	endsKnown bool      // until came from WhatsApp, not from the suspect window
}

// clearLocked resets the fields; the mutex itself must never be overwritten while held.
func (s *reachoutState) clearLocked() {
	s.active, s.source, s.typ = false, "", ""
	s.until, s.endsKnown = time.Time{}, false
}

func (s *reachoutState) snapshotLocked() ReachoutSnapshot {
	snap := ReachoutSnapshot{Active: s.active, Source: s.source, EnforcementType: s.typ}
	if s.active && s.endsKnown {
		ends := s.until
		snap.EndsAt = &ends
	}
	return snap
}

// expireLocked clears an expired state and reports whether it did.
func (s *reachoutState) expireLocked(now time.Time) bool {
	if s.active && !now.Before(s.until) {
		s.clearLocked()
		return true
	}
	return false
}

// read returns the snapshot at now and whether reading it cleared an expired state.
func (s *reachoutState) read(now time.Time) (ReachoutSnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	expired := s.expireLocked(now)
	return s.snapshotLocked(), expired
}

// applyEvent applies WhatsApp's notification and reports whether the visible state changed.
func (s *reachoutState) applyEvent(now time.Time, suspect time.Duration, active bool, typ string, ends time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked(now)
	before := s.snapshotLocked()
	if !active {
		s.clearLocked()
	} else {
		s.active, s.source, s.typ = true, ReachoutSourceEvent, typ
		if ends.IsZero() {
			s.until, s.endsKnown = now.Add(suspect), false
		} else {
			s.until, s.endsKnown = ends, true
		}
	}
	return !sameSnapshot(before, s.snapshotLocked())
}

// mark463 records a send refused with 463 and reports whether the state changed. An already
// active state is left as it is.
func (s *reachoutState) mark463(now time.Time, suspect time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked(now)
	if s.active {
		return false
	}
	s.active, s.source, s.typ = true, ReachoutSourceSend463, ""
	s.until, s.endsKnown = now.Add(suspect), false
	return true
}

func sameSnapshot(a, b ReachoutSnapshot) bool {
	if a.Active != b.Active || a.Source != b.Source || a.EnforcementType != b.EnforcementType {
		return false
	}
	if (a.EndsAt == nil) != (b.EndsAt == nil) {
		return false
	}
	return a.EndsAt == nil || a.EndsAt.Equal(*b.EndsAt)
}
