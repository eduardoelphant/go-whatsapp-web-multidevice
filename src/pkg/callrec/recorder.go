// Package callrec writes the stereo recording of a call, cleans old recordings and signs their
// download link (fork: elphant, voice calls stage 2b, B6).
package callrec

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"sync"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4/pkg/media/oggwriter"
	"github.com/purpshell/meowcaller/opus"
)

const (
	SampleRate   = 16000             // the WhatsApp leg's rate
	FrameSamples = 960               // per channel: 60 ms at 16 kHz
	Bitrate      = 48000             // total: 24 kbps a channel
	Format       = "ogg_opus_stereo" // recording.format in call.ended
)

var validCallID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// ValidCallID reports whether id is safe as a file name and a URL path segment.
func ValidCallID(id string) bool { return validCallID.MatchString(id) }

// Encoder encodes one interleaved stereo frame into one Opus packet.
type Encoder interface {
	Encode(pcm []float32) ([]byte, error)
}

// Info describes a closed recording.
type Info struct {
	Path     string
	Bytes    int64
	Duration time.Duration
}

var errClosed = errors.New("callrec: recorder closed")

// Recorder writes agent (left) and customer (right) into one Ogg Opus file as the call runs.
type Recorder struct {
	mu     sync.Mutex
	ow     *oggwriter.OggWriter
	enc    Encoder
	path   string
	pcm    []float32
	frames int
	seq    uint16
	ts     uint32
	closed bool
	info   Info
}

// Create starts a recording at path with libopus in forced stereo.
func Create(path string) (*Recorder, error) {
	enc, err := opus.NewEncoderChannels(SampleRate, 2, Bitrate, 5, false)
	if err != nil {
		return nil, fmt.Errorf("callrec: stereo encoder: %w", err)
	}
	return New(path, enc)
}

// New starts a recording at path with enc.
func New(path string, enc Encoder) (*Recorder, error) {
	ow, err := oggwriter.New(path, SampleRate, 2)
	if err != nil {
		return nil, err
	}
	return &Recorder{ow: ow, enc: enc, path: path, pcm: make([]float32, 2*FrameSamples)}, nil
}

// Write encodes one 60 ms frame (left = agent, right = customer) and appends it as its own Ogg
// page. A short or nil channel is padded with silence; samples are clamped to [-1, 1].
func (r *Recorder) Write(left, right []float32) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errClosed
	}
	for i := 0; i < FrameSamples; i++ {
		r.pcm[2*i] = sampleAt(left, i)
		r.pcm[2*i+1] = sampleAt(right, i)
	}
	pkt, err := r.enc.Encode(r.pcm)
	if err != nil {
		return err
	}
	r.seq++
	r.ts += FrameSamples * 3 // the Opus RTP clock is 48 kHz
	if err := r.ow.WriteRTP(&rtp.Packet{
		Header:  rtp.Header{Version: 2, PayloadType: 111, SequenceNumber: r.seq, Timestamp: r.ts},
		Payload: pkt,
	}); err != nil {
		return err
	}
	r.frames++
	return nil
}

func sampleAt(s []float32, i int) float32 {
	if i >= len(s) {
		return 0
	}
	switch v := s[i]; {
	case v > 1:
		return 1
	case v < -1:
		return -1
	default:
		return v
	}
}

// Close marks the end of stream and reports the file. A recording without frames is removed
// and reported with zero Bytes. Safe to call more than once.
func (r *Recorder) Close() (Info, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return r.info, nil
	}
	r.closed = true
	err := r.ow.Close()
	r.info = Info{Path: r.path, Duration: time.Duration(r.frames) * 60 * time.Millisecond}
	if r.frames == 0 {
		_ = os.Remove(r.path)
		return r.info, nil
	}
	if fi, statErr := os.Stat(r.path); statErr == nil {
		r.info.Bytes = fi.Size()
	}
	return r.info, err
}
