package chatstorage

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Fork (elphant): per-device voice calls switch (stage 2b, B7) and call limit (concurrent calls,
// D-22). It lives in its own table, created on first use, so upstream's numbered migrations stay
// untouched.

const createDeviceCallsTable = `CREATE TABLE IF NOT EXISTS elphant_device_calls (
	device_id VARCHAR(255) PRIMARY KEY,
	enabled BOOLEAN NOT NULL,
	max_calls INTEGER,
	updated_at TIMESTAMP NOT NULL
)`

var (
	deviceCallsSchemaMu    sync.Mutex
	deviceCallsSchemaReady = map[*sql.DB]bool{}
)

// ensureDeviceCallsTable creates the table on first use and adds max_calls to a table created
// before the column existed (stage 2b). Existing rows keep max_calls NULL, which means 1.
func (r *SQLiteRepository) ensureDeviceCallsTable() error {
	deviceCallsSchemaMu.Lock()
	defer deviceCallsSchemaMu.Unlock()
	if deviceCallsSchemaReady[r.db] {
		return nil
	}
	if _, err := r.db.Exec(createDeviceCallsTable); err != nil {
		return err
	}
	var columns int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('elphant_device_calls') WHERE name = 'max_calls'`).Scan(&columns); err != nil {
		return err
	}
	if columns == 0 {
		_, err := r.db.Exec(`ALTER TABLE elphant_device_calls ADD COLUMN max_calls INTEGER`)
		if err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column name") {
			return err
		}
	}
	deviceCallsSchemaReady[r.db] = true
	return nil
}

// SetDeviceCallsEnabled stores whether calls are on for a device slot; its call limit is kept.
func (r *SQLiteRepository) SetDeviceCallsEnabled(deviceID string, enabled bool) error {
	return r.SetDeviceCallsConfig(deviceID, enabled, nil)
}

// SetDeviceCallsConfig stores a slot's calls switch and, when maxCalls is not nil, its call
// limit. A nil maxCalls keeps the stored limit (NULL for a new row).
func (r *SQLiteRepository) SetDeviceCallsConfig(deviceID string, enabled bool, maxCalls *int) error {
	if strings.TrimSpace(deviceID) == "" {
		return fmt.Errorf("device id is required")
	}
	if err := r.ensureDeviceCallsTable(); err != nil {
		return err
	}
	var limit any
	if maxCalls != nil {
		limit = *maxCalls
	}
	_, err := r.db.Exec(`INSERT INTO elphant_device_calls (device_id, enabled, max_calls, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(device_id) DO UPDATE SET enabled = excluded.enabled,
			max_calls = COALESCE(excluded.max_calls, elphant_device_calls.max_calls),
			updated_at = excluded.updated_at`, deviceID, enabled, limit, time.Now())
	return err
}

// SeedDeviceCallsEnabled stores a slot's first value; it never overwrites an existing one.
func (r *SQLiteRepository) SeedDeviceCallsEnabled(deviceID string, enabled bool) error {
	if strings.TrimSpace(deviceID) == "" {
		return fmt.Errorf("device id is required")
	}
	if err := r.ensureDeviceCallsTable(); err != nil {
		return err
	}
	_, err := r.db.Exec(`INSERT INTO elphant_device_calls (device_id, enabled, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(device_id) DO NOTHING`, deviceID, enabled, time.Now())
	return err
}

// GetDeviceCallsEnabled reads a device slot's calls switch; found is false when the slot
// has never been set.
func (r *SQLiteRepository) GetDeviceCallsEnabled(deviceID string) (enabled, found bool, err error) {
	if err := r.ensureDeviceCallsTable(); err != nil {
		return false, false, err
	}
	err = r.db.QueryRow(`SELECT enabled FROM elphant_device_calls WHERE device_id = ?`, deviceID).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return enabled, true, nil
}

// GetDeviceCallsMaxCalls reads a device slot's call limit; set is false when the slot has no
// row or its limit was never stored (NULL).
func (r *SQLiteRepository) GetDeviceCallsMaxCalls(deviceID string) (maxCalls int, set bool, err error) {
	if err := r.ensureDeviceCallsTable(); err != nil {
		return 0, false, err
	}
	var v sql.NullInt64
	err = r.db.QueryRow(`SELECT max_calls FROM elphant_device_calls WHERE device_id = ?`, deviceID).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if !v.Valid {
		return 0, false, nil
	}
	return int(v.Int64), true, nil
}
