package whatsapp

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func captureGroupWebhooks(t *testing.T) *[]map[string]any {
	t.Helper()
	var got []map[string]any
	orig := forwardGroupWebhook
	forwardGroupWebhook = func(_ context.Context, body map[string]any, _ string) error {
		got = append(got, body)
		return nil
	}
	t.Cleanup(func() { forwardGroupWebhook = orig })
	return &got
}

func TestForwardGroupInfoToWebhookSettings(t *testing.T) {
	got := captureGroupWebhooks(t)
	sender := types.NewJID("5511999990000", types.DefaultUserServer)
	link := "abc"
	evt := &events.GroupInfo{
		JID:                    types.NewJID("120363000000000001", types.GroupServer),
		Sender:                 &sender,
		Timestamp:              time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC),
		Name:                   &types.GroupName{Name: "Novo"},
		Announce:               &types.GroupAnnounce{IsAnnounce: true},
		Ephemeral:              &types.GroupEphemeral{IsEphemeral: false},
		MembershipApprovalMode: &types.GroupMembershipApprovalMode{IsJoinApprovalRequired: true},
		NewInviteLink:          &link,
	}

	require.NoError(t, forwardGroupInfoToWebhook(context.Background(), evt, "dev", nil))
	require.Len(t, *got, 5)

	types_ := []string{}
	for _, b := range *got {
		assert.Equal(t, "group.info", b["event"])
		p := b["payload"].(map[string]any)
		assert.Equal(t, "120363000000000001@g.us", p["chat_id"])
		assert.Equal(t, "5511999990000@s.whatsapp.net", p["sender"])
		types_ = append(types_, p["type"].(string))
		switch p["type"] {
		case "name":
			assert.Equal(t, "Novo", p["value"])
		case "announce":
			assert.Equal(t, true, p["value"])
		case "ephemeral":
			assert.Equal(t, 0, p["value"])
		case "approval":
			assert.Equal(t, true, p["value"])
		case "invite_link_reset":
			assert.Nil(t, p["value"])
		}
	}
	assert.Equal(t, []string{"name", "announce", "ephemeral", "approval", "invite_link_reset"}, types_)
}

func TestForwardGroupInfoToWebhookOnlyJoinHasNoGroupInfo(t *testing.T) {
	got := captureGroupWebhooks(t)
	evt := &events.GroupInfo{
		JID:  types.NewJID("120363000000000001", types.GroupServer),
		Join: []types.JID{types.NewJID("5511999990000", types.DefaultUserServer)},
	}
	require.NoError(t, forwardGroupInfoToWebhook(context.Background(), evt, "dev", nil))
	for _, b := range *got {
		assert.NotEqual(t, "group.info", b["event"])
	}
	require.Len(t, *got, 1)
	assert.Equal(t, "group.participants", (*got)[0]["event"])
}
