// Package lidmap reads the LID to phone pairs whatsmeow keeps in whatsmeow_lid_map. The
// table has no device column: the pairs are shared by every device of the gateway.
package lidmap

import (
	"context"
	"database/sql"
	"fmt"
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
	return &Reader{db: db, postgres: driver == "postgres"}, nil
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
