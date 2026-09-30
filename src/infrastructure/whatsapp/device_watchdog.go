package whatsapp

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	domainDevice "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/device"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/safego"
	"github.com/sirupsen/logrus"
	"go.mau.fi/whatsmeow"
)

// Fork (elphant): reconnect watchdog for every paired device (spec 2026-09-30-gateway-g9).

const watchdogMaxWait = 15 * time.Minute

// watchedDevice is what the watchdog needs from a device; tests use a fake.
type watchedDevice interface {
	ID() string
	Paired() bool // has a stored identity: connecting resumes the session instead of pairing
	HasClient() bool
	Connected() bool
	Replaced() bool         // the session was opened elsewhere (StreamReplaced)
	ReconnectBlocked() bool // banned (TemporaryBan) or outdated client: redialing would only hurt
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
	ReconnectBlocked  bool       `json:"reconnect_blocked"`
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
			out[i] = instanceDevice{inst: inst, client: inst.GetClient()}
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

// watchdogConcurrency bounds how many devices a tick connects at once.
const watchdogConcurrency = 8

// Tick checks every device once. Devices are checked in parallel, so one slow connect does not
// delay the others.
func (w *ReconnectWatchdog) Tick(now time.Time) {
	devices := w.list()
	seen := make(map[string]bool, len(devices))
	var wg sync.WaitGroup
	sem := make(chan struct{}, watchdogConcurrency)
	for _, d := range devices {
		seen[d.ID()] = true
		wg.Add(1)
		sem <- struct{}{}
		go func(d watchedDevice) {
			defer safego.Recover("watchdog-tick")
			defer wg.Done()
			defer func() { <-sem }()
			w.check(d, now)
		}(d)
	}
	wg.Wait()
	w.mu.Lock()
	for id := range w.states {
		if !seen[id] {
			delete(w.states, id)
		}
	}
	w.mu.Unlock()
}

// check handles one device; a panic in it is contained so the other devices are still checked.
// The device is read before the lock is taken: those reads can wait on whatsmeow's socket lock
// while it dials, and a panic must never leave w.mu held.
func (w *ReconnectWatchdog) check(d watchedDevice, now time.Time) {
	defer safego.Recover("watchdog-device")

	id := d.ID()
	connected := d.Connected()
	eligible := !connected && d.HasClient() && d.Paired() && !d.Replaced() && !d.ReconnectBlocked()

	if !w.due(id, now, connected, eligible) {
		return
	}

	w.attempts.Add(1)
	if err := d.Connect(); err != nil {
		logrus.Warnf("Watchdog: reconnect of device %s failed: %v", id, err)
		return
	}
	w.successes.Add(1)
	logrus.Infof("Watchdog: reconnected device %s", id)
}

// due updates the device's state and reports whether a reconnect attempt must be made now.
func (w *ReconnectWatchdog) due(id string, now time.Time, connected, eligible bool) bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	st := w.states[id]
	if st == nil {
		st = &watchState{}
		w.states[id] = st
	}
	switch {
	case connected:
		st.attempts, st.nextAt, st.lastConnected = 0, time.Time{}, now
		return false
	case !eligible:
		return false
	case !st.nextAt.IsZero() && now.Before(st.nextAt):
		return false
	}
	// Due: schedule the next wait before connecting, so a panic or a hang still backs off.
	st.attempts++
	st.nextAt = now.Add(w.wait(st.attempts))
	return true
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

// Snapshot reports every current device. Devices are read before the lock is taken.
func (w *ReconnectWatchdog) Snapshot() []DeviceHealth {
	if w == nil {
		return nil
	}
	devices := w.list()
	out := make([]DeviceHealth, 0, len(devices))
	for _, d := range devices {
		out = append(out, DeviceHealth{
			ID:               d.ID(),
			State:            d.StateName(),
			Connected:        d.Connected(),
			LoggedIn:         d.LoggedIn(),
			StreamReplaced:   d.Replaced(),
			ReconnectBlocked: d.ReconnectBlocked(),
		})
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for i := range out {
		st := w.states[out[i].ID]
		if st == nil {
			continue
		}
		out[i].ReconnectAttempts = st.attempts
		if !st.lastConnected.IsZero() {
			t := st.lastConnected
			out[i].LastConnectedAt = &t
		}
		if !st.nextAt.IsZero() {
			t := st.nextAt
			out[i].NextAttemptAt = &t
		}
	}
	return out
}

// instanceDevice adapts a DeviceInstance to watchedDevice. The client is taken once, when the
// list is built, so one check never mixes two clients (a login can install a fresh one).
type instanceDevice struct {
	inst   *DeviceInstance
	client *whatsmeow.Client
}

func (d instanceDevice) ID() string { return d.inst.ID() }
func (d instanceDevice) Paired() bool {
	return d.client != nil && d.client.Store != nil && d.client.Store.ID != nil
}
func (d instanceDevice) HasClient() bool { return d.client != nil }
func (d instanceDevice) Connected() bool { return d.client != nil && d.client.IsConnected() }
func (d instanceDevice) Replaced() bool  { return !ShouldAutoReconnect(d.client) }
func (d instanceDevice) LoggedIn() bool  { return d.client != nil && d.client.IsLoggedIn() }
func (d instanceDevice) ReconnectBlocked() bool {
	return d.client != nil && reconnectBlocked(d.client, time.Now())
}

// StateName comes from the live client, not from DeviceInstance.State(): that cache is updated
// by whichever handler runs, and the registry slot can lag behind the client.
func (d instanceDevice) StateName() string {
	switch {
	case d.client != nil && d.client.IsLoggedIn():
		return string(domainDevice.DeviceStateLoggedIn)
	case d.client != nil && d.client.IsConnected():
		return string(domainDevice.DeviceStateConnected)
	default:
		return string(domainDevice.DeviceStateDisconnected)
	}
}

func (d instanceDevice) Connect() error {
	if d.client == nil {
		return errors.New("device has no client")
	}
	return d.client.Connect()
}

// reconnectBlockedClients holds the clients that must not be redialed: a banned account (until
// the ban ends) or an outdated client (until it connects again). Keyed by client.
var reconnectBlockedClients sync.Map // *whatsmeow.Client -> time.Time (zero: until Connected)

func blockReconnect(client *whatsmeow.Client, until time.Time) {
	if client != nil {
		reconnectBlockedClients.Store(client, until)
	}
}

// HoldReconnect keeps the watchdog off a client for d: a manual reconnect, login or logout from
// the API is in progress and a tick must not connect in the middle of it. The hold ends by
// itself, or when the client connects.
func HoldReconnect(client *whatsmeow.Client, d time.Duration) {
	blockReconnect(client, time.Now().Add(d))
}

func unblockReconnect(client *whatsmeow.Client) {
	if client != nil {
		reconnectBlockedClients.Delete(client)
	}
}

func reconnectBlocked(client *whatsmeow.Client, now time.Time) bool {
	value, ok := reconnectBlockedClients.Load(client)
	if !ok {
		return false
	}
	until := value.(time.Time)
	if !until.IsZero() && !now.Before(until) {
		reconnectBlockedClients.Delete(client)
		return false
	}
	return true
}
