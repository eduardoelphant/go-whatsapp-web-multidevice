// Package callaudio converts call audio between WhatsApp's 16 kHz / 60 ms frames and WebRTC's
// 48 kHz / 20 ms frames (fork: elphant, voice calls stage 2b, B9). Mono float32 PCM in [-1, 1].
package callaudio

import "math"

const (
	// Ratio is 48 kHz over 16 kHz.
	Ratio = 3
	taps  = 48
)

// lowpass is a Blackman-windowed sinc at 48 kHz, cutoff 7.2 kHz (under the 8 kHz Nyquist of
// the 16 kHz side), normalised to unit gain at DC.
var lowpass = designLowpass(taps, 7200.0/48000.0)

func designLowpass(n int, fc float64) []float32 {
	h := make([]float64, n)
	mid := float64(n-1) / 2
	var sum float64
	for i := range h {
		x := float64(i) - mid
		s := 2 * fc
		if x != 0 {
			s = math.Sin(2*math.Pi*fc*x) / (math.Pi * x)
		}
		w := 0.42 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(n-1)) + 0.08*math.Cos(4*math.Pi*float64(i)/float64(n-1))
		h[i] = s * w
		sum += h[i]
	}
	out := make([]float32, n)
	for i := range h {
		out[i] = float32(h[i] / sum)
	}
	return out
}

// fir filters one window of taps samples (the filter is symmetric).
func fir(window []float32) float32 {
	var acc float32
	for k, c := range lowpass {
		acc += window[k] * c
	}
	return acc
}

// Upsampler turns 16 kHz into 48 kHz: zero-stuffing, then the low-pass removes the images.
type Upsampler struct{ hist []float32 }

func NewUpsampler() *Upsampler { return &Upsampler{hist: make([]float32, taps-1)} }

func (u *Upsampler) Process(in []float32) []float32 {
	buf := make([]float32, len(u.hist)+len(in)*Ratio)
	copy(buf, u.hist)
	for i, s := range in {
		buf[len(u.hist)+i*Ratio] = s * Ratio
	}
	out := make([]float32, len(in)*Ratio)
	for i := range out {
		out[i] = fir(buf[i : i+taps])
	}
	copy(u.hist, buf[len(buf)-len(u.hist):])
	return out
}

// Downsampler turns 48 kHz into 16 kHz: the low-pass removes what would alias, then it keeps
// every third sample. Input of any length works; the phase carries over.
type Downsampler struct {
	hist  []float32
	phase int // index, in the next input, of the first sample that ends an output window
}

func NewDownsampler() *Downsampler { return &Downsampler{hist: make([]float32, taps-1)} }

func (d *Downsampler) Process(in []float32) []float32 {
	buf := make([]float32, len(d.hist)+len(in))
	copy(buf, d.hist)
	copy(buf[len(d.hist):], in)
	out := make([]float32, 0, len(in)/Ratio+1)
	p := d.phase
	for ; p < len(in); p += Ratio {
		out = append(out, fir(buf[p:p+taps]))
	}
	d.phase = p - len(in)
	copy(d.hist, buf[len(buf)-len(d.hist):])
	return out
}
