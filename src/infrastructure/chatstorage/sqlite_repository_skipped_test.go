package chatstorage

import (
	"context"
	"testing"
	"time"

	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/stretchr/testify/require"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func skippedTestEvent(id string, msg *waE2E.Message) *events.Message {
	chat := types.NewJID("5548999990000", types.DefaultUserServer)
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat},
			ID:            id,
			Timestamp:     time.Date(2026, 10, 27, 17, 57, 41, 0, time.UTC),
		},
		Message: msg,
	}
}

func ephemeralSetting() *waE2E.Message {
	return &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_EPHEMERAL_SETTING.Enum(), EphemeralExpiration: proto.Uint32(0),
	}}
}

func TestCreateMessageRecordsSkippedProtocolMessage(t *testing.T) {
	repo := newTestSQLiteRepository(t)
	ctx := whatsapp.ContextWithDevice(context.Background(), whatsapp.NewDeviceInstance("device-a", nil, nil))

	require.NoError(t, repo.CreateMessage(ctx, skippedTestEvent("FE599AAD8E1162D719", ephemeralSetting())))

	stored, err := repo.GetMessageByIDAndDevice("device-a", "FE599AAD8E1162D719")
	require.NoError(t, err)
	require.Nil(t, stored, "a message without text or media stays out of messages")

	skipped, err := repo.GetSkippedMessageByDevice("device-a", "FE599AAD8E1162D719")
	require.NoError(t, err)
	require.NotNil(t, skipped)
	require.Equal(t, "protocol:EPHEMERAL_SETTING", skipped.Reason)
	require.Equal(t, []string{"protocolMessage"}, skipped.Fields)
	require.Equal(t, "5548999990000@s.whatsapp.net", skipped.ChatJID)
	require.False(t, skipped.IsFromMe)
	require.True(t, skipped.Timestamp.Equal(time.Date(2026, 10, 27, 17, 57, 41, 0, time.UTC)))

	other, err := repo.GetSkippedMessageByDevice("device-b", "FE599AAD8E1162D719")
	require.NoError(t, err)
	require.Nil(t, other, "skip records are device scoped")
}

func TestCreateMessageDoesNotRecordStoredOrDeviceSyncMessages(t *testing.T) {
	repo := newTestSQLiteRepository(t)
	ctx := whatsapp.ContextWithDevice(context.Background(), whatsapp.NewDeviceInstance("device-a", nil, nil))

	require.NoError(t, repo.CreateMessage(ctx, skippedTestEvent("TEXT1", &waE2E.Message{Conversation: proto.String("oi")})))
	require.NoError(t, repo.CreateMessage(ctx, skippedTestEvent("SYNC1", &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_APP_STATE_SYNC_KEY_SHARE.Enum(),
	}})))

	for _, id := range []string{"TEXT1", "SYNC1"} {
		skipped, err := repo.GetSkippedMessageByDevice("device-a", id)
		require.NoError(t, err)
		require.Nil(t, skipped, id)
	}
	stored, err := repo.GetMessageByIDAndDevice("device-a", "TEXT1")
	require.NoError(t, err)
	require.NotNil(t, stored)
}

func TestSkippedMessageUpsertAndDeviceCleanup(t *testing.T) {
	repo := newTestSQLiteRepository(t)
	record := func(device, reason string) {
		require.NoError(t, repo.StoreSkippedMessage(&domainChatStorage.SkippedMessage{
			DeviceID: device, ID: "M1", ChatJID: "chat@s.whatsapp.net", Reason: reason, Timestamp: time.Now(),
		}))
	}
	record("device-a", "album")
	record("device-a", "protocol:EPHEMERAL_SETTING")
	record("device-b", "album")

	got, err := repo.GetSkippedMessageByDevice("device-a", "M1")
	require.NoError(t, err)
	require.Equal(t, "protocol:EPHEMERAL_SETTING", got.Reason, "redelivery replaces the record")
	require.Empty(t, got.Fields)

	require.NoError(t, repo.DeleteDeviceData("device-a"))
	got, err = repo.GetSkippedMessageByDevice("device-a", "M1")
	require.NoError(t, err)
	require.Nil(t, got)
	got, err = repo.GetSkippedMessageByDevice("device-b", "M1")
	require.NoError(t, err)
	require.NotNil(t, got)

	require.NoError(t, repo.TruncateAllChats())
	got, err = repo.GetSkippedMessageByDevice("device-b", "M1")
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestSkippedMessagePruneDropsExpiredRows(t *testing.T) {
	repo := newTestSQLiteRepository(t)
	require.NoError(t, repo.StoreSkippedMessage(&domainChatStorage.SkippedMessage{
		DeviceID: "device-a", ID: "OLD", ChatJID: "chat@s.whatsapp.net", Reason: "album", Timestamp: time.Now(),
	}))
	_, err := repo.db.Exec(`UPDATE elphant_skipped_messages SET created_at = ? WHERE id = 'OLD'`, time.Now().Add(-31*24*time.Hour))
	require.NoError(t, err)

	skippedMessageLastPrune.Store(0)
	require.NoError(t, repo.StoreSkippedMessage(&domainChatStorage.SkippedMessage{
		DeviceID: "device-a", ID: "NEW", ChatJID: "chat@s.whatsapp.net", Reason: "album", Timestamp: time.Now(),
	}))

	old, err := repo.GetSkippedMessageByDevice("device-a", "OLD")
	require.NoError(t, err)
	require.Nil(t, old)
	fresh, err := repo.GetSkippedMessageByDevice("device-a", "NEW")
	require.NoError(t, err)
	require.NotNil(t, fresh)
}
