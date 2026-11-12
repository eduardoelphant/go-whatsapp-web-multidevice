package usecase

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainNewsletter "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/newsletter"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/safego"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/validations"
	"github.com/disintegration/imaging"
	fiberUtils "github.com/gofiber/utils/v2"
	"github.com/sirupsen/logrus"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

var newsletterLinkPattern = regexp.MustCompile(`https?://[^\s]+`)

// Send publishes one post to a channel (fork: elphant). It never goes through
// CheckPermissions or the reach-out guard: neither applies to a channel, and the
// WhatsApp answer (not-authorized, item-not-found) is what the caller needs.
func (service serviceNewsletter) Send(ctx context.Context, request domainNewsletter.SendRequest) (response domainNewsletter.SendResponse, err error) {
	if err = validations.ValidateSendNewsletter(ctx, &request); err != nil {
		return response, err
	}

	api := service.apiFromContext(ctx)
	if api == nil {
		return response, pkgError.ErrWaCLI
	}

	recipient, err := types.ParseJID(request.NewsletterID)
	if err != nil || recipient.Server != types.NewsletterServer {
		return response, pkgError.ValidationError("newsletter_id: invalid JID")
	}

	msg, extra, content, err := service.buildNewsletterMessage(ctx, api, recipient, request)
	if err != nil {
		return response, err
	}
	extra.ID = types.MessageID(request.MessageID)

	sent, err := api.SendMessage(ctx, recipient, msg, extra)
	if err != nil {
		return response, mapNewsletterSendError(err, extra.MediaHandle != "")
	}

	if service.storeSent != nil {
		service.storeSent(ctx, api, sent.ID, recipient, content, sent.Timestamp, msg)
	}

	return domainNewsletter.SendResponse{
		MessageID: sent.ID,
		ServerID:  int(sent.ServerID),
		Timestamp: sent.Timestamp.Format(time.RFC3339),
	}, nil
}

// buildNewsletterMessage returns the message, the send extra (only MediaHandle is
// filled here) and the text stored in the chat history.
func (service serviceNewsletter) buildNewsletterMessage(ctx context.Context, api newsletterAPI, recipient types.JID, request domainNewsletter.SendRequest) (*waE2E.Message, whatsmeow.SendRequestExtra, string, error) {
	var extra whatsmeow.SendRequestExtra

	switch request.Type {
	case domainNewsletter.SendTypeText:
		return service.buildNewsletterText(ctx, api, recipient, request), extra, request.Text, nil

	case domainNewsletter.SendTypePoll:
		msg := api.BuildPollCreation(request.PollQuestion, request.PollOptions, request.PollMaxAnswer)
		return msg, extra, "📊 " + request.PollQuestion, nil

	case domainNewsletter.SendTypeImage:
		width, height, err := newsletterImageSize(request.File)
		if err != nil {
			return nil, extra, "", pkgError.ValidationError("file: not a valid image")
		}
		uploaded, err := uploadNewsletterMedia(ctx, api, whatsmeow.MediaImage, request.File)
		if err != nil {
			return nil, extra, "", err
		}
		extra.MediaHandle = uploaded.Handle
		msg := &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
			Caption:       proto.String(request.Text),
			URL:           proto.String(uploaded.URL),
			DirectPath:    proto.String(uploaded.DirectPath),
			Mimetype:      proto.String(http.DetectContentType(request.File)),
			FileSHA256:    uploaded.FileSHA256,
			FileLength:    proto.Uint64(uploaded.FileLength),
			Width:         proto.Uint32(uint32(width)),
			Height:        proto.Uint32(uint32(height)),
			JPEGThumbnail: newsletterImageThumbnail(request.File),
		}}
		return msg, extra, newsletterContent(request.Text, "🖼️ Image"), nil

	case domainNewsletter.SendTypeVideo:
		uploaded, err := uploadNewsletterMedia(ctx, api, whatsmeow.MediaVideo, request.File)
		if err != nil {
			return nil, extra, "", err
		}
		extra.MediaHandle = uploaded.Handle
		mimeType := http.DetectContentType(request.File)
		if !strings.HasPrefix(mimeType, "video/") {
			mimeType = "video/mp4"
		}
		msg := &waE2E.Message{VideoMessage: &waE2E.VideoMessage{
			Caption:    proto.String(request.Text),
			URL:        proto.String(uploaded.URL),
			DirectPath: proto.String(uploaded.DirectPath),
			Mimetype:   proto.String(mimeType),
			FileSHA256: uploaded.FileSHA256,
			FileLength: proto.Uint64(uploaded.FileLength),
		}}
		return msg, extra, newsletterContent(request.Text, "🎥 Video"), nil

	case domainNewsletter.SendTypeDocument:
		filename := request.Filename
		if filename == "" {
			filename = request.FileName
		}
		uploaded, err := uploadNewsletterMedia(ctx, api, whatsmeow.MediaDocument, request.File)
		if err != nil {
			return nil, extra, "", err
		}
		extra.MediaHandle = uploaded.Handle
		msg := &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{
			Caption:    proto.String(request.Text),
			FileName:   proto.String(filename),
			Title:      proto.String(filename),
			URL:        proto.String(uploaded.URL),
			DirectPath: proto.String(uploaded.DirectPath),
			Mimetype:   proto.String(resolveDocumentMIME(filename, request.File)),
			FileSHA256: uploaded.FileSHA256,
			FileLength: proto.Uint64(uploaded.FileLength),
		}}
		return msg, extra, newsletterContent(request.Text, "📄 "+filename), nil

	case domainNewsletter.SendTypeAudio:
		return service.buildNewsletterAudio(ctx, api, request)

	case domainNewsletter.SendTypeSticker:
		webp, width, height, err := newsletterStickerWebP(ctx, request.File)
		if err != nil {
			return nil, extra, "", err
		}
		uploaded, err := uploadNewsletterMedia(ctx, api, whatsmeow.MediaImage, webp)
		if err != nil {
			return nil, extra, "", err
		}
		extra.MediaHandle = uploaded.Handle
		msg := &waE2E.Message{StickerMessage: &waE2E.StickerMessage{
			URL:        proto.String(uploaded.URL),
			DirectPath: proto.String(uploaded.DirectPath),
			Mimetype:   proto.String("image/webp"),
			FileLength: proto.Uint64(uploaded.FileLength),
			FileSHA256: uploaded.FileSHA256,
			Width:      proto.Uint32(width),
			Height:     proto.Uint32(height),
			IsAnimated: proto.Bool(false),
		}}
		return msg, extra, "🎨 Sticker", nil
	}

	return nil, extra, "", pkgError.ValidationError("type: unsupported")
}

// buildNewsletterText sends plain text, or text with a link preview when asked. A
// failed preview lookup falls back to the plain text, never to an error (D4952).
func (service serviceNewsletter) buildNewsletterText(ctx context.Context, api newsletterAPI, recipient types.JID, request domainNewsletter.SendRequest) *waE2E.Message {
	if request.LinkPreview {
		if link := newsletterLinkPattern.FindString(request.Text); link != "" {
			msg, err := service.linkPreview(ctx, api, recipient, request.Text, link)
			if err == nil && msg != nil {
				return msg
			}
			logrus.Warnf("newsletter link preview failed, sending plain text: %v", err)
		}
	}
	return &waE2E.Message{Conversation: proto.String(request.Text)}
}

// buildNewsletterAudio sends OGG as a voice note (PTT) and any other format as a file.
func (service serviceNewsletter) buildNewsletterAudio(ctx context.Context, api newsletterAPI, request domainNewsletter.SendRequest) (*waE2E.Message, whatsmeow.SendRequestExtra, string, error) {
	var extra whatsmeow.SendRequestExtra

	filename := request.FileName
	mimeType := resolveAudioMIME(filename, request.File)
	isOgg := strings.HasPrefix(mimeType, "audio/ogg") || strings.HasPrefix(mimeType, "application/ogg") || bytes.HasPrefix(request.File, []byte("OggS"))
	if isOgg {
		mimeType = "audio/ogg; codecs=opus"
	}

	var seconds uint32
	var waveform []byte
	if tempPath := writeNewsletterTempAudio(request.File, filename); tempPath != "" {
		defer os.Remove(tempPath)
		seconds = getAudioDuration(tempPath)
		if isOgg {
			waveform = generateWaveform(tempPath, seconds)
		}
	}

	uploaded, err := uploadNewsletterMedia(ctx, api, whatsmeow.MediaAudio, request.File)
	if err != nil {
		return nil, extra, "", err
	}
	extra.MediaHandle = uploaded.Handle
	msg := &waE2E.Message{AudioMessage: &waE2E.AudioMessage{
		URL:        proto.String(uploaded.URL),
		DirectPath: proto.String(uploaded.DirectPath),
		Mimetype:   proto.String(mimeType),
		FileSHA256: uploaded.FileSHA256,
		FileLength: proto.Uint64(uploaded.FileLength),
		PTT:        proto.Bool(isOgg),
		Seconds:    proto.Uint32(seconds),
		Waveform:   waveform,
	}}
	return msg, extra, "🎵 Audio", nil
}

func writeNewsletterTempAudio(data []byte, filename string) string {
	path := filepath.Join(config.PathSendItems, "newsletter_audio_"+fiberUtils.UUIDv4()+filepath.Ext(filename))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return ""
	}
	return path
}

func uploadNewsletterMedia(ctx context.Context, api newsletterAPI, mediaType whatsmeow.MediaType, data []byte) (whatsmeow.UploadResponse, error) {
	uploaded, err := api.UploadNewsletter(ctx, data, mediaType)
	if err != nil {
		return uploaded, mapNewsletterUploadError(err)
	}
	return uploaded, nil
}

func newsletterContent(text, placeholder string) string {
	if strings.TrimSpace(text) != "" {
		return text
	}
	return placeholder
}

func newsletterImageSize(data []byte) (int, int, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0, err
	}
	return cfg.Width, cfg.Height, nil
}

// newsletterImageThumbnail returns a small JPEG preview, or nil when the image cannot be decoded.
func newsletterImageThumbnail(data []byte) []byte {
	decoded, _, err := utils.DecodeImageWithinBudget(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	var buffer bytes.Buffer
	if err = imaging.Encode(&buffer, utils.FitThumbnail(decoded), imaging.JPEG); err != nil {
		return nil
	}
	return buffer.Bytes()
}

// newsletterStickerWebP applies the conversion /send/sticker does for a still image:
// fit in 512 px, PNG, then ffmpeg (or cwebp) to WebP.
func newsletterStickerWebP(ctx context.Context, data []byte) ([]byte, uint32, uint32, error) {
	decoded, _, err := utils.DecodeImageWithinBudget(bytes.NewReader(data))
	if err != nil {
		return nil, 0, 0, pkgError.ValidationError("file: not a valid image")
	}
	decoded = utils.FitBox(decoded, stickerMaxEdge, stickerMaxEdge)

	absBaseDir, err := filepath.Abs(config.PathSendItems)
	if err != nil {
		return nil, 0, 0, pkgError.InternalServerError(fmt.Sprintf("failed to resolve base directory: %v", err))
	}
	if err = os.MkdirAll(absBaseDir, 0o755); err != nil {
		return nil, 0, 0, pkgError.InternalServerError(fmt.Sprintf("failed to create send directory: %v", err))
	}
	id := fiberUtils.UUIDv4()
	pngPath := filepath.Join(absBaseDir, fmt.Sprintf("newsletter_sticker_%s.png", id))
	webpPath := filepath.Join(absBaseDir, fmt.Sprintf("newsletter_sticker_%s.webp", id))
	defer os.Remove(pngPath)
	defer os.Remove(webpPath)

	if err = imaging.Save(decoded, pngPath); err != nil {
		return nil, 0, 0, pkgError.InternalServerError(fmt.Sprintf("failed to save temporary PNG: %v", err))
	}

	convCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	var command *exec.Cmd
	if _, lookErr := exec.LookPath("ffmpeg"); lookErr == nil {
		command = exec.CommandContext(convCtx, "ffmpeg", buildStickerWebPFFmpegArgs(pngPath, webpPath)...)
	} else if _, lookErr := exec.LookPath("cwebp"); lookErr == nil {
		command = exec.CommandContext(convCtx, "cwebp", "-q", "60", "-o", webpPath, pngPath)
	} else {
		return nil, 0, 0, pkgError.InternalServerError("neither ffmpeg nor cwebp is installed for WebP conversion")
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err = command.Run(); err != nil {
		return nil, 0, 0, pkgError.InternalServerError(fmt.Sprintf("failed to convert sticker to WebP: %v, stderr: %s", err, stderr.String()))
	}

	webp, err := os.ReadFile(webpPath)
	if err != nil {
		return nil, 0, 0, pkgError.InternalServerError(fmt.Sprintf("failed to read WebP sticker: %v", err))
	}
	bounds := decoded.Bounds()
	return webp, uint32(bounds.Dx()), uint32(bounds.Dy()), nil
}

// mapNewsletterSendError turns the WhatsApp answer into the codes the CRM reads.
// withMedia says the post carried an upload handle, so a 400 means the media was refused.
func mapNewsletterSendError(err error, withMedia bool) error {
	text := err.Error()
	switch {
	case errors.Is(err, whatsmeow.ErrIQNotAuthorized) || strings.Contains(text, "not-authorized"):
		return pkgError.NotAuthorized(text)
	case errors.Is(err, whatsmeow.ErrIQNotFound) || strings.Contains(text, "item-not-found"):
		return pkgError.ErrNewsletterNotFound
	case withMedia && (errors.Is(err, whatsmeow.ErrIQBadRequest) || strings.Contains(text, "bad-request")):
		return pkgError.MediaRejected(text)
	}
	return err
}

// mapNewsletterUploadError reads a 4xx from the media upload as a refusal of the file.
func mapNewsletterUploadError(err error) error {
	text := err.Error()
	if strings.Contains(text, "upload failed with status code 4") || errors.Is(err, whatsmeow.ErrIQBadRequest) {
		return pkgError.MediaRejected(text)
	}
	return err
}

// storeNewsletterSent keeps the post in the device chat history without blocking the answer.
func (service serviceNewsletter) storeNewsletterSent(ctx context.Context, api newsletterAPI, id string, recipient types.JID, content string, sentAt time.Time, msg *waE2E.Message) {
	client, ok := api.(*whatsmeow.Client)
	if !ok || service.chatStorageRepo == nil {
		return
	}
	sender := whatsapp.OwnSenderJID(client)
	go func() {
		defer safego.Recover("newsletter-send#1")
		storeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		if err := service.chatStorageRepo.StoreSentMessageWithContext(storeCtx, id, sender, recipient.String(), content, sentAt, msg); err != nil {
			logrus.Warnf("Failed to store sent newsletter post %s to %s: %v", id, recipient.String(), err)
		}
	}()
}

// defaultNewsletterLinkPreview builds the preview with the real client.
func defaultNewsletterLinkPreview(ctx context.Context, api newsletterAPI, recipient types.JID, text, link string) (*waE2E.Message, error) {
	client, ok := api.(*whatsmeow.Client)
	if !ok {
		return nil, errors.New("link preview needs the whatsmeow client")
	}
	return buildLinkPreviewMessage(ctx, client, recipient, text, link)
}
