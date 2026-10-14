package whatsapp

import (
	"context"
	"path/filepath"
	"sort"
	"sync"
	"time"

	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/sirupsen/logrus"
)

// Fork (elphant): voice calls on one device. See docs/specs/2026-10-14-gateway-voice-calls-design.md.

var (
	ErrCallNotFound = pkgError.ValidationError("call not found, already ended, or no longer ringing")
	ErrCallLimit    = pkgError.ValidationError("too many calls on this device")
	ErrCallsOff     = pkgError.ValidationError("calls are not enabled for this device")
)

type callHandle interface {
	ID() string
	PeerMasked() string
	Answer() error
	Reject() error
	Hangup() error
	Play(path string) error
	Record(path string) error
	OnEnd(func(reason string))
}

type callEngine interface {
	OnIncomingCall(func(callHandle))
	Call(ctx context.Context, target string) (callHandle, error)
}

type callManagerOptions struct {
	MaxCalls   int
	RingWindow time.Duration
	TestAudio  string
	RecordDir  string
	Now        func() time.Time
}

// CallInfo is the public view of one call.
type CallInfo struct {
	CallID    string    `json:"call_id"`
	Direction string    `json:"direction"`
	Peer      string    `json:"peer"`
	State     string    `json:"state"`
	StartedAt time.Time `json:"started_at"`
}

type trackedCall struct {
	handle callHandle
	info   CallInfo
}

// CallManager tracks ringing and active calls of one device client.
type CallManager struct {
	engine callEngine
	opts   callManagerOptions
	mu     sync.Mutex
	calls  map[string]*trackedCall
}

func newCallManager(engine callEngine, opts callManagerOptions) *CallManager {
	if opts.MaxCalls < 1 {
		opts.MaxCalls = 1
	}
	if opts.RingWindow <= 0 {
		opts.RingWindow = time.Minute
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	m := &CallManager{engine: engine, opts: opts, calls: map[string]*trackedCall{}}
	engine.OnIncomingCall(m.onIncoming)
	return m
}

func (m *CallManager) onIncoming(h callHandle) {
	m.mu.Lock()
	m.pruneLocked()
	if m.activeCountLocked() >= m.opts.MaxCalls {
		m.mu.Unlock()
		logrus.Infof("[CALLS] rejecting %s from %s: device at its call limit", h.ID(), h.PeerMasked())
		_ = h.Reject()
		return
	}
	m.track(h, "inbound", "ringing")
	m.mu.Unlock()
}

// track registers a call; caller holds m.mu.
func (m *CallManager) track(h callHandle, direction, state string) *trackedCall {
	tc := &trackedCall{handle: h, info: CallInfo{
		CallID: h.ID(), Direction: direction, Peer: h.PeerMasked(), State: state, StartedAt: m.opts.Now(),
	}}
	m.calls[h.ID()] = tc
	id := h.ID()
	h.OnEnd(func(reason string) {
		m.mu.Lock()
		delete(m.calls, id)
		m.mu.Unlock()
		logrus.Infof("[CALLS] call %s ended: %s", id, reason)
	})
	return tc
}

// pruneLocked drops offers that rang past the ring window.
func (m *CallManager) pruneLocked() {
	now := m.opts.Now()
	for id, tc := range m.calls {
		if tc.info.State == "ringing" && now.Sub(tc.info.StartedAt) > m.opts.RingWindow {
			delete(m.calls, id)
		}
	}
}

func (m *CallManager) activeCountLocked() int {
	n := 0
	for _, tc := range m.calls {
		if tc.info.State == "active" || tc.info.Direction == "outbound" {
			n++
		}
	}
	return n
}

func (m *CallManager) attachMedia(h callHandle) {
	if m.opts.TestAudio != "" {
		if err := h.Play(m.opts.TestAudio); err != nil {
			logrus.Warnf("[CALLS] play %s on %s: %v", m.opts.TestAudio, h.ID(), err)
		}
	}
	if m.opts.RecordDir != "" {
		if err := h.Record(filepath.Join(m.opts.RecordDir, h.ID()+".wav")); err != nil {
			logrus.Warnf("[CALLS] record %s: %v", h.ID(), err)
		}
	}
}

// Answer answers a ringing inbound call.
func (m *CallManager) Answer(callID string) error {
	m.mu.Lock()
	m.pruneLocked()
	tc, ok := m.calls[callID]
	if !ok || tc.info.State != "ringing" {
		m.mu.Unlock()
		return ErrCallNotFound
	}
	if m.activeCountLocked() >= m.opts.MaxCalls {
		m.mu.Unlock()
		return ErrCallLimit
	}
	tc.info.State = "active"
	m.mu.Unlock()

	if err := tc.handle.Answer(); err != nil {
		m.mu.Lock()
		delete(m.calls, callID)
		m.mu.Unlock()
		return err
	}
	m.attachMedia(tc.handle)
	return nil
}

// Start places an outbound call to target (phone number or JID).
func (m *CallManager) Start(ctx context.Context, target string) (CallInfo, error) {
	m.mu.Lock()
	m.pruneLocked()
	if m.activeCountLocked() >= m.opts.MaxCalls {
		m.mu.Unlock()
		return CallInfo{}, ErrCallLimit
	}
	m.mu.Unlock()

	h, err := m.engine.Call(ctx, target)
	if err != nil {
		return CallInfo{}, err
	}
	m.mu.Lock()
	tc := m.track(h, "outbound", "active")
	info := tc.info
	m.mu.Unlock()
	m.attachMedia(h)
	return info, nil
}

// Hangup ends a ringing or active call.
func (m *CallManager) Hangup(callID string) error {
	m.mu.Lock()
	tc, ok := m.calls[callID]
	m.mu.Unlock()
	if !ok {
		return ErrCallNotFound
	}
	if tc.info.State == "ringing" {
		return tc.handle.Reject()
	}
	return tc.handle.Hangup()
}

// Active lists ringing and active calls, oldest first.
func (m *CallManager) Active() []CallInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked()
	out := make([]CallInfo, 0, len(m.calls))
	for _, tc := range m.calls {
		out = append(out, tc.info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out
}
