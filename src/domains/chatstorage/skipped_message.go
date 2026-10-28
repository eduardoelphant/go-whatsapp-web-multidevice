package chatstorage

import "time"

// Fork (elphant): a message that reached chat storage without text or media
// and was therefore not stored in messages. Only the reason and the proto
// field names are kept, never values, so a consumer can look the id up and
// learn what the message was (for example protocol:EPHEMERAL_SETTING).
type SkippedMessage struct {
	DeviceID  string    `db:"device_id"`
	ID        string    `db:"id"`
	ChatJID   string    `db:"chat_jid"`
	Sender    string    `db:"sender"`
	IsFromMe  bool      `db:"is_from_me"`
	Reason    string    `db:"reason"`
	Fields    []string  `db:"-"`
	Timestamp time.Time `db:"timestamp"`
	CreatedAt time.Time `db:"created_at"`
}
