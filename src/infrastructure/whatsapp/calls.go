package whatsapp

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/safego"
	"github.com/sirupsen/logrus"
)

// Fork (elphant): voice calls on one device. Stage 2a: docs/specs/2026-10-14-gateway-voice-calls-design.md;
// stage 2b (browser leg, events, recording): docs/specs/2026-10-14-gateway-voice-calls-2b-design.md.

var (
	ErrCallNotFound = pkgError.ValidationError("call not found, already ended, or no longer ringing")
	ErrCallLimit    = pkgError.ValidationError("too many calls on this device")
	ErrCallsOff     = pkgError.ValidationError("calls are not enabled for this device")
	ErrCallAnswered = pkgError.ConflictError("call already answered")
	ErrCallsNoMedia = pkgError.ValidationError("browser audio is unavailable on this gateway (check WHATSAPP_CALLS_RTC_PORT and the start log)")
	ErrCallNoOffer  = pkgError.ValidationError("no offer was made for this call yet; fetch offer_sdp with GET /call/:call_id first")
)

// Meta's words for the direction of a call (B5).
const (
	DirectionUser     = "USER_INITIATED"     // the customer called the device
	DirectionBusiness = "BUSINESS_INITIATED" // the device called the customer
)

const (
	stateRinging = "ringing"
	stateActive  = "active"
)

// Call webhook events (stage 2b).
const (
	EventCallStatus   = "call.status"
	EventCallAnswered = "call.answered"
	EventCallEnded    = "call.ended"
)

// Why GOWA ended a call itself; empty when the engine reported the end.
const (
	localHangup      = "hangup"
	localReject      = "reject"
	localTimeout     = "timeout"
	localMaxDuration = "max_duration"
	localRTCFailed   = "rtc_failed"
	localError       = "error"
)

// CallPeer is the other party, as JID strings ("" when unknown). API responses and webhooks
// carry it in full; logs use maskPeer.
type CallPeer struct {
	PN  string `json:"pn"`
	LID string `json:"lid"`
}

// CallInfo is the public view of one call.
type CallInfo struct {
	CallID    string    `json:"call_id"`
	Direction string    `json:"direction"`
	Peer      CallPeer  `json:"peer"`
	State     string    `json:"state"`
	StartedAt time.Time `json:"started_at"`
}

// RecordingInfo describes a finished recording file.
type RecordingInfo struct {
	Path     string
	Bytes    int64
	Duration time.Duration
}

// CallEvent is one call.status, call.answered or call.ended event.
type CallEvent struct {
	Type       string
	Call       CallInfo
	Status     string // call.status: RINGING, ACCEPTED, REJECTED; call.ended: COMPLETED, FAILED, MISSED, REJECTED
	AnsweredBy string // call.answered: api or elsewhere
	EndReason  string // call.ended
	Duration   int    // call.ended, seconds of media on both legs
	Recording  *RecordingInfo
	At         time.Time
}

// pcmSource and pcmSink have the method sets of meowcaller's AudioSource and AudioSink:
// 16 kHz mono frames of meowcaller.FrameSamples.
type pcmSource interface {
	ReadFrame() ([]float32, error)
	Close() error
}

type pcmSink interface {
	WriteFrame(frame []float32) error
	Close() error
}

// callHandle is one WhatsApp call as the manager sees it.
type callHandle interface {
	ID() string
	Peer() CallPeer
	Answer() error
	Reject() error
	Hangup() error
	OnEnd(func(reason string))
	OnRinging(func()) // outbound: the peer's device rings
	OnAccept(func())  // outbound: the peer answered
	OnMedia(func())   // the WhatsApp leg carries media
	Bridge(src pcmSource, sink pcmSink)
}

type callEngine interface {
	OnIncomingCall(func(callHandle))
	Call(ctx context.Context, target string) (callHandle, error)
}

// mediaEvents are the browser leg's signals back to the manager.
type mediaEvents struct {
	Flowing func() // both legs carry audio
	Lost    func() // the browser leg was down longer than the grace period
}

// callMedia is the browser side of one call: its SDP, its audio bridge and its recording.
//
// Contract for implementations (Task 7): every method is safe for concurrent use. Close may run
// concurrently with Accept, Start or WhatsAppReady (a hangup or the peer's end can race an
// answer); Start or WhatsAppReady called after Close must be a no-op; Close is idempotent and
// returns the same recording every time. The manager drops the recording of a call it never
// answered, so Close should not leave an orphan file behind for one (or the file stays until
// retention deletes it).
type callMedia interface {
	LocalSDP() string                   // inbound: GOWA's offer; outbound: GOWA's answer
	Accept(answerSDP string) error      // inbound: the browser's answer
	Start(h callHandle, ev mediaEvents) // attach audio to the WhatsApp leg and watch the browser leg
	WhatsAppReady()                     // the WhatsApp leg carries media
	Close() *RecordingInfo              // end the browser leg and the recording; idempotent
}

type mediaFactory interface {
	Inbound() (callMedia, error)
	Outbound(offerSDP string) (callMedia, error)
}

type callManagerOptions struct {
	MaxCalls    int
	RingWindow  time.Duration // an inbound offer older than this is rejected as missed
	MaxDuration time.Duration // a tracked call older than this is hung up (0: no limit)
	OfferWait   time.Duration // how long OfferSDP waits for the engine to report an offer
	Now         func() time.Time
	Emit        func(CallEvent) // nil: no events; must not block or call back into the manager
	Media       mediaFactory    // nil: no browser leg (calls carry no audio)
}

type trackedCall struct {
	handle    callHandle
	info      CallInfo
	media     callMedia
	answering bool
	answered  bool // answered here (inbound) or by the peer (outbound)
	waReady   bool
	ended     bool
	local     string
	mediaAt   time.Time // both legs flowing
	timer     *time.Timer
	// emitMu orders this call's events: an event decided before the call ended is sent before
	// call.ended, and none is sent after it. Taken before m.mu, never inside it.
	emitMu sync.Mutex
}

// CallManager tracks the ringing and active calls of one device client. All fields of
// trackedCall are guarded by mu.
type CallManager struct {
	engine   callEngine
	opts     callManagerOptions
	mu       sync.Mutex
	calls    map[string]*trackedCall
	gone     map[string]time.Time // recently ended or refused call ids
	waiters  map[string][]chan struct{}
	reserved int    // outbound slots claimed before dialing
	slotID   string // slot the manager was attached with

	beforeEmit func(CallEvent) // tests only: runs just before an event is sent
}

func newCallManager(engine callEngine, opts callManagerOptions) *CallManager {
	if opts.MaxCalls < 1 {
		opts.MaxCalls = 1
	}
	if opts.RingWindow <= 0 {
		opts.RingWindow = time.Minute
	}
	if opts.OfferWait <= 0 {
		opts.OfferWait = 3 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	m := &CallManager{
		engine: engine, opts: opts,
		calls: map[string]*trackedCall{}, gone: map[string]time.Time{}, waiters: map[string][]chan struct{}{},
	}
	engine.OnIncomingCall(m.onIncoming)
	return m
}

func (m *CallManager) emit(ev CallEvent) {
	if m.beforeEmit != nil {
		m.beforeEmit(ev)
	}
	if m.opts.Emit != nil {
		m.opts.Emit(ev)
	}
}

func (m *CallManager) onIncoming(h callHandle) {
	m.prune()
	id := h.ID()
	m.mu.Lock()
	full := m.activeCountLocked() >= m.opts.MaxCalls
	if full {
		m.gone[id] = m.opts.Now()
		m.notifyLocked(id)
	}
	m.mu.Unlock()
	if full {
		info := CallInfo{CallID: id, Direction: DirectionUser, Peer: h.Peer(), State: stateRinging, StartedAt: m.opts.Now()}
		logrus.Infof("[CALLS] rejecting %s from %s: device at its call limit", id, maskPeer(info.Peer))
		_ = h.Reject()
		m.emit(CallEvent{Type: EventCallEnded, Call: info, Status: "MISSED", EndReason: "rejected", At: m.opts.Now()})
		return
	}
	tc := m.newTracked(h, DirectionUser, stateRinging, nil)
	m.mu.Lock()
	m.insertLocked(tc)
	m.mu.Unlock()
}

// newTracked builds a tracked call and wires the engine callbacks. It must be called without
// m.mu held: a handle may fire a callback (even OnEnd) synchronously.
func (m *CallManager) newTracked(h callHandle, direction, state string, media callMedia) *trackedCall {
	tc := &trackedCall{handle: h, media: media, info: CallInfo{
		CallID: h.ID(), Direction: direction, Peer: h.Peer(), State: state, StartedAt: m.opts.Now(),
	}}
	h.OnRinging(func() { m.onPeerRinging(tc) })
	h.OnAccept(func() { m.onPeerAccept(tc) })
	h.OnMedia(func() { m.onWhatsAppMedia(tc) })
	h.OnEnd(func(reason string) { m.finish(tc, reason) })
	return tc
}

// insertLocked registers a call unless it already ended, and arms its max duration.
func (m *CallManager) insertLocked(tc *trackedCall) {
	id := tc.info.CallID
	if !tc.ended {
		m.calls[id] = tc
		if m.opts.MaxDuration > 0 {
			tc.timer = time.AfterFunc(m.opts.MaxDuration, func() {
				defer safego.Recover("calls_max_duration")
				logrus.Warnf("[CALLS] call %s reached the maximum duration; hanging up", id)
				_ = m.endLocal(id, localMaxDuration)
			})
		}
	}
	m.notifyLocked(id)
}

func (m *CallManager) notifyLocked(id string) {
	for _, ch := range m.waiters[id] {
		close(ch)
	}
	delete(m.waiters, id)
}

// prune rejects inbound offers that rang past the ring window and forgets old ended ids.
// It takes m.mu itself; callers must not hold it.
func (m *CallManager) prune() {
	now := m.opts.Now()
	var expired []string
	m.mu.Lock()
	for id, tc := range m.calls {
		if tc.info.Direction == DirectionUser && tc.info.State == stateRinging && !tc.answering &&
			now.Sub(tc.info.StartedAt) > m.opts.RingWindow {
			expired = append(expired, id)
		}
	}
	for id, at := range m.gone {
		if now.Sub(at) > m.opts.RingWindow {
			delete(m.gone, id)
		}
	}
	m.mu.Unlock()
	for _, id := range expired {
		_ = m.endLocal(id, localTimeout)
	}
}

func (m *CallManager) activeCountLocked() int {
	n := m.reserved
	for _, tc := range m.calls {
		if tc.info.State == stateActive || tc.answering || tc.info.Direction == DirectionBusiness {
			n++
		}
	}
	return n
}

// waitTracked returns a tracked call, waiting up to OfferWait for the engine to report it:
// the call.offer webhook can run before meowcaller has seen the same offer.
func (m *CallManager) waitTracked(ctx context.Context, callID string) (*trackedCall, error) {
	deadline := time.NewTimer(m.opts.OfferWait)
	defer deadline.Stop()
	for {
		m.mu.Lock()
		if tc, ok := m.calls[callID]; ok {
			m.mu.Unlock()
			return tc, nil
		}
		if _, gone := m.gone[callID]; gone {
			m.mu.Unlock()
			return nil, ErrCallNotFound
		}
		ch := make(chan struct{})
		m.waiters[callID] = append(m.waiters[callID], ch)
		m.mu.Unlock()
		select {
		case <-ch:
		case <-deadline.C:
			m.dropWaiter(callID, ch)
			return nil, ErrCallNotFound
		case <-ctx.Done():
			m.dropWaiter(callID, ch)
			return nil, ErrCallNotFound
		}
	}
}

// dropWaiter removes one waiter that gave up, leaving the others in place.
func (m *CallManager) dropWaiter(callID string, ch chan struct{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := m.waiters[callID]
	for i, c := range list {
		if c == ch {
			list = append(list[:i:i], list[i+1:]...)
			break
		}
	}
	if len(list) == 0 {
		delete(m.waiters, callID)
	} else {
		m.waiters[callID] = list
	}
}

// OfferSDP returns GOWA's SDP offer for a ringing inbound call, creating the browser leg on
// first use. The call.offer webhook and GET /call/:call_id both use it.
func (m *CallManager) OfferSDP(ctx context.Context, callID string) (string, error) {
	if m.opts.Media == nil {
		return "", ErrCallsNoMedia
	}
	tc, err := m.waitTracked(ctx, callID)
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	if tc.info.Direction != DirectionUser || tc.ended {
		m.mu.Unlock()
		return "", ErrCallNotFound
	}
	if tc.media != nil {
		sdp := tc.media.LocalSDP()
		m.mu.Unlock()
		return sdp, nil
	}
	m.mu.Unlock()

	media, err := m.opts.Media.Inbound() // outside the lock: ICE gathering takes a moment
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	if tc.ended || tc.media != nil {
		existing, ended := tc.media, tc.ended
		m.mu.Unlock()
		media.Close()
		if ended {
			return "", ErrCallNotFound
		}
		return existing.LocalSDP(), nil
	}
	tc.media = media
	m.mu.Unlock()
	return media.LocalSDP(), nil
}

// Answer answers a ringing inbound call with the browser's SDP answer (B3).
func (m *CallManager) Answer(callID, answerSDP string) error {
	if m.opts.Media == nil && answerSDP != "" {
		return ErrCallsNoMedia
	}
	m.prune()
	m.mu.Lock()
	tc, ok := m.calls[callID]
	if !ok || tc.info.Direction != DirectionUser {
		m.mu.Unlock()
		return ErrCallNotFound
	}
	if tc.answering || tc.answered {
		m.mu.Unlock()
		return ErrCallAnswered
	}
	if m.activeCountLocked() >= m.opts.MaxCalls {
		m.mu.Unlock()
		return ErrCallLimit
	}
	media := tc.media
	if m.opts.Media != nil && media == nil {
		m.mu.Unlock()
		return ErrCallNoOffer
	}
	tc.answering = true
	m.mu.Unlock()

	if media != nil {
		if err := media.Accept(answerSDP); err != nil {
			m.mu.Lock()
			tc.answering = false
			ended := tc.ended
			m.mu.Unlock()
			if ended {
				return ErrCallNotFound
			}
			return pkgError.ValidationError("invalid sdp: " + err.Error())
		}
	}
	// A hangup or the peer's end may have landed while the browser answer was applied: never
	// answer or reject a dead call (meowcaller still transmits a reject for one).
	m.mu.Lock()
	ended := tc.ended
	m.mu.Unlock()
	if ended {
		return ErrCallNotFound
	}
	if err := tc.handle.Answer(); err != nil {
		m.mu.Lock()
		ended := tc.ended
		if tc.local == "" {
			tc.local = localError
		}
		m.mu.Unlock()
		if ended {
			return ErrCallNotFound
		}
		_ = tc.handle.Reject()
		m.finish(tc, "")
		return err
	}
	m.mu.Lock()
	if tc.ended {
		m.mu.Unlock()
		return ErrCallNotFound
	}
	tc.answering, tc.answered, tc.info.State = false, true, stateActive
	ready := tc.waReady
	m.mu.Unlock()
	if media != nil {
		media.Start(tc.handle, m.mediaEvents(tc))
	}
	if ready {
		m.whatsAppReady(tc, media)
	}
	return nil
}

// Start places an outbound call to target (phone number or JID) with the browser's SDP offer
// and returns GOWA's SDP answer at once (B4). A bad offer places no call.
func (m *CallManager) Start(ctx context.Context, target, offerSDP string) (CallInfo, string, error) {
	if m.opts.Media == nil && offerSDP != "" {
		return CallInfo{}, "", ErrCallsNoMedia
	}
	m.prune()
	m.mu.Lock()
	if m.activeCountLocked() >= m.opts.MaxCalls {
		m.mu.Unlock()
		return CallInfo{}, "", ErrCallLimit
	}
	m.reserved++
	m.mu.Unlock()
	release := func() {
		m.mu.Lock()
		m.reserved--
		m.mu.Unlock()
	}

	var media callMedia
	if m.opts.Media != nil {
		var err error
		if media, err = m.opts.Media.Outbound(offerSDP); err != nil {
			release()
			var typed pkgError.GenericError
			if errors.As(err, &typed) {
				return CallInfo{}, "", err // the factory chose the status (an engine failure is a 500)
			}
			return CallInfo{}, "", pkgError.ValidationError("invalid sdp: " + err.Error())
		}
	}
	h, err := m.engine.Call(ctx, target)
	if err != nil {
		release()
		if media != nil {
			media.Close()
		}
		return CallInfo{}, "", err
	}
	tc := m.newTracked(h, DirectionBusiness, stateRinging, media)
	m.mu.Lock()
	m.reserved--
	m.insertLocked(tc)
	ended, info := tc.ended, tc.info
	m.mu.Unlock()
	if ended {
		return CallInfo{}, "", ErrCallNotFound
	}
	if media == nil {
		return info, "", nil
	}
	media.Start(h, m.mediaEvents(tc))
	return info, media.LocalSDP(), nil
}

// Reject declines a ringing inbound call.
func (m *CallManager) Reject(callID string) error {
	m.mu.Lock()
	tc, ok := m.calls[callID]
	ringing := ok && tc.info.Direction == DirectionUser && tc.info.State == stateRinging && !tc.answering
	m.mu.Unlock()
	if !ringing {
		return ErrCallNotFound
	}
	return m.endLocal(callID, localReject)
}

// Hangup ends a call: a ringing inbound call is rejected, anything else is hung up.
func (m *CallManager) Hangup(callID string) error { return m.endLocal(callID, localHangup) }

// endLocal ends a call on GOWA's initiative and records why. It finishes the call itself when
// the engine does not report the end (an engine that already forgot the call).
func (m *CallManager) endLocal(callID, why string) error {
	m.mu.Lock()
	tc, ok := m.calls[callID]
	if !ok {
		m.mu.Unlock()
		return ErrCallNotFound
	}
	ringingIn := tc.info.Direction == DirectionUser && tc.info.State == stateRinging && !tc.answering
	if tc.local == "" {
		tc.local = why
		if why == localHangup && ringingIn {
			tc.local = localReject
		}
	}
	h := tc.handle
	m.mu.Unlock()
	var err error
	if ringingIn {
		err = h.Reject()
	} else {
		err = h.Hangup()
	}
	m.finish(tc, "")
	return err
}

// closeAll ends every call of the manager (client discarded, or calls switched off).
func (m *CallManager) closeAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.calls))
	for id := range m.calls {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		_ = m.endLocal(id, localHangup)
	}
}

// emitFor sends the event decide returns, unless decide (run under m.mu) declines. Holding
// tc.emitMu across the decision and the send keeps every event of a call before its call.ended.
func (m *CallManager) emitFor(tc *trackedCall, decide func() (CallEvent, bool)) {
	tc.emitMu.Lock()
	defer tc.emitMu.Unlock()
	m.mu.Lock()
	ev, ok := decide()
	m.mu.Unlock()
	if ok {
		m.emit(ev)
	}
}

func (m *CallManager) onPeerRinging(tc *trackedCall) {
	m.emitFor(tc, func() (CallEvent, bool) {
		if tc.ended || tc.answered || tc.info.Direction != DirectionBusiness {
			return CallEvent{}, false
		}
		return CallEvent{Type: EventCallStatus, Call: tc.info, Status: "RINGING", At: m.opts.Now()}, true
	})
}

func (m *CallManager) onPeerAccept(tc *trackedCall) {
	var ready bool
	var media callMedia
	m.emitFor(tc, func() (CallEvent, bool) {
		if tc.ended || tc.answered || tc.info.Direction != DirectionBusiness {
			return CallEvent{}, false
		}
		tc.answered, tc.info.State = true, stateActive
		ready, media = tc.waReady, tc.media
		return CallEvent{Type: EventCallStatus, Call: tc.info, Status: "ACCEPTED", At: m.opts.Now()}, true
	})
	if ready {
		m.whatsAppReady(tc, media)
	}
}

// onWhatsAppMedia records that the WhatsApp leg carries media. With early media the relay can
// report it while the call still rings, so it only counts once the call is answered.
func (m *CallManager) onWhatsAppMedia(tc *trackedCall) {
	m.mu.Lock()
	if tc.ended || tc.waReady {
		m.mu.Unlock()
		return
	}
	tc.waReady = true
	answered, media := tc.answered, tc.media
	m.mu.Unlock()
	if answered {
		m.whatsAppReady(tc, media)
	}
}

func (m *CallManager) whatsAppReady(tc *trackedCall, media callMedia) {
	if media != nil {
		media.WhatsAppReady()
		return
	}
	m.markFlowing(tc)
}

// markFlowing records that media flows on both legs and sends call.answered (answered_by api).
func (m *CallManager) markFlowing(tc *trackedCall) {
	m.emitFor(tc, func() (CallEvent, bool) {
		if tc.ended || !tc.mediaAt.IsZero() {
			return CallEvent{}, false
		}
		tc.mediaAt = m.opts.Now()
		logrus.Infof("[CALLS] call %s: media flowing on both legs", tc.info.CallID)
		return CallEvent{Type: EventCallAnswered, Call: tc.info, AnsweredBy: "api", At: tc.mediaAt}, true
	})
}

func (m *CallManager) mediaEvents(tc *trackedCall) mediaEvents {
	id := tc.info.CallID
	return mediaEvents{
		Flowing: func() { m.markFlowing(tc) },
		Lost: func() {
			logrus.Warnf("[CALLS] call %s: browser leg lost; hanging up", id)
			_ = m.endLocal(id, localRTCFailed)
		},
	}
}

// finish ends a call once: it drops it, stops its timer, closes the browser leg and the
// recording, and sends the end events.
func (m *CallManager) finish(tc *trackedCall, engineReason string) {
	m.mu.Lock()
	if tc.ended {
		m.mu.Unlock()
		return
	}
	tc.ended = true
	id := tc.info.CallID
	if m.calls[id] == tc {
		delete(m.calls, id)
	}
	now := m.opts.Now()
	m.gone[id] = now
	m.notifyLocked(id)
	info, local, answered, mediaAt, media, timer := tc.info, tc.local, tc.answered, tc.mediaAt, tc.media, tc.timer
	m.mu.Unlock()

	if timer != nil {
		timer.Stop()
	}
	var rec *RecordingInfo
	if media != nil {
		rec = media.Close()
		if !answered {
			rec = nil // never answered here (missed, rejected, answered elsewhere): no recording
		}
	}
	status, reason := classifyEnd(endInput{Engine: engineReason, Local: local, Answered: answered, MediaFlowed: !mediaAt.IsZero()})
	duration := 0
	if !mediaAt.IsZero() {
		duration = int(math.Round(now.Sub(mediaAt).Seconds()))
	}
	tc.emitMu.Lock() // after tc.ended is set: waits for an event decided before the end
	defer tc.emitMu.Unlock()
	if reason == "answered_elsewhere" {
		m.emit(CallEvent{Type: EventCallAnswered, Call: info, AnsweredBy: "elsewhere", At: now})
	}
	if info.Direction == DirectionBusiness && status == "REJECTED" && local == "" {
		m.emit(CallEvent{Type: EventCallStatus, Call: info, Status: "REJECTED", At: now})
	}
	logrus.Infof("[CALLS] call %s with %s ended: %s (%s, engine %q, %ds)", id, maskPeer(info.Peer), reason, status, MaskDigits(engineReason), duration)
	m.emit(CallEvent{Type: EventCallEnded, Call: info, Status: status, EndReason: reason, Duration: duration, Recording: rec, At: now})
}

type endInput struct {
	Engine      string // meowcaller's reason; "" when the peer hung up
	Local       string // why GOWA ended it, or ""
	Answered    bool
	MediaFlowed bool
}

// classifyEnd maps an end to call.ended's status and end_reason (B5).
func classifyEnd(in endInput) (status, reason string) {
	switch in.Local {
	case localMaxDuration:
		return "FAILED", "timeout"
	case localTimeout:
		return "MISSED", "timeout"
	case localReject:
		return "REJECTED", "rejected"
	case localRTCFailed, localError:
		if in.MediaFlowed {
			return "COMPLETED", "error"
		}
		return "FAILED", "error"
	case localHangup:
		return afterMedia(in), "hangup_agent"
	}
	switch {
	case in.Engine == "accepted_elsewhere":
		return "COMPLETED", "answered_elsewhere"
	case in.Engine == "rejected" || in.Engine == "rejected_elsewhere":
		return "REJECTED", "rejected"
	case in.Engine == "timeout":
		return "MISSED", "timeout"
	case in.Engine == "offer_failed" || strings.HasPrefix(in.Engine, "server:"):
		if in.MediaFlowed {
			return "COMPLETED", "error"
		}
		return "FAILED", "error"
	}
	return afterMedia(in), "hangup_peer"
}

func afterMedia(in endInput) string {
	switch {
	case in.MediaFlowed:
		return "COMPLETED"
	case in.Answered:
		return "FAILED"
	default:
		return "MISSED"
	}
}

// Get returns a tracked call.
func (m *CallManager) Get(callID string) (CallInfo, bool) {
	m.prune()
	m.mu.Lock()
	defer m.mu.Unlock()
	tc, ok := m.calls[callID]
	if !ok {
		return CallInfo{}, false
	}
	return tc.info, true
}

// Active lists ringing and active calls, oldest first.
func (m *CallManager) Active() []CallInfo {
	m.prune()
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]CallInfo, 0, len(m.calls))
	for _, tc := range m.calls {
		out = append(out, tc.info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out
}

// maskPeer is the peer for logs: the last 4 digits only.
func maskPeer(p CallPeer) string {
	switch {
	case p.LID != "":
		return MaskDigits(p.LID)
	case p.PN != "":
		return MaskDigits(p.PN)
	default:
		return "unknown"
	}
}
