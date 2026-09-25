package webhookoutbox

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

func TestRedeliverRefusesARowThatIsStillPending(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestStore(t)
	row := mustInsert(t, s, "http://a.test", "message")
	if err := s.MarkRetry(ctx, row.ID, 3, s.now().Add(time.Hour), "HTTP 500", 500); err != nil {
		t.Fatal(err)
	}

	got, err := s.Redeliver(ctx, row.EventID)
	if !errors.Is(err, ErrAlreadyPending) || got != nil {
		t.Fatalf("Redeliver(pending) = %+v, %v; want ErrAlreadyPending", got, err)
	}
	if still := mustGet(t, s, row.EventID); still.Attempts != 3 || still.Replay {
		t.Fatalf("pending row was reset: %+v", still)
	}
}

func TestStoreSyncsEveryCommitToDisk(t *testing.T) {
	s := openStore(t)
	var mode int
	if err := s.db.QueryRow(`PRAGMA synchronous`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != 2 { // FULL: a queued event survives an OS crash or power loss
		t.Fatalf("synchronous = %d, want 2 (FULL)", mode)
	}
}

func TestIdleWorkerExitsAndARowRestartsIt(t *testing.T) {
	store := openStore(t)
	_, srv := startReceiver(t, always(http.StatusOK))
	policy := fastPolicy
	policy.IdleTimeout = 30 * time.Millisecond
	outbox := New(store, staticSecret("s"), policy)
	first := enqueueN(t, outbox, srv.URL, 1)[0]
	startOutbox(t, outbox)

	waitFor(t, "first delivery", func() bool { return statusOf(store, first.EventID) == StatusDelivered })
	waitFor(t, "idle worker to exit", func() bool { return outbox.workerCount() == 0 })

	second := enqueueN(t, outbox, srv.URL, 1)[0]
	waitFor(t, "delivery through a new worker", func() bool { return statusOf(store, second.EventID) == StatusDelivered })
}

func TestURLQueryNeverReachesLastErrorOrLogs(t *testing.T) {
	var logs bytes.Buffer
	previousOut, previousLevel := logrus.StandardLogger().Out, logrus.GetLevel()
	logrus.SetOutput(&logs)
	logrus.SetLevel(logrus.InfoLevel)
	t.Cleanup(func() { logrus.SetOutput(previousOut); logrus.SetLevel(previousLevel) })

	store := openStore(t)
	outbox := New(store, staticSecret("s"), fastPolicy)
	// Port 1 refuses connections, so the error carries the request URL.
	row := enqueueN(t, outbox, "http://127.0.0.1:1/hook?token=secret123", 1)[0]
	startOutbox(t, outbox)

	waitFor(t, "a failed attempt", func() bool {
		r, _ := store.Get(context.Background(), row.EventID)
		return r != nil && r.Attempts >= 1 && strings.Contains(logs.String(), "failing")
	})
	got, _ := store.Get(context.Background(), row.EventID)
	if strings.Contains(got.LastError, "secret123") || got.LastError == "" {
		t.Fatalf("last_error = %q", got.LastError)
	}
	if strings.Contains(logs.String(), "secret123") {
		t.Fatalf("log leaked the query: %s", logs.String())
	}
	if !strings.Contains(logs.String(), "http://127.0.0.1:1/hook") {
		t.Fatalf("log lost the destination: %s", logs.String())
	}
}

func TestRedactURL(t *testing.T) {
	cases := map[string]string{
		"https://crm.test/hook":                "https://crm.test/hook",
		"https://crm.test/hook?token=x&a=1":    "https://crm.test/hook",
		"https://user:pass@crm.test/hook#frag": "https://crm.test/hook",
		"http://[::1":                          "<invalid URL>",
	}
	for in, want := range cases {
		if got := redactURL(in); got != want {
			t.Errorf("redactURL(%q) = %q, want %q", in, got, want)
		}
	}
}
