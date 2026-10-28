package utils

import (
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Fork (elphant): chat storage keeps only messages with text or media. The
// rest (protocol messages, albums, encrypted reactions, linked-device
// placeholders, formats GOWA cannot render) used to vanish without a trace,
// so a consumer looking a message up by id could not tell "never arrived"
// from "arrived, but had nothing to show". SkippedMessageReason names what
// such a message was, so storage can record the skip.

// skippedReasonIgnoredFields never describe what a message was: they ride
// along with any content.
var skippedReasonIgnoredFields = map[string]bool{
	"messageContextInfo": true,
}

// PopulatedMessageFields lists the names (never the values) of the top-level
// fields set on msg, in proto field order.
func PopulatedMessageFields(msg *waE2E.Message) []string {
	if msg == nil {
		return nil
	}
	var names []string
	msg.ProtoReflect().Range(func(fd protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
		names = append(names, string(fd.Name()))
		return true
	})
	return names
}

// SkippedMessageReason returns a short, stable label for a message that has
// no text and no media: "protocol:<TYPE>" for protocol messages (for example
// protocol:EPHEMERAL_SETTING), "placeholder:<TYPE>" for messages WhatsApp
// masks on linked devices, "album", "enc_reaction", "sender_key_distribution",
// "field:<protoField>" for any other populated field, and "empty" when the
// message carries nothing at all. The label never includes message values.
func SkippedMessageReason(msg *waE2E.Message) string {
	msg = UnwrapMessage(msg)
	if msg == nil {
		return "empty"
	}
	switch {
	case msg.GetProtocolMessage() != nil:
		return "protocol:" + msg.GetProtocolMessage().GetType().String()
	case msg.GetPlaceholderMessage() != nil:
		return "placeholder:" + msg.GetPlaceholderMessage().GetType().String()
	case msg.GetAlbumMessage() != nil:
		return "album"
	case msg.GetEncReactionMessage() != nil:
		return "enc_reaction"
	}
	fields := SkippedMessageFields(msg)
	for _, field := range fields {
		if field != "senderKeyDistributionMessage" && field != "fastRatchetKeySenderKeyDistributionMessage" {
			return "field:" + field
		}
	}
	if len(fields) > 0 {
		return "sender_key_distribution"
	}
	return "empty"
}

// SkippedMessageFields lists the populated top-level fields of the unwrapped
// message, minus fields that never describe what a message was.
func SkippedMessageFields(msg *waE2E.Message) []string {
	var out []string
	for _, field := range PopulatedMessageFields(UnwrapMessage(msg)) {
		if !skippedReasonIgnoredFields[field] {
			out = append(out, field)
		}
	}
	return out
}

// deviceSyncProtocolTypes are protocol messages one's own devices exchange to
// keep in sync. They never stand for a chat message anyone sees, so they are
// not recorded as skipped (they would only grow the table).
var deviceSyncProtocolTypes = map[waE2E.ProtocolMessage_Type]bool{
	waE2E.ProtocolMessage_EPHEMERAL_SYNC_RESPONSE:                      true,
	waE2E.ProtocolMessage_HISTORY_SYNC_NOTIFICATION:                    true,
	waE2E.ProtocolMessage_APP_STATE_SYNC_KEY_SHARE:                     true,
	waE2E.ProtocolMessage_APP_STATE_SYNC_KEY_REQUEST:                   true,
	waE2E.ProtocolMessage_MSG_FANOUT_BACKFILL_REQUEST:                  true,
	waE2E.ProtocolMessage_INITIAL_SECURITY_NOTIFICATION_SETTING_SYNC:   true,
	waE2E.ProtocolMessage_APP_STATE_FATAL_EXCEPTION_NOTIFICATION:       true,
	waE2E.ProtocolMessage_PEER_DATA_OPERATION_REQUEST_MESSAGE:          true,
	waE2E.ProtocolMessage_PEER_DATA_OPERATION_REQUEST_RESPONSE_MESSAGE: true,
	waE2E.ProtocolMessage_LID_MIGRATION_MAPPING_SYNC:                   true,
	waE2E.ProtocolMessage_SHARED_DEVICE_CONTACT_HASH_KEY_SHARE:         true,
	waE2E.ProtocolMessage_SHARED_DEVICE_CONTACT_HASH_KEY_REQUEST:       true,
}

// ShouldRecordSkippedMessage reports whether a skipped message is worth
// recording: everything except own-device sync traffic.
func ShouldRecordSkippedMessage(msg *waE2E.Message) bool {
	pm := UnwrapMessage(msg).GetProtocolMessage()
	return pm == nil || !deviceSyncProtocolTypes[pm.GetType()]
}
