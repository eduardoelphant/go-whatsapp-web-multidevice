package usecase

import (
	"context"
	"errors"
	"fmt"
	"testing"

	domainNewsletter "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/newsletter"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"go.mau.fi/whatsmeow"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
)

func TestFindNewsletterMessage(t *testing.T) {
	target := &types.NewsletterMessage{MessageServerID: 8872, MessageID: "target"}

	tests := []struct {
		name     string
		messages []*types.NewsletterMessage
		serverID int
		want     *types.NewsletterMessage
		wantErr  string
	}{
		{
			name:     "finds exact server id",
			messages: []*types.NewsletterMessage{{MessageServerID: 8871}, target},
			serverID: 8872,
			want:     target,
		},
		{
			name:     "rejects empty result",
			serverID: 8872,
			wantErr:  "newsletter message with server ID 8872 not found",
		},
		{
			name:     "rejects mismatched result",
			messages: []*types.NewsletterMessage{{MessageServerID: 8871}},
			serverID: 8872,
			wantErr:  "newsletter message with server ID 8872 not found",
		},
		{
			name:     "ignores nil entries",
			messages: []*types.NewsletterMessage{nil},
			serverID: 8872,
			wantErr:  "newsletter message with server ID 8872 not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := findNewsletterMessage(tt.messages, tt.serverID)
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				assert.Nil(t, got)
				return
			}

			require.NoError(t, err)
			assert.Same(t, tt.want, got)
		})
	}
}

func TestNewsletterDownloadableMedia(t *testing.T) {
	tests := []struct {
		name      string
		message   *waE2E.Message
		mediaType string
		wantErr   string
	}{
		{name: "image", message: &waE2E.Message{ImageMessage: &waE2E.ImageMessage{}}, mediaType: "image"},
		{name: "video", message: &waE2E.Message{VideoMessage: &waE2E.VideoMessage{}}, mediaType: "video"},
		{name: "video note", message: &waE2E.Message{PtvMessage: &waE2E.VideoMessage{}}, mediaType: "video_note"},
		{name: "audio", message: &waE2E.Message{AudioMessage: &waE2E.AudioMessage{}}, mediaType: "audio"},
		{name: "document", message: &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{}}, mediaType: "document"},
		{name: "sticker", message: &waE2E.Message{StickerMessage: &waE2E.StickerMessage{}}, mediaType: "sticker"},
		{name: "nil message", wantErr: "newsletter message does not contain downloadable media"},
		{name: "text only", message: &waE2E.Message{Conversation: stringPointer("hello")}, wantErr: "newsletter message does not contain downloadable media"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			media, mediaType, err := newsletterDownloadableMedia(tt.message)
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				assert.Nil(t, media)
				assert.Empty(t, mediaType)
				return
			}

			require.NoError(t, err)
			assert.NotNil(t, media)
			assert.Equal(t, tt.mediaType, mediaType)
		})
	}
}

func stringPointer(value string) *string {
	return &value
}

type fakeNewsletterAPI struct {
	calls      []string
	info       *types.NewsletterMetadata
	infoErr    error
	created    *types.NewsletterMetadata
	createArgs whatsmeow.CreateNewsletterParams
	tosArgs    []string
}

func (f *fakeNewsletterAPI) GetNewsletterInfo(_ context.Context, _ types.JID) (*types.NewsletterMetadata, error) {
	f.calls = append(f.calls, "info")
	return f.info, f.infoErr
}

func (f *fakeNewsletterAPI) CreateNewsletter(_ context.Context, params whatsmeow.CreateNewsletterParams) (*types.NewsletterMetadata, error) {
	f.calls = append(f.calls, "create")
	f.createArgs = params
	return f.created, nil
}

func (f *fakeNewsletterAPI) AcceptTOSNotice(_ context.Context, noticeID, stage string) error {
	f.calls = append(f.calls, "tos")
	f.tosArgs = []string{noticeID, stage}
	return nil
}

func newTestNewsletterService(api newsletterAPI) serviceNewsletter {
	return serviceNewsletter{apiFromContext: func(context.Context) newsletterAPI { return api }}
}

func sampleNewsletterMetadata() *types.NewsletterMetadata {
	return &types.NewsletterMetadata{
		ID:    types.NewJID("120363000000000001", types.NewsletterServer),
		State: types.WrappedNewsletterState{Type: types.NewsletterStateActive},
		ThreadMeta: types.NewsletterThreadMetadata{
			InviteCode:        "0029VaABC",
			Name:              types.NewsletterText{Text: "Canal"},
			Description:       types.NewsletterText{Text: "Descrição"},
			SubscriberCount:   42,
			VerificationState: types.NewsletterVerificationStateVerified,
			Picture:           &types.ProfilePictureInfo{URL: "https://pps.whatsapp.net/x.jpg"},
		},
		ViewerMeta: &types.NewsletterViewerMetadata{Role: types.NewsletterRoleOwner},
	}
}

func TestNewsletterInfoMapeiaNoveCampos(t *testing.T) {
	api := &fakeNewsletterAPI{info: sampleNewsletterMetadata()}
	got, err := newTestNewsletterService(api).Info(context.Background(), domainNewsletter.InfoRequest{NewsletterID: "120363000000000001@newsletter"})
	require.NoError(t, err)
	assert.Equal(t, domainNewsletter.NewsletterInfoResponse{
		NewsletterID: "120363000000000001@newsletter", Name: "Canal", Description: "Descrição", Invite: "0029VaABC",
		SubscribersCount: 42, State: "active", Role: "owner", Verification: "verified", PictureURL: "https://pps.whatsapp.net/x.jpg",
	}, got)
}

func TestNewsletterInfoSemViewerNemFoto(t *testing.T) {
	meta := sampleNewsletterMetadata()
	meta.ViewerMeta = nil
	meta.ThreadMeta.Picture = nil
	got, err := newTestNewsletterService(&fakeNewsletterAPI{info: meta}).Info(context.Background(), domainNewsletter.InfoRequest{NewsletterID: "1@newsletter"})
	require.NoError(t, err)
	assert.Empty(t, got.Role)
	assert.Empty(t, got.PictureURL)
}

func TestNewsletterInfoCanalInexistenteVira404(t *testing.T) {
	notFound := fmt.Errorf("graphql error: %w", types.GraphQLErrors{{Extensions: types.GraphQLErrorExtensions{ErrorCode: 404}}})
	cases := map[string]*fakeNewsletterAPI{
		"iq item-not-found": {infoErr: fmt.Errorf("iq: %w", whatsmeow.ErrIQNotFound)},
		"graphql 404":       {infoErr: notFound},
		"metadado ausente":  {},
	}
	for name, api := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := newTestNewsletterService(api).Info(context.Background(), domainNewsletter.InfoRequest{NewsletterID: "1@newsletter"})
			require.ErrorIs(t, err, pkgError.ErrNewsletterNotFound)
			assert.Equal(t, 404, pkgError.ErrNewsletterNotFound.StatusCode())
			assert.Equal(t, "NOT_FOUND", pkgError.ErrNewsletterNotFound.ErrCode())
		})
	}
}

func TestNewsletterInfoOutroErroNaoViraNotFound(t *testing.T) {
	api := &fakeNewsletterAPI{infoErr: errors.New("boom")}
	_, err := newTestNewsletterService(api).Info(context.Background(), domainNewsletter.InfoRequest{NewsletterID: "1@newsletter"})
	require.EqualError(t, err, "boom")
}

func TestNewsletterInfoSemCliente(t *testing.T) {
	_, err := newTestNewsletterService(nil).Info(context.Background(), domainNewsletter.InfoRequest{NewsletterID: "1@newsletter"})
	require.ErrorIs(t, err, pkgError.ErrWaCLI)
}

func TestNewsletterCreateAceitaTermoAntesERepassaDados(t *testing.T) {
	api := &fakeNewsletterAPI{created: sampleNewsletterMetadata()}
	picture := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, make([]byte, 64)...)
	got, err := newTestNewsletterService(api).Create(context.Background(), domainNewsletter.CreateRequest{
		Name: "Canal", Description: "Descrição", Picture: picture,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"tos", "create"}, api.calls)
	assert.Equal(t, []string{"20601218", "5"}, api.tosArgs)
	assert.Equal(t, "Canal", api.createArgs.Name)
	assert.Equal(t, "Descrição", api.createArgs.Description)
	assert.Equal(t, picture, api.createArgs.Picture)
	assert.Equal(t, "120363000000000001@newsletter", got.NewsletterID)
}

func TestNewsletterCreateInvalidoNaoChamaOCliente(t *testing.T) {
	api := &fakeNewsletterAPI{created: sampleNewsletterMetadata()}
	_, err := newTestNewsletterService(api).Create(context.Background(), domainNewsletter.CreateRequest{})
	require.Error(t, err)
	assert.Empty(t, api.calls)
}
