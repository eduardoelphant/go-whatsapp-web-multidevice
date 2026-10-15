package callrtc

import (
	"math"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/purpshell/meowcaller/opus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func requireOpus(t *testing.T) {
	t.Helper()
	if !opus.Available {
		t.Skip("libopus not linked (CGO_ENABLED=0)")
	}
}

func newTestEngine(t *testing.T, publicIP string) *Engine {
	t.Helper()
	e, err := NewEngine(Config{Port: 0, ListenIP: "127.0.0.1", PublicIP: publicIP, IncludeLoopback: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = e.Close() })
	return e
}

func sineFrame(hz float64) []float32 {
	out := make([]float32, FrameSamples)
	for i := range out {
		out[i] = float32(0.4 * math.Sin(2*math.Pi*hz*float64(i)/SampleRate))
	}
	return out
}

func rms(p []float32) float64 {
	var s float64
	for _, v := range p {
		s += float64(v) * float64(v)
	}
	return math.Sqrt(s / float64(len(p)))
}

// browser is a full (not lite) pion peer playing the agent's browser.
type browser struct {
	pc    *webrtc.PeerConnection
	track *webrtc.TrackLocalStaticSample
	got   atomic.Int64 // RTP packets received from GOWA
}

func newBrowser(t *testing.T) *browser {
	t.Helper()
	se := webrtc.SettingEngine{}
	se.SetIncludeLoopbackCandidate(true)
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	// Chrome and Firefox answer an actpass offer with a=setup:active even when the offerer is
	// ICE-lite; pion would pick passive there, so pin the browser's answering role.
	require.NoError(t, se.SetAnsweringDTLSRole(webrtc.DTLSRoleClient))
	me := &webrtc.MediaEngine{}
	require.NoError(t, me.RegisterDefaultCodecs())
	pc, err := webrtc.NewAPI(webrtc.WithMediaEngine(me), webrtc.WithSettingEngine(se)).NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	track, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, "mic", "browser")
	require.NoError(t, err)
	_, err = pc.AddTrack(track)
	require.NoError(t, err)
	b := &browser{pc: pc, track: track}
	pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		go func() {
			for {
				if _, _, err := tr.ReadRTP(); err != nil {
					return
				}
				b.got.Add(1)
			}
		}()
	})
	t.Cleanup(func() { _ = pc.Close() })
	return b
}

// gather sets the local description and waits for ICE gathering: the browser side of non-trickle.
func gather(t *testing.T, pc *webrtc.PeerConnection, desc webrtc.SessionDescription) string {
	t.Helper()
	done := webrtc.GatheringCompletePromise(pc)
	require.NoError(t, pc.SetLocalDescription(desc))
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("browser gathering timed out")
	}
	return pc.LocalDescription().SDP
}

// speak sends a 440 Hz tone from the browser every 20 ms until the test ends.
func (b *browser) speak(t *testing.T) {
	enc, err := opus.NewEncoder(SampleRate, 32000, 5, false)
	require.NoError(t, err)
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				pkt, err := enc.Encode(sineFrame(440))
				if err == nil {
					_ = b.track.WriteSample(media.Sample{Data: pkt, Duration: 20 * time.Millisecond})
				}
			}
		}
	}()
}

func ssrcs(sdp string) map[string]bool {
	out := map[string]bool{}
	for _, line := range strings.Split(sdp, "\r\n") {
		if strings.HasPrefix(line, "a=ssrc:") {
			out[strings.Fields(strings.TrimPrefix(line, "a=ssrc:"))[0]] = true
		}
	}
	return out
}

func assertGOWASDP(t *testing.T, sdp, setup, ip string) {
	t.Helper()
	assert.NotContains(t, strings.ReplaceAll(sdp, "\r\n", ""), "\n", "every line ends in CRLF")
	assert.True(t, strings.HasSuffix(sdp, "\r\n"))
	assert.Contains(t, sdp, "a=ice-lite")
	assert.Contains(t, sdp, "a=setup:"+setup)
	assert.Contains(t, sdp, "opus/48000/2")
	assert.Contains(t, sdp, "a=candidate:")
	assert.Contains(t, sdp, " "+ip+" ", "host candidate on the public address")
	assert.Len(t, ssrcs(sdp), 1, "one audio SSRC")
}

func waitUp(t *testing.T, leg *Leg) {
	t.Helper()
	up := make(chan struct{}, 1)
	leg.OnConnected(func(ok bool) {
		if ok {
			select {
			case up <- struct{}{}:
			default:
			}
		}
	})
	select {
	case <-up:
	case <-time.After(10 * time.Second):
		t.Fatal("ICE did not connect")
	}
}

// exchangeAudio sends GOWA's tone to the browser and counts the browser's tone at GOWA.
func exchangeAudio(t *testing.T, leg *Leg, b *browser) {
	t.Helper()
	var heard atomic.Int64
	leg.OnFrame(func(pcm []float32) {
		if rms(pcm) > 0.01 {
			heard.Add(1)
		}
	})
	b.speak(t)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && (heard.Load() < 10 || b.got.Load() < 10) {
		require.NoError(t, leg.WriteFrame(sineFrame(880)))
		time.Sleep(20 * time.Millisecond)
	}
	assert.GreaterOrEqual(t, heard.Load(), int64(10), "decoded browser frames at GOWA")
	assert.GreaterOrEqual(t, b.got.Load(), int64(10), "RTP packets from GOWA at the browser")
}

func TestInboundLegConnectsAndCarriesAudio(t *testing.T) {
	requireOpus(t)
	eng := newTestEngine(t, "127.0.0.1")
	leg, err := eng.NewInbound()
	require.NoError(t, err)
	t.Cleanup(func() { _ = leg.Close() })
	assertGOWASDP(t, leg.LocalSDP(), "actpass", "127.0.0.1")

	b := newBrowser(t)
	require.NoError(t, b.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: leg.LocalSDP()}))
	answer, err := b.pc.CreateAnswer(nil)
	require.NoError(t, err)
	answerSDP := gather(t, b.pc, answer)
	assert.Contains(t, answerSDP, "a=setup:active", "the browser is the DTLS client")
	require.NoError(t, leg.SetAnswer(answerSDP))

	waitUp(t, leg)
	exchangeAudio(t, leg, b)
}

// Review Focus 5: an LF-only browser offer still works.
func TestOutboundLegAnswersBrowserOffer(t *testing.T) {
	requireOpus(t)
	eng := newTestEngine(t, "127.0.0.1")
	b := newBrowser(t)
	offer, err := b.pc.CreateOffer(nil)
	require.NoError(t, err)
	offerSDP := gather(t, b.pc, offer)

	leg, err := eng.NewOutbound(strings.ReplaceAll(offerSDP, "\r\n", "\n"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = leg.Close() })
	assertGOWASDP(t, leg.LocalSDP(), "passive", "127.0.0.1")

	require.NoError(t, b.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: leg.LocalSDP()}))
	waitUp(t, leg)
	exchangeAudio(t, leg, b)
}

// Review Focus 5: an offer without Opus is refused before anything else happens.
func TestOutboundOfferWithoutOpusIsRefused(t *testing.T) {
	requireOpus(t)
	eng := newTestEngine(t, "127.0.0.1")
	pcmu := "v=0\r\no=- 1 1 IN IP4 0.0.0.0\r\ns=-\r\nt=0 0\r\nm=audio 9 UDP/TLS/RTP/SAVPF 0\r\na=rtpmap:0 PCMU/8000\r\n"
	_, err := eng.NewOutbound(pcmu)
	assert.ErrorIs(t, err, errNoOpus)
	_, err = eng.NewOutbound("not an sdp")
	assert.Error(t, err)
	videoOnly := "v=0\r\no=- 1 1 IN IP4 0.0.0.0\r\ns=-\r\nt=0 0\r\nm=video 9 UDP/TLS/RTP/SAVPF 111\r\na=rtpmap:111 opus/48000/2\r\n"
	_, err = eng.NewOutbound(videoOnly)
	assert.ErrorIs(t, err, errNoOpus, "Opus outside an m=audio section does not count")
}

func TestPublicIPReplacesHostCandidates(t *testing.T) {
	requireOpus(t)
	eng := newTestEngine(t, "203.0.113.7")
	leg, err := eng.NewInbound()
	require.NoError(t, err)
	t.Cleanup(func() { _ = leg.Close() })
	assert.Contains(t, leg.LocalSDP(), " 203.0.113.7 ")
	assert.NotContains(t, leg.LocalSDP(), " 127.0.0.1 ")
}

func TestEngineRefusesABusyPort(t *testing.T) {
	eng := newTestEngine(t, "")
	port := eng.Addr().(*net.UDPAddr).Port
	_, err := NewEngine(Config{Port: port, ListenIP: "127.0.0.1"})
	assert.Error(t, err)
}

func TestNormalizeSDP(t *testing.T) {
	assert.Equal(t, "v=0\r\na=x\r\n", NormalizeSDP("v=0\na=x"))
	assert.Equal(t, "v=0\r\na=x\r\n", NormalizeSDP("v=0\r\na=x\r\n"))
	assert.Equal(t, "v=0\r\na=x\r\n", NormalizeSDP("v=0\r\na=x\n\n"))
	assert.Equal(t, "", NormalizeSDP(""))
}

// connectInbound runs the incoming-call flow against a new browser and waits for the leg to be up.
func connectInbound(t *testing.T, eng *Engine) (*Leg, *browser) {
	t.Helper()
	leg, err := eng.NewInbound()
	require.NoError(t, err)
	t.Cleanup(func() { _ = leg.Close() })
	b := newBrowser(t)
	require.NoError(t, b.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: leg.LocalSDP()}))
	answer, err := b.pc.CreateAnswer(nil)
	require.NoError(t, err)
	require.NoError(t, leg.SetAnswer(gather(t, b.pc, answer)))
	waitUp(t, leg)
	return leg, b
}

// Two calls share the engine's one port; closing one leaves the other carrying audio.
func TestTwoLegsShareOneEngine(t *testing.T) {
	requireOpus(t)
	eng := newTestEngine(t, "127.0.0.1")
	first, _ := connectInbound(t, eng)
	second, b2 := connectInbound(t, eng)
	require.NoError(t, first.Close())
	exchangeAudio(t, second, b2)
}

// ICE alone is not "up": with a browser fingerprint that does not match, DTLS fails and the leg
// is never reported connected.
func TestLegIsNotUpWithoutDTLS(t *testing.T) {
	requireOpus(t)
	eng := newTestEngine(t, "127.0.0.1")
	leg, err := eng.NewInbound()
	require.NoError(t, err)
	t.Cleanup(func() { _ = leg.Close() })
	var ups atomic.Int64
	leg.OnConnected(func(ok bool) {
		if ok {
			ups.Add(1)
		}
	})

	b := newBrowser(t)
	require.NoError(t, b.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: leg.LocalSDP()}))
	answer, err := b.pc.CreateAnswer(nil)
	require.NoError(t, err)
	answerSDP := gather(t, b.pc, answer)
	const prefix = "a=fingerprint:sha-256 "
	i := strings.Index(answerSDP, prefix)
	require.GreaterOrEqual(t, i, 0)
	at := i + len(prefix)
	flipped := "00"
	if answerSDP[at:at+2] == "00" {
		flipped = "11"
	}
	// The leg itself watches the peer-connection state, so the ICE hook is free for the test.
	var iceUp atomic.Bool
	leg.pc.OnICEConnectionStateChange(func(s webrtc.ICEConnectionState) {
		if s == webrtc.ICEConnectionStateConnected {
			iceUp.Store(true)
		}
	})
	require.NoError(t, leg.SetAnswer(answerSDP[:at]+flipped+answerSDP[at+2:]))

	require.Eventually(t, func() bool {
		s := leg.pc.ConnectionState()
		return s == webrtc.PeerConnectionStateFailed || s == webrtc.PeerConnectionStateClosed
	}, 10*time.Second, 10*time.Millisecond, "DTLS fails on the fingerprint")
	assert.True(t, iceUp.Load(), "ICE itself connected")
	assert.Zero(t, ups.Load(), "a leg without DTLS is not up")
}
