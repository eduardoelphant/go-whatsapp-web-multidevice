package chatstorage

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
)

// Fork (elphant): record of messages CreateMessage skipped for having no text
// or media. It lives in its own table, created on first use, so upstream's
// numbered migrations stay untouched. Rows older than skippedMessageRetention
// are pruned on write, at most once per skippedMessagePruneEvery.

const createSkippedMessagesTable = `CREATE TABLE IF NOT EXISTS elphant_skipped_messages (
	device_id VARCHAR(255) NOT NULL,
	id VARCHAR(255) NOT NULL,
	chat_jid VARCHAR(255) NOT NULL,
	sender VARCHAR(255) NOT NULL DEFAULT '',
	is_from_me BOOLEAN NOT NULL DEFAULT FALSE,
	reason VARCHAR(255) NOT NULL,
	fields TEXT NOT NULL DEFAULT '',
	timestamp TIMESTAMP NOT NULL,
	created_at TIMESTAMP NOT NULL,
	PRIMARY KEY (device_id, id)
)`

const createSkippedMessagesCreatedIndex = `CREATE INDEX IF NOT EXISTS idx_elphant_skipped_messages_created
	ON elphant_skipped_messages (created_at)`

const (
	skippedMessageRetention  = 30 * 24 * time.Hour
	skippedMessagePruneEvery = time.Hour
)

// skippedMessageLastPrune holds the Unix time of the last prune.
var skippedMessageLastPrune atomic.Int64

func (r *SQLiteRepository) ensureSkippedMessagesTable() error {
	if _, err := r.db.Exec(createSkippedMessagesTable); err != nil {
		return err
	}
	_, err := r.db.Exec(createSkippedMessagesCreatedIndex)
	return err
}

// StoreSkippedMessage records a skipped message. A second record for the same
// device and id (WhatsApp redelivery) replaces the first.
func (r *SQLiteRepository) StoreSkippedMessage(skipped *domainChatStorage.SkippedMessage) error {
	if skipped == nil {
		return nil
	}
	if strings.TrimSpace(skipped.DeviceID) == "" || strings.TrimSpace(skipped.ID) == "" {
		return fmt.Errorf("device id and message id are required")
	}
	if err := r.ensureSkippedMessagesTable(); err != nil {
		return err
	}
	now := time.Now()
	skipped.CreatedAt = now
	_, err := r.db.Exec(`INSERT INTO elphant_skipped_messages
			(device_id, id, chat_jid, sender, is_from_me, reason, fields, timestamp, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(device_id, id) DO UPDATE SET chat_jid = excluded.chat_jid, sender = excluded.sender,
			is_from_me = excluded.is_from_me, reason = excluded.reason, fields = excluded.fields,
			timestamp = excluded.timestamp, created_at = excluded.created_at`,
		skipped.DeviceID, skipped.ID, skipped.ChatJID, skipped.Sender, skipped.IsFromMe, skipped.Reason,
		strings.Join(skipped.Fields, ","), skipped.Timestamp, now)
	if err != nil {
		return err
	}
	r.pruneSkippedMessages(now)
	return nil
}

func (r *SQLiteRepository) pruneSkippedMessages(now time.Time) {
	last := skippedMessageLastPrune.Load()
	if now.Unix()-last < int64(skippedMessagePruneEvery/time.Second) {
		return
	}
	if !skippedMessageLastPrune.CompareAndSwap(last, now.Unix()) {
		return
	}
	_, _ = r.db.Exec(`DELETE FROM elphant_skipped_messages WHERE created_at < ?`, now.Add(-skippedMessageRetention))
}

// GetSkippedMessageByDevice returns the skip record for a message id, or nil
// when the message was never skipped (or the record expired).
func (r *SQLiteRepository) GetSkippedMessageByDevice(deviceID, id string) (*domainChatStorage.SkippedMessage, error) {
	if err := r.ensureSkippedMessagesTable(); err != nil {
		return nil, err
	}
	var skipped domainChatStorage.SkippedMessage
	var fields string
	err := r.db.QueryRow(`SELECT device_id, id, chat_jid, sender, is_from_me, reason, fields, timestamp, created_at
		FROM elphant_skipped_messages WHERE device_id = ? AND id = ?`, deviceID, id).Scan(
		&skipped.DeviceID, &skipped.ID, &skipped.ChatJID, &skipped.Sender, &skipped.IsFromMe,
		&skipped.Reason, &fields, &skipped.Timestamp, &skipped.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if fields != "" {
		skipped.Fields = strings.Split(fields, ",")
	}
	return &skipped, nil
}

// deleteSkippedMessagesForDevice removes a device's skip records; called by
// DeleteDeviceData. The table may not exist yet.
func deleteSkippedMessagesForDevice(tx *sql.Tx, deviceID string) error {
	if _, err := tx.Exec(createSkippedMessagesTable); err != nil {
		return err
	}
	_, err := tx.Exec(`DELETE FROM elphant_skipped_messages WHERE device_id = ?`, deviceID)
	return err
}
