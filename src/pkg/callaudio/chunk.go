package callaudio

// Rechunker regroups a stream of samples into frames of a fixed size.
type Rechunker struct {
	size int
	buf  []float32
}

func NewRechunker(size int) *Rechunker { return &Rechunker{size: size} }

// Push appends samples and returns every complete frame, in order.
func (r *Rechunker) Push(in []float32) [][]float32 {
	r.buf = append(r.buf, in...)
	var out [][]float32
	for len(r.buf) >= r.size {
		frame := make([]float32, r.size)
		copy(frame, r.buf[:r.size])
		out = append(out, frame)
		r.buf = r.buf[r.size:]
	}
	r.buf = append([]float32(nil), r.buf...) // do not pin the consumed prefix
	return out
}
