package usecase

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainNewsletter "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/newsletter"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

func (f *fakeNewsletterAPI) SendMexIQ(_ context.Context, queryID string, variables any) (json.RawMessage, error) {
	f.calls = append(f.calls, "mex")
	f.mexQueryID = queryID
	f.mexVars = variables
	return f.mexData, f.mexErr
}

const updateTestJID = "120363000000000001@newsletter"

func strPtr(s string) *string { return &s }

func TestBuildNewsletterUpdateVariables(t *testing.T) {
	cases := map[string]struct {
		name, description *string
		picture           []byte
		remove            bool
		updates           map[string]any
	}{
		"só nome":         {name: strPtr("Novo"), updates: map[string]any{"name": "Novo", "settings": nil}},
		"só descrição":    {description: strPtr("D"), updates: map[string]any{"description": "D", "settings": nil}},
		"descrição vazia": {description: strPtr(""), updates: map[string]any{"description": "", "settings": nil}},
		"foto em base64":  {picture: []byte{0xff, 0xd8, 0xff}, updates: map[string]any{"picture": "/9j/", "settings": nil}},
		"remover foto":    {remove: true, updates: map[string]any{"picture": "", "settings": nil}},
		"todos": {name: strPtr("N"), description: strPtr("D"), picture: []byte{0xff, 0xd8, 0xff},
			updates: map[string]any{"name": "N", "description": "D", "picture": "/9j/", "settings": nil}},
	}
	for label, tc := range cases {
		t.Run(label, func(t *testing.T) {
			got := buildNewsletterUpdateVariables(updateTestJID, tc.name, tc.description, tc.picture, tc.remove)
			assert.Equal(t, map[string]any{"newsletter_id": updateTestJID, "updates": tc.updates}, got)
			assert.Contains(t, got["updates"], "settings")
		})
	}
}

func TestNewsletterUpdateQueryID(t *testing.T) {
	previous := config.WhatsappNewsletterUpdateQueryID
	t.Cleanup(func() { config.WhatsappNewsletterUpdateQueryID = previous })

	config.WhatsappNewsletterUpdateQueryID = ""
	assert.Equal(t, "7150902998257522", newsletterUpdateQueryID())
	config.WhatsappNewsletterUpdateQueryID = "24250201037901610"
	assert.Equal(t, "24250201037901610", newsletterUpdateQueryID())
}

const updateFullAnswer = `{"xwa2_newsletter_update":{"id":"120363000000000001@newsletter","state":{"type":"ACTIVE"},` +
	`"thread_metadata":{"name":{"text":"Novo"},"description":{"text":"Desc"},"invite":"0029VaABC","subscribers_count":"42",` +
	`"verification":"VERIFIED","picture":{"direct_path":"/v/x.jpg","id":"1","type":"IMAGE"}}}}`

func TestNewsletterUpdateUsesAnswerOfMutation(t *testing.T) {
	previous := config.WhatsappNewsletterUpdateQueryID
	config.WhatsappNewsletterUpdateQueryID = ""
	t.Cleanup(func() { config.WhatsappNewsletterUpdateQueryID = previous })

	api := &fakeNewsletterAPI{mexData: json.RawMessage(updateFullAnswer)}
	got, err := newTestNewsletterService(api).Update(context.Background(), domainNewsletter.UpdateRequest{
		NewsletterID: updateTestJID, Name: strPtr("Novo"),
	})

	require.NoError(t, err)
	assert.Equal(t, []string{"mex"}, api.calls)
	assert.Equal(t, "7150902998257522", api.mexQueryID)
	assert.Equal(t, map[string]any{"newsletter_id": updateTestJID, "updates": map[string]any{"name": "Novo", "settings": nil}}, api.mexVars)
	assert.Equal(t, updateTestJID, got.NewsletterID)
	assert.Equal(t, "Novo", got.Name)
	assert.Equal(t, "Desc", got.Description)
	assert.False(t, got.PictureChecked)
	assert.Nil(t, got.SubscribersCount)
	out, _ := json.Marshal(got)
	assert.NotContains(t, string(out), "subscribers_count")
	assert.NotContains(t, string(out), "picture_checked")
}

func TestNewsletterUpdateRemovePictureRereadsAndFlagsChecked(t *testing.T) {
	meta := sampleNewsletterMetadata()
	meta.ThreadMeta.Picture = nil
	api := &fakeNewsletterAPI{mexData: json.RawMessage(updateFullAnswer), info: meta}
	got, err := newTestNewsletterService(api).Update(context.Background(), domainNewsletter.UpdateRequest{
		NewsletterID: updateTestJID, RemovePicture: true,
	})

	require.NoError(t, err)
	assert.Equal(t, []string{"mex", "info"}, api.calls)
	assert.True(t, got.PictureChecked)
	assert.Empty(t, got.PictureID)
	assert.Empty(t, got.PictureURL)
	assert.Nil(t, got.SubscribersCount)
}

func TestNewsletterUpdateNewPictureRereadsAndReturnsPictureID(t *testing.T) {
	meta := sampleNewsletterMetadata()
	meta.ThreadMeta.Picture = &types.ProfilePictureInfo{ID: "pic-9", URL: "https://pps.whatsapp.net/x.jpg"}
	api := &fakeNewsletterAPI{mexData: json.RawMessage(updateFullAnswer), info: meta}
	got, err := newTestNewsletterService(api).Update(context.Background(), domainNewsletter.UpdateRequest{
		NewsletterID: updateTestJID, Picture: []byte{0xff, 0xd8, 0xff, 0xe0},
	})

	require.NoError(t, err)
	assert.Equal(t, []string{"mex", "info"}, api.calls)
	assert.True(t, got.PictureChecked)
	assert.Equal(t, "pic-9", got.PictureID)
	out, _ := json.Marshal(got)
	assert.NotContains(t, string(out), "subscribers_count")
}

func TestNewsletterUpdateCompletesWithInfoWhenPictureIsMissing(t *testing.T) {
	answer := `{"xwa2_newsletter_update":{"id":"120363000000000001@newsletter","thread_metadata":{"name":{"text":"Novo"}}}}`
	api := &fakeNewsletterAPI{mexData: json.RawMessage(answer), info: sampleNewsletterMetadata()}
	got, err := newTestNewsletterService(api).Update(context.Background(), domainNewsletter.UpdateRequest{
		NewsletterID: updateTestJID, Picture: []byte{0xff, 0xd8, 0xff, 0xe0},
	})

	require.NoError(t, err)
	assert.Equal(t, []string{"mex", "info"}, api.calls)
	assert.Equal(t, "https://pps.whatsapp.net/x.jpg", got.PictureURL)
}

func TestNewsletterUpdateCompletesWithInfoWhenAnswerIsEmpty(t *testing.T) {
	api := &fakeNewsletterAPI{mexData: json.RawMessage(`{}`), info: sampleNewsletterMetadata()}
	got, err := newTestNewsletterService(api).Update(context.Background(), domainNewsletter.UpdateRequest{
		NewsletterID: updateTestJID, RemovePicture: true,
	})

	require.NoError(t, err)
	assert.Equal(t, []string{"mex", "info"}, api.calls)
	assert.Equal(t, "Canal", got.Name)
}

func TestNewsletterUpdateGraphQLErrors(t *testing.T) {
	gql := func(code int, text string) error {
		return fmt.Errorf("graphql error: %w", types.GraphQLErrors{{Message: text, Extensions: types.GraphQLErrorExtensions{ErrorCode: code}}})
	}
	cases := map[string]struct {
		err      error
		wantCode string
		wantHTTP int
	}{
		"sem papel":     {gql(403, "forbidden"), "NOT_AUTHORIZED", 403},
		"canal ausente": {gql(404, "not found"), "NOT_FOUND", 404},
		"recusa":        {gql(400, "name too long"), "UPDATE_REFUSED", 422},
	}
	for label, tc := range cases {
		t.Run(label, func(t *testing.T) {
			api := &fakeNewsletterAPI{mexErr: tc.err}
			_, err := newTestNewsletterService(api).Update(context.Background(), domainNewsletter.UpdateRequest{
				NewsletterID: updateTestJID, Name: strPtr("x"),
			})
			var coded interface {
				ErrCode() string
				StatusCode() int
			}
			require.ErrorAs(t, err, &coded)
			assert.Equal(t, tc.wantCode, coded.ErrCode())
			assert.Equal(t, tc.wantHTTP, coded.StatusCode())
		})
	}

	_, err := newTestNewsletterService(&fakeNewsletterAPI{mexErr: gql(400, "name too long")}).Update(context.Background(),
		domainNewsletter.UpdateRequest{NewsletterID: updateTestJID, Name: strPtr("x")})
	assert.Contains(t, err.Error(), "name too long")
}

func TestNewsletterUpdateTransportErrorsUseSendMapping(t *testing.T) {
	api := &fakeNewsletterAPI{mexErr: whatsmeow.ErrIQNotAuthorized}
	_, err := newTestNewsletterService(api).Update(context.Background(), domainNewsletter.UpdateRequest{NewsletterID: updateTestJID, Name: strPtr("x")})
	assert.Equal(t, pkgError.NotAuthorized(whatsmeow.ErrIQNotAuthorized.Error()), err)
}

func TestNewsletterUpdateValidation(t *testing.T) {
	api := &fakeNewsletterAPI{}
	service := newTestNewsletterService(api)
	for name, request := range map[string]domainNewsletter.UpdateRequest{
		"nada enviado":     {NewsletterID: updateTestJID},
		"nome vazio":       {NewsletterID: updateTestJID, Name: strPtr("")},
		"nome grande":      {NewsletterID: updateTestJID, Name: strPtr(string(make([]rune, 101)))},
		"descrição grande": {NewsletterID: updateTestJID, Description: strPtr(string(make([]byte, 1025)))},
		"foto não é JPEG":  {NewsletterID: updateTestJID, Picture: []byte("GIF89a....")},
		"foto e remover":   {NewsletterID: updateTestJID, Picture: []byte{0xff, 0xd8, 0xff, 0xe0}, RemovePicture: true},
		"sem canal":        {Name: strPtr("x")},
		"não é canal":      {NewsletterID: "5511999999999@s.whatsapp.net", Name: strPtr("x")},
	} {
		_, err := service.Update(context.Background(), request)
		assert.Error(t, err, name)
	}
	assert.Empty(t, api.calls)
}
