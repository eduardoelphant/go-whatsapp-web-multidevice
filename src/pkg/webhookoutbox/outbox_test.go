package webhookoutbox

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var fastPolicy = Policy{
	Delays:        []time.Duration{20 * time.Millisecond},
	MaxAge:        time.Hour,
	Timeout:       2 * time.Second,
	MaxRetryAfter: time.Hour,
}

type received struct {
	eventID string
	raw     []byte
	header  http.Header
	at      time.Time
}

type receiver struct {
	mu  sync.Mutex
	got []received
}

func (r *receiver) snapshot() []received {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]received(nil), r.got...)
}

// startReceiver records every request; respond gets the 1-based request count.
func startReceiver(t *testing.T, respond func(n int, w http.ResponseWriter)) (*receiver, *httptest.Server) {
	t.Helper()
	rec := &receiver{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		id, _ := body["event_id"].(string)
		rec.mu.Lock()
		rec.got = append(rec.got, received{eventID: id, raw: raw, header: r.Header.Clone(), at: time.Now()})
		n := len(rec.got)
		rec.mu.Unlock()
		respond(n, w)
	}))
	t.Cleanup(srv.Close)
	return rec, srv
}

func always(status int) func(int, http.ResponseWriter) {
	return func(_ int, w http.ResponseWriter) { w.WriteHeader(status) }
}

func staticSecret(secret string) Resolver {
	return func(context.Context, string) (string, bool, error) { return secret, false, nil }
}

func startOutbox(t *testing.T, o *Outbox) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := o.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
}

func enqueueN(t *testing.T, o *Outbox, url string, n int) []Row {
	t.Helper()
	rows := make([]Row, 0, n)
	for i := range n {
		row, err := o.Enqueue(context.Background(), url, "global", "message", map[string]any{"event": "message", "seq": i})
		if err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
		rows = append(rows, row)
	}
	return rows
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func statusOf(s *Store, eventID string) string {
	row, err := s.Get(context.Background(), eventID)
	if err != nil || row == nil {
		return ""
	}
	return row.Status
}

func TestWorkerDeliversInOrderThroughTemporaryFailures(t *testing.T) {
	store := openStore(t)
	rec, srv := startReceiver(t, func(n int, w http.ResponseWriter) {
		if n <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	outbox := New(store, staticSecret("s"), fastPolicy)
	rows := enqueueN(t, outbox, srv.URL, 3) // queued before Start: order fixed before the first send
	startOutbox(t, outbox)

	waitFor(t, "three deliveries", func() bool { return statusOf(store, rows[2].EventID) == StatusDelivered })

	got := rec.snapshot()
	wantIDs := []string{rows[0].EventID, rows[0].EventID, rows[0].EventID, rows[1].EventID, rows[2].EventID}
	wantAttempts := []string{"1", "2", "3", "1", "1"}
	if len(got) != len(wantIDs) {
		t.Fatalf("got %d requests, want %d", len(got), len(wantIDs))
	}
	for i := range got {
		if got[i].eventID != wantIDs[i] || got[i].header.Get("X-Webhook-Attempt") != wantAttempts[i] {
			t.Fatalf("request %d = %s attempt %s; want %s attempt %s", i, got[i].eventID, got[i].header.Get("X-Webhook-Attempt"), wantIDs[i], wantAttempts[i])
		}
	}
	first, _ := store.Get(context.Background(), rows[0].EventID)
	if first.Attempts != 3 || first.LastStatusCode != 200 || first.DeliveredAt == nil {
		t.Fatalf("first row %+v", first)
	}
}

func TestPermanentRejectionGoesDeadWithoutBlockingTheQueue(t *testing.T) {
	store := openStore(t)
	rec, srv := startReceiver(t, func(n int, w http.ResponseWriter) {
		if n == 1 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	outbox := New(store, staticSecret("s"), fastPolicy)
	rows := enqueueN(t, outbox, srv.URL, 2)
	startOutbox(t, outbox)

	waitFor(t, "second row delivered", func() bool { return statusOf(store, rows[1].EventID) == StatusDelivered })
	dead, _ := store.Get(context.Background(), rows[0].EventID)
	if dead.Status != StatusDead || dead.Attempts != 1 || dead.LastStatusCode != 400 || dead.LastError != "HTTP 400" {
		t.Fatalf("rejected row %+v", dead)
	}
	if got := rec.snapshot(); len(got) != 2 || got[0].eventID != rows[0].EventID || got[1].eventID != rows[1].EventID {
		t.Fatalf("requests %+v", got)
	}
}

func TestRetryAfterIsHonoredOn429(t *testing.T) {
	store := openStore(t)
	rec, srv := startReceiver(t, func(n int, w http.ResponseWriter) {
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	outbox := New(store, staticSecret("s"), fastPolicy)
	rows := enqueueN(t, outbox, srv.URL, 1)
	startOutbox(t, outbox)

	waitFor(t, "delivery after Retry-After", func() bool { return statusOf(store, rows[0].EventID) == StatusDelivered })
	got := rec.snapshot()
	if gap := got[1].at.Sub(got[0].at); gap < 900*time.Millisecond {
		t.Fatalf("retried after %s, want about 1s", gap)
	}
}

func TestSecretIsResolvedAtSendTime(t *testing.T) {
	store := openStore(t)
	var secret atomic.Value
	secret.Store("old")
	resolve := func(_ context.Context, ref string) (string, bool, error) {
		if ref != "device:org_2" {
			return "", false, errors.New("unexpected ref " + ref)
		}
		return secret.Load().(string), false, nil
	}
	rec, srv := startReceiver(t, always(http.StatusOK))
	outbox := New(store, resolve, fastPolicy)
	row, err := outbox.Enqueue(context.Background(), srv.URL, "device:org_2", "message", map[string]any{"event": "message"})
	if err != nil {
		t.Fatal(err)
	}
	secret.Store("new") // rotated while the row was queued
	startOutbox(t, outbox)

	waitFor(t, "delivery", func() bool { return statusOf(store, row.EventID) == StatusDelivered })
	got := rec.snapshot()[0]
	mac := hmac.New(sha256.New, []byte("new"))
	mac.Write(got.raw)
	if want := "sha256=" + hex.EncodeToString(mac.Sum(nil)); got.header.Get("X-Hub-Signature-256") != want {
		t.Fatalf("signature %q, want %q", got.header.Get("X-Hub-Signature-256"), want)
	}
}

func TestHeadersCarryTheEventIdentity(t *testing.T) {
	store := openStore(t)
	rec, srv := startReceiver(t, always(http.StatusNoContent))
	outbox := New(store, staticSecret("s"), fastPolicy)
	row := enqueueN(t, outbox, srv.URL, 1)[0]
	startOutbox(t, outbox)
	waitFor(t, "delivery", func() bool { return statusOf(store, row.EventID) == StatusDelivered })

	first := rec.snapshot()[0]
	h := first.header
	if first.eventID != row.EventID || h.Get("X-Webhook-Id") != row.EventID ||
		h.Get("X-Webhook-Timestamp") != row.CreatedAt.UTC().Format(time.RFC3339) ||
		h.Get("X-Webhook-Attempt") != "1" || h.Get("X-Webhook-Replay") != "" ||
		h.Get("Content-Type") != "application/json" {
		t.Fatalf("headers %v, body id %s, row %+v", h, first.eventID, row)
	}

	if again, err := outbox.Redeliver(context.Background(), row.EventID); err != nil || again == nil {
		t.Fatalf("Redeliver = %v, %v", again, err)
	}
	waitFor(t, "redelivery", func() bool { return len(rec.snapshot()) == 2 })
	second := rec.snapshot()[1]
	if second.eventID != row.EventID || second.header.Get("X-Webhook-Replay") != "true" || second.header.Get("X-Webhook-Attempt") != "1" {
		t.Fatalf("redelivery headers %v", second.header)
	}
}

func TestRestartResumesPendingRows(t *testing.T) {
	uri := "file:" + filepath.Join(t.TempDir(), "outbox.db")
	first, err := OpenStore(uri)
	if err != nil {
		t.Fatal(err)
	}
	rec, srv := startReceiver(t, always(http.StatusOK))
	// GOWA queued two events and stopped before sending them.
	a := mustInsert(t, first, srv.URL, "message")
	b := mustInsert(t, first, srv.URL, "message.ack")
	first.Close()

	second, err := OpenStore(uri)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { second.Close() })
	startOutbox(t, New(second, staticSecret("s"), fastPolicy))

	waitFor(t, "resumed deliveries", func() bool { return statusOf(second, b.EventID) == StatusDelivered })
	if got := rec.snapshot(); len(got) != 2 || got[0].eventID != a.EventID || got[1].eventID != b.EventID {
		t.Fatalf("requests after restart %+v", got)
	}
}

func TestFailingRowGoesDeadAfterMaxAge(t *testing.T) {
	store := openStore(t)
	_, srv := startReceiver(t, always(http.StatusServiceUnavailable))
	policy := fastPolicy
	policy.MaxAge = 100 * time.Millisecond
	outbox := New(store, staticSecret("s"), policy)
	row := enqueueN(t, outbox, srv.URL, 1)[0]
	startOutbox(t, outbox)

	waitFor(t, "dead after max age", func() bool { return statusOf(store, row.EventID) == StatusDead })
	dead, _ := store.Get(context.Background(), row.EventID)
	if dead.Attempts < 2 || dead.LastStatusCode != 503 || !strings.Contains(dead.LastError, "gave up after") {
		t.Fatalf("dead row %+v", dead)
	}
}

func TestInsecureFlagSelectsTheTLSClient(t *testing.T) {
	store := openStore(t)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0) // silence the expected handshake errors
	srv.StartTLS()
	t.Cleanup(srv.Close)

	var insecure atomic.Bool
	resolve := func(context.Context, string) (string, bool, error) { return "s", insecure.Load(), nil }
	outbox := New(store, resolve, fastPolicy)
	row := enqueueN(t, outbox, srv.URL, 1)[0]
	startOutbox(t, outbox)

	waitFor(t, "a certificate failure", func() bool {
		r, _ := store.Get(context.Background(), row.EventID)
		return r != nil && r.Attempts >= 1 && strings.Contains(r.LastError, "certificate")
	})
	if statusOf(store, row.EventID) != StatusPending {
		t.Fatal("a TLS failure must be retried")
	}
	insecure.Store(true)
	waitFor(t, "delivery with skip-verify", func() bool { return statusOf(store, row.EventID) == StatusDelivered })
}

func TestShutdownMidSendKeepsTheRowPending(t *testing.T) {
	store := openStore(t)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) }) // runs before srv.Close

	outbox := New(store, staticSecret("s"), fastPolicy)
	row := enqueueN(t, outbox, srv.URL, 1)[0]
	ctx, cancel := context.WithCancel(context.Background())
	if err := outbox.Start(ctx); err != nil {
		t.Fatal(err)
	}
	<-entered
	cancel()
	time.Sleep(200 * time.Millisecond)

	got, _ := store.Get(context.Background(), row.EventID)
	if got.Status != StatusPending || got.Attempts != 0 || got.LastError != "" {
		t.Fatalf("row after shutdown %+v", got)
	}
}

func TestMalformedURLGoesDeadAtOnce(t *testing.T) {
	store := openStore(t)
	outbox := New(store, staticSecret("s"), fastPolicy)
	row := enqueueN(t, outbox, "http://[::1", 1)[0]
	startOutbox(t, outbox)

	waitFor(t, "dead", func() bool { return statusOf(store, row.EventID) == StatusDead })
	if got, _ := store.Get(context.Background(), row.EventID); got.Attempts != 1 {
		t.Fatalf("malformed URL retried: %+v", got)
	}
}

func TestPolicyDelay(t *testing.T) {
	cases := map[int]time.Duration{
		1: 10 * time.Second, 2: 30 * time.Second, 3: time.Minute, 4: 2 * time.Minute,
		5: 5 * time.Minute, 6: 10 * time.Minute, 7: 30 * time.Minute, 8: time.Hour, 50: time.Hour,
	}
	for attempt, want := range cases {
		if got := DefaultPolicy.delay(attempt); got != want {
			t.Errorf("delay(%d) = %s, want %s", attempt, got, want)
		}
	}
	if DefaultPolicy.MaxAge != 72*time.Hour || DefaultPolicy.Timeout != 10*time.Second {
		t.Fatalf("DefaultPolicy %+v", DefaultPolicy)
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		code int
		err  error
		want outcome
	}{
		{200, nil, outcomeDelivered},
		{204, nil, outcomeDelivered},
		{0, errors.New("connection refused"), outcomeRetry},
		{0, permanentError{errors.New("bad url")}, outcomeDead},
		{500, nil, outcomeRetry},
		{503, nil, outcomeRetry},
		{408, nil, outcomeRetry},
		{429, nil, outcomeRetry},
		{400, nil, outcomeDead},
		{401, nil, outcomeDead},
		{404, nil, outcomeDead},
		{422, nil, outcomeDead},
		{302, nil, outcomeRetry},
	}
	for _, c := range cases {
		if got := classify(c.code, c.err); got != c.want {
			t.Errorf("classify(%d, %v) = %d, want %d", c.code, c.err, got, c.want)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	cases := map[string]time.Duration{
		"":   0,
		"5":  5 * time.Second,
		"-1": 0,
		now.Add(90 * time.Second).Format(http.TimeFormat): 90 * time.Second,
		now.Add(-time.Minute).Format(http.TimeFormat):     0,
		"soon": 0,
	}
	for value, want := range cases {
		if got := parseRetryAfter(value, now); got != want {
			t.Errorf("parseRetryAfter(%q) = %s, want %s", value, got, want)
		}
	}
}

func TestUnsendableURLGoesDeadAtOnce(t *testing.T) {
	for _, url := range []string{"crm.example.com/hook", "ftp://x/y", "http:///nohost"} {
		store := openStore(t)
		outbox := New(store, staticSecret("s"), fastPolicy)
		row := enqueueN(t, outbox, url, 1)[0]
		startOutbox(t, outbox)

		waitFor(t, "dead "+url, func() bool { return statusOf(store, row.EventID) == StatusDead })
		if got, _ := store.Get(context.Background(), row.EventID); got.Attempts != 1 {
			t.Fatalf("%s retried: %+v", url, got)
		}
	}
}
