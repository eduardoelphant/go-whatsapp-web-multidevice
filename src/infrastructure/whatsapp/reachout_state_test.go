package whatsapp

import (
	"testing"
	"time"
)

var reachoutNow = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

const reachoutSuspect = 30 * time.Minute

func TestReachoutStateUntouchedIsInactive(t *testing.T) {
	var s reachoutState
	snap, expired := s.read(reachoutNow)
	if snap.Active || expired {
		t.Fatalf("untouched state = %+v expired=%v, want inactive", snap, expired)
	}
}

func TestReachoutEventActiveWithEnds(t *testing.T) {
	var s reachoutState
	ends := reachoutNow.Add(3 * time.Hour)

	changed := s.applyEvent(reachoutNow, reachoutSuspect, true, "spam", ends)

	snap, _ := s.read(reachoutNow)
	if !changed || !snap.Active || snap.Source != ReachoutSourceEvent || snap.EnforcementType != "spam" {
		t.Fatalf("state = %+v changed=%v", snap, changed)
	}
	if snap.EndsAt == nil || !snap.EndsAt.Equal(ends) {
		t.Fatalf("EndsAt = %v, want %v", snap.EndsAt, ends)
	}
}

func TestReachoutEventActiveWithoutEndsExpiresBySuspectWindow(t *testing.T) {
	var s reachoutState
	s.applyEvent(reachoutNow, reachoutSuspect, true, "", time.Time{})

	snap, _ := s.read(reachoutNow)
	if !snap.Active || snap.EndsAt != nil {
		t.Fatalf("state = %+v, want active with unknown end", snap)
	}
	snap, expired := s.read(reachoutNow.Add(reachoutSuspect))
	if snap.Active || !expired {
		t.Fatalf("after the window: %+v expired=%v, want cleared and expired", snap, expired)
	}
	if _, again := s.read(reachoutNow.Add(reachoutSuspect + time.Minute)); again {
		t.Fatal("expiry must be reported once")
	}
}

func TestReachoutEventInactiveClears(t *testing.T) {
	var s reachoutState
	s.applyEvent(reachoutNow, reachoutSuspect, true, "x", reachoutNow.Add(time.Hour))

	changed := s.applyEvent(reachoutNow, reachoutSuspect, false, "", time.Time{})

	if snap, _ := s.read(reachoutNow); !changed || snap.Active {
		t.Fatalf("state = %+v changed=%v, want cleared", snap, changed)
	}
}

func TestReachoutInactiveWhenAlreadyClearedIsNoChange(t *testing.T) {
	var s reachoutState
	if s.applyEvent(reachoutNow, reachoutSuspect, false, "", time.Time{}) {
		t.Fatal("clearing a cleared state must not report a change")
	}
}

func TestReachoutIdenticalEventTwiceIsNoChange(t *testing.T) {
	var s reachoutState
	ends := reachoutNow.Add(time.Hour)
	s.applyEvent(reachoutNow, reachoutSuspect, true, "x", ends)

	if s.applyEvent(reachoutNow, reachoutSuspect, true, "x", ends) {
		t.Fatal("the same event must not report a change")
	}
	if !s.applyEvent(reachoutNow, reachoutSuspect, true, "x", ends.Add(time.Hour)) {
		t.Fatal("a different end must report a change")
	}
	if !s.applyEvent(reachoutNow, reachoutSuspect, true, "y", ends.Add(time.Hour)) {
		t.Fatal("a different type must report a change")
	}
}

func TestReachout463WhenClearedActivates(t *testing.T) {
	var s reachoutState

	changed := s.mark463(reachoutNow, reachoutSuspect)

	snap, _ := s.read(reachoutNow)
	if !changed || !snap.Active || snap.Source != ReachoutSourceSend463 || snap.EndsAt != nil {
		t.Fatalf("state = %+v changed=%v", snap, changed)
	}
}

func TestReachout463WhenActiveIsNoChange(t *testing.T) {
	var s reachoutState
	s.applyEvent(reachoutNow, reachoutSuspect, true, "x", reachoutNow.Add(time.Hour))

	if s.mark463(reachoutNow, reachoutSuspect) {
		t.Fatal("a 463 on an active state must not report a change")
	}
	if snap, _ := s.read(reachoutNow); snap.Source != ReachoutSourceEvent {
		t.Fatalf("source = %q, the event state must stay as it was", snap.Source)
	}
}

func TestReachout463AfterExpiryActivatesAgain(t *testing.T) {
	var s reachoutState
	s.mark463(reachoutNow, reachoutSuspect)

	later := reachoutNow.Add(reachoutSuspect + time.Second)
	if !s.mark463(later, reachoutSuspect) {
		t.Fatal("a 463 after the window expired must activate again")
	}
}
