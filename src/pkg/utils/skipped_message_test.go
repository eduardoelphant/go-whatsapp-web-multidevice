package utils

import (
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func protocolOfType(typ waE2E.ProtocolMessage_Type) *waE2E.Message {
	return &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{Type: typ.Enum(), EphemeralExpiration: proto.Uint32(0)}}
}

func TestSkippedMessageReason(t *testing.T) {
	cases := []struct {
		name   string
		msg    *waE2E.Message
		reason string
		fields []string
	}{
		{"nil", nil, "empty", nil},
		{"no fields", &waE2E.Message{}, "empty", nil},
		{"only context info", &waE2E.Message{MessageContextInfo: &waE2E.MessageContextInfo{}}, "empty", nil},
		{"ephemeral setting from a hosted business", protocolOfType(waE2E.ProtocolMessage_EPHEMERAL_SETTING),
			"protocol:EPHEMERAL_SETTING", []string{"protocolMessage"}},
		{"protocol inside an ephemeral wrapper",
			&waE2E.Message{EphemeralMessage: &waE2E.FutureProofMessage{Message: protocolOfType(waE2E.ProtocolMessage_EPHEMERAL_SETTING)}},
			"protocol:EPHEMERAL_SETTING", []string{"protocolMessage"}},
		{"placeholder masked on linked devices",
			&waE2E.Message{PlaceholderMessage: &waE2E.PlaceholderMessage{Type: waE2E.PlaceholderMessage_MASK_LINKED_DEVICES.Enum()}},
			"placeholder:MASK_LINKED_DEVICES", []string{"placeholderMessage"}},
		{"album header", &waE2E.Message{AlbumMessage: &waE2E.AlbumMessage{}, MessageContextInfo: &waE2E.MessageContextInfo{}},
			"album", []string{"albumMessage"}},
		{"encrypted reaction", &waE2E.Message{EncReactionMessage: &waE2E.EncReactionMessage{}}, "enc_reaction", []string{"encReactionMessage"}},
		{"sender key only", &waE2E.Message{SenderKeyDistributionMessage: &waE2E.SenderKeyDistributionMessage{}},
			"sender_key_distribution", []string{"senderKeyDistributionMessage"}},
		{"format GOWA does not render", &waE2E.Message{
			SenderKeyDistributionMessage: &waE2E.SenderKeyDistributionMessage{},
			EventMessage:                 &waE2E.EventMessage{},
		}, "field:eventMessage", []string{"senderKeyDistributionMessage", "eventMessage"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SkippedMessageReason(tc.msg); got != tc.reason {
				t.Fatalf("reason = %q, want %q", got, tc.reason)
			}
			got := SkippedMessageFields(tc.msg)
			if len(got) != len(tc.fields) {
				t.Fatalf("fields = %v, want %v", got, tc.fields)
			}
			for i := range got {
				if got[i] != tc.fields[i] {
					t.Fatalf("fields = %v, want %v", got, tc.fields)
				}
			}
		})
	}
}

func TestShouldRecordSkippedMessage(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  *waE2E.Message
		want bool
	}{
		{"ephemeral setting", protocolOfType(waE2E.ProtocolMessage_EPHEMERAL_SETTING), true},
		{"cloud api thread control", protocolOfType(waE2E.ProtocolMessage_CLOUD_API_THREAD_CONTROL_NOTIFICATION), true},
		{"history sync notification", protocolOfType(waE2E.ProtocolMessage_HISTORY_SYNC_NOTIFICATION), false},
		{"app state key share", protocolOfType(waE2E.ProtocolMessage_APP_STATE_SYNC_KEY_SHARE), false},
		{"peer data response", protocolOfType(waE2E.ProtocolMessage_PEER_DATA_OPERATION_REQUEST_RESPONSE_MESSAGE), false},
		{"album", &waE2E.Message{AlbumMessage: &waE2E.AlbumMessage{}}, true},
		{"empty", &waE2E.Message{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShouldRecordSkippedMessage(tc.msg); got != tc.want {
				t.Fatalf("ShouldRecordSkippedMessage = %v, want %v", got, tc.want)
			}
		})
	}
}
