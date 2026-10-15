package callrec

import (
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/staticurl"
)

// resource is what a recording link signs. The "call-recording:" prefix keeps it apart from
// every path a /statics signature can open.
func resource(callID string) string { return "call-recording:" + callID }

// URLPath is the recording's path below the server root (with APP_BASE_PATH), signed until
// expires when secret is set. Without a secret the route needs Basic Auth.
func URLPath(basePath, callID string, secret []byte, expires time.Time) string {
	p := basePath + "/call/" + callID + "/recording"
	if len(secret) == 0 {
		return p
	}
	return p + "?" + staticurl.Sign(secret, resource(callID), expires)
}

// Verify reports whether exp and sig open the recording of callID at now.
func Verify(secret []byte, callID, exp, sig string, now time.Time) bool {
	return staticurl.Verify(secret, resource(callID), exp, sig, now)
}
