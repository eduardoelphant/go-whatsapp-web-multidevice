package callaudio

import (
	"math"
	"testing"
)

func sine(n, rate int, hz, amp float64) []float32 {
	out := make([]float32, n)
	for i := range out {
		out[i] = float32(amp * math.Sin(2*math.Pi*hz*float64(i)/float64(rate)))
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

func zeroCrossings(p []float32) int {
	n := 0
	for i := 1; i < len(p); i++ {
		if (p[i-1] < 0) != (p[i] < 0) {
			n++
		}
	}
	return n
}

func TestUpsampleKeepsToneAndLevel(t *testing.T) {
	in := sine(16000, 16000, 1000, 0.5) // 1 s at 16 kHz
	out := NewUpsampler().Process(in)
	if len(out) != 48000 {
		t.Fatalf("len %d, want 48000", len(out))
	}
	steady := out[480:]
	if r := rms(steady) / rms(in); r < 0.97 || r > 1.03 {
		t.Fatalf("level ratio %.3f, want about 1", r)
	}
	if z := zeroCrossings(steady); z < 1960 || z > 2000 {
		t.Fatalf("%d zero crossings, want about 1970 for 1 kHz", z)
	}
}

func TestDownsampleKeepsToneAndRejectsAliases(t *testing.T) {
	in := sine(48000, 48000, 1000, 0.5)
	out := NewDownsampler().Process(in)
	if len(out) != 16000 {
		t.Fatalf("len %d, want 16000", len(out))
	}
	steady := out[160:]
	if r := rms(steady) / rms(in); r < 0.97 || r > 1.03 {
		t.Fatalf("level ratio %.3f, want about 1", r)
	}
	if z := zeroCrossings(steady); z < 1960 || z > 2000 {
		t.Fatalf("%d zero crossings, want about 1970", z)
	}
	alias := NewDownsampler().Process(sine(48000, 48000, 12000, 0.5))
	if r := rms(alias[160:]) / 0.3536; r > 0.05 {
		t.Fatalf("12 kHz leaks through at %.3f of its level, want under 0.05", r)
	}
}

func TestResamplersAreChunkInvariant(t *testing.T) {
	in16 := sine(4800, 16000, 440, 0.4)
	whole := NewUpsampler().Process(in16)
	up := NewUpsampler()
	var parts []float32
	for i := 0; i < len(in16); i += 960 {
		parts = append(parts, up.Process(in16[i:i+960])...)
	}
	assertClose(t, whole, parts)

	in48 := sine(14400, 48000, 440, 0.4)
	wholeDown := NewDownsampler().Process(in48)
	down := NewDownsampler()
	var partsDown []float32
	for i := 0; i < len(in48); i += 1000 { // not a multiple of 3: the phase must carry over
		end := min(i+1000, len(in48))
		partsDown = append(partsDown, down.Process(in48[i:end])...)
	}
	assertClose(t, wholeDown, partsDown)
}

func assertClose(t *testing.T, a, b []float32) {
	t.Helper()
	if len(a) != len(b) {
		t.Fatalf("lengths %d and %d differ", len(a), len(b))
	}
	for i := range a {
		if math.Abs(float64(a[i]-b[i])) > 1e-5 {
			t.Fatalf("sample %d: %f vs %f", i, a[i], b[i])
		}
	}
}

func TestRechunker(t *testing.T) {
	r := NewRechunker(960)
	if frames := r.Push(make([]float32, 2880)); len(frames) != 3 || len(frames[2]) != 960 {
		t.Fatalf("2880 samples gave %d frames, want 3 of 960", len(frames))
	}
	if frames := r.Push(make([]float32, 500)); len(frames) != 0 {
		t.Fatal("500 samples must not make a frame")
	}
	in := sine(500, 48000, 440, 0.5)
	frames := r.Push(in)
	if len(frames) != 1 {
		t.Fatalf("1000 samples gave %d frames, want 1", len(frames))
	}
	if frames[0][500] != in[0] {
		t.Fatal("samples must keep their order across pushes")
	}
}

func TestFIFOPrefillUnderrunAndOverflow(t *testing.T) {
	f := NewFIFO(960, 3840)
	f.Write(sine(500, 16000, 440, 0.5))
	if rms(f.Read(960)) != 0 {
		t.Fatal("before prefill the reader gets silence")
	}
	f.Write(sine(1460, 16000, 440, 0.5)) // 1960 buffered
	if rms(f.Read(960)) == 0 {
		t.Fatal("after prefill the reader gets audio")
	}
	f.Read(960) // 40 left
	f.Read(960) // underrun
	if u, _ := f.Stats(); u != 1 {
		t.Fatalf("underruns %d, want 1", u)
	}
	f.Write(sine(500, 16000, 440, 0.5))
	if rms(f.Read(960)) != 0 {
		t.Fatal("after an underrun it waits for prefill again")
	}
	f.Write(make([]float32, 5000))
	if _, dropped := f.Stats(); dropped == 0 {
		t.Fatal("samples beyond max must be dropped")
	}
}
