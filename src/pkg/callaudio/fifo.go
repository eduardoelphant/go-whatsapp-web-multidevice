package callaudio

import "sync"

// FIFO is the jitter buffer on the WebRTC side: the network writes samples as packets arrive,
// and the reader pulls fixed frames on its own clock. It plays silence until it holds prefill
// samples, again after an underrun, and drops the oldest samples beyond max.
type FIFO struct {
	mu        sync.Mutex
	buf       []float32
	prefill   int
	max       int
	playing   bool
	underruns int
	dropped   int
}

func NewFIFO(prefill, max int) *FIFO { return &FIFO{prefill: prefill, max: max} }

func (f *FIFO) Write(pcm []float32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.buf = append(f.buf, pcm...)
	if over := len(f.buf) - f.max; over > 0 {
		f.buf = append([]float32(nil), f.buf[over:]...)
		f.dropped += over
	}
}

// Read returns exactly n samples, padded with silence when the buffer runs short.
func (f *FIFO) Read(n int) []float32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]float32, n)
	if !f.playing {
		if len(f.buf) < f.prefill {
			return out
		}
		f.playing = true
	}
	if len(f.buf) < n {
		copy(out, f.buf)
		f.buf = f.buf[:0]
		f.playing = false
		f.underruns++
		return out
	}
	copy(out, f.buf[:n])
	f.buf = append(f.buf[:0], f.buf[n:]...)
	return out
}

// Stats reports underruns and dropped samples since the FIFO was made.
func (f *FIFO) Stats() (underruns, dropped int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.underruns, f.dropped
}
