package whatsapp

import (
	"errors"
	"testing"
	"time"
)

type fakeWatched struct {
	id         string
	paired     bool
	hasClient  bool
	connected  bool
	replaced   bool
	loggedIn   bool
	state      string
	connectErr error
	panicOn    bool
	connects   int
}

func (f *fakeWatched) ID() string        { return f.id }
func (f *fakeWatched) Paired() bool      { return f.paired }
func (f *fakeWatched) HasClient() bool   { return f.hasClient }
func (f *fakeWatched) Connected() bool   { return f.connected }
func (f *fakeWatched) Replaced() bool    { return f.replaced }
func (f *fakeWatched) LoggedIn() bool    { return f.loggedIn }
func (f *fakeWatched) StateName() string { return f.state }
func (f *fakeWatched) Connect() error {
	f.connects++
	if f.panicOn {
		panic("connect boom")
	}
	return f.connectErr
}

var wdNow = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

const wdInterval = 2 * time.Minute

func newWD(devs ...*fakeWatched) *ReconnectWatchdog {
	return newReconnectWatchdog(func() []watchedDevice {
		out := make([]watchedDevice, len(devs))
		for i, d := range devs {
			out[i] = d
		}
		return out
	}, wdInterval)
}

func down(id string) *fakeWatched {
	return &fakeWatched{id: id, paired: true, hasClient: true, state: "disconnected"}
}

func TestWatchdogConnectedDeviceResetsAndRecordsTheTime(t *testing.T) {
	d := down("a")
	w := newWD(d)
	w.Tick(wdNow) // one failed attempt schedules a wait
	d.connected = true

	w.Tick(wdNow.Add(time.Minute))

	h := w.Snapshot()[0]
	if h.ReconnectAttempts != 0 || h.NextAttemptAt != nil || h.LastConnectedAt == nil || !h.LastConnectedAt.Equal(wdNow.Add(time.Minute)) {
		t.Fatalf("health = %+v, want reset with the last connected time", h)
	}
}

func TestWatchdogNeverConnectsUnpairedClientlessOrReplaced(t *testing.T) {
	unpaired := down("unpaired")
	unpaired.paired = false
	clientless := down("clientless")
	clientless.hasClient = false
	replaced := down("replaced")
	replaced.replaced = true
	w := newWD(unpaired, clientless, replaced)

	w.Tick(wdNow)

	for _, d := range []*fakeWatched{unpaired, clientless, replaced} {
		if d.connects != 0 {
			t.Errorf("%s was connected %d times, want 0", d.id, d.connects)
		}
	}
	if attempts, _ := w.Counters(); attempts != 0 {
		t.Fatalf("attempts = %d, want 0", attempts)
	}
}

func TestWatchdogConnectsADueDeviceAndCountsIt(t *testing.T) {
	d := down("a")
	w := newWD(d)

	w.Tick(wdNow)

	attempts, successes := w.Counters()
	if d.connects != 1 || attempts != 1 || successes != 1 {
		t.Fatalf("connects=%d attempts=%d successes=%d", d.connects, attempts, successes)
	}
}

func TestWatchdogWaitGrowsPerFailureAndIsCapped(t *testing.T) {
	d := down("a")
	d.connectErr = errors.New("no route")
	w := newWD(d)

	now := wdNow
	var waits []time.Duration
	for i := 0; i < 6; i++ {
		w.Tick(now)
		next := w.Snapshot()[0].NextAttemptAt
		if next == nil {
			t.Fatalf("attempt %d: no next attempt time", i+1)
		}
		waits = append(waits, next.Sub(now))
		now = *next
	}

	want := []time.Duration{2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 15 * time.Minute, 15 * time.Minute, 15 * time.Minute}
	for i := range want {
		if waits[i] != want[i] {
			t.Errorf("wait %d = %v, want %v", i+1, waits[i], want[i])
		}
	}
	if _, successes := w.Counters(); successes != 0 {
		t.Fatalf("successes = %d, want 0", successes)
	}
}

func TestWatchdogSkipsADeviceThatIsNotYetDue(t *testing.T) {
	d := down("a")
	d.connectErr = errors.New("no route")
	w := newWD(d)
	w.Tick(wdNow)

	w.Tick(wdNow.Add(time.Minute)) // inside the 2 minute wait

	if d.connects != 1 {
		t.Fatalf("connects = %d, want 1", d.connects)
	}
}

func TestWatchdogForgetsARemovedDevice(t *testing.T) {
	d := down("a")
	list := []*fakeWatched{d}
	w := newReconnectWatchdog(func() []watchedDevice {
		out := make([]watchedDevice, len(list))
		for i, x := range list {
			out[i] = x
		}
		return out
	}, wdInterval)
	w.Tick(wdNow)

	list = nil
	w.Tick(wdNow.Add(time.Minute))

	if got := w.Snapshot(); len(got) != 0 {
		t.Fatalf("snapshot = %+v, want empty", got)
	}
	if len(w.states) != 0 {
		t.Fatalf("state entries = %d, want 0", len(w.states))
	}
}

func TestWatchdogAPanicInConnectDoesNotStopTheTick(t *testing.T) {
	bad := down("bad")
	bad.panicOn = true
	good := down("good")
	w := newWD(bad, good)

	w.Tick(wdNow)

	if good.connects != 1 {
		t.Fatalf("good device connects = %d, want 1 after a panic on another device", good.connects)
	}
}

func TestWatchdogSnapshotReportsStateAndFlags(t *testing.T) {
	d := down("a")
	d.state = "logged_in"
	d.connected, d.loggedIn, d.replaced = true, true, true
	w := newWD(d)

	h := w.Snapshot()[0]
	if h.ID != "a" || h.State != "logged_in" || !h.Connected || !h.LoggedIn || !h.StreamReplaced {
		t.Fatalf("health = %+v", h)
	}
}
