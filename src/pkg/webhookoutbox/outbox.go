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
