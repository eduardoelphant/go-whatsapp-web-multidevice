package whatsapp

import (
	"testing"
	"time"
)

var reachoutNow = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

const reachoutSuspect = 30 * time.Minute

func evChanged(s *reachoutState, now time.Time, active bool, typ string, ends time.Time) bool {
	_, changed := s.applyEvent(now, reachoutSuspect, active, typ, ends)
	return changed
}

func nChanged(s *reachoutState, now time.Time) bool {
	_, changed := s.mark463(now, reachoutSuspect)
	return changed
}

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

	changed := evChanged(&s, reachoutNow, true, "spam", ends)

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
	evChanged(&s, reachoutNow, true, "", time.Time{})

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
	evChanged(&s, reachoutNow, true, "x", reachoutNow.Add(time.Hour))

	changed := evChanged(&s, reachoutNow, false, "", time.Time{})

	if snap, _ := s.read(reachoutNow); !changed || snap.Active {
		t.Fatalf("state = %+v changed=%v, want cleared", snap, changed)
	}
}

func TestReachoutInactiveWhenAlreadyClearedIsNoChange(t *testing.T) {
	var s reachoutState
	if evChanged(&s, reachoutNow, false, "", time.Time{}) {
		t.Fatal("clearing a cleared state must not report a change")
	}
}

func TestReachoutIdenticalEventTwiceIsNoChange(t *testing.T) {
	var s reachoutState
	ends := reachoutNow.Add(time.Hour)
	evChanged(&s, reachoutNow, true, "x", ends)

	if evChanged(&s, reachoutNow, true, "x", ends) {
		t.Fatal("the same event must not report a change")
	}
	if !evChanged(&s, reachoutNow, true, "x", ends.Add(time.Hour)) {
		t.Fatal("a different end must report a change")
	}
	if !evChanged(&s, reachoutNow, true, "y", ends.Add(time.Hour)) {
		t.Fatal("a different type must report a change")
	}
}

func TestReachout463WhenClearedActivates(t *testing.T) {
	var s reachoutState

	changed := nChanged(&s, reachoutNow)

	snap, _ := s.read(reachoutNow)
	if !changed || !snap.Active || snap.Source != ReachoutSourceSend463 || snap.EndsAt != nil {
		t.Fatalf("state = %+v changed=%v", snap, changed)
	}
}

func TestReachout463WhenActiveIsNoChange(t *testing.T) {
	var s reachoutState
	evChanged(&s, reachoutNow, true, "x", reachoutNow.Add(time.Hour))

	if nChanged(&s, reachoutNow) {
		t.Fatal("a 463 on an active state must not report a change")
	}
	if snap, _ := s.read(reachoutNow); snap.Source != ReachoutSourceEvent {
		t.Fatalf("source = %q, the event state must stay as it was", snap.Source)
	}
}

// The consumer's last webhook still says active, so a 463 after the window is a renewal, not a
// new activation: the state is active again and nothing is reported.
func TestReachout463AfterExpiryKeepsItActiveWithoutAFlap(t *testing.T) {
	var s reachoutState
	nChanged(&s, reachoutNow)

	later := reachoutNow.Add(reachoutSuspect + time.Second)
	if nChanged(&s, later) {
		t.Fatal("a 463 after the window must not report a change: the last webhook still said active")
	}
	if snap, _ := s.read(later); !snap.Active {
		t.Fatal("the state must be active again")
	}
}

// An active state with no known end expires by time; the consumer's last webhook still says
// active, so WhatsApp's later "inactive" must still be reported as a change.
func TestReachoutInactiveEventAfterSilentExpiryStillReportsTheClear(t *testing.T) {
	var s reachoutState
	evChanged(&s, reachoutNow, true, "", time.Time{})

	later := reachoutNow.Add(reachoutSuspect + time.Minute)
	snap, changed := s.applyEvent(later, reachoutSuspect, false, "", time.Time{})

	if !changed || snap.Active {
		t.Fatalf("snap=%+v changed=%v, want a reported clear", snap, changed)
	}
}

func TestReachoutActiveEventAfterSilentExpiryIsNoFlap(t *testing.T) {
	var s reachoutState
	evChanged(&s, reachoutNow, true, "x", time.Time{})

	later := reachoutNow.Add(reachoutSuspect + time.Minute)
	snap, changed := s.applyEvent(later, reachoutSuspect, true, "x", time.Time{})

	if changed || !snap.Active {
		t.Fatalf("snap=%+v changed=%v, want still active with no new report", snap, changed)
	}
}

func TestReachoutEventWithAnEndInThePastIsCleared(t *testing.T) {
	var s reachoutState

	snap, changed := s.applyEvent(reachoutNow, reachoutSuspect, true, "x", reachoutNow.Add(-time.Hour))

	if changed || snap.Active {
		t.Fatalf("snap=%+v changed=%v, want cleared and unchanged", snap, changed)
	}
}

func TestReachout463OnAnActiveUnknownEndStateRenewsTheWindow(t *testing.T) {
	var s reachoutState
	nChanged(&s, reachoutNow)

	renewedAt := reachoutNow.Add(20 * time.Minute)
	if nChanged(&s, renewedAt) {
		t.Fatal("renewing must not report a change")
	}
	if snap, _ := s.read(reachoutNow.Add(40 * time.Minute)); !snap.Active {
		t.Fatal("the window must run from the latest 463")
	}
	if snap, _ := s.read(renewedAt.Add(reachoutSuspect)); snap.Active {
		t.Fatal("the renewed window must still end")
	}
}

func TestReachoutResetReportsWhetherItClearedAnActiveState(t *testing.T) {
	var s reachoutState
	if _, was := s.reset(); was {
		t.Fatal("resetting a cleared state is not a change")
	}
	nChanged(&s, reachoutNow)
	snap, was := s.reset()
	if !was || snap.Active {
		t.Fatalf("snap=%+v was=%v, want a reported clear", snap, was)
	}
	if after, _ := s.read(reachoutNow); after.Active {
		t.Fatal("the state must be cleared")
	}
}
