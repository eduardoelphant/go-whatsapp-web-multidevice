// Package lidmap reads the LID to phone pairs whatsmeow keeps in whatsmeow_lid_map. The
// table has no device column: the pairs are shared by every device of the gateway.
package lidmap

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Pair is one mapping, as the user parts stored by whatsmeow (no server suffix).
type Pair struct {
	LID string
	PN  string
}

// Reader is a read-only handle on the store database.
type Reader struct {
	db       *sql.DB
	postgres bool
}

// Open opens a handle with the driver and DSN the whatsmeow store uses.
func Open(driver, dsn string) (*Reader, error) {
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("open lid map: %w", err)
	}
	r := &Reader{db: db, postgres: driver == "postgres"}
	// One connection: the pool stays small next to the store's own, and the read-only pragma set
	// below stays on the connection that serves every query. SQLite: query_only refuses writes.
	// Postgres: the session is set read only the same way.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	readOnly := "PRAGMA query_only = ON"
	if r.postgres {
		readOnly = "SET default_transaction_read_only = on"
	}
	if _, err := db.Exec(readOnly); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open lid map read only: %w", err)
	}
	return r, nil
}

func (r *Reader) Close() error { return r.db.Close() }

// List returns up to limit pairs ordered by lid, starting after the given lid (empty for the
// first page). The keyset on lid stays stable while the table grows.
func (r *Reader) List(ctx context.Context, after string, limit int) ([]Pair, error) {
	query := "SELECT lid, pn FROM whatsmeow_lid_map WHERE lid > ? ORDER BY lid LIMIT ?"
	if r.postgres {
		query = "SELECT lid, pn FROM whatsmeow_lid_map WHERE lid > $1 ORDER BY lid LIMIT $2"
	}
	rows, err := r.db.QueryContext(ctx, query, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list lid map: %w", err)
	}
	defer rows.Close()

	var pairs []Pair
	for rows.Next() {
		var p Pair
		if err := rows.Scan(&p.LID, &p.PN); err != nil {
			return nil, fmt.Errorf("scan lid map: %w", err)
		}
		pairs = append(pairs, p)
	}
	return pairs, rows.Err()
}

// PNsForLIDs returns the phone (user part) of each known LID (user part) in one query. Unknown
// LIDs are absent from the result. It reads the table directly, so a miss is never cached and
// never takes whatsmeow's cache lock.
func (r *Reader) PNsForLIDs(ctx context.Context, lids []string) (map[string]string, error) {
	out := make(map[string]string, len(lids))
	if len(lids) == 0 {
		return out, nil
	}
	unique := make([]string, 0, len(lids))
	seen := make(map[string]bool, len(lids))
	for _, lid := range lids {
		if !seen[lid] {
			seen[lid] = true
			unique = append(unique, lid)
		}
	}

	placeholders := make([]string, len(unique))
	args := make([]any, len(unique))
	for i, lid := range unique {
		if r.postgres {
			placeholders[i] = fmt.Sprintf("$%d", i+1)
		} else {
			placeholders[i] = "?"
		}
		args[i] = lid
	}
	query := "SELECT lid, pn FROM whatsmeow_lid_map WHERE lid IN (" + strings.Join(placeholders, ",") + ")"

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("lookup lid map: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var lid, pn string
		if err := rows.Scan(&lid, &pn); err != nil {
			return nil, fmt.Errorf("scan lid map: %w", err)
		}
		out[lid] = pn
	}
	return out, rows.Err()
}
