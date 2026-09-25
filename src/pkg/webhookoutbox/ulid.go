package webhookoutbox

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"sync"
	"time"
)

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var errULIDOverflow = errors.New("webhookoutbox: ULID random part overflowed within one millisecond")

// ulidSource makes ULIDs: a 48-bit millisecond timestamp and 80 random bits,
// encoded as 26 Crockford base32 characters. Within one millisecond, or when
// the clock goes back, the random part is incremented instead of redrawn, so
// ids from one source always sort in creation order.
type ulidSource struct {
	mu     sync.Mutex
	lastMS uint64
	random [10]byte
}

func (u *ulidSource) next(now time.Time) (string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()

	ms := uint64(now.UnixMilli())
	if ms > u.lastMS {
		if _, err := rand.Read(u.random[:]); err != nil {
			return "", err
		}
		u.lastMS = ms
	} else if !incrementBytes(u.random[:]) {
		return "", errULIDOverflow
	}

	var id [16]byte
	for i := 0; i < 6; i++ {
		id[i] = byte(u.lastMS >> (40 - 8*i))
	}
	copy(id[6:], u.random[:])
	return encodeULID(id), nil
}

// incrementBytes adds one to b read as a big-endian number; false on overflow.
func incrementBytes(b []byte) bool {
	for i := len(b) - 1; i >= 0; i-- {
		b[i]++
		if b[i] != 0 {
			return true
		}
	}
	return false
}

// encodeULID writes the 128 bits as 26 base32 digits, most significant first;
// the first digit carries only the top 3 bits.
func encodeULID(id [16]byte) string {
	hi := binary.BigEndian.Uint64(id[:8])
	lo := binary.BigEndian.Uint64(id[8:])
	var out [26]byte
	for i := 25; i >= 0; i-- {
		out[i] = crockford[lo&31]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return string(out[:])
}
