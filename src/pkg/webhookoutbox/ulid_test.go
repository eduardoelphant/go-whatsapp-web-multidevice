package webhookoutbox

import (
	"strings"
	"testing"
	"time"
)

func TestEncodeULIDKnownValues(t *testing.T) {
	var zero [16]byte
	if got := encodeULID(zero); got != strings.Repeat("0", 26) {
		t.Fatalf("zero = %s", got)
	}

	var max [16]byte
	for i := range max {
		max[i] = 0xFF
	}
	if got := encodeULID(max); got != "7"+strings.Repeat("Z", 25) {
		t.Fatalf("max = %s", got)
	}

	// Timestamp from the ULID spec example (01ARYZ6S41...).
	var id [16]byte
	ms := uint64(1469918176385)
	for i := 0; i < 6; i++ {
		id[i] = byte(ms >> (40 - 8*i))
	}
	if got := encodeULID(id); got != "01ARYZ6S41"+strings.Repeat("0", 16) {
		t.Fatalf("time prefix = %s", got)
	}
}

func TestULIDSortsInCreationOrder(t *testing.T) {
	var src ulidSource
	at := time.UnixMilli(1469918176385)

	first, err := src.next(at)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := src.next(at)                  // same millisecond
	third, _ := src.next(at.Add(-time.Second)) // clock went back
	fourth, _ := src.next(at.Add(time.Millisecond))

	if !(first < second && second < third && third < fourth) {
		t.Fatalf("not increasing: %s %s %s %s", first, second, third, fourth)
	}
	if len(first) != 26 || first[:10] != "01ARYZ6S41" || third[:10] != "01ARYZ6S41" {
		t.Fatalf("unexpected ids %s %s", first, third)
	}
}

func TestIncrementBytes(t *testing.T) {
	b := []byte{0x00, 0xFF}
	if !incrementBytes(b) || b[0] != 1 || b[1] != 0 {
		t.Fatalf("carry: %v", b)
	}
	b = []byte{0xFF, 0xFF}
	if incrementBytes(b) {
		t.Fatal("overflow not reported")
	}
}
