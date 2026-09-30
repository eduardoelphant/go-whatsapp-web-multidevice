# G3 Durable Webhooks Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** With `WHATSAPP_WEBHOOK_DELIVERY=durable`, every webhook event except `chat_presence` goes through a SQLite outbox and is delivered in order per URL, retried for up to 72 hours, with dead-letter, redelivery and replay; a failed outbox write for a message withholds the WhatsApp ack so nothing is lost.

**Architecture:** A pure package `src/pkg/webhookoutbox` owns the queue (store) and the per-URL workers (retry policy, signed HTTP send). A fork file `src/infrastructure/whatsapp/webhook_durable.go` swaps `submitWebhookFn` for an enqueue, carries a failure flag in the handler context, registers the success-status event handler and runs message/receipt/session forwarding synchronously in durable mode. A fork file `src/ui/rest/webhook_outbox.go` exposes the operations API. Upstream files only get short `// Fork (elphant):` hooks.

**Tech Stack:** Go 1.26 (`GOTOOLCHAIN=auto`), whatsmeow (`AddEventHandlerWithSuccessStatus`, `EnableDecryptedEventBuffer`), `database/sql` over GOWA's `pkg/sqlite` driver, Fiber v3, logrus.

**Spec:** `docs/specs/2026-09-25-gateway-g3-durable-webhooks-design.md`

### Notes against the spec (decided while planning)

1. **Decrypted-event buffer.** Returning `false` from the handler only helps if the redelivered message can be decrypted again. Without `client.EnableDecryptedEventBuffer`, the Signal ratchet has already advanced and the redelivery fails with an old-counter error, so the message would be lost anyway. Durable mode sets `EnableDecryptedEventBuffer = true` on every client (whatsmeow then keeps the plaintext in `whatsmeow_event_buffer` until the handler succeeds). Task 4.
2. **`queued_at` column.** The 72-hour window is counted from `queued_at` (set on insert, reset by redeliver/replay), not `created_at`; otherwise a replayed week-old row would go dead on its first failure. `created_at` stays the event time used by `X-Webhook-Timestamp` and the replay filter. Task 1.
3. **Startup hook in `cmd/root.go` `initApp`**, not `cmd/rest.go`: the outbox must exist before `InitWaCLI` creates the first client (Note 1), and `mcp` mode emits webhooks too. Task 3.
4. **`chat_presence` bypass lives inside the durable submit** (it checks `payload["event"]`), so `event_chat_presence.go` needs no hook. Task 3.
5. **Env vars are read with `os.Getenv`**, like `WHATSAPP_STABLE_AUDIT_DIR`; no upstream config/flag changes. Task 3.

## Global Constraints

- Branch `elphant` of the fork; commits in English, `type(scope): subject`, no attribution line.
- Pushes, tags, image publication, stack updates and anything on noria/GitHub need the owner's OK at that moment (Task 8 is **[GATED]**).
- Fork logic in new files; upstream files get short hooks marked `// Fork (elphant):`.
- `src/go.mod` and `src/go.sum` must not change (no new dependency; ULID is generated in-package).
- Env: `WHATSAPP_WEBHOOK_DELIVERY` = `direct` (default, today's behavior) or `durable`; `WHATSAPP_WEBHOOK_OUTBOX_DB` default `file:storages/webhook-outbox.db`.
- In `direct` mode nothing observable changes: no `event_id`, no new headers, handler always reports success, ops routes answer `404`.
- Body key `event_id` (ULID, 26 chars, Crockford base32); headers exactly `X-Webhook-Id`, `X-Webhook-Timestamp` (RFC3339 UTC, queued time), `X-Webhook-Attempt`, `X-Webhook-Replay: true`; `X-Hub-Signature-256` = `sha256=` + hex HMAC-SHA256 of the body.
- Retry: network error, timeout, `5xx`, `408`, `429` retry after 10 s, 30 s, 1 min, 2 min, 5 min, 10 min, 30 min, then every hour; `Retry-After` honored on `429`; 72 h → `dead`; any other `4xx` → `dead` at once. Per-attempt timeout 10 s.
- Statuses exactly `pending`, `delivered`, `dead`. Retention: delivered 7 days, dead 30 days, hourly cleanup.
- `config_ref` exactly `global`, `device:<session_id>` or `jid:<device jid>`; secrets are never stored in the outbox.
- Ops routes exactly: `GET /webhooks/stats`, `GET /webhooks/deliveries?status=&limit=&before_id=`, `GET /webhooks/deliveries/:event_id`, `POST /webhooks/deliveries/:event_id/redeliver`, `POST /webhooks/replay?since=<RFC3339>[&url=]`.
- Go commands run from `src/` with `GOTOOLCHAIN=auto`.
- The e2e check uses an unpaired throwaway device in a scratch directory; never the owner's session.

## Review Focus

1. **The outbox write fails while a message arrives** (disk full, file locked): the handler must report failure so whatsmeow skips the ack, and the redelivered message must decrypt from the buffer. Tests in Task 4 (`TestDurableHandlerReportsFailureWhenTheQueueIsDown`, `TestRegisterEventHandlerEnablesTheDecryptedEventBufferOnlyInDurableMode`); the real redelivery is observed in Task 8 only indirectly (no `Handler for … failed`, buffer plaintext cleared).
2. **Device and global legs enqueue the same payload concurrently** (`WHATSAPP_WEBHOOK_DEVICE_MERGE_GLOBAL`): no data race, one row and one `event_id` per URL. Test in Task 3 (`TestDeviceAndGlobalLegsQueueConcurrently`, run with `-race`).
3. **The process stops in the middle of a send**: the row stays `pending` with no attempt recorded and is sent again after restart with the same `event_id`. Test in Task 2 (`TestShutdownMidSendKeepsTheRowPending`).
4. **A malformed destination URL**: the row goes `dead` at once instead of retrying for 72 hours, and rows behind it still flow. Test in Task 2 (`TestMalformedURLGoesDeadAtOnce`).
5. **A receiver with a self-signed certificate and the per-device TLS-skip flag**: the skip-verify client is used only when the resolved config asks for it. Test in Task 2 (`TestInsecureFlagSelectsTheTLSClient`).

---

### Task 1: Outbox store and ULID

**Files:**
- Create: `src/pkg/webhookoutbox/ulid.go`
- Create: `src/pkg/webhookoutbox/store.go`
- Test: `src/pkg/webhookoutbox/ulid_test.go`
- Test: `src/pkg/webhookoutbox/store_test.go`

**Interfaces:**
- Produces:
  - constants `StatusPending = "pending"`, `StatusDelivered = "delivered"`, `StatusDead = "dead"`; `DeliveredRetention = 7*24h`, `DeadRetention = 30*24h`; `DefaultListLimit = 50`, `MaxListLimit = 500`
  - `type Row struct { ID int64; EventID, TargetURL, ConfigRef, EventName string; Body json.RawMessage; Status string; Attempts int; NextAttemptAt time.Time; LastError string; LastStatusCode int; Replay bool; CreatedAt, QueuedAt time.Time; DeliveredAt *time.Time }` (JSON: `id, event_id, url, config_ref, event, body (omitempty), status, attempts, next_attempt_at, last_error, last_status_code, replay, created_at, queued_at, delivered_at`)
  - `type URLStats struct { URL string; Pending, Delivered, Dead int64 }`, `type Stats struct { Pending, Delivered, Dead int64; OldestPendingAt *time.Time; OldestPendingAgeSeconds int64; URLs []URLStats }`
  - `func OpenStore(uri string) (*Store, error)`; `(*Store).Close() error`
  - `(*Store).Insert(ctx, targetURL, configRef, eventName string, body map[string]any) (Row, error)`
  - `(*Store).Head(ctx, targetURL string) (*Row, error)` (nil when empty)
  - `(*Store).MarkDelivered(ctx, id int64, attempts, statusCode int) error`
  - `(*Store).MarkRetry(ctx, id int64, attempts int, next time.Time, lastError string, statusCode int) error`
  - `(*Store).MarkDead(ctx, id int64, attempts int, lastError string, statusCode int) error`
  - `(*Store).PendingURLs(ctx) ([]string, error)`; `(*Store).CountPending(ctx, targetURL string) (int64, error)`
  - `(*Store).Redeliver(ctx, eventID string) (*Row, error)` (nil when unknown)
  - `(*Store).Replay(ctx, since time.Time, targetURL string) (int64, []string, error)`
  - `(*Store).Stats(ctx) (Stats, error)`; `(*Store).List(ctx, status string, limit int, beforeID int64) ([]Row, error)`; `(*Store).Get(ctx, eventID string) (*Row, error)`; `(*Store).Cleanup(ctx) (int64, error)`
  - unexported: `Store.now func() time.Time` (tests replace it), `(*ulidSource).next(now time.Time) (string, error)`, `encodeULID([16]byte) string`, `incrementBytes([]byte) bool`

- [ ] **Step 1: Write the failing ULID test**

Create `src/pkg/webhookoutbox/ulid_test.go`:

```go
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
	second, _ := src.next(at)                    // same millisecond
	third, _ := src.next(at.Add(-time.Second))   // clock went back
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
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd src && GOTOOLCHAIN=auto go test ./pkg/webhookoutbox/ -run 'ULID|IncrementBytes' -count=1`
Expected: FAIL to build, `undefined: encodeULID` (and `ulidSource`, `incrementBytes`).

- [ ] **Step 3: Implement the ULID source**

Create `src/pkg/webhookoutbox/ulid.go`:

```go
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
```

- [ ] **Step 4: Run it to verify it passes**

Run: `cd src && GOTOOLCHAIN=auto go test ./pkg/webhookoutbox/ -run 'ULID|IncrementBytes' -count=1 -v`
Expected: PASS for `TestEncodeULIDKnownValues`, `TestULIDSortsInCreationOrder`, `TestIncrementBytes`.

- [ ] **Step 5: Write the failing store tests**

Create `src/pkg/webhookoutbox/store_test.go`:

```go
package webhookoutbox

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type testClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *testClock) Add(d time.Duration) {
	c.mu.Lock()
	c.at = c.at.Add(d)
	c.mu.Unlock()
}

// openStore opens a store on a temp file with the real clock.
func openStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore("file:" + filepath.Join(t.TempDir(), "outbox.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// newTestStore opens a store whose clock only moves when the test moves it.
func newTestStore(t *testing.T) (*Store, *testClock) {
	t.Helper()
	store := openStore(t)
	clock := &testClock{at: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)}
	store.now = clock.Now
	return store, clock
}

func mustInsert(t *testing.T, s *Store, url, event string) Row {
	t.Helper()
	row, err := s.Insert(context.Background(), url, "global", event, map[string]any{"event": event, "payload": map[string]any{"id": "m1"}})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	return row
}

func mustGet(t *testing.T, s *Store, eventID string) *Row {
	t.Helper()
	row, err := s.Get(context.Background(), eventID)
	if err != nil || row == nil {
		t.Fatalf("Get(%s) = %v, %v", eventID, row, err)
	}
	return row
}

func TestInsertAddsEventIDToACopyOfTheBody(t *testing.T) {
	s, _ := newTestStore(t)
	body := map[string]any{"event": "message", "payload": map[string]any{"id": "m1"}}

	row, err := s.Insert(context.Background(), "http://a.test/hook", "global", "message", body)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := body["event_id"]; ok {
		t.Fatal("caller body was changed")
	}
	if len(row.EventID) != 26 {
		t.Fatalf("event id %q", row.EventID)
	}

	got := mustGet(t, s, row.EventID)
	var decoded map[string]any
	if err := json.Unmarshal(got.Body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["event_id"] != row.EventID || decoded["event"] != "message" {
		t.Fatalf("body %s", got.Body)
	}
	if got.Status != StatusPending || got.ConfigRef != "global" || got.EventName != "message" || got.Attempts != 0 || got.Replay {
		t.Fatalf("row %+v", got)
	}
	if !got.CreatedAt.Equal(got.QueuedAt) || !got.NextAttemptAt.Equal(got.CreatedAt) {
		t.Fatalf("times %+v", got)
	}
}

func TestHeadIsTheOldestPendingRowOfEachURL(t *testing.T) {
	ctx := context.Background()
	s, clock := newTestStore(t)
	a1 := mustInsert(t, s, "http://a.test", "message")
	b1 := mustInsert(t, s, "http://b.test", "message")
	a2 := mustInsert(t, s, "http://a.test", "message.ack")

	head, err := s.Head(ctx, "http://a.test")
	if err != nil || head == nil || head.ID != a1.ID {
		t.Fatalf("head a = %+v, %v; want %d", head, err, a1.ID)
	}

	// A backed-off head stays first: order is strict.
	next := clock.Now().Add(time.Hour)
	if err := s.MarkRetry(ctx, a1.ID, 1, next, "HTTP 500", 500); err != nil {
		t.Fatal(err)
	}
	head, _ = s.Head(ctx, "http://a.test")
	if head.ID != a1.ID || head.Attempts != 1 || !head.NextAttemptAt.Equal(next) || head.LastError != "HTTP 500" || head.LastStatusCode != 500 {
		t.Fatalf("backed-off head %+v", head)
	}

	if err := s.MarkDelivered(ctx, a1.ID, 2, 200); err != nil {
		t.Fatal(err)
	}
	if head, _ = s.Head(ctx, "http://a.test"); head.ID != a2.ID {
		t.Fatalf("head after delivery %+v, want %d", head, a2.ID)
	}
	if head, _ = s.Head(ctx, "http://b.test"); head.ID != b1.ID {
		t.Fatalf("head b %+v, want %d", head, b1.ID)
	}
	if head, err = s.Head(ctx, "http://c.test"); err != nil || head != nil {
		t.Fatalf("head c = %+v, %v", head, err)
	}

	delivered := mustGet(t, s, a1.EventID)
	if delivered.Status != StatusDelivered || delivered.Attempts != 2 || delivered.DeliveredAt == nil || delivered.LastError != "" {
		t.Fatalf("delivered row %+v", delivered)
	}
}

func TestMarkDeadAndRedeliver(t *testing.T) {
	ctx := context.Background()
	s, clock := newTestStore(t)
	row := mustInsert(t, s, "http://a.test", "message")

	if err := s.MarkDead(ctx, row.ID, 1, "HTTP 400", 400); err != nil {
		t.Fatal(err)
	}
	dead := mustGet(t, s, row.EventID)
	if dead.Status != StatusDead || dead.LastError != "HTTP 400" || dead.LastStatusCode != 400 || dead.Attempts != 1 {
		t.Fatalf("dead row %+v", dead)
	}

	clock.Add(time.Hour)
	again, err := s.Redeliver(ctx, row.EventID)
	if err != nil || again == nil {
		t.Fatalf("Redeliver = %v, %v", again, err)
	}
	if again.Status != StatusPending || again.Attempts != 0 || !again.Replay || again.EventID != row.EventID ||
		again.LastError != "" || again.LastStatusCode != 0 ||
		!again.QueuedAt.Equal(clock.Now()) || !again.NextAttemptAt.Equal(clock.Now()) || !again.CreatedAt.Equal(row.CreatedAt) {
		t.Fatalf("redelivered row %+v", again)
	}

	missing, err := s.Redeliver(ctx, "01NOTANEVENTID000000000000")
	if err != nil || missing != nil {
		t.Fatalf("unknown event = %v, %v", missing, err)
	}
}

func TestReplayRequeuesFinishedRowsSince(t *testing.T) {
	ctx := context.Background()
	s, clock := newTestStore(t)

	old := mustInsert(t, s, "http://a.test", "message")
	clock.Add(time.Hour)
	since := clock.Now()
	dead := mustInsert(t, s, "http://a.test", "message")
	pending := mustInsert(t, s, "http://a.test", "message")
	clock.Add(time.Hour)
	other := mustInsert(t, s, "http://b.test", "message")

	for _, err := range []error{
		s.MarkDelivered(ctx, old.ID, 1, 200),
		s.MarkDead(ctx, dead.ID, 1, "HTTP 400", 400),
		s.MarkDelivered(ctx, other.ID, 1, 200),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}

	n, urls, err := s.Replay(ctx, since, "http://b.test")
	if err != nil || n != 1 || len(urls) != 1 || urls[0] != "http://b.test" {
		t.Fatalf("replay b = %d %v %v", n, urls, err)
	}
	n, urls, err = s.Replay(ctx, since, "")
	if err != nil || n != 1 || len(urls) != 1 || urls[0] != "http://a.test" {
		t.Fatalf("replay all = %d %v %v", n, urls, err)
	}

	if got := mustGet(t, s, old.EventID); got.Status != StatusDelivered {
		t.Fatalf("row before since was replayed: %+v", got)
	}
	if got := mustGet(t, s, dead.EventID); got.Status != StatusPending || !got.Replay {
		t.Fatalf("dead row not replayed: %+v", got)
	}
	if got := mustGet(t, s, pending.EventID); got.Replay {
		t.Fatalf("pending row was touched: %+v", got)
	}
}

func TestStatsCountsPerStatusAndURL(t *testing.T) {
	ctx := context.Background()
	s, clock := newTestStore(t)

	empty, err := s.Stats(ctx)
	if err != nil || empty.URLs == nil || len(empty.URLs) != 0 || empty.OldestPendingAt != nil {
		t.Fatalf("empty stats %+v, %v", empty, err)
	}

	start := clock.Now()
	mustInsert(t, s, "http://a.test", "message")
	clock.Add(time.Minute)
	mustInsert(t, s, "http://a.test", "message")
	dead := mustInsert(t, s, "http://a.test", "message")
	delivered := mustInsert(t, s, "http://b.test", "message")
	if err := s.MarkDead(ctx, dead.ID, 1, "HTTP 400", 400); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkDelivered(ctx, delivered.ID, 1, 200); err != nil {
		t.Fatal(err)
	}
	clock.Add(9 * time.Minute)

	stats, err := s.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Pending != 2 || stats.Dead != 1 || stats.Delivered != 1 {
		t.Fatalf("totals %+v", stats)
	}
	if stats.OldestPendingAt == nil || !stats.OldestPendingAt.Equal(start) || stats.OldestPendingAgeSeconds != 600 {
		t.Fatalf("oldest %+v", stats)
	}
	want := []URLStats{{URL: "http://a.test", Pending: 2, Dead: 1}, {URL: "http://b.test", Delivered: 1}}
	if len(stats.URLs) != 2 || stats.URLs[0] != want[0] || stats.URLs[1] != want[1] {
		t.Fatalf("per URL %+v", stats.URLs)
	}
}

func TestListNewestFirstWithoutBodies(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestStore(t)
	r1 := mustInsert(t, s, "http://a.test", "message")
	r2 := mustInsert(t, s, "http://a.test", "message")
	r3 := mustInsert(t, s, "http://a.test", "message")
	if err := s.MarkDead(ctx, r2.ID, 1, "HTTP 400", 400); err != nil {
		t.Fatal(err)
	}

	page, err := s.List(ctx, "", 2, 0)
	if err != nil || len(page) != 2 || page[0].ID != r3.ID || page[1].ID != r2.ID {
		t.Fatalf("first page %+v, %v", page, err)
	}
	for _, row := range page {
		if row.Body != nil {
			t.Fatalf("list returned a body: %+v", row)
		}
	}
	page, _ = s.List(ctx, "", 2, r2.ID)
	if len(page) != 1 || page[0].ID != r1.ID {
		t.Fatalf("second page %+v", page)
	}
	page, _ = s.List(ctx, StatusDead, 50, 0)
	if len(page) != 1 || page[0].ID != r2.ID {
		t.Fatalf("dead filter %+v", page)
	}
	page, _ = s.List(ctx, StatusDelivered, 50, 0)
	if page == nil || len(page) != 0 {
		t.Fatalf("empty filter must be an empty slice, got %#v", page)
	}
}

func TestCleanupKeepsRetentionWindows(t *testing.T) {
	ctx := context.Background()
	s, clock := newTestStore(t)
	day := 24 * time.Hour

	deadOld := mustInsert(t, s, "http://a.test", "message")
	pendingOld := mustInsert(t, s, "http://a.test", "message")
	if err := s.MarkDead(ctx, deadOld.ID, 1, "HTTP 400", 400); err != nil {
		t.Fatal(err)
	}

	clock.Add(25 * day)
	deliveredOld := mustInsert(t, s, "http://a.test", "message")
	deadNew := mustInsert(t, s, "http://a.test", "message")
	if err := s.MarkDelivered(ctx, deliveredOld.ID, 1, 200); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkDead(ctx, deadNew.ID, 1, "HTTP 400", 400); err != nil {
		t.Fatal(err)
	}

	clock.Add(6 * day)
	deliveredNew := mustInsert(t, s, "http://a.test", "message")
	if err := s.MarkDelivered(ctx, deliveredNew.ID, 1, 200); err != nil {
		t.Fatal(err)
	}

	clock.Add(2 * day) // now = start + 33 days
	n, err := s.Cleanup(ctx)
	if err != nil || n != 2 {
		t.Fatalf("Cleanup = %d, %v; want 2", n, err)
	}
	for _, gone := range []Row{deadOld, deliveredOld} {
		if row, _ := s.Get(ctx, gone.EventID); row != nil {
			t.Fatalf("row kept past retention: %+v", row)
		}
	}
	for _, kept := range []Row{pendingOld, deadNew, deliveredNew} {
		mustGet(t, s, kept.EventID)
	}
}

func TestPendingURLsAndCount(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestStore(t)
	mustInsert(t, s, "http://b.test", "message")
	mustInsert(t, s, "http://a.test", "message")
	mustInsert(t, s, "http://a.test", "message")
	done := mustInsert(t, s, "http://c.test", "message")
	if err := s.MarkDelivered(ctx, done.ID, 1, 200); err != nil {
		t.Fatal(err)
	}

	urls, err := s.PendingURLs(ctx)
	if err != nil || len(urls) != 2 || urls[0] != "http://a.test" || urls[1] != "http://b.test" {
		t.Fatalf("PendingURLs = %v, %v", urls, err)
	}
	if n, err := s.CountPending(ctx, "http://a.test"); err != nil || n != 2 {
		t.Fatalf("CountPending = %d, %v", n, err)
	}
}

func TestStoreSurvivesReopen(t *testing.T) {
	uri := "file:" + filepath.Join(t.TempDir(), "outbox.db")
	first, err := OpenStore(uri)
	if err != nil {
		t.Fatal(err)
	}
	row := mustInsert(t, first, "http://a.test", "message")
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := OpenStore(uri)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	head, err := second.Head(context.Background(), "http://a.test")
	if err != nil || head == nil || head.EventID != row.EventID {
		t.Fatalf("head after reopen = %+v, %v", head, err)
	}
}
```

- [ ] **Step 6: Run them to verify they fail**

Run: `cd src && GOTOOLCHAIN=auto go test ./pkg/webhookoutbox/ -count=1`
Expected: FAIL to build, `undefined: Store` / `OpenStore`.

- [ ] **Step 7: Implement the store**

Create `src/pkg/webhookoutbox/store.go`:

```go
// Package webhookoutbox is the durable webhook queue of the elphant fork: a
// SQLite table of pending deliveries and one worker per destination URL that
// sends them in order, retries temporary failures and dead-letters the rest.
package webhookoutbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/sqlite"
)

// Row statuses.
const (
	StatusPending   = "pending"
	StatusDelivered = "delivered"
	StatusDead      = "dead"
)

// Retention of finished rows, applied by Cleanup.
const (
	DeliveredRetention = 7 * 24 * time.Hour
	DeadRetention      = 30 * 24 * time.Hour
)

// Page size bounds of List.
const (
	DefaultListLimit = 50
	MaxListLimit     = 500
)

const schemaSQL = `
CREATE TABLE IF NOT EXISTS webhook_outbox (
	id               INTEGER PRIMARY KEY AUTOINCREMENT,
	event_id         TEXT    NOT NULL UNIQUE,
	target_url       TEXT    NOT NULL,
	config_ref       TEXT    NOT NULL,
	event_name       TEXT    NOT NULL,
	body_json        TEXT    NOT NULL,
	status           TEXT    NOT NULL,
	attempts         INTEGER NOT NULL DEFAULT 0,
	next_attempt_at  INTEGER NOT NULL,
	last_error       TEXT    NOT NULL DEFAULT '',
	last_status_code INTEGER NOT NULL DEFAULT 0,
	replay           INTEGER NOT NULL DEFAULT 0,
	created_at       INTEGER NOT NULL,
	queued_at        INTEGER NOT NULL,
	delivered_at     INTEGER
);
CREATE INDEX IF NOT EXISTS idx_webhook_outbox_url_status_id ON webhook_outbox (target_url, status, id);
CREATE INDEX IF NOT EXISTS idx_webhook_outbox_status_created ON webhook_outbox (status, created_at);
`

const rowColumns = `id, event_id, target_url, config_ref, event_name, status, attempts, next_attempt_at,
	last_error, last_status_code, replay, created_at, queued_at, delivered_at`

// requeueSet puts a row back in the queue as a replay with a fresh 72-hour window.
const requeueSet = `status = 'pending', attempts = 0, next_attempt_at = ?, queued_at = ?, replay = 1,
	last_error = '', last_status_code = 0, delivered_at = NULL`

// Row is one queued delivery: one event for one destination URL.
type Row struct {
	ID             int64           `json:"id"`
	EventID        string          `json:"event_id"`
	TargetURL      string          `json:"url"`
	ConfigRef      string          `json:"config_ref"`
	EventName      string          `json:"event"`
	Body           json.RawMessage `json:"body,omitempty"`
	Status         string          `json:"status"`
	Attempts       int             `json:"attempts"`
	NextAttemptAt  time.Time       `json:"next_attempt_at"`
	LastError      string          `json:"last_error"`
	LastStatusCode int             `json:"last_status_code"`
	Replay         bool            `json:"replay"`
	CreatedAt      time.Time       `json:"created_at"`
	QueuedAt       time.Time       `json:"queued_at"`
	DeliveredAt    *time.Time      `json:"delivered_at"`
}

// URLStats counts the rows of one destination URL.
type URLStats struct {
	URL       string `json:"url"`
	Pending   int64  `json:"pending"`
	Delivered int64  `json:"delivered"`
	Dead      int64  `json:"dead"`
}

// Stats summarizes the queue.
type Stats struct {
	Pending                 int64      `json:"pending"`
	Delivered               int64      `json:"delivered"`
	Dead                    int64      `json:"dead"`
	OldestPendingAt         *time.Time `json:"oldest_pending_at"`
	OldestPendingAgeSeconds int64      `json:"oldest_pending_age_seconds"`
	URLs                    []URLStats `json:"urls"`
}

// Store is the outbox table in its own SQLite file.
type Store struct {
	db  *sql.DB
	ids ulidSource
	now func() time.Time
}

// OpenStore opens (and creates) the outbox database in WAL mode.
func OpenStore(uri string) (*Store, error) {
	db, err := sql.Open(sqlite.DriverName, sqlite.FormatChatStorageURI(uri, true, false))
	if err != nil {
		return nil, err
	}
	// One connection: every access is serialized in-process, so the workers and
	// the event handlers never race each other into SQLITE_BUSY.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("webhook outbox schema: %w", err)
	}
	return &Store{db: db, now: time.Now}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// clock is the current time as stored: UTC, millisecond precision.
func (s *Store) clock() time.Time { return s.now().UTC().Truncate(time.Millisecond) }

func ms(t time.Time) int64 { return t.UnixMilli() }

func fromMS(v int64) time.Time { return time.UnixMilli(v).UTC() }

// Insert queues body for targetURL. It adds event_id to a shallow copy of
// body, so the caller's map is never changed, and returns the new row.
func (s *Store) Insert(ctx context.Context, targetURL, configRef, eventName string, body map[string]any) (Row, error) {
	now := s.clock()
	eventID, err := s.ids.next(now)
	if err != nil {
		return Row{}, err
	}
	copied := make(map[string]any, len(body)+1)
	for k, v := range body {
		copied[k] = v
	}
	copied["event_id"] = eventID
	raw, err := json.Marshal(copied)
	if err != nil {
		return Row{}, fmt.Errorf("marshal webhook body: %w", err)
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO webhook_outbox
		(event_id, target_url, config_ref, event_name, body_json, status, next_attempt_at, created_at, queued_at)
		VALUES (?, ?, ?, ?, ?, 'pending', ?, ?, ?)`,
		eventID, targetURL, configRef, eventName, string(raw), ms(now), ms(now), ms(now))
	if err != nil {
		return Row{}, fmt.Errorf("insert webhook outbox row: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Row{}, err
	}
	return Row{
		ID: id, EventID: eventID, TargetURL: targetURL, ConfigRef: configRef, EventName: eventName,
		Body: raw, Status: StatusPending, NextAttemptAt: now, CreatedAt: now, QueuedAt: now,
	}, nil
}

type scanner interface{ Scan(dest ...any) error }

func scanRow(sc scanner, withBody bool) (Row, error) {
	var (
		r                     Row
		next, created, queued int64
		delivered             sql.NullInt64
		replay                int
		body                  string
	)
	dest := []any{&r.ID, &r.EventID, &r.TargetURL, &r.ConfigRef, &r.EventName, &r.Status, &r.Attempts, &next,
		&r.LastError, &r.LastStatusCode, &replay, &created, &queued, &delivered}
	if withBody {
		dest = append(dest, &body)
	}
	if err := sc.Scan(dest...); err != nil {
		return Row{}, err
	}
	r.NextAttemptAt, r.CreatedAt, r.QueuedAt = fromMS(next), fromMS(created), fromMS(queued)
	if delivered.Valid {
		at := fromMS(delivered.Int64)
		r.DeliveredAt = &at
	}
	r.Replay = replay == 1
	if withBody {
		r.Body = json.RawMessage(body)
	}
	return r, nil
}

func (s *Store) queryOne(ctx context.Context, where string, args ...any) (*Row, error) {
	r, err := scanRow(s.db.QueryRowContext(ctx, `SELECT `+rowColumns+`, body_json FROM webhook_outbox `+where, args...), true)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// Head returns the oldest pending row of targetURL, or nil when there is none.
// A row waiting for its next attempt is still the head: order is strict.
func (s *Store) Head(ctx context.Context, targetURL string) (*Row, error) {
	return s.queryOne(ctx, `WHERE target_url = ? AND status = 'pending' ORDER BY id LIMIT 1`, targetURL)
}

// Get returns the row of eventID with its body, or nil when unknown.
func (s *Store) Get(ctx context.Context, eventID string) (*Row, error) {
	return s.queryOne(ctx, `WHERE event_id = ?`, eventID)
}

// MarkDelivered records a 2xx answer.
func (s *Store) MarkDelivered(ctx context.Context, id int64, attempts, statusCode int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE webhook_outbox SET status = 'delivered', attempts = ?,
		last_status_code = ?, last_error = '', delivered_at = ? WHERE id = ?`, attempts, statusCode, ms(s.clock()), id)
	return err
}

// MarkRetry records a temporary failure and when to try again.
func (s *Store) MarkRetry(ctx context.Context, id int64, attempts int, next time.Time, lastError string, statusCode int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE webhook_outbox SET attempts = ?, next_attempt_at = ?,
		last_error = ?, last_status_code = ? WHERE id = ?`, attempts, ms(next), lastError, statusCode, id)
	return err
}

// MarkDead gives up on a row.
func (s *Store) MarkDead(ctx context.Context, id int64, attempts int, lastError string, statusCode int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE webhook_outbox SET status = 'dead', attempts = ?,
		last_error = ?, last_status_code = ? WHERE id = ?`, attempts, lastError, statusCode, id)
	return err
}

// PendingURLs lists the URLs with pending rows, sorted.
func (s *Store) PendingURLs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT target_url FROM webhook_outbox WHERE status = 'pending' ORDER BY target_url`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanStrings(rows)
}

func scanStrings(rows *sql.Rows) ([]string, error) {
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// CountPending counts the pending rows of targetURL.
func (s *Store) CountPending(ctx context.Context, targetURL string) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM webhook_outbox WHERE target_url = ? AND status = 'pending'`, targetURL).Scan(&n)
	return n, err
}

// Redeliver puts one row back in the queue with attempts reset, the same
// event_id and replay set. It returns nil when eventID is unknown.
func (s *Store) Redeliver(ctx context.Context, eventID string) (*Row, error) {
	now := ms(s.clock())
	res, err := s.db.ExecContext(ctx, `UPDATE webhook_outbox SET `+requeueSet+` WHERE event_id = ?`, now, now, eventID)
	if err != nil {
		return nil, err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return nil, err
	}
	return s.Get(ctx, eventID)
}

// Replay puts every delivered or dead row created at or after since (only
// those of targetURL when it is not empty) back in the queue. It returns how
// many rows were requeued and their URLs, sorted.
func (s *Store) Replay(ctx context.Context, since time.Time, targetURL string) (int64, []string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback()

	const filter = ` WHERE status IN ('delivered', 'dead') AND created_at >= ? AND (? = '' OR target_url = ?)`
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT target_url FROM webhook_outbox`+filter+` ORDER BY target_url`,
		ms(since), targetURL, targetURL)
	if err != nil {
		return 0, nil, err
	}
	urls, err := scanStrings(rows)
	rows.Close()
	if err != nil {
		return 0, nil, err
	}

	now := ms(s.clock())
	res, err := tx.ExecContext(ctx, `UPDATE webhook_outbox SET `+requeueSet+filter, now, now, ms(since), targetURL, targetURL)
	if err != nil {
		return 0, nil, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, nil, err
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, err
	}
	return n, urls, nil
}

// Stats counts rows per status and URL and reports the oldest pending row.
func (s *Store) Stats(ctx context.Context) (Stats, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT target_url, status, COUNT(*), MIN(created_at)
		FROM webhook_outbox GROUP BY target_url, status ORDER BY target_url`)
	if err != nil {
		return Stats{}, err
	}
	defer rows.Close()

	stats := Stats{URLs: []URLStats{}}
	index := map[string]int{}
	var oldest int64
	for rows.Next() {
		var (
			url, status       string
			count, minCreated int64
		)
		if err := rows.Scan(&url, &status, &count, &minCreated); err != nil {
			return Stats{}, err
		}
		i, ok := index[url]
		if !ok {
			i = len(stats.URLs)
			index[url] = i
			stats.URLs = append(stats.URLs, URLStats{URL: url})
		}
		switch status {
		case StatusPending:
			stats.Pending += count
			stats.URLs[i].Pending += count
			if oldest == 0 || minCreated < oldest {
				oldest = minCreated
			}
		case StatusDelivered:
			stats.Delivered += count
			stats.URLs[i].Delivered += count
		case StatusDead:
			stats.Dead += count
			stats.URLs[i].Dead += count
		}
	}
	if err := rows.Err(); err != nil {
		return Stats{}, err
	}
	if oldest != 0 {
		at := fromMS(oldest)
		stats.OldestPendingAt = &at
		stats.OldestPendingAgeSeconds = int64(s.now().Sub(at) / time.Second)
	}
	return stats, nil
}

// List returns rows newest first, without bodies. status "" means every
// status; beforeID 0 starts from the newest row. limit is clamped to
// 1..MaxListLimit (DefaultListLimit when below 1).
func (s *Store) List(ctx context.Context, status string, limit int, beforeID int64) ([]Row, error) {
	if limit < 1 {
		limit = DefaultListLimit
	}
	limit = min(limit, MaxListLimit)
	rows, err := s.db.QueryContext(ctx, `SELECT `+rowColumns+` FROM webhook_outbox
		WHERE (? = '' OR status = ?) AND (? = 0 OR id < ?) ORDER BY id DESC LIMIT ?`,
		status, status, beforeID, beforeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Row{}
	for rows.Next() {
		r, err := scanRow(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Cleanup deletes delivered rows older than DeliveredRetention (by delivery
// time) and dead rows older than DeadRetention (by event time).
func (s *Store) Cleanup(ctx context.Context) (int64, error) {
	now := s.clock()
	res, err := s.db.ExecContext(ctx, `DELETE FROM webhook_outbox
		WHERE (status = 'delivered' AND delivered_at < ?) OR (status = 'dead' AND created_at < ?)`,
		ms(now.Add(-DeliveredRetention)), ms(now.Add(-DeadRetention)))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
```

- [ ] **Step 8: Run the package tests**

Run: `cd src && gofmt -w pkg/webhookoutbox && GOTOOLCHAIN=auto go vet ./pkg/webhookoutbox/ && GOTOOLCHAIN=auto go test ./pkg/webhookoutbox/ -count=1 -v`
Expected: vet clean; all ULID and store tests PASS.

- [ ] **Step 9: Commit**

```bash
git add src/pkg/webhookoutbox/ulid.go src/pkg/webhookoutbox/ulid_test.go src/pkg/webhookoutbox/store.go src/pkg/webhookoutbox/store_test.go
git commit -m "feat(webhookoutbox): add the durable webhook queue store"
```

---

### Task 2: Per-URL delivery workers

**Files:**
- Create: `src/pkg/webhookoutbox/outbox.go`
- Test: `src/pkg/webhookoutbox/outbox_test.go`

**Interfaces:**
- Consumes (Task 1): `Store` and all its methods, `Row`, `Status*` constants, `Store.now`.
- Produces:
  - `type Resolver func(ctx context.Context, configRef string) (secret string, insecureSkipVerify bool, err error)`
  - `type Policy struct { Delays []time.Duration; MaxAge, Timeout, MaxRetryAfter time.Duration }`; `var DefaultPolicy Policy`
  - `func New(store *Store, resolve Resolver, policy Policy) *Outbox`
  - `(*Outbox).Start(ctx context.Context) error` (workers for pending URLs + hourly cleanup; stop with ctx)
  - `(*Outbox).Enqueue(ctx, targetURL, configRef, eventName string, body map[string]any) (Row, error)`
  - `(*Outbox).Redeliver(ctx, eventID string) (*Row, error)`; `(*Outbox).Replay(ctx, since time.Time, targetURL string) (int64, error)`
  - `(*Outbox).Store() *Store`
  - unexported: `classify(code int, err error) outcome`, `parseRetryAfter(value string, now time.Time) time.Duration`, `(Policy).delay(attempt int) time.Duration`, `permanentError`

- [ ] **Step 1: Write the failing tests**

Create `src/pkg/webhookoutbox/outbox_test.go`:

```go
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
```

- [ ] **Step 2: Run them to verify they fail**

Run: `cd src && GOTOOLCHAIN=auto go test ./pkg/webhookoutbox/ -count=1`
Expected: FAIL to build, `undefined: Policy` / `New` / `Resolver`.

- [ ] **Step 3: Implement the workers**

Create `src/pkg/webhookoutbox/outbox.go`:

```go
package webhookoutbox

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// Resolver returns the HMAC secret and the TLS-skip flag for a row's
// config_ref. It runs at every attempt, so a rotated secret applies to rows
// that are already queued.
type Resolver func(ctx context.Context, configRef string) (secret string, insecureSkipVerify bool, err error)

// Policy controls retries.
type Policy struct {
	// Delays[n-1] is the wait after the n-th failed attempt; the last one repeats.
	Delays []time.Duration
	// MaxAge is how long a failing row keeps retrying, counted from when it was queued.
	MaxAge time.Duration
	// Timeout bounds one HTTP attempt.
	Timeout time.Duration
	// MaxRetryAfter caps a receiver's Retry-After.
	MaxRetryAfter time.Duration
}

// DefaultPolicy retries after 10 s, 30 s, 1, 2, 5, 10 and 30 minutes, then
// every hour, for up to 72 hours.
var DefaultPolicy = Policy{
	Delays: []time.Duration{
		10 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute,
		5 * time.Minute, 10 * time.Minute, 30 * time.Minute, time.Hour,
	},
	MaxAge:        72 * time.Hour,
	Timeout:       10 * time.Second,
	MaxRetryAfter: time.Hour,
}

func (p Policy) withDefaults() Policy {
	if len(p.Delays) == 0 {
		p.Delays = DefaultPolicy.Delays
	}
	if p.MaxAge <= 0 {
		p.MaxAge = DefaultPolicy.MaxAge
	}
	if p.Timeout <= 0 {
		p.Timeout = DefaultPolicy.Timeout
	}
	if p.MaxRetryAfter <= 0 {
		p.MaxRetryAfter = DefaultPolicy.MaxRetryAfter
	}
	return p
}

func (p Policy) delay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > len(p.Delays) {
		return p.Delays[len(p.Delays)-1]
	}
	return p.Delays[attempt-1]
}

// storeRetryPause is the wait after a database error before a worker tries again.
const storeRetryPause = 5 * time.Second

// Outbox runs one delivery worker per destination URL.
type Outbox struct {
	store   *Store
	resolve Resolver
	policy  Policy
	clients [2]*http.Client // [0] verifies TLS, [1] skips verification

	mu      sync.Mutex
	ctx     context.Context // set by Start; nil means workers are not running yet
	workers map[string]chan struct{}
}

// New prepares an outbox. Nothing is sent until Start.
func New(store *Store, resolve Resolver, policy Policy) *Outbox {
	policy = policy.withDefaults()
	return &Outbox{
		store:   store,
		resolve: resolve,
		policy:  policy,
		clients: [2]*http.Client{newClient(policy.Timeout, false), newClient(policy.Timeout, true)},
		workers: map[string]chan struct{}{},
	}
}

// newClient mirrors the upstream webhook client: a fresh transport with the
// TLS verification choice and a per-request timeout.
func newClient(timeout time.Duration, insecureSkipVerify bool) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: insecureSkipVerify}},
	}
}

// Store returns the queue, for the operations API.
func (o *Outbox) Store() *Store { return o.store }

// Start runs a worker for every URL with pending rows and the hourly cleanup.
// Workers stop when ctx ends; rows left pending are sent after the next Start.
func (o *Outbox) Start(ctx context.Context) error {
	o.mu.Lock()
	o.ctx = ctx
	o.mu.Unlock()
	urls, err := o.store.PendingURLs(ctx)
	if err != nil {
		return err
	}
	for _, url := range urls {
		o.wake(url)
	}
	go o.cleanupLoop(ctx)
	return nil
}

// Enqueue writes the event to the queue and wakes the URL's worker.
func (o *Outbox) Enqueue(ctx context.Context, targetURL, configRef, eventName string, body map[string]any) (Row, error) {
	row, err := o.store.Insert(ctx, targetURL, configRef, eventName, body)
	if err != nil {
		return Row{}, err
	}
	o.wake(targetURL)
	return row, nil
}

// Redeliver requeues one row (see Store.Redeliver) and wakes its worker.
func (o *Outbox) Redeliver(ctx context.Context, eventID string) (*Row, error) {
	row, err := o.store.Redeliver(ctx, eventID)
	if err != nil || row == nil {
		return row, err
	}
	o.wake(row.TargetURL)
	return row, nil
}

// Replay requeues finished rows (see Store.Replay) and wakes their workers.
func (o *Outbox) Replay(ctx context.Context, since time.Time, targetURL string) (int64, error) {
	n, urls, err := o.store.Replay(ctx, since, targetURL)
	for _, url := range urls {
		o.wake(url)
	}
	return n, err
}

// wake starts the URL's worker if needed and signals it.
func (o *Outbox) wake(targetURL string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.ctx == nil {
		return
	}
	ch, ok := o.workers[targetURL]
	if !ok {
		ch = make(chan struct{}, 1)
		o.workers[targetURL] = ch
		go o.run(o.ctx, targetURL, ch)
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

// run sends the URL's rows oldest first until ctx ends.
func (o *Outbox) run(ctx context.Context, targetURL string, wake <-chan struct{}) {
	failing := false
	for {
		row, err := o.store.Head(ctx, targetURL)
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			logrus.Errorf("Webhook outbox: read queue of %s: %v", targetURL, err)
			if !o.pause(ctx) {
				return
			}
		case row == nil:
			select {
			case <-ctx.Done():
				return
			case <-wake:
			}
		case row.NextAttemptAt.After(o.store.now()):
			if !o.waitUntil(ctx, wake, row.NextAttemptAt) {
				return
			}
		default:
			failing = o.attempt(ctx, row, failing)
		}
	}
}

// waitUntil blocks until at, a wake signal or the end of ctx (then false).
func (o *Outbox) waitUntil(ctx context.Context, wake <-chan struct{}, at time.Time) bool {
	timer := time.NewTimer(max(at.Sub(o.store.now()), 0))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-wake:
	case <-timer.C:
	}
	return true
}

func (o *Outbox) pause(ctx context.Context) bool {
	timer := time.NewTimer(storeRetryPause)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// attempt sends one row and records the outcome. failing tracks whether the
// URL is currently failing, so only transitions are logged.
func (o *Outbox) attempt(ctx context.Context, row *Row, failing bool) bool {
	attempts := row.Attempts + 1
	code, retryAfter, sendErr := o.send(ctx, row, attempts)
	if ctx.Err() != nil {
		// Shutting down: the row stays pending and is sent again after a restart.
		return failing
	}
	reason := describe(code, sendErr)

	var err error
	switch classify(code, sendErr) {
	case outcomeDelivered:
		err = o.store.MarkDelivered(ctx, row.ID, attempts, code)
		if err == nil && failing {
			logrus.Infof("Webhook outbox: %s recovered (%d pending)", row.TargetURL, o.pending(ctx, row.TargetURL))
		}
		failing = false
	case outcomeDead:
		err = o.store.MarkDead(ctx, row.ID, attempts, reason, code)
		logrus.Warnf("Webhook outbox: %s rejected %s %s (%s); marked dead", row.TargetURL, row.EventName, row.EventID, reason)
	default:
		now := o.store.now()
		if now.Sub(row.QueuedAt) >= o.policy.MaxAge {
			err = o.store.MarkDead(ctx, row.ID, attempts, "gave up after "+o.policy.MaxAge.String()+": "+reason, code)
			logrus.Warnf("Webhook outbox: %s still failing after %s; %s %s marked dead", row.TargetURL, o.policy.MaxAge, row.EventName, row.EventID)
		} else {
			wait := o.policy.delay(attempts)
			if code == http.StatusTooManyRequests && retryAfter > 0 {
				wait = min(retryAfter, o.policy.MaxRetryAfter)
			}
			err = o.store.MarkRetry(ctx, row.ID, attempts, now.Add(wait), reason, code)
			if !failing {
				logrus.Warnf("Webhook outbox: %s failing (%s); %d pending, retrying in %s", row.TargetURL, reason, o.pending(ctx, row.TargetURL), wait)
			}
		}
		failing = true
	}
	if err != nil {
		logrus.Errorf("Webhook outbox: update row %d: %v", row.ID, err)
		o.pause(ctx)
	}
	return failing
}

func (o *Outbox) pending(ctx context.Context, targetURL string) int64 {
	n, _ := o.store.CountPending(ctx, targetURL)
	return n
}

// send POSTs the row once and returns the status code and the Retry-After wait.
func (o *Outbox) send(ctx context.Context, row *Row, attempt int) (int, time.Duration, error) {
	secret, insecure, err := o.resolve(ctx, row.ConfigRef)
	if err != nil {
		return 0, 0, fmt.Errorf("resolve webhook config %s: %w", row.ConfigRef, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, row.TargetURL, bytes.NewReader(row.Body))
	if err != nil {
		return 0, 0, permanentError{err}
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(row.Body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	req.Header.Set("X-Webhook-Id", row.EventID)
	req.Header.Set("X-Webhook-Timestamp", row.CreatedAt.UTC().Format(time.RFC3339))
	req.Header.Set("X-Webhook-Attempt", strconv.Itoa(attempt))
	if row.Replay {
		req.Header.Set("X-Webhook-Replay", "true")
	}

	client := o.clients[0]
	if insecure {
		client = o.clients[1]
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, parseRetryAfter(resp.Header.Get("Retry-After"), o.store.now()), nil
}

func (o *Outbox) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		if n, err := o.store.Cleanup(ctx); err != nil {
			if ctx.Err() == nil {
				logrus.Errorf("Webhook outbox: cleanup: %v", err)
			}
		} else if n > 0 {
			logrus.Infof("Webhook outbox: removed %d finished row(s) past retention", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// permanentError marks a failure that retrying cannot fix, such as a malformed URL.
type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

type outcome int

const (
	outcomeRetry outcome = iota
	outcomeDelivered
	outcomeDead
)

func classify(code int, err error) outcome {
	var permanent permanentError
	switch {
	case errors.As(err, &permanent):
		return outcomeDead
	case err != nil:
		return outcomeRetry
	case code >= 200 && code < 300:
		return outcomeDelivered
	case code == http.StatusRequestTimeout || code == http.StatusTooManyRequests:
		return outcomeRetry
	case code >= 400 && code < 500:
		return outcomeDead
	default:
		return outcomeRetry
	}
}

func describe(code int, err error) string {
	if err != nil {
		msg := err.Error()
		if len(msg) > 500 {
			msg = msg[:500]
		}
		return msg
	}
	return "HTTP " + strconv.Itoa(code)
}

// parseRetryAfter reads Retry-After as seconds or an HTTP date; 0 when absent,
// invalid or in the past.
func parseRetryAfter(value string, now time.Time) time.Duration {
	if value == "" {
		return 0
	}
	if secs, err := strconv.Atoi(value); err == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil && at.After(now) {
		return at.Sub(now)
	}
	return 0
}
```

- [ ] **Step 4: Run the package tests, with the race detector**

Run: `cd src && gofmt -w pkg/webhookoutbox && GOTOOLCHAIN=auto go vet ./pkg/webhookoutbox/ && GOTOOLCHAIN=auto go test ./pkg/webhookoutbox/ -count=1 && GOTOOLCHAIN=auto go test -race ./pkg/webhookoutbox/ -count=1`
Expected: vet clean; PASS twice (the second run with `-race` reports no race).

- [ ] **Step 5: Commit**

```bash
git add src/pkg/webhookoutbox/outbox.go src/pkg/webhookoutbox/outbox_test.go
git commit -m "feat(webhookoutbox): deliver queued webhooks in order with retries"
```

---

### Task 3: Durable submit, config refs and startup

**Files:**
- Create: `src/infrastructure/whatsapp/webhook_durable.go`
- Test: `src/infrastructure/whatsapp/webhook_durable_test.go`
- Modify: `src/cmd/root.go` (`initApp`, right after `chatStorageRepo.InitializeSchema()`)

**Interfaces:**
- Consumes (Tasks 1-2): `webhookoutbox.OpenStore`, `webhookoutbox.New`, `webhookoutbox.DefaultPolicy`, `(*Outbox).Start/Enqueue/Store`, `(*Store).List/Get/Close`, `webhookoutbox.StatusPending`, `webhookoutbox.Row`.
- Consumes (existing): `submitWebhookFn`, `submitWebhook`, `forwardPayloadToConfiguredWebhooks`, `getWebhookConfigForDevice`, `getWebhookConfigForSlot`, `webhookStorageForTest`, `sessionIDForJIDFn`, `config.WhatsappWebhookSecret`, `config.WhatsappWebhookInsecureSkipVerify`.
- Produces:
  - `func StartDurableWebhooks(ctx context.Context) error`
  - `func DurableWebhookOutbox() *webhookoutbox.Outbox` (nil in direct mode)
  - unexported: `durableOutbox`, `durableWebhooksEnabled() bool`, `submitWebhookDurable(ctx, payload, url, webhookConfig) error`, `webhookConfigRef(payload, webhookConfig) string`, `resolveWebhookSecret(ctx, ref) (string, bool, error)`, `withHandlerFailureFlag(ctx) (context.Context, *atomic.Bool)`, `markHandlerFailed(ctx)`, constants `webhookDeliveryEnv`, `webhookOutboxDBEnv`, `defaultOutboxDBURI`
  - test helpers (in `webhook_durable_test.go`, reused by Task 4): `useTestOutbox(t) *webhookoutbox.Outbox`, `useGlobalWebhooks(t, urls ...string)`, `pendingRows(t, outbox) []webhookoutbox.Row`

- [ ] **Step 1: Write the failing tests**

Create `src/infrastructure/whatsapp/webhook_durable_test.go`:

```go
package whatsapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/webhookoutbox"
)

// useTestOutbox switches to durable mode with an outbox that is never started,
// so queued rows stay pending for the test to inspect.
func useTestOutbox(t *testing.T) *webhookoutbox.Outbox {
	t.Helper()
	store, err := webhookoutbox.OpenStore("file:" + filepath.Join(t.TempDir(), "outbox.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	previousOutbox, previousSubmit := durableOutbox, submitWebhookFn
	outbox := webhookoutbox.New(store, resolveWebhookSecret, webhookoutbox.DefaultPolicy)
	durableOutbox, submitWebhookFn = outbox, submitWebhookDurable
	t.Cleanup(func() {
		durableOutbox, submitWebhookFn = previousOutbox, previousSubmit
		store.Close()
	})
	return outbox
}

// useGlobalWebhooks sets the global webhook URLs and stubs device lookups.
func useGlobalWebhooks(t *testing.T, urls ...string) {
	t.Helper()
	prevURLs, prevEvents := config.WhatsappWebhook, config.WhatsappWebhookEvents
	prevMerge, prevChatwoot := config.WhatsappWebhookDeviceMergeGlobal, config.ChatwootEnabled
	prevStorage, prevSession := webhookStorageForTest, sessionIDForJIDFn
	config.WhatsappWebhook, config.WhatsappWebhookEvents = urls, nil
	config.WhatsappWebhookDeviceMergeGlobal, config.ChatwootEnabled = false, false
	webhookStorageForTest = func(string) (*domainChatStorage.DeviceRecord, error) { return nil, nil }
	sessionIDForJIDFn = func(string) string { return "" }
	t.Cleanup(func() {
		config.WhatsappWebhook, config.WhatsappWebhookEvents = prevURLs, prevEvents
		config.WhatsappWebhookDeviceMergeGlobal, config.ChatwootEnabled = prevMerge, prevChatwoot
		webhookStorageForTest, sessionIDForJIDFn = prevStorage, prevSession
	})
}

// pendingRows lists the pending rows oldest first.
func pendingRows(t *testing.T, outbox *webhookoutbox.Outbox) []webhookoutbox.Row {
	t.Helper()
	rows, err := outbox.Store().List(context.Background(), webhookoutbox.StatusPending, 500, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	slices.Reverse(rows)
	return rows
}

func TestDurableSubmitQueuesOneRowPerURL(t *testing.T) {
	outbox := useTestOutbox(t)
	useGlobalWebhooks(t, "http://a.test/hook", "http://b.test/hook")
	payload := map[string]any{"event": "message", "device_id": "5511999999999@s.whatsapp.net", "payload": map[string]any{"id": "m1"}}

	if err := forwardPayloadToConfiguredWebhooks(context.Background(), payload, "message"); err != nil {
		t.Fatal(err)
	}
	rows := pendingRows(t, outbox)
	if len(rows) != 2 || rows[0].TargetURL != "http://a.test/hook" || rows[1].TargetURL != "http://b.test/hook" {
		t.Fatalf("rows %+v", rows)
	}
	if rows[0].EventID == rows[1].EventID {
		t.Fatal("two URLs share an event_id")
	}
	for _, row := range rows {
		if row.ConfigRef != "global" || row.EventName != "message" {
			t.Fatalf("row %+v", row)
		}
	}
	if _, ok := payload["event_id"]; ok {
		t.Fatal("caller payload gained event_id")
	}
}

func TestDurableSubmitRefersToTheDeviceWebhookConfig(t *testing.T) {
	outbox := useTestOutbox(t)
	useGlobalWebhooks(t)
	deviceURL := "http://device.test/hook"
	webhookStorageForTest = func(string) (*domainChatStorage.DeviceRecord, error) {
		return &domainChatStorage.DeviceRecord{WebhookURL: &deviceURL}, nil
	}
	body := func() map[string]any {
		return map[string]any{"event": "message", "device_id": "5511999999999@s.whatsapp.net"}
	}

	if err := forwardPayloadToConfiguredWebhooks(context.Background(), body(), "message"); err != nil {
		t.Fatal(err)
	}
	sessionIDForJIDFn = func(string) string { return "org_2" }
	if err := forwardPayloadToConfiguredWebhooks(context.Background(), body(), "message"); err != nil {
		t.Fatal(err)
	}

	rows := pendingRows(t, outbox)
	if len(rows) != 2 || rows[0].ConfigRef != "jid:5511999999999@s.whatsapp.net" || rows[1].ConfigRef != "device:org_2" {
		t.Fatalf("config refs %+v", rows)
	}
	if rows[0].TargetURL != deviceURL {
		t.Fatalf("device URL not used: %+v", rows[0])
	}
}

func TestDurableSubmitFailureMarksTheHandlerFailed(t *testing.T) {
	outbox := useTestOutbox(t)
	useGlobalWebhooks(t, "http://a.test/hook")
	outbox.Store().Close() // the queue is unavailable

	ctx, failed := withHandlerFailureFlag(context.Background())
	err := forwardPayloadToConfiguredWebhooks(ctx, map[string]any{"event": "message"}, "message")
	if err == nil || !failed.Load() {
		t.Fatalf("err = %v, failed = %v; want an error and the flag set", err, failed.Load())
	}
}

func TestChatPresenceBypassesTheOutbox(t *testing.T) {
	outbox := useTestOutbox(t)
	got := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		got <- body
	}))
	t.Cleanup(srv.Close)
	useGlobalWebhooks(t, srv.URL)

	payload := map[string]any{"event": "chat_presence", "payload": map[string]any{"state": "composing"}}
	if err := forwardPayloadToConfiguredWebhooks(context.Background(), payload, "chat_presence"); err != nil {
		t.Fatal(err)
	}
	select {
	case body := <-got:
		if _, ok := body["event_id"]; ok {
			t.Fatalf("chat_presence went through the outbox: %v", body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("chat_presence was not sent directly")
	}
	if rows := pendingRows(t, outbox); len(rows) != 0 {
		t.Fatalf("chat_presence queued: %+v", rows)
	}
}

func TestDeviceAndGlobalLegsQueueConcurrently(t *testing.T) {
	outbox := useTestOutbox(t)
	useGlobalWebhooks(t, "http://global.test/hook")
	config.WhatsappWebhookDeviceMergeGlobal = true
	deviceURL := "http://device.test/hook"
	webhookStorageForTest = func(string) (*domainChatStorage.DeviceRecord, error) {
		return &domainChatStorage.DeviceRecord{WebhookURL: &deviceURL}, nil
	}

	for i := range 20 {
		payload := map[string]any{"event": "message", "device_id": "5511999999999@s.whatsapp.net",
			"payload": map[string]any{"id": i, "nested": map[string]any{"text": "hi"}}}
		if err := forwardPayloadToConfiguredWebhooks(context.Background(), payload, "message"); err != nil {
			t.Fatal(err)
		}
	}

	rows := pendingRows(t, outbox)
	perURL := map[string]int{}
	for _, row := range rows {
		perURL[row.TargetURL]++
		full, err := outbox.Store().Get(context.Background(), row.EventID)
		if err != nil || full == nil {
			t.Fatalf("Get: %v", err)
		}
		var body map[string]any
		if err := json.Unmarshal(full.Body, &body); err != nil || body["event_id"] != row.EventID {
			t.Fatalf("body %s", full.Body)
		}
	}
	if perURL[deviceURL] != 20 || perURL["http://global.test/hook"] != 20 {
		t.Fatalf("rows per URL %v", perURL)
	}
}

func TestResolveWebhookSecret(t *testing.T) {
	prevSecret, prevInsecure, prevStorage := config.WhatsappWebhookSecret, config.WhatsappWebhookInsecureSkipVerify, webhookStorageForTest
	t.Cleanup(func() {
		config.WhatsappWebhookSecret, config.WhatsappWebhookInsecureSkipVerify, webhookStorageForTest = prevSecret, prevInsecure, prevStorage
	})
	config.WhatsappWebhookSecret, config.WhatsappWebhookInsecureSkipVerify = "global-secret", false
	ctx := context.Background()
	jid := "5511999999999@s.whatsapp.net"
	deviceURL := "http://device.test/hook"

	check := func(ref, wantSecret string, wantInsecure bool) {
		t.Helper()
		secret, insecure, err := resolveWebhookSecret(ctx, ref)
		if err != nil || secret != wantSecret || insecure != wantInsecure {
			t.Fatalf("resolve(%s) = %q, %v, %v; want %q, %v", ref, secret, insecure, err, wantSecret, wantInsecure)
		}
	}

	check("global", "global-secret", false)

	webhookStorageForTest = func(got string) (*domainChatStorage.DeviceRecord, error) {
		if got != jid {
			return nil, errors.New("unexpected jid " + got)
		}
		return &domainChatStorage.DeviceRecord{WebhookURL: &deviceURL, WebhookSecret: "device-secret", WebhookInsecureSkipVerify: true}, nil
	}
	check("jid:"+jid, "device-secret", true)

	webhookStorageForTest = func(string) (*domainChatStorage.DeviceRecord, error) {
		return &domainChatStorage.DeviceRecord{WebhookURL: &deviceURL}, nil
	}
	check("jid:"+jid, "global-secret", false) // device webhook without its own secret

	webhookStorageForTest = func(string) (*domainChatStorage.DeviceRecord, error) { return nil, nil }
	check("jid:"+jid, "global-secret", false) // device webhook removed since queueing

	webhookStorageForTest = func(string) (*domainChatStorage.DeviceRecord, error) { return nil, errors.New("db locked") }
	if _, _, err := resolveWebhookSecret(ctx, "jid:"+jid); err == nil {
		t.Fatal("lookup error must be returned so the attempt is retried")
	}
	if _, _, err := resolveWebhookSecret(ctx, "bogus"); err == nil {
		t.Fatal("unknown ref accepted")
	}
}

func TestStartDurableWebhooksModes(t *testing.T) {
	previousOutbox, previousSubmit := durableOutbox, submitWebhookFn
	t.Cleanup(func() {
		if durableOutbox != nil && durableOutbox != previousOutbox {
			durableOutbox.Store().Close()
		}
		durableOutbox, submitWebhookFn = previousOutbox, previousSubmit
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	for _, mode := range []string{"", "direct", " Direct "} {
		t.Setenv(webhookDeliveryEnv, mode)
		if err := StartDurableWebhooks(ctx); err != nil || durableWebhooksEnabled() {
			t.Fatalf("mode %q: err %v, enabled %v", mode, err, durableWebhooksEnabled())
		}
	}

	t.Setenv(webhookDeliveryEnv, "queue")
	if err := StartDurableWebhooks(ctx); err == nil || durableWebhooksEnabled() {
		t.Fatalf("invalid mode accepted: %v", err)
	}

	t.Setenv(webhookDeliveryEnv, "durable")
	t.Setenv(webhookOutboxDBEnv, "file:"+filepath.Join(t.TempDir(), "outbox.db"))
	if err := StartDurableWebhooks(ctx); err != nil || !durableWebhooksEnabled() || DurableWebhookOutbox() == nil {
		t.Fatalf("durable mode: err %v, enabled %v", err, durableWebhooksEnabled())
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `cd src && GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/ -run 'Durable|ChatPresenceBypasses|DeviceAndGlobalLegs|ResolveWebhookSecret' -count=1`
Expected: FAIL to build, `undefined: durableOutbox` (and `submitWebhookDurable`, `resolveWebhookSecret`, …).

- [ ] **Step 3: Implement the durable submit**

Create `src/infrastructure/whatsapp/webhook_durable.go`:

```go
package whatsapp

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/webhookoutbox"
	"github.com/sirupsen/logrus"
)

// Fork (elphant): durable webhook delivery (G3). With
// WHATSAPP_WEBHOOK_DELIVERY=durable every webhook event except chat_presence is
// written to a SQLite outbox and sent by webhookoutbox workers, in order per
// URL, retried for up to 72 hours. See docs/reference/elphant-fork.md.

const (
	webhookDeliveryEnv = "WHATSAPP_WEBHOOK_DELIVERY"
	webhookOutboxDBEnv = "WHATSAPP_WEBHOOK_OUTBOX_DB"
	defaultOutboxDBURI = "file:storages/webhook-outbox.db"
)

// durableOutbox is nil in direct mode.
var durableOutbox *webhookoutbox.Outbox

func durableWebhooksEnabled() bool { return durableOutbox != nil }

// DurableWebhookOutbox returns the outbox, or nil in direct mode.
func DurableWebhookOutbox() *webhookoutbox.Outbox { return durableOutbox }

// StartDurableWebhooks opens the outbox and starts its workers when
// WHATSAPP_WEBHOOK_DELIVERY=durable; direct (the default) changes nothing. It
// must run before the first WhatsApp client is created.
func StartDurableWebhooks(ctx context.Context) error {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv(webhookDeliveryEnv)))
	switch mode {
	case "", "direct":
		return nil
	case "durable":
	default:
		return fmt.Errorf("%s=%q: want direct or durable", webhookDeliveryEnv, mode)
	}

	uri := strings.TrimSpace(os.Getenv(webhookOutboxDBEnv))
	if uri == "" {
		uri = defaultOutboxDBURI
	}
	store, err := webhookoutbox.OpenStore(uri)
	if err != nil {
		return fmt.Errorf("open webhook outbox: %w", err)
	}
	outbox := webhookoutbox.New(store, resolveWebhookSecret, webhookoutbox.DefaultPolicy)
	if err := outbox.Start(ctx); err != nil {
		store.Close()
		return fmt.Errorf("start webhook outbox: %w", err)
	}
	durableOutbox = outbox
	submitWebhookFn = submitWebhookDurable
	logrus.Infof("Webhook delivery: durable (outbox %s)", uri)
	return nil
}

// submitWebhookDurable replaces submitWebhook in durable mode: it queues one
// row for url and returns. A queue error marks the event handler as failed so
// whatsmeow does not ack the message (see handleEventWithStatus).
func submitWebhookDurable(ctx context.Context, payload map[string]any, url string, webhookConfig *domainChatStorage.DeviceWebhookConfig) error {
	eventName, _ := payload["event"].(string)
	if eventName == "chat_presence" {
		// A typing indicator is worthless hours later: send it directly.
		return submitWebhook(ctx, payload, url, webhookConfig)
	}
	if _, err := durableOutbox.Enqueue(ctx, url, webhookConfigRef(payload, webhookConfig), eventName, payload); err != nil {
		markHandlerFailed(ctx)
		return fmt.Errorf("queue webhook %s for %s: %w", eventName, url, err)
	}
	return nil
}

// webhookConfigRef names where the secret of this delivery comes from, so it
// is read again at send time and never stored in the outbox.
func webhookConfigRef(payload map[string]any, webhookConfig *domainChatStorage.DeviceWebhookConfig) string {
	if webhookConfig == nil {
		return "global"
	}
	if sessionID, _ := payload["session_id"].(string); sessionID != "" {
		return "device:" + sessionID
	}
	deviceID, _ := payload["device_id"].(string)
	return "jid:" + deviceID
}

// resolveWebhookSecret applies submitWebhook's rules to a config_ref: the
// device secret when set, else the global one; TLS verification is skipped
// when either the global or the device setting says so.
func resolveWebhookSecret(_ context.Context, ref string) (string, bool, error) {
	var (
		webhookConfig *domainChatStorage.DeviceWebhookConfig
		err           error
	)
	switch {
	case ref == "global":
	case strings.HasPrefix(ref, "device:"):
		webhookConfig, err = getWebhookConfigForSlot(map[string]any{"session_id": strings.TrimPrefix(ref, "device:")})
	case strings.HasPrefix(ref, "jid:"):
		webhookConfig, err = getWebhookConfigForDevice(strings.TrimPrefix(ref, "jid:"))
	default:
		return "", false, fmt.Errorf("unknown webhook config ref %q", ref)
	}
	if err != nil {
		return "", false, err
	}
	secret, insecure := config.WhatsappWebhookSecret, config.WhatsappWebhookInsecureSkipVerify
	if webhookConfig != nil {
		if webhookConfig.WebhookInsecureSkipVerify {
			insecure = true
		}
		if webhookConfig.WebhookSecret != "" {
			secret = webhookConfig.WebhookSecret
		}
	}
	return secret, insecure, nil
}

type handlerFailureKey struct{}

// withHandlerFailureFlag returns a context that markHandlerFailed can flag.
func withHandlerFailureFlag(ctx context.Context) (context.Context, *atomic.Bool) {
	failed := new(atomic.Bool)
	return context.WithValue(ctx, handlerFailureKey{}, failed), failed
}

// markHandlerFailed flags the event handler run that ctx belongs to; it is a
// no-op for contexts without the flag.
func markHandlerFailed(ctx context.Context) {
	if failed, ok := ctx.Value(handlerFailureKey{}).(*atomic.Bool); ok {
		failed.Store(true)
	}
}
```

- [ ] **Step 4: Add the startup hook**

In `src/cmd/root.go`, `initApp`, right after `chatStorageRepo.InitializeSchema()`:

```go
	// Fork (elphant): durable webhook outbox; no-op unless WHATSAPP_WEBHOOK_DELIVERY=durable.
	// Before InitWaCLI: clients read the mode when they are created.
	if err := whatsapp.StartDurableWebhooks(ctx); err != nil {
		logrus.Fatalf("failed to start durable webhooks: %v", err)
	}
```

- [ ] **Step 5: Run the tests, with the race detector**

Run: `cd src && gofmt -w infrastructure/whatsapp cmd && GOTOOLCHAIN=auto go vet ./infrastructure/whatsapp/ ./cmd/ && GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/ -count=1 && GOTOOLCHAIN=auto go test -race ./infrastructure/whatsapp/ -run 'Durable|ChatPresenceBypasses|DeviceAndGlobalLegs|ResolveWebhookSecret' -count=1`
Expected: vet clean; the whole package PASSES (existing webhook tests unchanged in direct mode); the `-race` run PASSES with no race report.

- [ ] **Step 6: Commit**

```bash
git add src/infrastructure/whatsapp/webhook_durable.go src/infrastructure/whatsapp/webhook_durable_test.go src/cmd/root.go
git commit -m "feat(whatsapp): queue webhooks in the durable outbox when enabled"
```

---

### Task 4: Withhold the WhatsApp ack when a webhook cannot be queued

**Files:**
- Modify: `src/infrastructure/whatsapp/webhook_durable.go` (append)
- Modify: `src/infrastructure/whatsapp/init.go:121-123` (handler registration)
- Modify: `src/infrastructure/whatsapp/device_manager.go:853-855` (handler registration)
- Modify: `src/infrastructure/whatsapp/event_message_handler.go` (`handleWebhookForward`, before the `go func`)
- Modify: `src/infrastructure/whatsapp/event_handler.go` (`handleReceipt`, before `if sendReceipt {`)
- Modify: `src/infrastructure/whatsapp/event_session.go` (`sessionStatusDispatch`, fork file)
- Test: `src/infrastructure/whatsapp/webhook_durable_handler_test.go`

**Interfaces:**
- Consumes (Task 3): `durableWebhooksEnabled`, `withHandlerFailureFlag`, `submitWebhookDurable` (via `submitWebhookFn`), test helpers `useTestOutbox`, `useGlobalWebhooks`, `pendingRows`.
- Consumes (existing): `handler(ctx, *DeviceInstance, any)`, `forwardMessageToWebhook`, `forwardReceiptToWebhook`, `NewDeviceInstance`, test helpers `messageHandlerRepoSpy`, `reactionEventForTest`, `newTestSQLStore`.
- Produces: `registerEventHandler(ctx, *whatsmeow.Client, *DeviceInstance)`, `handleEventWithStatus(ctx, *DeviceInstance, any) bool`, `forwardMessageDurably(ctx, *whatsmeow.Client, *events.Message, *webhookPollPayload)`, `forwardReceiptDurably(ctx, *events.Receipt, string, *whatsmeow.Client)`, `dispatchSessionStatus(func())`.

- [ ] **Step 1: Write the failing tests**

Create `src/infrastructure/whatsapp/webhook_durable_handler_test.go`:

```go
package whatsapp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// quietMessageHandler turns off logging, auto-reply and auto-read for handler tests.
func quietMessageHandler(t *testing.T) {
	t.Helper()
	prevLog, prevReply, prevRead := log, config.WhatsappAutoReplyMessage, config.WhatsappAutoMarkRead
	log, config.WhatsappAutoReplyMessage, config.WhatsappAutoMarkRead = waLog.Noop, "", false
	t.Cleanup(func() { log, config.WhatsappAutoReplyMessage, config.WhatsappAutoMarkRead = prevLog, prevReply, prevRead })
}

func testInstance() *DeviceInstance {
	return NewDeviceInstance("dev-a", nil, &messageHandlerRepoSpy{})
}

func TestDurableHandlerQueuesTheMessageBeforeReturning(t *testing.T) {
	outbox := useTestOutbox(t)
	useGlobalWebhooks(t, "http://a.test/hook")
	quietMessageHandler(t)

	if !handleEventWithStatus(context.Background(), testInstance(), reactionEventForTest("R1", "M1", "\U0001f44d")) {
		t.Fatal("handler reported failure")
	}
	// No waiting: in durable mode the row exists when the handler returns.
	rows := pendingRows(t, outbox)
	if len(rows) != 1 || rows[0].EventName != EventTypeMessageReaction {
		t.Fatalf("rows %+v", rows)
	}
}

func TestDurableHandlerReportsFailureWhenTheQueueIsDown(t *testing.T) {
	outbox := useTestOutbox(t)
	useGlobalWebhooks(t, "http://a.test/hook")
	quietMessageHandler(t)
	outbox.Store().Close()

	if handleEventWithStatus(context.Background(), testInstance(), reactionEventForTest("R2", "M1", "\U0001f44d")) {
		t.Fatal("handler reported success with the outbox closed; WhatsApp would not redeliver")
	}
}

func TestDirectHandlerAlwaysReportsSuccess(t *testing.T) {
	useGlobalWebhooks(t, "http://a.test/hook")
	quietMessageHandler(t)
	previous := submitWebhookFn
	delivered := make(chan struct{}, 1)
	submitWebhookFn = func(context.Context, map[string]any, string, *domainChatStorage.DeviceWebhookConfig) error {
		delivered <- struct{}{}
		return errors.New("receiver down")
	}
	t.Cleanup(func() { submitWebhookFn = previous })

	if !handleEventWithStatus(context.Background(), testInstance(), reactionEventForTest("R3", "M1", "\U0001f44d")) {
		t.Fatal("direct mode must never withhold the ack")
	}
	select {
	case <-delivered: // still forwarded from a goroutine, as upstream does
	case <-time.After(5 * time.Second):
		t.Fatal("direct mode did not forward the webhook")
	}
}

func TestDurableReceiptIsQueuedBeforeReturning(t *testing.T) {
	outbox := useTestOutbox(t)
	useGlobalWebhooks(t, "http://a.test/hook")
	quietMessageHandler(t)
	contact := types.NewJID("5511999999999", types.DefaultUserServer)
	evt := &events.Receipt{
		MessageSource: types.MessageSource{Chat: contact, Sender: contact},
		MessageIDs:    []types.MessageID{"M1"},
		Timestamp:     time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC),
		Type:          types.ReceiptTypeRead,
	}

	if !handleEventWithStatus(context.Background(), testInstance(), evt) {
		t.Fatal("handler reported failure")
	}
	rows := pendingRows(t, outbox)
	if len(rows) != 1 || rows[0].EventName != "message.ack" {
		t.Fatalf("rows %+v", rows)
	}
}

func TestSessionStatusIsQueuedInTheCallerInDurableMode(t *testing.T) {
	useTestOutbox(t)
	ran := false
	dispatchSessionStatus(func() { ran = true })
	if !ran {
		t.Fatal("durable mode must queue session.status before returning")
	}
}

func TestSessionStatusRunsOffTheCallerInDirectMode(t *testing.T) {
	previous := durableOutbox
	durableOutbox = nil
	t.Cleanup(func() { durableOutbox = previous })

	block, done := make(chan struct{}), make(chan struct{})
	dispatchSessionStatus(func() {
		<-block
		close(done)
	}) // returns while deliver is still blocked
	close(block)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("delivery never ran")
	}
}

func TestRegisterEventHandlerEnablesTheDecryptedEventBufferOnlyInDurableMode(t *testing.T) {
	container := newTestSQLStore(t)
	instance := testInstance()

	direct := whatsmeow.NewClient(container.NewDevice(), nil)
	registerEventHandler(context.Background(), direct, instance)
	if direct.EnableDecryptedEventBuffer {
		t.Fatal("direct mode enabled the decrypted-event buffer")
	}

	useTestOutbox(t)
	durable := whatsmeow.NewClient(container.NewDevice(), nil)
	registerEventHandler(context.Background(), durable, instance)
	if !durable.EnableDecryptedEventBuffer {
		t.Fatal("durable mode must buffer decrypted events so a redelivered message can be read again")
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `cd src && GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/ -run 'DurableHandler|DirectHandler|DurableReceipt|SessionStatusIs|SessionStatusRuns|RegisterEventHandler' -count=1`
Expected: FAIL to build, `undefined: handleEventWithStatus` (and `dispatchSessionStatus`, `registerEventHandler`).

- [ ] **Step 3: Add the handler wiring to `webhook_durable.go`**

Add these imports to `src/infrastructure/whatsapp/webhook_durable.go`: `"time"`, `"go.mau.fi/whatsmeow"`, `"go.mau.fi/whatsmeow/types/events"`. Append:

```go
// registerEventHandler attaches the device event handler with a success
// status. In durable mode the client also keeps decrypted events in
// whatsmeow's buffer: a message whose handler failed is not acked, and when
// WhatsApp redelivers it, whatsmeow reads it back from the buffer instead of
// failing to decrypt it a second time.
func registerEventHandler(ctx context.Context, client *whatsmeow.Client, instance *DeviceInstance) {
	client.EnableDecryptedEventBuffer = durableWebhooksEnabled()
	client.AddEventHandlerWithSuccessStatus(func(rawEvt any) bool {
		return handleEventWithStatus(ctx, instance, rawEvt)
	})
}

// handleEventWithStatus runs the handler and reports false when a webhook of
// this event could not be queued. It is always true in direct mode.
func handleEventWithStatus(ctx context.Context, instance *DeviceInstance, rawEvt any) bool {
	ctx, failed := withHandlerFailureFlag(ctx)
	handler(ctx, instance, rawEvt)
	return !failed.Load()
}

// forwardMessageDurably is handleWebhookForward in durable mode: it runs in
// the event handler, so a queue error reaches handleEventWithStatus before
// whatsmeow acks the message.
func forwardMessageDurably(ctx context.Context, client *whatsmeow.Client, evt *events.Message, poll *webhookPollPayload) {
	webhookCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := forwardMessageToWebhook(webhookCtx, client, evt, poll); err != nil {
		logrus.Error("Failed forward to webhook: ", err)
	}
}

// forwardReceiptDurably is the receipt forward of handleReceipt in durable mode.
func forwardReceiptDurably(ctx context.Context, evt *events.Receipt, deviceID string, client *whatsmeow.Client) {
	webhookCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := forwardReceiptToWebhook(webhookCtx, evt, deviceID, client); err != nil {
		logrus.Errorf("Failed to forward ack event to webhook: %v", err)
	}
}
```

- [ ] **Step 4: Add the hooks in the upstream files**

`src/infrastructure/whatsapp/init.go`, replace

```go
	client.AddEventHandler(func(rawEvt any) {
		handler(ctx, instance, rawEvt)
	})
```

with

```go
	// Fork (elphant): success-status handler for durable webhooks. See webhook_durable.go.
	registerEventHandler(ctx, client, instance)
```

`src/infrastructure/whatsapp/device_manager.go`, replace

```go
	client.AddEventHandler(func(rawEvt any) {
		handler(ctx, inst, rawEvt)
	})
```

with

```go
	// Fork (elphant): success-status handler for durable webhooks. See webhook_durable.go.
	registerEventHandler(ctx, client, inst)
```

`src/infrastructure/whatsapp/event_message_handler.go`, in `handleWebhookForward`, right before `go func(e *events.Message, c *whatsmeow.Client, poll *webhookPollPayload) {`:

```go
	if durableWebhooksEnabled() {
		// Fork (elphant): queue in the handler so a failed write withholds the ack. See webhook_durable.go.
		forwardMessageDurably(ctx, client, evt, pollPayload)
		return
	}
```

`src/infrastructure/whatsapp/event_handler.go`, in `handleReceipt`, right before `if sendReceipt {`:

```go
	if sendReceipt && durableWebhooksEnabled() {
		// Fork (elphant): queue the ack in the handler. See webhook_durable.go.
		forwardReceiptDurably(ctx, evt, deviceID, client)
		return
	}
```

`src/infrastructure/whatsapp/event_session.go`, replace

```go
// sessionStatusDispatch runs a session.status delivery off the caller's
// goroutine. Tests replace it to wait for deliveries or to skip them.
var sessionStatusDispatch = func(deliver func()) { go deliver() }
```

with

```go
// sessionStatusDispatch runs a session.status delivery. Tests replace it to
// wait for deliveries or to skip them.
var sessionStatusDispatch = dispatchSessionStatus

// dispatchSessionStatus delivers off the caller's goroutine, except in
// durable mode: there delivery is only an outbox write, so it runs in the
// caller and events are queued in the order they happened.
func dispatchSessionStatus(deliver func()) {
	if durableWebhooksEnabled() {
		deliver()
		return
	}
	go deliver()
}
```

- [ ] **Step 5: Run the package tests, with the race detector**

Run: `cd src && gofmt -w infrastructure/whatsapp && GOTOOLCHAIN=auto go vet ./infrastructure/whatsapp/ && GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/ -count=1 && GOTOOLCHAIN=auto go test -race ./infrastructure/whatsapp/ -count=1`
Expected: vet clean; PASS twice, no race report. `grep -rn "AddEventHandler(" infrastructure/whatsapp --include='*.go' | grep -v _test` prints nothing.

- [ ] **Step 6: Commit**

```bash
git add src/infrastructure/whatsapp/webhook_durable.go src/infrastructure/whatsapp/webhook_durable_handler_test.go \
  src/infrastructure/whatsapp/init.go src/infrastructure/whatsapp/device_manager.go \
  src/infrastructure/whatsapp/event_message_handler.go src/infrastructure/whatsapp/event_handler.go \
  src/infrastructure/whatsapp/event_session.go
git commit -m "feat(whatsapp): withhold the WhatsApp ack when a webhook cannot be queued"
```

---

### Task 5: Operations API

**Files:**
- Create: `src/ui/rest/webhook_outbox.go`
- Test: `src/ui/rest/webhook_outbox_test.go`
- Modify: `src/cmd/rest.go` (right after `rest.InitRestAppInfo(apiGroup)`)

**Interfaces:**
- Consumes (Tasks 1-3): `webhookoutbox.Outbox` (`Store`, `Redeliver`, `Replay`, `Enqueue`), `(*Store).Stats/List/Get/MarkDead/MarkDelivered`, `Status*`, `DefaultListLimit`, `MaxListLimit`; `whatsapp.DurableWebhookOutbox`.
- Produces: `func InitRestWebhookOutbox(app fiber.Router, outbox func() *webhookoutbox.Outbox)` and the five routes.

- [ ] **Step 1: Write the failing tests**

Create `src/ui/rest/webhook_outbox_test.go`:

```go
package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/webhookoutbox"
	"github.com/gofiber/fiber/v3"
)

func newOutboxTestApp(outbox *webhookoutbox.Outbox) *fiber.App {
	app := fiber.New()
	InitRestWebhookOutbox(app, func() *webhookoutbox.Outbox { return outbox })
	return app
}

func callOutbox(t *testing.T, app *fiber.App, method, target string) (int, map[string]any) {
	t.Helper()
	resp, err := app.Test(httptest.NewRequest(method, target, nil))
	if err != nil {
		t.Fatalf("%s %s: %v", method, target, err)
	}
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body
}

func TestWebhookOutboxRoutesAre404InDirectMode(t *testing.T) {
	app := newOutboxTestApp(nil)
	for _, route := range [][2]string{
		{http.MethodGet, "/webhooks/stats"},
		{http.MethodGet, "/webhooks/deliveries"},
		{http.MethodGet, "/webhooks/deliveries/X"},
		{http.MethodPost, "/webhooks/deliveries/X/redeliver"},
		{http.MethodPost, "/webhooks/replay?since=2026-09-25T00:00:00Z"},
	} {
		if status, _ := callOutbox(t, app, route[0], route[1]); status != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", route[0], route[1], status)
		}
	}
}

func TestWebhookOutboxRoutes(t *testing.T) {
	ctx := context.Background()
	store, err := webhookoutbox.OpenStore("file:" + filepath.Join(t.TempDir(), "outbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	resolve := func(context.Context, string) (string, bool, error) { return "", false, nil }
	outbox := webhookoutbox.New(store, resolve, webhookoutbox.DefaultPolicy) // not started: nothing is sent
	first, _ := outbox.Enqueue(ctx, "http://a.test/hook", "global", "message", map[string]any{"event": "message"})
	second, _ := outbox.Enqueue(ctx, "http://a.test/hook", "global", "message.ack", map[string]any{"event": "message.ack"})
	if err := store.MarkDead(ctx, first.ID, 1, "HTTP 400", 400); err != nil {
		t.Fatal(err)
	}
	app := newOutboxTestApp(outbox)

	status, body := callOutbox(t, app, http.MethodGet, "/webhooks/stats")
	stats, _ := body["results"].(map[string]any)
	if status != 200 || stats["pending"] != 1.0 || stats["dead"] != 1.0 {
		t.Fatalf("stats %d %v", status, body)
	}

	status, body = callOutbox(t, app, http.MethodGet, "/webhooks/deliveries?status=dead")
	list, _ := body["results"].([]any)
	if status != 200 || len(list) != 1 {
		t.Fatalf("dead list %d %v", status, body)
	}
	item := list[0].(map[string]any)
	if _, hasBody := item["body"]; hasBody || item["event_id"] != first.EventID || item["last_error"] != "HTTP 400" {
		t.Fatalf("list item %v", item)
	}

	status, body = callOutbox(t, app, http.MethodGet, "/webhooks/deliveries/"+second.EventID)
	row, _ := body["results"].(map[string]any)
	stored, _ := row["body"].(map[string]any)
	if status != 200 || stored["event_id"] != second.EventID {
		t.Fatalf("get %d %v", status, body)
	}
	if status, _ = callOutbox(t, app, http.MethodGet, "/webhooks/deliveries/NOPE"); status != 404 {
		t.Fatalf("unknown get = %d", status)
	}

	status, body = callOutbox(t, app, http.MethodPost, "/webhooks/deliveries/"+first.EventID+"/redeliver")
	row, _ = body["results"].(map[string]any)
	if status != 200 || row["status"] != "pending" || row["replay"] != true {
		t.Fatalf("redeliver %d %v", status, body)
	}
	if status, _ = callOutbox(t, app, http.MethodPost, "/webhooks/deliveries/NOPE/redeliver"); status != 404 {
		t.Fatalf("unknown redeliver = %d", status)
	}

	for _, bad := range []string{
		"/webhooks/deliveries?status=lost",
		"/webhooks/deliveries?limit=0",
		"/webhooks/deliveries?limit=501",
		"/webhooks/deliveries?before_id=abc",
	} {
		if status, _ := callOutbox(t, app, http.MethodGet, bad); status != 400 {
			t.Errorf("GET %s = %d, want 400", bad, status)
		}
	}
	for _, bad := range []string{"/webhooks/replay", "/webhooks/replay?since=yesterday"} {
		if status, _ := callOutbox(t, app, http.MethodPost, bad); status != 400 {
			t.Errorf("POST %s = %d, want 400", bad, status)
		}
	}

	if err := store.MarkDelivered(ctx, second.ID, 1, 200); err != nil {
		t.Fatal(err)
	}
	status, body = callOutbox(t, app, http.MethodPost, "/webhooks/replay?since=2000-01-01T00:00:00Z")
	result, _ := body["results"].(map[string]any)
	if status != 200 || result["requeued"] != 1.0 {
		t.Fatalf("replay %d %v", status, body)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `cd src && GOTOOLCHAIN=auto go test ./ui/rest/ -run WebhookOutbox -count=1`
Expected: FAIL to build, `undefined: InitRestWebhookOutbox`.

- [ ] **Step 3: Implement the handlers**

Create `src/ui/rest/webhook_outbox.go`:

```go
package rest

import (
	"net/http"
	"strconv"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/webhookoutbox"
	"github.com/gofiber/fiber/v3"
)

// Fork (elphant): operations API of the durable webhook outbox. Every route
// answers 404 while WHATSAPP_WEBHOOK_DELIVERY is direct.
type WebhookOutbox struct {
	Outbox func() *webhookoutbox.Outbox
}

func InitRestWebhookOutbox(app fiber.Router, outbox func() *webhookoutbox.Outbox) {
	handler := WebhookOutbox{Outbox: outbox}
	app.Get("/webhooks/stats", handler.Stats)
	app.Get("/webhooks/deliveries", handler.ListDeliveries)
	app.Get("/webhooks/deliveries/:event_id", handler.GetDelivery)
	app.Post("/webhooks/deliveries/:event_id/redeliver", handler.Redeliver)
	app.Post("/webhooks/replay", handler.Replay)
}

func (handler WebhookOutbox) Stats(c fiber.Ctx) error {
	outbox := handler.Outbox()
	if outbox == nil {
		return outboxOff(c)
	}
	stats, err := outbox.Store().Stats(c.Context())
	if err != nil {
		return outboxFailure(c, err)
	}
	return c.JSON(utils.ResponseData{Status: http.StatusOK, Code: "SUCCESS", Message: "Webhook outbox stats", Results: stats})
}

func (handler WebhookOutbox) ListDeliveries(c fiber.Ctx) error {
	outbox := handler.Outbox()
	if outbox == nil {
		return outboxOff(c)
	}
	status := c.Query("status")
	switch status {
	case "", webhookoutbox.StatusPending, webhookoutbox.StatusDelivered, webhookoutbox.StatusDead:
	default:
		return badOutboxRequest(c, "status must be pending, delivered or dead")
	}
	limit := webhookoutbox.DefaultListLimit
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > webhookoutbox.MaxListLimit {
			return badOutboxRequest(c, "limit must be between 1 and 500")
		}
		limit = n
	}
	var beforeID int64
	if raw := c.Query("before_id"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 1 {
			return badOutboxRequest(c, "before_id must be a positive integer")
		}
		beforeID = n
	}
	rows, err := outbox.Store().List(c.Context(), status, limit, beforeID)
	if err != nil {
		return outboxFailure(c, err)
	}
	return c.JSON(utils.ResponseData{Status: http.StatusOK, Code: "SUCCESS", Message: "Webhook deliveries", Results: rows})
}

func (handler WebhookOutbox) GetDelivery(c fiber.Ctx) error {
	outbox := handler.Outbox()
	if outbox == nil {
		return outboxOff(c)
	}
	row, err := outbox.Store().Get(c.Context(), c.Params("event_id"))
	if err != nil {
		return outboxFailure(c, err)
	}
	if row == nil {
		return deliveryNotFound(c)
	}
	return c.JSON(utils.ResponseData{Status: http.StatusOK, Code: "SUCCESS", Message: "Webhook delivery", Results: row})
}

func (handler WebhookOutbox) Redeliver(c fiber.Ctx) error {
	outbox := handler.Outbox()
	if outbox == nil {
		return outboxOff(c)
	}
	row, err := outbox.Redeliver(c.Context(), c.Params("event_id"))
	if err != nil {
		return outboxFailure(c, err)
	}
	if row == nil {
		return deliveryNotFound(c)
	}
	row.Body = nil
	return c.JSON(utils.ResponseData{Status: http.StatusOK, Code: "SUCCESS", Message: "Webhook delivery queued again", Results: row})
}

func (handler WebhookOutbox) Replay(c fiber.Ctx) error {
	outbox := handler.Outbox()
	if outbox == nil {
		return outboxOff(c)
	}
	since, err := time.Parse(time.RFC3339, c.Query("since"))
	if err != nil {
		return badOutboxRequest(c, "since must be an RFC3339 time, e.g. 2026-09-25T12:00:00Z")
	}
	n, err := outbox.Replay(c.Context(), since, c.Query("url"))
	if err != nil {
		return outboxFailure(c, err)
	}
	return c.JSON(utils.ResponseData{Status: http.StatusOK, Code: "SUCCESS", Message: "Webhook deliveries queued again", Results: map[string]any{"requeued": n}})
}

func outboxOff(c fiber.Ctx) error {
	return c.Status(http.StatusNotFound).JSON(utils.ResponseData{
		Status: http.StatusNotFound, Code: "NOT_FOUND",
		Message: "durable webhook delivery is off (WHATSAPP_WEBHOOK_DELIVERY=direct)",
	})
}

func deliveryNotFound(c fiber.Ctx) error {
	return c.Status(http.StatusNotFound).JSON(utils.ResponseData{Status: http.StatusNotFound, Code: "DELIVERY_NOT_FOUND", Message: "no webhook delivery with this event_id"})
}

func badOutboxRequest(c fiber.Ctx, message string) error {
	return c.Status(http.StatusBadRequest).JSON(utils.ResponseData{Status: http.StatusBadRequest, Code: "BAD_REQUEST", Message: message})
}

func outboxFailure(c fiber.Ctx, err error) error {
	return c.Status(http.StatusInternalServerError).JSON(utils.ResponseData{Status: http.StatusInternalServerError, Code: "INTERNAL_SERVER_ERROR", Message: err.Error()})
}
```

- [ ] **Step 4: Register the routes**

In `src/cmd/rest.go`, right after `rest.InitRestAppInfo(apiGroup)`:

```go
	// Fork (elphant): durable webhook outbox operations; 404 unless WHATSAPP_WEBHOOK_DELIVERY=durable.
	rest.InitRestWebhookOutbox(apiGroup, whatsapp.DurableWebhookOutbox)
```

- [ ] **Step 5: Run the tests**

Run: `cd src && gofmt -w ui/rest cmd && GOTOOLCHAIN=auto go vet ./ui/rest/ ./cmd/ && GOTOOLCHAIN=auto go test ./ui/rest/ ./cmd/ -count=1`
Expected: vet clean; PASS (including `TestDeviceGroupIsRegisteredLast`).

- [ ] **Step 6: Commit**

```bash
git add src/ui/rest/webhook_outbox.go src/ui/rest/webhook_outbox_test.go src/cmd/rest.go
git commit -m "feat(rest): add webhook outbox operations endpoints"
```

---

### Task 6: Documentation

**Files:**
- Modify: `docs/reference/elphant-fork.md` (new section after `session.status`)
- Modify: `~/Documents/elphantcrm-whatsapp-gateway/docs/reference/38-gowa.md` (section 2 YAML + new section 9), local commit only

- [ ] **Step 1: Add the fork section**

Append to `docs/reference/elphant-fork.md`:

```markdown
## Durable webhook delivery

Upstream sends each webhook from a goroutine started after WhatsApp was already told the message
arrived, and gives up after 5 attempts over about 15 seconds. An event is lost when the receiver is
down for longer, or when the process stops in between.

Set `WHATSAPP_WEBHOOK_DELIVERY=durable` (default `direct`, the upstream behavior) to queue every
webhook event except `chat_presence` in a SQLite outbox at `WHATSAPP_WEBHOOK_OUTBOX_DB` (default
`file:storages/webhook-outbox.db`). Both variables are read from the process environment only,
not from `.env`.

- Message events, `message.ack` and `session.status` are written to the outbox inside the WhatsApp
  event handler. If the write fails, the handler reports failure and whatsmeow does not acknowledge
  the message, so WhatsApp delivers it again. Durable mode turns on whatsmeow's decrypted-event
  buffer, so the redelivered message is read back instead of failing to decrypt.
- A message redelivered this way runs the whole handler again: chat storage, Chatwoot and
  auto-reply (a second auto-reply) included.
- Group, label, call, newsletter and app-state events are queued from their existing goroutines; a
  crash between the WhatsApp acknowledgement and the write can still lose one of them.
- `chat_presence` (typing) is sent directly, as upstream does.
- With `WHATSAPP_AUTO_DOWNLOAD_MEDIA=true` the media download happens inside the handler and slows
  message processing down. Keep it off; fetch media with `GET /message/:message_id/media`.

Delivery: one worker per destination URL sends rows oldest first. A failing row holds back the rows
behind it until it succeeds or is given up. Network errors, timeouts, `5xx`, `408` and `429` retry
after 10 s, 30 s, 1, 2, 5, 10 and 30 minutes, then every hour; `Retry-After` on `429` is honored. A
row still failing 72 hours after it was queued becomes `dead`; any other `4xx` makes it `dead` at
once. Queued rows survive restarts; a row in flight during a crash is sent again. Delivered rows are
kept 7 days, dead rows 30 days.

Contract (durable mode only):

- The body gets a top-level `event_id` (ULID), the same on every attempt, redelivery and replay.
  Each destination URL gets its own `event_id` for the same event.
- Headers: `X-Webhook-Id` (the `event_id`), `X-Webhook-Timestamp` (when the event was queued,
  RFC3339 UTC), `X-Webhook-Attempt` (1, 2, 3…) and `X-Webhook-Replay: true` on redeliveries and
  replays. `X-Hub-Signature-256` is unchanged. The secret is read from the current configuration at
  every attempt, so a rotated secret applies to queued rows.
- Answer `2xx` to confirm, and deduplicate by `event_id`: the same event can arrive more than once.

Operations API (Basic Auth; `404` in direct mode):

| Endpoint | Purpose |
|---|---|
| `GET /webhooks/stats` | counts per status and per URL, the oldest pending row and its age |
| `GET /webhooks/deliveries?status=&limit=&before_id=` | rows newest first, without bodies (`limit` 1-500, default 50) |
| `GET /webhooks/deliveries/:event_id` | one row with its body |
| `POST /webhooks/deliveries/:event_id/redeliver` | back to `pending`, attempts reset, same `event_id`, sent with `X-Webhook-Replay` |
| `POST /webhooks/replay?since=<RFC3339>[&url=]` | every delivered or dead row queued since then goes back to `pending` |

A redelivered or replayed row keeps its id, so it is sent before newer rows of the same URL.
```

- [ ] **Step 2: Update the CRM runbook**

In `~/Documents/elphantcrm-whatsapp-gateway/docs/reference/38-gowa.md`, section 2 YAML, add under `WHATSAPP_STABLE_AUDIT_DIR: "/app/storages/stable-audit"`:

```yaml
      WHATSAPP_WEBHOOK_DELIVERY: "durable"
```

Append a new section at the end of the file:

````markdown
## 9. Entrega durável de webhooks

Com `WHATSAPP_WEBHOOK_DELIVERY: "durable"` na stack, o GOWA grava cada webhook (menos
`chat_presence`) numa fila em `gowa_storages/webhook-outbox.db` antes de enviar. Se o CRM ficar fora
do ar, os eventos esperam na fila e saem na ordem em que aconteceram, com novas tentativas por até 72
horas. O arquivo entra no backup junto com os outros `.db` do volume.

**Contrato para o driver do CRM:** o corpo traz `event_id` (ULID), o mesmo em toda tentativa. Os
cabeçalhos trazem `X-Webhook-Id`, `X-Webhook-Timestamp`, `X-Webhook-Attempt` e, em reenvio,
`X-Webhook-Replay: true`. O CRM responde `2xx` para confirmar e ignora um `event_id` que já
processou. Um `4xx` (fora `408` e `429`) manda o evento direto para `dead`: usar só para evento que
nunca vai ser aceito, como assinatura inválida.

**O que olhar:**

```bash
A="$(cat ~/.config/elphant/gowa-basic-auth)"
curl -s -u "$A" https://devias.elphant.com.br/webhooks/stats
curl -s -u "$A" "https://devias.elphant.com.br/webhooks/deliveries?status=dead&limit=20"
```

`pending` alto ou `oldest_pending_age_seconds` crescendo: o CRM não está confirmando. `dead` maior
que zero: evento que o CRM recusou ou que passou de 72 horas.

**Reenviar:**

```bash
curl -s -u "$A" -X POST https://devias.elphant.com.br/webhooks/deliveries/<event_id>/redeliver
curl -s -u "$A" -X POST "https://devias.elphant.com.br/webhooks/replay?since=2026-09-25T00:00:00Z"
```

O replay reenvia tudo que foi entregue ou morreu desde a data (entregues ficam 7 dias na fila, mortos
30 dias). Serve, por exemplo, depois de restaurar o banco do CRM.

**Limites:** mensagens, `message.ack` e `session.status` não se perdem nem se o GOWA cair. Eventos de
grupo, etiqueta, ligação e canal ainda podem se perder se o GOWA cair no instante exato entre o
WhatsApp e a fila.
````

- [ ] **Step 3: Check the YAML still renders**

Run:
```bash
python3 -c 'import re; print(re.search(r"```yaml\n(.*?)```", open("/Users/eduardocarlos/Documents/elphantcrm-whatsapp-gateway/docs/reference/38-gowa.md").read(), re.S).group(1))' \
  | ssh root@<gateway-host> 'GOWA_VERSION=v0.0.0-check GOWA_BASIC_AUTH=user:check docker stack config -c - | grep -c WHATSAPP_WEBHOOK_DELIVERY'
```
Expected: `1` (read-only render on noria; nothing is deployed).

- [ ] **Step 4: Commit both (the CRM commit stays local)**

```bash
git add docs/reference/elphant-fork.md
git commit -m "docs: document durable webhook delivery"
cd ~/Documents/elphantcrm-whatsapp-gateway && git add docs/reference/38-gowa.md && git commit -m "docs(reference): entrega durável de webhooks do GOWA"
```

---

### Task 7: Local end-to-end check (no commit)

Runs a throwaway GOWA with an **unpaired** device in a scratch directory. Its only events are
`session.status` QR codes, which is enough to exercise the outbox end to end. Never point it at a
real session.

- [ ] **Step 1: Build and prepare**

```bash
E2E=/private/tmp/claude-501/-Users-eduardocarlos-Documents-whatsapp-gateway/997f9c4d-0bcf-496d-98ae-edf33908f2fb/scratchpad/g3-e2e
rm -rf "$E2E" && mkdir -p "$E2E/run"
cd src && GOTOOLCHAIN=auto go build -o "$E2E/whatsapp" .
cat > "$E2E/receiver.py" <<'EOF'
import hashlib, hmac, http.server, json, sys
SECRET = b"e2e-secret"
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        raw = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        body = json.loads(raw)
        sig = "sha256=" + hmac.new(SECRET, raw, hashlib.sha256).hexdigest()
        with open(sys.argv[2], "a") as f:
            f.write(json.dumps({
                "id": self.headers.get("X-Webhook-Id"), "event_id": body.get("event_id"),
                "attempt": self.headers.get("X-Webhook-Attempt"), "ts": self.headers.get("X-Webhook-Timestamp"),
                "replay": self.headers.get("X-Webhook-Replay"), "event": body.get("event"),
                "sig_ok": sig == self.headers.get("X-Hub-Signature-256"),
            }) + "\n")
        self.send_response(200)
        self.end_headers()
    def log_message(self, *args):
        pass
http.server.HTTPServer(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
EOF
```
Expected: binary built, receiver written.

- [ ] **Step 2: Start GOWA in durable mode with the receiver down** (background command)

```bash
cd "$E2E/run" && APP_PORT=3999 APP_BASIC_AUTH=e2e:e2e WHATSAPP_WEBHOOK=http://127.0.0.1:3998/hook \
  WHATSAPP_WEBHOOK_SECRET=e2e-secret WHATSAPP_WEBHOOK_DELIVERY=durable "$E2E/whatsapp" rest > "$E2E/gowa-1.log" 2>&1
```
Then: `sleep 5; grep -c "Webhook delivery: durable" "$E2E/gowa-1.log"; curl -s -u e2e:e2e http://127.0.0.1:3999/webhooks/stats`
Expected: `1`; stats JSON with `pending: 0`.

- [ ] **Step 3: Produce events while the receiver is down**

```bash
curl -s -u e2e:e2e -X POST -H 'Content-Type: application/json' -d '{"device_id":"e2e"}' http://127.0.0.1:3999/devices
curl -s -u e2e:e2e -H 'X-Device-Id: e2e' http://127.0.0.1:3999/app/login > /dev/null
sleep 45
curl -s -u e2e:e2e http://127.0.0.1:3999/webhooks/stats
```
Expected: `pending` ≥ 2 (QR `session.status` events), one URL `http://127.0.0.1:3998/hook`, `last_error` of the head row mentions `connection refused` (check with `curl -s -u e2e:e2e 'http://127.0.0.1:3999/webhooks/deliveries?limit=1'`). Note the pending count as P.

- [ ] **Step 4: Crash and restart**

```bash
pkill -9 -f "$E2E/whatsapp"; sleep 1
```
Start GOWA again exactly as in Step 2, logging to `"$E2E/gowa-2.log"` (background). Then:
`sleep 5; curl -s -u e2e:e2e http://127.0.0.1:3999/webhooks/stats`
Expected: `pending` is still at least P: the queue survived `kill -9`.

- [ ] **Step 5: Bring the receiver up and wait for the queue to drain**

Start `python3 "$E2E/receiver.py" 3998 "$E2E/received.jsonl"` (background). Poll every 15 s for up to 5 minutes:
`curl -s -u e2e:e2e http://127.0.0.1:3999/webhooks/stats`
Expected: `pending` reaches `0` and `delivered` ≥ P; `gowa-2.log` has one `recovered` line for the URL, not one line per attempt.

- [ ] **Step 6: Check order, identity and signature**

```bash
python3 - "$E2E/received.jsonl" <<'EOF'
import json, sys
rows = [json.loads(line) for line in open(sys.argv[1])]
ids = [r["id"] for r in rows]
assert all(r["id"] == r["event_id"] for r in rows), "header and body ids differ"
assert all(r["sig_ok"] for r in rows), "bad signature"
assert all(r["ts"] and r["ts"].endswith("Z") for r in rows), "bad timestamp"
assert len(ids) == len(set(ids)), "duplicate delivery"
assert ids == sorted(ids), "out of order"
print("received", len(rows), "in order, signed, no duplicates")
EOF
```
Expected: `received N in order, signed, no duplicates` with N equal to `delivered` from Step 5.

- [ ] **Step 7: Redeliver one event**

```bash
ID=$(head -1 "$E2E/received.jsonl" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
curl -s -u e2e:e2e -X POST "http://127.0.0.1:3999/webhooks/deliveries/$ID/redeliver"; sleep 3
tail -1 "$E2E/received.jsonl"
```
Expected: the last line has the same `id`, `"attempt": "1"`, `"replay": "true"`.

- [ ] **Step 8: Clean up**

```bash
pkill -9 -f "$E2E/whatsapp"; pkill -f "$E2E/receiver.py"; rm -rf "$E2E"
```
Expected: no process left on ports 3998/3999 (`lsof -i :3998 -i :3999` prints nothing); scratch directory removed.

---

### Task 8 [GATED]: Release `v9.5.0-elphant.5` and enable durable delivery on devias (gowa)

- [ ] **Step 1: Full local gate**

Run: `cd src && GOTOOLCHAIN=auto go vet ./... && GOTOOLCHAIN=auto go test ./... -count=1 && GOTOOLCHAIN=auto go test -race -count=1 ./pkg/webhookoutbox/ ./infrastructure/whatsapp/ ./ui/rest/ && cd ../contract && GOTOOLCHAIN=auto go test ./... -count=1 && git -C .. diff --quiet 93ee3ff -- src/go.mod src/go.sum && echo gomod-unchanged`
Expected: all PASS; `gomod-unchanged`.

- [ ] **Step 2: Ask the owner for OK** to push `elphant`, tag `v9.5.0-elphant.5` (publishes the image), and update the `gowa` stack (image + `WHATSAPP_WEBHOOK_DELIVERY=durable`), which restarts the container once.

- [ ] **Step 3: Push, CI, tag, image**

```bash
git push origin elphant
gh run watch "$(gh run list --repo eduardoelphant/go-whatsapp-web-multidevice --workflow 'Elphant CI' --branch elphant --limit 1 --json databaseId --jq '.[0].databaseId')" --repo eduardoelphant/go-whatsapp-web-multidevice --exit-status
git tag -a v9.5.0-elphant.5 -m v9.5.0-elphant.5 && git push origin v9.5.0-elphant.5
```
Then watch `Elphant image`. Expected: CI green; image run green; `ghcr.io/eduardoelphant/gowa:v9.5.0-elphant.5` for amd64 and arm64.

- [ ] **Step 4: Update the stack** through the Portainer API (`PUT /api/stacks/12?endpointId=1`, body built from the doc 38 YAML with `GOWA_VERSION=v9.5.0-elphant.5` and the current `GOWA_BASIC_AUTH`, sent with `curl`, temp body file mode 600 and deleted afterwards).
Expected: update `completed`; service running `v9.5.0-elphant.5`.

- [ ] **Step 5: Verify production**

```bash
A="$(cat ~/.config/elphant/gowa-basic-auth)"
curl -s -u "$A" https://devias.elphant.com.br/webhooks/stats
ssh root@<gateway-host> 'docker service logs --since 10m gowa_gowa 2>&1 | grep -c "Webhook delivery: durable"; docker service logs --since 10m gowa_gowa 2>&1 | grep -ciE "decrypt|Handler for .* failed|StreamReplaced|logged out"'
```
Expected: stats `200` with zero counts (no webhook URL is configured on the test number yet); `1` durable line; `0` errors; device `logged_in` in `GET /devices`.
Then ask the owner to send one message to the test number and run:
```bash
ssh root@<gateway-host> "python3 -c \"import sqlite3; c=sqlite3.connect('file:/var/lib/docker/volumes/gowa_storages/_data/whatsapp.db?mode=ro', uri=True); print(c.execute('select count(*), coalesce(sum(plaintext is not null),0) from whatsmeow_event_buffer').fetchone())\""
```
Expected: `(n, 0)` with n ≥ 1: the decrypted-event buffer is in use and cleared after each successful handler run; the log shows the `Received message` line and no `Handler for … failed`.

- [ ] **Step 6: Record the state** in the project memory (`project_phase2_next_steps.md`): G3 shipped in `v9.5.0-elphant.5`, durable on devias, full round trip pending the CRM driver.
