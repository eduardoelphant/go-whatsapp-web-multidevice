package staticurl

import (
	"net/url"
	"testing"
	"time"
)

var (
	secret = []byte("a-long-random-secret")
	now    = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
)

func parse(t *testing.T, query string) (exp, sig string) {
	t.Helper()
	values, err := url.ParseQuery(query)
	if err != nil {
		t.Fatal(err)
	}
	return values.Get("exp"), values.Get("sig")
}

func TestASignedPathVerifiesUntilItExpires(t *testing.T) {
	query := Sign(secret, "qrcode/scan-qr-slot.png", now.Add(15*time.Minute))
	exp, sig := parse(t, query)

	if !Verify(secret, "qrcode/scan-qr-slot.png", exp, sig, now) {
		t.Fatal("a fresh signature must verify")
	}
	if !Verify(secret, "qrcode/scan-qr-slot.png", exp, sig, now.Add(14*time.Minute)) {
		t.Fatal("it must verify until the end")
	}
	if Verify(secret, "qrcode/scan-qr-slot.png", exp, sig, now.Add(15*time.Minute)) {
		t.Fatal("it must not verify at the expiry")
	}
	if Verify(secret, "qrcode/scan-qr-slot.png", exp, sig, now.Add(time.Hour)) {
		t.Fatal("it must not verify after the expiry")
	}
}

func TestASignatureIsForOnePath(t *testing.T) {
	exp, sig := parse(t, Sign(secret, "media/a.jpeg", now.Add(time.Minute)))

	if Verify(secret, "media/b.jpeg", exp, sig, now) {
		t.Fatal("a signature for one file must not open another")
	}
	if Verify(secret, "media/../media/a.jpeg", exp, sig, now) {
		t.Fatal("a different spelling of the path is a different path")
	}
}

func TestTamperedExpiryOrSignatureOrSecretFails(t *testing.T) {
	exp, sig := parse(t, Sign(secret, "media/a.jpeg", now.Add(time.Minute)))

	if Verify(secret, "media/a.jpeg", "9999999999", sig, now) {
		t.Fatal("a longer expiry must invalidate the signature")
	}
	if Verify(secret, "media/a.jpeg", exp, sig+"00", now) {
		t.Fatal("a tampered signature must fail")
	}
	if Verify([]byte("another-secret"), "media/a.jpeg", exp, sig, now) {
		t.Fatal("another secret must fail")
	}
}

func TestMissingOrMalformedPartsFail(t *testing.T) {
	_, sig := parse(t, Sign(secret, "media/a.jpeg", now.Add(time.Minute)))

	for name, args := range map[string][2]string{
		"no exp":         {"", sig},
		"no sig":         {"1790000000", ""},
		"exp not number": {"soon", sig},
	} {
		if Verify(secret, "media/a.jpeg", args[0], args[1], now) {
			t.Errorf("%s must fail", name)
		}
	}
	if Verify(nil, "media/a.jpeg", "1790000000", sig, now) || Verify([]byte{}, "media/a.jpeg", "1790000000", sig, now) {
		t.Fatal("an empty secret must never verify")
	}
}
