package whatsapp

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/safego"
	"github.com/sirupsen/logrus"
)

// Fork (elphant): reconnect watchdog for every paired device (spec 2026-09-30-gateway-g9).

const watchdogMaxWait = 15 * time.Minute

// watchedDevice is what the watchdog needs from a device; tests use a fake.
type watchedDevice interface {
	ID() string
	Paired() bool // has a stored identity: connecting resumes the session instead of pairing
	HasClient() bool
	Connected() bool
	Replaced() bool // the session was opened elsewhere (StreamReplaced)
	LoggedIn() bool
	StateName() string
	Connect() error
}

// DeviceHealth is one device in GET /health/devices.
type DeviceHealth struct {
	ID                string     `json:"id"`
	State             string     `json:"state"`
	Connected         bool       `json:"connected"`
	LoggedIn          bool       `json:"logged_in"`
	StreamReplaced    bool       `json:"stream_replaced"`
	LastConnectedAt   *time.Time `json:"last_connected_at"`
	ReconnectAttempts int        `json:"reconnect_attempts"`
	NextAttemptAt     *time.Time `json:"next_attempt_at"`
}

type watchState struct {
	attempts      int
	nextAt        time.Time
	lastConnected time.Time
}

// ReconnectWatchdog reconnects each paired, disconnected device that is not in StreamReplaced,
// with a wait that grows per device and resets when the device connects.
type ReconnectWatchdog struct {
	list     func() []watchedDevice
	interval time.Duration

	mu     sync.Mutex
	states map[string]*watchState

	attempts  atomic.Int64
	successes atomic.Int64
}

func newReconnectWatchdog(list func() []watchedDevice, interval time.Duration) *ReconnectWatchdog {
	return &ReconnectWatchdog{list: list, interval: interval, states: map[string]*watchState{}}
}

// NewReconnectWatchdog watches the devices the list function returns.
func NewReconnectWatchdog(list func() []*DeviceInstance, interval time.Duration) *ReconnectWatchdog {
	return newReconnectWatchdog(func() []watchedDevice {
		instances := list()
		out := make([]watchedDevice, len(instances))
		for i, inst := range instances {
			out[i] = instanceDevice{inst}
		}
		return out
	}, interval)
}

// Start runs the watchdog every interval until ctx ends. An interval of zero or less disables it.
func (w *ReconnectWatchdog) Start(ctx context.Context) {
	if w == nil || w.interval <= 0 {
		logrus.Info("reconnect watchdog disabled")
		return
	}
	safego.Loop("reconnect-watchdog", func() {
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				w.Tick(now)
			}
		}
	})
}

// Tick checks every device once.
func (w *ReconnectWatchdog) Tick(now time.Time) {
	devices := w.list()
	seen := make(map[string]bool, len(devices))
	for _, d := range devices {
		seen[d.ID()] = true
		w.check(d, now)
	}
	w.mu.Lock()
	for id := range w.states {
		if !seen[id] {
			delete(w.states, id)
		}
	}
	w.mu.Unlock()
}

// check handles one device; a panic in it is contained so the other devices are still checked.
func (w *ReconnectWatchdog) check(d watchedDevice, now time.Time) {
	defer safego.Recover("watchdog-device")

	w.mu.Lock()
	st := w.states[d.ID()]
	if st == nil {
		st = &watchState{}
		w.states[d.ID()] = st
	}
	switch {
	case d.Connected():
		st.attempts, st.nextAt, st.lastConnected = 0, time.Time{}, now
		w.mu.Unlock()
		return
	case !d.HasClient() || !d.Paired() || d.Replaced():
		w.mu.Unlock()
		return
	case !st.nextAt.IsZero() && now.Before(st.nextAt):
		w.mu.Unlock()
		return
	}
	// Due: schedule the next wait before connecting, so a panic or a hang below still backs off.
	st.attempts++
	st.nextAt = now.Add(w.wait(st.attempts))
	w.mu.Unlock()

	w.attempts.Add(1)
	if err := d.Connect(); err != nil {
		logrus.Warnf("Watchdog: reconnect of device %s failed: %v", d.ID(), err)
		return
	}
	w.successes.Add(1)
	logrus.Infof("Watchdog: reconnected device %s", d.ID())
}

// wait is interval * 2^(attempts-1), capped.
func (w *ReconnectWatchdog) wait(attempts int) time.Duration {
	d := w.interval
	for i := 1; i < attempts && d < watchdogMaxWait; i++ {
		d *= 2
	}
	if d > watchdogMaxWait {
		return watchdogMaxWait
	}
	return d
}

// Counters returns the reconnect attempts and the successful ones since start.
func (w *ReconnectWatchdog) Counters() (attempts, successes int64) {
	if w == nil {
		return 0, 0
	}
	return w.attempts.Load(), w.successes.Load()
}

// Snapshot reports every current device.
func (w *ReconnectWatchdog) Snapshot() []DeviceHealth {
	if w == nil {
		return nil
	}
	devices := w.list()
	out := make([]DeviceHealth, 0, len(devices))
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, d := range devices {
		h := DeviceHealth{
			ID:             d.ID(),
			State:          d.StateName(),
			Connected:      d.Connected(),
			LoggedIn:       d.LoggedIn(),
			StreamReplaced: d.Replaced(),
		}
		if st := w.states[d.ID()]; st != nil {
			h.ReconnectAttempts = st.attempts
			if !st.lastConnected.IsZero() {
				t := st.lastConnected
				h.LastConnectedAt = &t
			}
			if !st.nextAt.IsZero() {
				t := st.nextAt
				h.NextAttemptAt = &t
			}
		}
		out = append(out, h)
	}
	return out
}

// instanceDevice adapts a DeviceInstance to watchedDevice.
type instanceDevice struct{ inst *DeviceInstance }

func (d instanceDevice) ID() string { return d.inst.ID() }
func (d instanceDevice) Paired() bool {
	client := d.inst.GetClient()
	return client != nil && client.Store != nil && client.Store.ID != nil
}
func (d instanceDevice) HasClient() bool   { return d.inst.GetClient() != nil }
func (d instanceDevice) Connected() bool   { return d.inst.IsConnected() }
func (d instanceDevice) Replaced() bool    { return !ShouldAutoReconnect(d.inst.GetClient()) }
func (d instanceDevice) LoggedIn() bool    { return d.inst.IsLoggedIn() }
func (d instanceDevice) StateName() string { return string(d.inst.State()) }
func (d instanceDevice) Connect() error {
	client := d.inst.GetClient()
	if client == nil {
		return nil
	}
	return client.Connect()
}
