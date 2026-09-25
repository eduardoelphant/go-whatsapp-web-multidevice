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
	"strings"
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
	// WAL plus synchronous=FULL: a queued event survives an OS crash or power
	// loss, which is the point of the outbox.
	dsn := sqlite.FormatChatStorageURI(uri, true, false)
	if strings.Contains(dsn, "?") {
		dsn += "&" + fullSyncParam
	} else {
		dsn += "?" + fullSyncParam
	}
	db, err := sql.Open(sqlite.DriverName, dsn)
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

// ErrAlreadyPending is returned by Redeliver for a row that is still queued:
// its worker may be sending it right now and would overwrite the reset.
var ErrAlreadyPending = errors.New("webhook delivery is still pending")

// Redeliver puts one delivered or dead row back in the queue with attempts
// reset, the same event_id and replay set. It returns nil when eventID is
// unknown and ErrAlreadyPending when the row is still queued.
func (s *Store) Redeliver(ctx context.Context, eventID string) (*Row, error) {
	now := ms(s.clock())
	res, err := s.db.ExecContext(ctx, `UPDATE webhook_outbox SET `+requeueSet+` WHERE event_id = ? AND status != 'pending'`, now, now, eventID)
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	row, err := s.Get(ctx, eventID)
	if err != nil || row == nil {
		return nil, err
	}
	if n == 0 {
		return nil, ErrAlreadyPending
	}
	return row, nil
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
