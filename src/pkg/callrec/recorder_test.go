package callrec

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/purpshell/meowcaller/opus"
)

// tocEncoder returns a minimal valid Opus packet: TOC 0x5C is SILK wideband, 60 ms, stereo,
// one frame. It keeps the last frame it got.
type tocEncoder struct{ last []float32 }

func (e *tocEncoder) Encode(pcm []float32) ([]byte, error) {
	if len(pcm) != 2*FrameSamples {
		return nil, fmt.Errorf("got %d values, want %d", len(pcm), 2*FrameSamples)
	}
	e.last = append([]float32(nil), pcm...)
	return []byte{0x5C, 0x01, 0x02}, nil
}

type oggPage struct {
	headerType byte
	granule    uint64
	body       []byte
}

func readPages(t *testing.T, path string) []oggPage {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var pages []oggPage
	for len(data) >= 27 {
		if string(data[:4]) != "OggS" {
			t.Fatalf("no OggS capture pattern at a page start")
		}
		nseg := int(data[26])
		size := 0
		for _, s := range data[27 : 27+nseg] {
			size += int(s)
		}
		pages = append(pages, oggPage{
			headerType: data[5],
			granule:    binary.LittleEndian.Uint64(data[6:14]),
			body:       data[27+nseg : 27+nseg+size],
		})
		data = data[27+nseg+size:]
	}
	return pages
}

func TestRecorderWritesStereoOggOpus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "CALL1.ogg")
	r, err := New(path, &tocEncoder{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := r.Write(make([]float32, FrameSamples), make([]float32, FrameSamples)); err != nil {
			t.Fatal(err)
		}
	}
	info, err := r.Close()
	if err != nil {
		t.Fatal(err)
	}
	pages := readPages(t, path)
	if len(pages) != 7 {
		t.Fatalf("%d pages, want 2 headers + 5 audio", len(pages))
	}
	head := pages[0].body
	if string(head[:8]) != "OpusHead" || head[9] != 2 || binary.LittleEndian.Uint32(head[12:16]) != SampleRate {
		t.Fatalf("OpusHead: channels %d, rate %d", head[9], binary.LittleEndian.Uint32(head[12:16]))
	}
	if string(pages[1].body[:8]) != "OpusTags" {
		t.Fatal("second page must be OpusTags")
	}
	last := pages[len(pages)-1]
	if last.headerType&0x04 == 0 {
		t.Fatal("last page must carry the end-of-stream flag")
	}
	if last.granule != 5*2880 {
		t.Fatalf("granule %d, want %d (5 x 60 ms at 48 kHz)", last.granule, 5*2880)
	}
	fi, _ := os.Stat(path)
	if info.Bytes != fi.Size() || info.Duration != 300*time.Millisecond || info.Path != path {
		t.Fatalf("info %+v, file %d bytes", info, fi.Size())
	}
}

func TestRecorderKeepsPagesWrittenBeforeACrash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "CALL2.ogg")
	r, err := New(path, &tocEncoder{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := r.Write(nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	// No Close: the process died. Every packet is already a page on disk.
	if pages := readPages(t, path); len(pages) != 5 {
		t.Fatalf("%d pages on disk, want 2 headers + 3 audio", len(pages))
	}
}

func TestRecorderPutsAgentLeftAndCustomerRight(t *testing.T) {
	enc := &tocEncoder{}
	r, err := New(filepath.Join(t.TempDir(), "CALL3.ogg"), enc)
	if err != nil {
		t.Fatal(err)
	}
	left := make([]float32, FrameSamples)
	left[0], left[1] = 0.5, 2.0
	right := []float32{-0.25} // short: padded with silence
	if err := r.Write(left, right); err != nil {
		t.Fatal(err)
	}
	if enc.last[0] != 0.5 || enc.last[1] != -0.25 || enc.last[2] != 1 || enc.last[3] != 0 {
		t.Fatalf("interleaved %v, want L0=0.5 R0=-0.25 L1=1 (clamped) R1=0", enc.last[:4])
	}
}

func TestRecorderEmptyIsRemovedAndCloseIsFinal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "CALL4.ogg")
	r, err := New(path, &tocEncoder{})
	if err != nil {
		t.Fatal(err)
	}
	info, err := r.Close()
	if err != nil || info.Bytes != 0 {
		t.Fatalf("empty close: %+v, %v", info, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("an empty recording must be removed")
	}
	if _, err := r.Close(); err != nil {
		t.Fatal("Close must be idempotent")
	}
	if err := r.Write(nil, nil); err == nil {
		t.Fatal("Write after Close must fail")
	}
}

func TestCreateEncodesRealStereoOpus(t *testing.T) {
	if !opus.Available {
		t.Skip("libopus not linked (CGO_ENABLED=0)")
	}
	path := filepath.Join(t.TempDir(), "CALL5.ogg")
	r, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	left, right := make([]float32, FrameSamples), make([]float32, FrameSamples)
	for i := range left {
		left[i] = float32(0.3 * math.Sin(2*math.Pi*440*float64(i)/SampleRate))
		right[i] = float32(0.3 * math.Sin(2*math.Pi*1300*float64(i)/SampleRate))
	}
	for i := 0; i < 10; i++ {
		if err := r.Write(left, right); err != nil {
			t.Fatal(err)
		}
	}
	info, err := r.Close()
	if err != nil {
		t.Fatal(err)
	}
	pages := readPages(t, path)
	if len(pages) != 12 || pages[2].body[0]&0x04 == 0 {
		t.Fatalf("%d pages; first audio TOC 0x%02x must be stereo", len(pages), pages[2].body[0])
	}
	if info.Duration != 600*time.Millisecond {
		t.Fatalf("duration %v, want 600ms", info.Duration)
	}
}

func TestValidCallID(t *testing.T) {
	for _, ok := range []string{"A1B2C3D4E5F60718293A4B5C6D7E8F90", "abc_DEF-1"} {
		if !ValidCallID(ok) {
			t.Errorf("%q must be valid", ok)
		}
	}
	for _, bad := range []string{"", "../x", "a/b", "a.ogg", "a b"} {
		if ValidCallID(bad) {
			t.Errorf("%q must be invalid", bad)
		}
	}
}
