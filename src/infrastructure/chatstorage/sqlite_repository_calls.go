package chatstorage

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Fork (elphant): per-device voice calls switch (stage 2b, B7). It lives in its own table,
// created on first use, so upstream's numbered migrations stay untouched.

const createDeviceCallsTable = `CREATE TABLE IF NOT EXISTS elphant_device_calls (
	device_id VARCHAR(255) PRIMARY KEY,
	enabled BOOLEAN NOT NULL,
	updated_at TIMESTAMP NOT NULL
)`

// SetDeviceCallsEnabled stores whether calls are on for a device slot.
func (r *SQLiteRepository) SetDeviceCallsEnabled(deviceID string, enabled bool) error {
	if strings.TrimSpace(deviceID) == "" {
		return fmt.Errorf("device id is required")
	}
	if _, err := r.db.Exec(createDeviceCallsTable); err != nil {
		return err
	}
	_, err := r.db.Exec(`INSERT INTO elphant_device_calls (device_id, enabled, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(device_id) DO UPDATE SET enabled = excluded.enabled, updated_at = excluded.updated_at`, deviceID, enabled, time.Now())
	return err
}

// SeedDeviceCallsEnabled stores a slot's first value; it never overwrites an existing one.
func (r *SQLiteRepository) SeedDeviceCallsEnabled(deviceID string, enabled bool) error {
	if strings.TrimSpace(deviceID) == "" {
		return fmt.Errorf("device id is required")
	}
	if _, err := r.db.Exec(createDeviceCallsTable); err != nil {
		return err
	}
	_, err := r.db.Exec(`INSERT INTO elphant_device_calls (device_id, enabled, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(device_id) DO NOTHING`, deviceID, enabled, time.Now())
	return err
}

// GetDeviceCallsEnabled reads a device slot's calls switch; found is false when the slot
// has never been set.
func (r *SQLiteRepository) GetDeviceCallsEnabled(deviceID string) (enabled, found bool, err error) {
	if _, err := r.db.Exec(createDeviceCallsTable); err != nil {
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
