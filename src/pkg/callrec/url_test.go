package callrec

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestURLPathSignsOneCall(t *testing.T) {
	secret := []byte("s3cret")
	now := time.Now()
	link := URLPath("/gowa", "CALL1", secret, now.Add(time.Hour))
	if !strings.HasPrefix(link, "/gowa/call/CALL1/recording?exp=") {
		t.Fatalf("link %q", link)
	}
	u, _ := url.Parse(link)
	exp, sig := u.Query().Get("exp"), u.Query().Get("sig")
	if !Verify(secret, "CALL1", exp, sig, now) {
		t.Fatal("a fresh signature must verify")
	}
	if Verify(secret, "CALL2", exp, sig, now) {
		t.Fatal("a signature opens one call only")
	}
	if Verify(secret, "CALL1", exp, sig, now.Add(2*time.Hour)) {
		t.Fatal("an expired signature must fail")
	}
	if Verify([]byte("other"), "CALL1", exp, sig, now) {
		t.Fatal("another secret must fail")
	}
	if got := URLPath("", "CALL1", nil, now); got != "/call/CALL1/recording" {
		t.Fatalf("without a secret: %q", got)
	}
}
