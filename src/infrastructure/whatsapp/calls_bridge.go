package whatsapp

import (
	"errors"
	"io"
	"path/filepath"
	"sync"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/callaudio"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/callrec"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/callrtc"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/safego"
	meowcaller "github.com/purpshell/meowcaller"
	"github.com/purpshell/meowcaller/opus"
	"github.com/sirupsen/logrus"
)

// Fork (elphant): the audio bridge between the WhatsApp leg (meowcaller, 16 kHz / 60 ms) and
// the browser leg (pion, 48 kHz / 20 ms), with the stereo recorder tapping both directions
// (stage 2b: B9, B10, B6).

type rtcLeg interface {
	LocalSDP() string
	SetAnswer(sdp string) error
	WriteFrame(pcm []float32) error
	OnFrame(func(pcm []float32))
	OnConnected(func(up bool))
	Close() error
}

type rtcEngine interface {
	NewInbound() (rtcLeg, error)
	NewOutbound(offer string) (rtcLeg, error)
}

type recorder interface {
	Write(left, right []float32) error
	Close() (callrec.Info, error)
}

// pionEngine adapts *callrtc.Engine to rtcEngine.
type pionEngine struct{ e *callrtc.Engine }

func (p pionEngine) NewInbound() (rtcLeg, error) {
	l, err := p.e.NewInbound()
	if err != nil {
		return nil, err
	}
	return l, nil
}

func (p pionEngine) NewOutbound(offer string) (rtcLeg, error) {
	l, err := p.e.NewOutbound(offer)
	if err != nil {
		return nil, err
	}
	return l, nil
}

const (
	waFrame  = meowcaller.FrameSamples // 60 ms at 16 kHz
	rtcFrame = callrtc.FrameSamples    // 20 ms at 48 kHz
	// agentPrefill is the jitter buffer's start level at 16 kHz: one 60 ms pull plus two 20 ms
	// browser frames (1600 samples), so 40 to 80 ms stay buffered after each pull.
	agentPrefill = waFrame + 2*rtcFrame/callaudio.Ratio
	agentMax     = agentPrefill + 2*waFrame
	customerMax  = 4 * waFrame // customer audio waiting for the recorder (240 ms)
	// browserPrefill / browserMax size the browser-side jitter buffer at 48 kHz. WhatsApp sends
	// nothing during silence (DTX) and bursts afterwards, so the pacer holds 80 ms (four 20 ms
	// frames) before it starts and drops the oldest audio past 300 ms to bound the latency.
	browserPrefill = 4 * rtcFrame  // 3840 samples, 80 ms
	browserMax     = 15 * rtcFrame // 14400 samples, 300 ms
	// browserTick is the pacer's period: one rtcFrame per tick keeps the RTP clock on wall time.
	browserTick = 20 * time.Millisecond
)

// bridgeFactory makes one callBridge per call; it is the manager's mediaFactory.
type bridgeFactory struct {
	rtc         rtcEngine
	recordDir   string // empty: no recording
	grace       time.Duration
	newRecorder func(path string) (recorder, error)
}

func (f *bridgeFactory) Inbound() (callMedia, error) {
	leg, err := f.rtc.NewInbound()
	if err != nil {
		return nil, legError(err)
	}
	return newCallBridge(f, leg), nil
}

func (f *bridgeFactory) Outbound(offer string) (callMedia, error) {
	leg, err := f.rtc.NewOutbound(offer)
	if err != nil {
		return nil, legError(err)
	}
	return newCallBridge(f, leg), nil
}

// legError keeps a refused browser offer as it is (the manager answers 400) and turns anything
// else into a server error.
func legError(err error) error {
	switch {
	case errors.Is(err, callrtc.ErrBadOffer):
		return err
	case !opus.Available:
		return pkgError.InternalServerError("calls need libopus (a cgo build): " + err.Error())
	default:
		return pkgError.InternalServerError("browser leg: " + err.Error())
	}
}

// graceToken identifies one armed grace timer, so a timer that fired too late to be stopped
// cannot hang up a later outage.
type graceToken struct{ t *time.Timer }

// callBridge is one call's callMedia. Ownership of the single-goroutine audio stages:
// up runs on meowcaller's receive goroutine (the sink), down on the browser leg's reader
// goroutine; agent is the locked hand-off to meowcaller's send loop (the source); toBrowser is
// drained by the pacer goroutine, the only caller of leg.WriteFrame.
type callBridge struct {
	f         *bridgeFactory
	leg       rtcLeg
	up        *callaudio.Upsampler
	down      *callaudio.Downsampler
	agent     *callaudio.FIFO // agent audio (16 kHz) for meowcaller's send loop
	customer  *callaudio.FIFO // customer audio (16 kHz) for the recorder, sample-granular
	toBrowser *callaudio.FIFO // customer audio (48 kHz) waiting for the pacer
	tick      time.Duration   // pacer period (browserTick; tests shorten it)
	done      chan struct{}   // closed by close(): stops the pacer
	pacerDone chan struct{}   // closed when the pacer has exited; nil until it starts

	closeOnce sync.Once

	mu      sync.Mutex
	rec     recorder
	ev      mediaEvents
	started bool
	waReady bool
	rtcUp   bool
	flowing bool
	closed  bool
	grace   *graceToken
	result  *RecordingInfo
}

func newCallBridge(f *bridgeFactory, leg rtcLeg) *callBridge {
	b := &callBridge{
		f: f, leg: leg,
		up:   callaudio.NewUpsampler(),
		down: callaudio.NewDownsampler(), agent: callaudio.NewFIFO(agentPrefill, agentMax),
		customer:  callaudio.NewFIFO(0, customerMax),
		toBrowser: callaudio.NewFIFO(browserPrefill, browserMax),
		tick:      browserTick, done: make(chan struct{}),
	}
	// Registered before the answer is applied, so an early "up" is never missed.
	leg.OnFrame(b.fromBrowser)
	leg.OnConnected(b.onBrowser)
	return b
}

func (b *callBridge) LocalSDP() string              { return b.leg.LocalSDP() }
func (b *callBridge) Accept(answerSDP string) error { return b.leg.SetAnswer(answerSDP) }

func (b *callBridge) Start(h callHandle, ev mediaEvents) {
	b.mu.Lock()
	done := b.closed || b.started
	b.mu.Unlock()
	if done {
		return
	}
	rec := b.openRecorder(h)
	b.mu.Lock()
	if b.closed || b.started {
		b.mu.Unlock()
		if rec != nil {
			_, _ = rec.Close() // no frames: the file is removed
		}
		return
	}
	b.started, b.ev, b.rec = true, ev, rec
	b.pacerDone = make(chan struct{})
	safego.Go("calls_browser_pacer", b.pace)
	if !b.rtcUp {
		b.armGraceLocked()
	}
	b.mu.Unlock()
	h.Bridge(bridgeSource{b}, bridgeSink{b})
	b.maybeFlowing()
}

// pace sends one 20 ms frame to the browser per tick, silence when the jitter buffer is empty
// or still filling, so the RTP timestamps follow wall time whatever WhatsApp's DTX does.
func (b *callBridge) pace() {
	defer close(b.pacerDone)
	t := time.NewTicker(b.tick)
	defer t.Stop()
	for {
		select {
		case <-b.done:
			return
		case <-t.C:
		}
		select { // a tick that raced with close must not touch the closing leg
		case <-b.done:
			return
		default:
		}
		if err := b.leg.WriteFrame(b.toBrowser.Read(rtcFrame)); err != nil {
			logrus.Debugf("[CALLS] browser write: %v", err)
		}
	}
}

func (b *callBridge) openRecorder(h callHandle) recorder {
	if b.f.recordDir == "" || b.f.newRecorder == nil {
		return nil
	}
	id := h.ID()
	if !callrec.ValidCallID(id) {
		logrus.Warnf("[CALLS] not recording a call with %s: unsafe call id", maskPeer(h.Peer()))
		return nil
	}
	r, err := b.f.newRecorder(filepath.Join(b.f.recordDir, id+".ogg"))
	if err != nil {
		logrus.Warnf("[CALLS] call %s: recording unavailable: %v", id, err)
		return nil
	}
	return r
}

func (b *callBridge) WhatsAppReady() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.waReady = true
	b.mu.Unlock()
	b.maybeFlowing()
}

// onBrowser follows the browser leg's state; callrtc serialises its deliveries.
func (b *callBridge) onBrowser(up bool) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.rtcUp = up
	if b.started {
		if up {
			b.stopGraceLocked()
		} else {
			b.armGraceLocked()
		}
	}
	b.mu.Unlock()
	if up {
		b.maybeFlowing()
	}
}

func (b *callBridge) maybeFlowing() {
	b.mu.Lock()
	fire := b.started && !b.closed && b.waReady && b.rtcUp && !b.flowing
	if fire {
		b.flowing = true
	}
	fn := b.ev.Flowing
	b.mu.Unlock()
	if fire && fn != nil {
		fn()
	}
}

// armGraceLocked starts the B10 timer unless one runs: when it fires with the browser leg still
// down, the manager hangs up the WhatsApp leg.
func (b *callBridge) armGraceLocked() {
	if b.closed || b.grace != nil || b.f.grace <= 0 {
		return
	}
	tok := &graceToken{}
	tok.t = time.AfterFunc(b.f.grace, func() {
		defer safego.Recover("calls_rtc_grace")
		b.graceFired(tok)
	})
	b.grace = tok
}

func (b *callBridge) stopGraceLocked() {
	if b.grace != nil {
		b.grace.t.Stop()
		b.grace = nil
	}
}

func (b *callBridge) graceFired(tok *graceToken) {
	b.mu.Lock()
	if b.grace != tok {
		b.mu.Unlock()
		return // stopped or replaced while firing
	}
	b.grace = nil
	lost := !b.closed && !b.rtcUp
	fn := b.ev.Lost
	b.mu.Unlock()
	if lost && fn != nil {
		fn()
	}
}

// fromBrowser takes the agent's decoded audio (48 kHz, any packet length) into the jitter
// buffer (16 kHz).
func (b *callBridge) fromBrowser(pcm48 []float32) { b.agent.Write(b.down.Process(pcm48)) }

// bridgeSource is what meowcaller plays to the customer. Its ReadFrame is pulled every 60 ms by
// the send loop: it is the bridge's clock, so it also feeds the recorder.
type bridgeSource struct{ b *callBridge }

func (s bridgeSource) ReadFrame() ([]float32, error) {
	b := s.b
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, io.EOF
	}
	var rec recorder
	if b.waReady { // answered: ringing (early media) is not recorded
		rec = b.rec
	}
	b.mu.Unlock()
	agent := b.agent.Read(waFrame)
	customer := b.customer.Read(waFrame) // consumed even before the answer, so it stays current
	if rec != nil {
		if err := rec.Write(agent, customer); err != nil {
			logrus.Debugf("[CALLS] recorder: %v", err)
		}
	}
	return agent, nil
}

func (s bridgeSource) Close() error { return nil }

// bridgeSink receives the customer's decoded audio from meowcaller (16 kHz, 60 ms) and sends it
// to the browser's jitter buffer; the pacer sends it on a 20 ms clock.
type bridgeSink struct{ b *callBridge }

func (s bridgeSink) WriteFrame(frame []float32) error {
	b := s.b
	pcm := append([]float32(nil), frame...)
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.mu.Unlock()
	b.customer.Write(pcm)
	b.toBrowser.Write(b.up.Process(pcm))
	return nil
}

func (s bridgeSink) Close() error { return nil }

// Close ends the browser leg and the recording (B10: whichever leg ended first). Idempotent;
// concurrent callers wait for the first and all get the same recording.
func (b *callBridge) Close() *RecordingInfo {
	b.closeOnce.Do(b.close)
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.result
}

func (b *callBridge) close() {
	b.mu.Lock()
	b.closed = true
	b.stopGraceLocked()
	rec := b.rec
	b.rec = nil
	pd := b.pacerDone
	b.mu.Unlock()

	close(b.done)
	if pd != nil {
		<-pd // the pacer is gone before the leg closes
	}

	au, ad := b.agent.Stats()
	cu, cd := b.customer.Stats()
	logrus.Debugf("[CALLS] bridge closed: agent jitter buffer %d underruns, %d samples dropped; customer queue %d underruns, %d samples dropped", au, ad, cu, cd)

	bu, bd := b.toBrowser.Stats()
	logrus.Debugf("[CALLS] browser jitter buffer %d underruns, %d samples dropped", bu, bd)

	_ = b.leg.Close()
	var result *RecordingInfo
	if rec != nil {
		info, err := rec.Close()
		if err != nil {
			logrus.Warnf("[CALLS] close recording: %v", err)
		}
		if info.Bytes > 0 {
			result = &RecordingInfo{Path: info.Path, Bytes: info.Bytes, Duration: info.Duration}
		}
	}
	b.mu.Lock()
	b.result = result
	b.mu.Unlock()
}
