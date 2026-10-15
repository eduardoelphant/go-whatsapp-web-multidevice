// Package callrtc is the browser leg of a WhatsApp call (fork: elphant, voice calls stage 2b,
// B1/B2): pion WebRTC as an ICE-lite, DTLS-server endpoint on one UDP port, carrying mono Opus
// at 48 kHz in 20 ms frames, with non-trickle SDP.
package callrtc

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/safego"
	"github.com/pion/logging"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/purpshell/meowcaller/opus"
)

const (
	SampleRate    = 48000
	FrameSamples  = 960 // 20 ms at 48 kHz
	opusBitrate   = 32000
	gatherTimeout = 5 * time.Second
)

var errNoOpus = errors.New("callrtc: the offer has no Opus 48 kHz audio")

var opusCapability = webrtc.RTPCodecCapability{
	MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2, SDPFmtpLine: "minptime=10;useinbandfec=1",
}

// Config of the WebRTC endpoint.
type Config struct {
	Port            int    // UDP port shared by every call (ICE UDP mux)
	PublicIP        string // public IPv4 put in the host candidates (1:1 NAT); empty = local addresses
	ListenIP        string // address to bind; empty = all IPv4 addresses
	IncludeLoopback bool   // tests only
}

// Engine is the process-wide WebRTC endpoint.
type Engine struct {
	api  *webrtc.API
	conn *net.UDPConn
	mux  interface{ Close() error }
}

func NewEngine(cfg Config) (*Engine, error) {
	ip := net.IPv4zero
	if cfg.ListenIP != "" {
		if ip = net.ParseIP(cfg.ListenIP); ip == nil {
			return nil, fmt.Errorf("callrtc: bad listen address %q", cfg.ListenIP)
		}
	}
	if cfg.PublicIP != "" && net.ParseIP(cfg.PublicIP) == nil {
		return nil, fmt.Errorf("callrtc: bad public address %q", cfg.PublicIP)
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ip, Port: cfg.Port})
	if err != nil {
		return nil, fmt.Errorf("callrtc: listen on UDP %d: %w", cfg.Port, err)
	}
	mux := webrtc.NewICEUDPMux(logging.NewDefaultLoggerFactory().NewLogger("calls-rtc"), conn)

	se := webrtc.SettingEngine{}
	se.SetLite(true)
	se.SetICEUDPMux(mux)
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	se.SetIncludeLoopbackCandidate(cfg.IncludeLoopback)
	fail := func(err error) (*Engine, error) {
		_ = mux.Close()
		_ = conn.Close()
		return nil, err
	}
	if err := se.SetAnsweringDTLSRole(webrtc.DTLSRoleServer); err != nil {
		return fail(err)
	}
	if cfg.PublicIP != "" {
		if err := se.SetICEAddressRewriteRules(webrtc.ICEAddressRewriteRule{
			External: []string{cfg.PublicIP}, AsCandidateType: webrtc.ICECandidateTypeHost, Mode: webrtc.ICEAddressRewriteReplace,
		}); err != nil {
			return fail(err)
		}
	}
	me := &webrtc.MediaEngine{}
	if err := me.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: opusCapability, PayloadType: 111}, webrtc.RTPCodecTypeAudio); err != nil {
		return fail(err)
	}
	return &Engine{api: webrtc.NewAPI(webrtc.WithMediaEngine(me), webrtc.WithSettingEngine(se)), conn: conn, mux: mux}, nil
}

// Addr is the bound UDP address.
func (e *Engine) Addr() net.Addr { return e.conn.LocalAddr() }

func (e *Engine) Close() error {
	err := e.mux.Close()
	_ = e.conn.Close()
	return err
}

// Leg is one call's peer connection with the agent's browser.
type Leg struct {
	pc    *webrtc.PeerConnection
	track *webrtc.TrackLocalStaticSample
	enc   *opus.Encoder
	dec   *opus.Decoder
	local string

	reading sync.Once // only the first audio track is decoded: the decoder is not shared

	deliverMu   sync.Mutex // serialises OnConnected deliveries, replay included
	mu          sync.Mutex
	onFrame     func([]float32)
	onConnected func(bool)
	connected   bool
	closed      bool
}

func (e *Engine) newLeg() (*Leg, error) {
	enc, err := opus.NewEncoder(SampleRate, opusBitrate, 5, false)
	if err != nil {
		return nil, fmt.Errorf("callrtc: opus encoder: %w", err)
	}
	dec, err := opus.NewDecoder(SampleRate)
	if err != nil {
		return nil, fmt.Errorf("callrtc: opus decoder: %w", err)
	}
	pc, err := e.api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, err
	}
	track, err := webrtc.NewTrackLocalStaticSample(opusCapability, "audio", "gowa")
	if err != nil {
		_ = pc.Close()
		return nil, err
	}
	l := &Leg{pc: pc, track: track, enc: enc, dec: dec}
	pc.OnConnectionStateChange(l.onPCState)
	pc.OnTrack(l.onTrack)
	return l, nil
}

// NewInbound makes a leg whose LocalSDP is GOWA's offer (the incoming-call flow, B3).
func (e *Engine) NewInbound() (*Leg, error) {
	l, err := e.newLeg()
	if err != nil {
		return nil, err
	}
	sender, err := l.pc.AddTrack(l.track)
	if err != nil {
		_ = l.Close()
		return nil, err
	}
	l.drainRTCP(sender)
	offer, err := l.pc.CreateOffer(nil)
	if err == nil {
		err = l.finishLocal(offer)
	}
	if err != nil {
		_ = l.Close()
		return nil, err
	}
	return l, nil
}

// NewOutbound makes a leg that answers the browser's offer (the outbound-call flow, B4).
func (e *Engine) NewOutbound(remoteOffer string) (*Leg, error) {
	sdp := NormalizeSDP(remoteOffer)
	if err := requireOpusAudio(sdp); err != nil {
		return nil, err
	}
	l, err := e.newLeg()
	if err != nil {
		return nil, err
	}
	if err := l.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: sdp}); err != nil {
		_ = l.Close()
		return nil, fmt.Errorf("callrtc: browser offer: %w", err)
	}
	sender, err := l.pc.AddTrack(l.track)
	if err != nil {
		_ = l.Close()
		return nil, err
	}
	l.drainRTCP(sender)
	answer, err := l.pc.CreateAnswer(nil)
	if err == nil {
		err = l.finishLocal(answer)
	}
	if err != nil {
		_ = l.Close()
		return nil, err
	}
	return l, nil
}

// finishLocal applies desc and waits for ICE gathering, so the SDP carries every candidate.
func (l *Leg) finishLocal(desc webrtc.SessionDescription) error {
	gathered := webrtc.GatheringCompletePromise(l.pc)
	if err := l.pc.SetLocalDescription(desc); err != nil {
		return err
	}
	select {
	case <-gathered:
	case <-time.After(gatherTimeout):
		return errors.New("callrtc: ICE gathering timed out")
	}
	l.local = NormalizeSDP(l.pc.LocalDescription().SDP)
	return nil
}

func (l *Leg) LocalSDP() string { return l.local }

// SetAnswer applies the browser's answer to GOWA's offer.
func (l *Leg) SetAnswer(answer string) error {
	return l.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: NormalizeSDP(answer)})
}

// WriteFrame encodes one 20 ms frame and sends it to the browser. The frame must be
// FrameSamples of 48 kHz mono. Not safe for concurrent use: call it from one goroutine (shared
// encoder and output buffer).
func (l *Leg) WriteFrame(pcm []float32) error {
	pkt, err := l.enc.Encode(pcm)
	if err != nil {
		return err
	}
	return l.track.WriteSample(media.Sample{Data: pkt, Duration: time.Duration(len(pcm)) * time.Second / SampleRate})
}

// OnFrame registers fn for the browser's decoded audio: 48 kHz mono, one call per RTP packet, so
// the length follows the packet duration (960 samples for the browsers' default 20 ms, not always).
// fn runs on the leg's single reader goroutine.
func (l *Leg) OnFrame(fn func([]float32)) {
	l.mu.Lock()
	l.onFrame = fn
	l.mu.Unlock()
}

// OnConnected registers fn for up/down changes of the peer connection (ICE and DTLS) and replays
// "up" when the leg is already connected. Deliveries are serialised, the replay included, so the
// last state fn sees is the leg's state. fn must not call OnConnected.
func (l *Leg) OnConnected(fn func(bool)) {
	l.deliverMu.Lock()
	defer l.deliverMu.Unlock()
	l.mu.Lock()
	l.onConnected = fn
	up := l.connected
	l.mu.Unlock()
	if up {
		fn(true)
	}
}

// onPCState runs on goroutines pion starts per change, so their order is not guaranteed: it reads
// the current state under the delivery lock instead of trusting the argument.
func (l *Leg) onPCState(webrtc.PeerConnectionState) {
	l.deliverMu.Lock()
	defer l.deliverMu.Unlock()
	var up bool
	switch l.pc.ConnectionState() {
	case webrtc.PeerConnectionStateConnected:
		up = true
	case webrtc.PeerConnectionStateDisconnected, webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
		up = false
	default:
		return
	}
	l.mu.Lock()
	if l.connected == up {
		l.mu.Unlock()
		return
	}
	l.connected = up
	fn := l.onConnected
	l.mu.Unlock()
	if fn != nil {
		fn(up)
	}
}

func (l *Leg) onTrack(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
	if track.Kind() != webrtc.RTPCodecTypeAudio {
		return
	}
	l.reading.Do(func() { l.readTrack(track) })
}

func (l *Leg) readTrack(track *webrtc.TrackRemote) {
	safego.Go("calls_rtc_read", func() {
		for {
			pkt, _, err := track.ReadRTP()
			if err != nil {
				return
			}
			pcm := l.dec.Decode(pkt.Payload)
			if len(pcm) == 0 {
				continue
			}
			l.mu.Lock()
			fn := l.onFrame
			l.mu.Unlock()
			if fn != nil {
				fn(pcm)
			}
		}
	})
}

// drainRTCP reads the sender's RTCP so pion's interceptors keep running.
func (l *Leg) drainRTCP(sender *webrtc.RTPSender) {
	safego.Go("calls_rtc_rtcp", func() {
		buf := make([]byte, 1500)
		for {
			if _, _, err := sender.Read(buf); err != nil {
				return
			}
		}
	})
}

// Close ends the peer connection; safe to call more than once.
func (l *Leg) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	l.mu.Unlock()
	return l.pc.Close()
}
