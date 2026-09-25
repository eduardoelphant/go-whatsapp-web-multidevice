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
