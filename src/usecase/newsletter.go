package usecase

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	domainNewsletter "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/newsletter"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/validations"
	"github.com/sirupsen/logrus"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
)

// newsletterAPI is the slice of whatsmeow.Client the channel data and creation
// routes use; tests replace it with a fake.
type newsletterAPI interface {
	GetNewsletterInfo(ctx context.Context, jid types.JID) (*types.NewsletterMetadata, error)
	CreateNewsletter(ctx context.Context, params whatsmeow.CreateNewsletterParams) (*types.NewsletterMetadata, error)
	AcceptTOSNotice(ctx context.Context, noticeID, stage string) error
	UploadNewsletter(ctx context.Context, data []byte, appInfo whatsmeow.MediaType) (whatsmeow.UploadResponse, error)
	SendMessage(ctx context.Context, to types.JID, message *waE2E.Message, extra ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error)
	BuildPollCreation(name string, optionNames []string, selectableOptionCount int) *waE2E.Message
	BuildEdit(chat types.JID, id types.MessageID, newContent *waE2E.Message) *waE2E.Message
	BuildRevoke(chat, sender types.JID, id types.MessageID) *waE2E.Message
}

type serviceNewsletter struct {
	// apiFromContext resolves the device client of the request; nil when logged out.
	apiFromContext func(ctx context.Context) newsletterAPI
	// chatStorageRepo keeps published posts in the device chat history; nil skips it.
	chatStorageRepo domainChatStorage.IChatStorageRepository
	// linkPreview builds a text message with a link preview; tests replace it.
	linkPreview func(ctx context.Context, api newsletterAPI, recipient types.JID, text, link string) (*waE2E.Message, error)
	// storeSent records a published post; nil in tests.
	storeSent func(ctx context.Context, api newsletterAPI, id string, recipient types.JID, content string, sentAt time.Time, msg *waE2E.Message)
}

func NewNewsletterService(chatStorageRepo domainChatStorage.IChatStorageRepository) domainNewsletter.INewsletterUsecase {
	service := &serviceNewsletter{
		apiFromContext: func(ctx context.Context) newsletterAPI {
			client := whatsapp.ClientFromContext(ctx)
			if client == nil {
				return nil
			}
			return client
		},
		chatStorageRepo: chatStorageRepo,
		linkPreview:     defaultNewsletterLinkPreview,
	}
	service.storeSent = service.storeNewsletterSent
	return service
}

const (
	newsletterCreateTOSNotice = "20601218"
	newsletterCreateTOSStage  = "5"
)

func (service serviceNewsletter) Info(ctx context.Context, request domainNewsletter.InfoRequest) (response domainNewsletter.NewsletterInfoResponse, err error) {
	if err = validations.ValidateNewsletterInfo(ctx, request); err != nil {
		return response, err
	}

	api := service.apiFromContext(ctx)
	if api == nil {
		return response, pkgError.ErrWaCLI
	}

	JID, err := types.ParseJID(request.NewsletterID)
	if err != nil {
		return response, pkgError.ValidationError("newsletter_id: invalid JID")
	}

	metadata, err := api.GetNewsletterInfo(ctx, JID)
	if err != nil {
		if isNewsletterNotFound(err) {
			return response, pkgError.ErrNewsletterNotFound
		}
		return response, err
	}
	if metadata == nil {
		return response, pkgError.ErrNewsletterNotFound
	}

	return newsletterInfoResponse(metadata), nil
}

func (service serviceNewsletter) Create(ctx context.Context, request domainNewsletter.CreateRequest) (response domainNewsletter.NewsletterInfoResponse, err error) {
	if err = validations.ValidateCreateNewsletter(ctx, request); err != nil {
		return response, err
	}

	api := service.apiFromContext(ctx)
	if api == nil {
		return response, pkgError.ErrWaCLI
	}

	if err = api.AcceptTOSNotice(ctx, newsletterCreateTOSNotice, newsletterCreateTOSStage); err != nil {
		return response, fmt.Errorf("failed to accept newsletter terms: %w", err)
	}

	metadata, err := api.CreateNewsletter(ctx, whatsmeow.CreateNewsletterParams{
		Name:        request.Name,
		Description: request.Description,
		Picture:     request.Picture,
	})
	if err != nil {
		return response, err
	}
	if metadata == nil {
		return response, fmt.Errorf("newsletter creation returned no data")
	}

	return newsletterInfoResponse(metadata), nil
}

// isNewsletterNotFound reads the GraphQL error code 404 WhatsApp answers for an unknown channel.
func isNewsletterNotFound(err error) bool {
	if errors.Is(err, whatsmeow.ErrIQNotFound) {
		return true
	}
	var gqlErrors types.GraphQLErrors
	if errors.As(err, &gqlErrors) {
		for _, gqlErr := range gqlErrors {
			if gqlErr.Extensions.ErrorCode == 404 {
				return true
			}
		}
	}
	return false
}

func newsletterInfoResponse(metadata *types.NewsletterMetadata) domainNewsletter.NewsletterInfoResponse {
	response := domainNewsletter.NewsletterInfoResponse{
		NewsletterID:     metadata.ID.String(),
		Name:             metadata.ThreadMeta.Name.Text,
		Description:      metadata.ThreadMeta.Description.Text,
		Invite:           metadata.ThreadMeta.InviteCode,
		SubscribersCount: metadata.ThreadMeta.SubscriberCount,
		State:            string(metadata.State.Type),
		Verification:     string(metadata.ThreadMeta.VerificationState),
	}
	if metadata.ViewerMeta != nil {
		response.Role = string(metadata.ViewerMeta.Role)
	}
	if metadata.ThreadMeta.Picture != nil {
		response.PictureURL = metadata.ThreadMeta.Picture.URL
	}
	return response
}

func (service serviceNewsletter) Unfollow(ctx context.Context, request domainNewsletter.UnfollowRequest) (err error) {
	if err = validations.ValidateUnfollowNewsletter(ctx, request); err != nil {
		return err
	}

	client := whatsapp.ClientFromContext(ctx)
	if client == nil {
		return pkgError.ErrWaCLI
	}

	JID, err := utils.ValidateJidWithLogin(client, request.NewsletterID)
	if err != nil {
		return err
	}

	return client.UnfollowNewsletter(ctx, JID)
}

func (service serviceNewsletter) GetMessages(ctx context.Context, request domainNewsletter.GetMessagesRequest) (response domainNewsletter.GetMessagesResponse, err error) {
	if err = validations.ValidateGetNewsletterMessages(ctx, &request); err != nil {
		return response, err
	}

	client := whatsapp.ClientFromContext(ctx)
	if client == nil {
		return response, pkgError.ErrWaCLI
	}

	JID, err := utils.ValidateJidWithLogin(client, request.NewsletterID)
	if err != nil {
		return response, err
	}

	params := &whatsmeow.GetNewsletterMessagesParams{
		Count: request.Count,
	}
	if request.Before != 0 {
		params.Before = types.MessageServerID(request.Before)
	}

	messages, err := client.GetNewsletterMessages(ctx, JID, params)
	if err != nil {
		return response, err
	}

	response.Data = make([]domainNewsletter.Message, 0, len(messages))
	for _, msg := range messages {
		response.Data = append(response.Data, newsletterMessageResponse(msg))
	}

	return response, nil
}

func (service serviceNewsletter) DownloadMedia(ctx context.Context, request domainNewsletter.DownloadMediaRequest) (response domainNewsletter.DownloadMediaResponse, err error) {
	if err = validations.ValidateDownloadNewsletterMedia(ctx, request); err != nil {
		return response, err
	}

	client := whatsapp.ClientFromContext(ctx)
	if client == nil {
		return response, pkgError.ErrWaCLI
	}

	JID, err := utils.ValidateJidWithLogin(client, request.NewsletterID)
	if err != nil {
		return response, err
	}

	messages, err := client.GetNewsletterMessages(ctx, JID, &whatsmeow.GetNewsletterMessagesParams{
		Count:  1,
		Before: types.MessageServerID(request.ServerID + 1),
	})
	if err != nil {
		return response, fmt.Errorf("failed to get newsletter message: %w", err)
	}

	message, err := findNewsletterMessage(messages, request.ServerID)
	if err != nil {
		return response, err
	}

	downloadable, mediaType, err := newsletterDownloadableMedia(message.Message)
	if err != nil {
		return response, fmt.Errorf("newsletter message %d does not contain downloadable media", request.ServerID)
	}

	newsletterDir := utils.ExtractPhoneNumber(JID.String())
	dateDir := filepath.Join(config.PathMedia, newsletterDir, message.Timestamp.Format("2006-01-02"))
	if err = os.MkdirAll(dateDir, 0755); err != nil {
		return response, fmt.Errorf("failed to create media directory: %w", err)
	}

	extractedMedia, err := utils.ExtractMedia(ctx, client, dateDir, downloadable)
	if err != nil {
		return response, fmt.Errorf("failed to download newsletter media: %w", err)
	}

	fileInfo, statErr := os.Stat(extractedMedia.MediaPath)
	if statErr != nil {
		logrus.Warnf("Could not get file size for %s: %v", extractedMedia.MediaPath, statErr)
	}

	response.ServerID = request.ServerID
	response.MessageID = string(message.MessageID)
	response.Status = fmt.Sprintf("Media downloaded successfully to %s", extractedMedia.MediaPath)
	response.MediaType = mediaType
	response.MimeType = extractedMedia.MimeType
	response.Filename = filepath.Base(extractedMedia.MediaPath)
	response.FilePath = extractedMedia.MediaPath
	if fileInfo != nil {
		response.FileSize = fileInfo.Size()
	}

	return response, nil
}

func findNewsletterMessage(messages []*types.NewsletterMessage, serverID int) (*types.NewsletterMessage, error) {
	for _, message := range messages {
		if message != nil && int(message.MessageServerID) == serverID {
			return message, nil
		}
	}

	return nil, fmt.Errorf("newsletter message with server ID %d not found", serverID)
}

func newsletterDownloadableMedia(message *waE2E.Message) (whatsmeow.DownloadableMessage, string, error) {
	if message != nil {
		switch {
		case message.GetImageMessage() != nil:
			return message.GetImageMessage(), "image", nil
		case message.GetVideoMessage() != nil:
			return message.GetVideoMessage(), "video", nil
		case message.GetPtvMessage() != nil:
			return message.GetPtvMessage(), "video_note", nil
		case message.GetAudioMessage() != nil:
			return message.GetAudioMessage(), "audio", nil
		case message.GetDocumentMessage() != nil:
			return message.GetDocumentMessage(), "document", nil
		case message.GetStickerMessage() != nil:
			return message.GetStickerMessage(), "sticker", nil
		}
	}

	return nil, "", fmt.Errorf("newsletter message does not contain downloadable media")
}

// newsletterMessageResponse maps one channel post to the REST shape. The first keys are
// the original ones and stay as they were; forwards_count, media_type and caption are
// the fork's additions.
func newsletterMessageResponse(msg *types.NewsletterMessage) domainNewsletter.Message {
	mediaType, caption := newsletterMediaTypeAndCaption(msg.Message)
	return domainNewsletter.Message{
		ServerID:       int(msg.MessageServerID),
		MessageID:      string(msg.MessageID),
		Type:           msg.Type,
		Timestamp:      msg.Timestamp.Format(time.RFC3339),
		ViewsCount:     msg.ViewsCount,
		ReactionCounts: msg.ReactionCounts,
		Text:           utils.ExtractMessageTextFromProto(msg.Message),
		ForwardsCount:  msg.ForwardsCount,
		MediaType:      mediaType,
		Caption:        caption,
	}
}

// newsletterMediaTypeAndCaption names the kind of a media or poll post; the caption is
// only read for image, video and document.
func newsletterMediaTypeAndCaption(message *waE2E.Message) (string, string) {
	switch {
	case message.GetImageMessage() != nil:
		return "image", message.GetImageMessage().GetCaption()
	case message.GetVideoMessage() != nil:
		return "video", message.GetVideoMessage().GetCaption()
	case message.GetDocumentMessage() != nil:
		return "document", message.GetDocumentMessage().GetCaption()
	case message.GetAudioMessage() != nil:
		return "audio", ""
	case message.GetStickerMessage() != nil:
		return "sticker", ""
	case message.GetPollCreationMessage() != nil, message.GetPollCreationMessageV2() != nil, message.GetPollCreationMessageV3() != nil:
		return "poll", ""
	}
	return "", ""
}
