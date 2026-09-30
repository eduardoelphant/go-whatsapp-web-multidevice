// Package staticurl signs and verifies expiring /statics URLs (fork: elphant, D-13).
//
// A signed URL carries `exp` (Unix seconds) and `sig`, an HMAC-SHA256 over the path below
// /statics (decoded, exactly as served) and the expiry. It opens one file until it expires,
// for a browser that cannot send Basic Auth credentials (an <img> tag on another origin).
package staticurl

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strconv"
	"time"
)

func mac(secret []byte, path string, exp int64) []byte {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte("statics|" + path + "|" + strconv.FormatInt(exp, 10)))
	return h.Sum(nil)
}

// Sign returns the query string ("exp=...&sig=...") that opens path until expires. path is the
// file path below /statics, for example "qrcode/scan-qr-slot.png".
func Sign(secret []byte, path string, expires time.Time) string {
	exp := expires.Unix()
	values := url.Values{}
	values.Set("exp", strconv.FormatInt(exp, 10))
	values.Set("sig", hex.EncodeToString(mac(secret, path, exp)))
	return values.Encode()
}

// Verify reports whether exp and sig are a valid, unexpired signature of path at now. An empty
// secret never verifies.
func Verify(secret []byte, path, exp, sig string, now time.Time) bool {
	if len(secret) == 0 || exp == "" || sig == "" {
		return false
	}
	expUnix, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || !now.Before(time.Unix(expUnix, 0)) {
		return false
	}
	given, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	return hmac.Equal(given, mac(secret, path, expUnix))
}
