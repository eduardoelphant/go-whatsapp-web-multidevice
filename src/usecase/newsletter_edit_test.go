package usecase

import (
	"context"
	"testing"
	"time"

	domainNewsletter "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/newsletter"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

const editTestMessageID = "3EB0A1B2C3D4E5F6A7B8C9"

func TestNewsletterEditBuildsEditForTheChannel(t *testing.T) {
	api := &fakeNewsletterAPI{}
	response, err := newTestNewsletterService(api).Edit(context.Background(), domainNewsletter.EditRequest{
		NewsletterID: sendTestJID, MessageID: editTestMessageID, Text: "novo texto",
	})

	require.NoError(t, err)
	assert.Equal(t, editTestMessageID, response.MessageID)
	assert.Equal(t, []string{"build_edit", "send"}, api.calls)
	assert.Equal(t, sendTestJID, api.editChat.String())
	assert.Equal(t, types.MessageID(editTestMessageID), api.editID)
	assert.Equal(t, "novo texto", api.editContent.GetConversation())
	assert.Equal(t, sendTestJID, api.sendTo.String())
	assert.NotNil(t, api.sendMsg.GetEditedMessage())
}

func TestNewsletterEditRefusalBecomesEditRefused(t *testing.T) {
	api := &fakeNewsletterAPI{sendErr: whatsmeow.ErrIQBadRequest}
	_, err := newTestNewsletterService(api).Edit(context.Background(), domainNewsletter.EditRequest{
		NewsletterID: sendTestJID, MessageID: editTestMessageID, Text: "x",
	})

	var coded interface {
		ErrCode() string
		StatusCode() int
	}
	require.ErrorAs(t, err, &coded)
	assert.Equal(t, "EDIT_REFUSED", coded.ErrCode())
	assert.Equal(t, 422, coded.StatusCode())
	assert.Contains(t, err.Error(), "bad-request")
}

func TestNewsletterEditNotAuthorizedStays403(t *testing.T) {
	api := &fakeNewsletterAPI{sendErr: whatsmeow.ErrIQNotAuthorized}
	_, err := newTestNewsletterService(api).Edit(context.Background(), domainNewsletter.EditRequest{
		NewsletterID: sendTestJID, MessageID: editTestMessageID, Text: "x",
	})
	assert.Equal(t, pkgError.NotAuthorized(whatsmeow.ErrIQNotAuthorized.Error()), err)
}

func TestNewsletterEditValidation(t *testing.T) {
	api := &fakeNewsletterAPI{}
	service := newTestNewsletterService(api)
	for name, request := range map[string]domainNewsletter.EditRequest{
		"sem texto":       {NewsletterID: sendTestJID, MessageID: editTestMessageID},
		"id inválido":     {NewsletterID: sendTestJID, MessageID: "a/b", Text: "x"},
		"não é canal":     {NewsletterID: "5511999999999@s.whatsapp.net", MessageID: editTestMessageID, Text: "x"},
		"sem id":          {NewsletterID: sendTestJID, Text: "x"},
		"id muito grande": {NewsletterID: sendTestJID, MessageID: string(make([]byte, 65)), Text: "x"},
	} {
		_, err := service.Edit(context.Background(), request)
		assert.Error(t, err, name)
	}
	assert.Empty(t, api.calls)
}

func TestNewsletterRevokeBuildsRevokeForTheChannel(t *testing.T) {
	api := &fakeNewsletterAPI{}
	response, err := newTestNewsletterService(api).Revoke(context.Background(), domainNewsletter.RevokeRequest{
		NewsletterID: sendTestJID, MessageID: editTestMessageID,
	})

	require.NoError(t, err)
	assert.Equal(t, editTestMessageID, response.MessageID)
	assert.Equal(t, []string{"build_revoke", "send"}, api.calls)
	assert.Equal(t, sendTestJID, api.revokeChat.String())
	assert.True(t, api.revokeSender.IsEmpty())
	assert.Equal(t, types.MessageID(editTestMessageID), api.revokeID)
	assert.Equal(t, sendTestJID, api.sendTo.String())
}

func TestNewsletterRevokeErrors(t *testing.T) {
	api := &fakeNewsletterAPI{sendErr: whatsmeow.ErrIQNotFound}
	_, err := newTestNewsletterService(api).Revoke(context.Background(), domainNewsletter.RevokeRequest{
		NewsletterID: sendTestJID, MessageID: editTestMessageID,
	})
	assert.Equal(t, pkgError.ErrNewsletterNotFound, err)

	_, err = newTestNewsletterService(&fakeNewsletterAPI{}).Revoke(context.Background(), domainNewsletter.RevokeRequest{
		NewsletterID: sendTestJID, MessageID: "",
	})
	assert.Error(t, err)
}

func TestNewsletterMessageResponseNewFields(t *testing.T) {
	at := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	base := types.NewsletterMessage{MessageServerID: 7, MessageID: "ID7", Type: "text", Timestamp: at, ViewsCount: 10, ForwardsCount: 3, ReactionCounts: map[string]int{"👍": 2}}

	cases := map[string]struct {
		message   *waE2E.Message
		mediaType string
		caption   string
		text      string
	}{
		"image":    {&waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("legenda")}}, "image", "legenda", "legenda"},
		"video":    {&waE2E.Message{VideoMessage: &waE2E.VideoMessage{Caption: proto.String("v")}}, "video", "v", "v"},
		"document": {&waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{Caption: proto.String("d")}}, "document", "d", "d"},
		"audio":    {&waE2E.Message{AudioMessage: &waE2E.AudioMessage{}}, "audio", "", ""},
		"sticker":  {&waE2E.Message{StickerMessage: &waE2E.StickerMessage{}}, "sticker", "", ""},
		"poll":     {&waE2E.Message{PollCreationMessage: &waE2E.PollCreationMessage{Name: proto.String("q")}}, "poll", "", ""},
		"text":     {&waE2E.Message{Conversation: proto.String("oi")}, "", "", "oi"},
	}
	for name, c := range cases {
		msg := base
		msg.Message = c.message
		got := newsletterMessageResponse(&msg)
		assert.Equal(t, c.mediaType, got.MediaType, name)
		assert.Equal(t, c.caption, got.Caption, name)
		assert.Equal(t, 3, got.ForwardsCount, name)
		assert.Equal(t, 10, got.ViewsCount, name)
		assert.Equal(t, 7, got.ServerID, name)
		assert.Equal(t, "ID7", got.MessageID, name)
		assert.Equal(t, "2026-11-12T10:00:00Z", got.Timestamp, name)
		assert.Equal(t, map[string]int{"👍": 2}, got.ReactionCounts, name)
		if c.text != "" {
			assert.NotEmpty(t, got.Text, name)
		}
	}
}
