package validations

import (
	"context"
	"errors"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainNewsletter "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/newsletter"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	validation "github.com/go-ozzo/ozzo-validation/v4"
)

func ValidateUnfollowNewsletter(ctx context.Context, request domainNewsletter.UnfollowRequest) error {
	err := validation.ValidateStructWithContext(ctx, &request,
		validation.Field(&request.NewsletterID, validation.Required),
	)

	if err != nil {
		return pkgError.ValidationError(err.Error())
	}

	return nil
}

func ValidateGetNewsletterMessages(ctx context.Context, request *domainNewsletter.GetMessagesRequest) error {
	// Set default count if not provided
	if request.Count == 0 {
		request.Count = 50
	}

	err := validation.ValidateStructWithContext(ctx, request,
		validation.Field(&request.NewsletterID, validation.Required, validation.By(validateNewsletterJIDShape)),
		validation.Field(&request.Count, validation.Min(1), validation.Max(100)),
		validation.Field(&request.Before, validation.Min(0)),
	)

	if err != nil {
		return pkgError.ValidationError(err.Error())
	}

	return nil
}

func ValidateDownloadNewsletterMedia(ctx context.Context, request domainNewsletter.DownloadMediaRequest) error {
	err := validation.ValidateStructWithContext(ctx, &request,
		validation.Field(&request.NewsletterID, validation.Required, validation.By(validateNewsletterJIDShape)),
		validation.Field(&request.ServerID, validation.By(validatePositiveServerID)),
	)

	if err != nil {
		return pkgError.ValidationError(err.Error())
	}

	return nil
}

func validatePositiveServerID(value any) error {
	serverID, ok := value.(int)
	if !ok || serverID < 1 {
		return errors.New("must be no less than 1")
	}
	if serverID == math.MaxInt {
		return errors.New("must be less than the maximum integer value")
	}

	return nil
}

// validateNewsletterJIDShape rejects newsletter IDs that are not shaped like a
// newsletter JID (e.g. a phone/group JID), so mistargeted requests fail fast
// with a clear validation error instead of a WhatsApp protocol round-trip.
func validateNewsletterJIDShape(value any) error {
	id, ok := value.(string)
	if !ok || id == "" {
		// Presence is already enforced by validation.Required; skip format
		// checking on empty/non-string values here.
		return nil
	}
	if !strings.HasSuffix(id, config.WhatsappTypeNewsletter) {
		return pkgError.ValidationError("must end with " + config.WhatsappTypeNewsletter)
	}
	return nil
}

func ValidateNewsletterInfo(ctx context.Context, request domainNewsletter.InfoRequest) error {
	err := validation.ValidateStructWithContext(ctx, &request,
		validation.Field(&request.NewsletterID, validation.Required, validation.By(validateNewsletterJIDShape)),
	)
	if err != nil {
		return pkgError.ValidationError(err.Error())
	}

	return nil
}

func ValidateCreateNewsletter(ctx context.Context, request domainNewsletter.CreateRequest) error {
	err := validation.ValidateStructWithContext(ctx, &request,
		validation.Field(&request.Name, validation.Required, validation.RuneLength(1, 100)),
		validation.Field(&request.Description, validation.RuneLength(0, 1024)),
		validation.Field(&request.Picture, validation.By(validateNewsletterPicture)),
	)
	if err != nil {
		return pkgError.ValidationError(err.Error())
	}

	return nil
}

// validateNewsletterPicture accepts an absent picture or one whose bytes sniff as JPEG.
func validateNewsletterPicture(value any) error {
	picture, ok := value.([]byte)
	if !ok || len(picture) == 0 {
		return nil
	}
	if http.DetectContentType(picture) != "image/jpeg" {
		return errors.New("must be image/jpeg")
	}

	return nil
}

var newsletterMessageIDShape = regexp.MustCompile(`^[0-9A-Za-z]{1,64}$`)

// ValidateSendNewsletter checks a publish request and normalises poll_max_answer
// (0 becomes 1). It does not decode the media: the usecase does that.
func ValidateSendNewsletter(ctx context.Context, request *domainNewsletter.SendRequest) error {
	request.Type = strings.ToLower(strings.TrimSpace(request.Type))
	if request.Type == domainNewsletter.SendTypeDocument {
		return pkgError.TypeUnsupported("type: document is not available in channels")
	}
	if request.Type == domainNewsletter.SendTypePoll && request.PollMaxAnswer == 0 {
		request.PollMaxAnswer = 1
	}

	mediaType := slices.Contains([]string{
		domainNewsletter.SendTypeImage, domainNewsletter.SendTypeVideo, domainNewsletter.SendTypeAudio,
		domainNewsletter.SendTypeSticker,
	}, request.Type)

	err := validation.ValidateStructWithContext(ctx, request,
		validation.Field(&request.NewsletterID, validation.Required, validation.By(validateNewsletterJIDShape)),
		validation.Field(&request.MessageID, validation.Match(newsletterMessageIDShape).Error("must be 1 to 64 letters or digits")),
		validation.Field(&request.Type, validation.Required, validation.In(
			domainNewsletter.SendTypeText, domainNewsletter.SendTypeImage, domainNewsletter.SendTypeVideo,
			domainNewsletter.SendTypeAudio, domainNewsletter.SendTypeSticker,
			domainNewsletter.SendTypePoll,
		)),
		validation.Field(&request.Text, validation.When(request.Type == domainNewsletter.SendTypeText, validation.Required)),
		validation.Field(&request.File, validation.When(mediaType, validation.Required.Error("cannot be blank"))),
		validation.Field(&request.PollQuestion, validation.When(request.Type == domainNewsletter.SendTypePoll, validation.Required)),
		validation.Field(&request.PollOptions, validation.When(request.Type == domainNewsletter.SendTypePoll,
			validation.Length(2, 12), validation.Each(validation.Required))),
		validation.Field(&request.PollMaxAnswer, validation.When(request.Type == domainNewsletter.SendTypePoll,
			validation.Min(1), validation.Max(max(len(request.PollOptions), 1)))),
	)
	if err != nil {
		return pkgError.ValidationError(err.Error())
	}

	return nil
}

// ValidateUpdateNewsletter checks a channel edit: JID, at least one field, and the same
// limits as creation for the fields sent.
func ValidateUpdateNewsletter(ctx context.Context, request domainNewsletter.UpdateRequest) error {
	if request.Name == nil && request.Description == nil && len(request.Picture) == 0 && !request.RemovePicture {
		return pkgError.ValidationError("at least one of name, description, picture or remove_picture is required")
	}
	if len(request.Picture) > 0 && request.RemovePicture {
		return pkgError.ValidationError("picture and remove_picture cannot be sent together")
	}
	err := validation.ValidateStructWithContext(ctx, &request,
		validation.Field(&request.NewsletterID, validation.Required, validation.By(validateNewsletterJIDShape)),
		validation.Field(&request.Picture, validation.By(validateNewsletterPicture)),
	)
	if err == nil && request.Name != nil {
		err = validation.Validate(*request.Name, validation.Required, validation.RuneLength(1, 100))
		if err != nil {
			err = errors.New("name: " + err.Error())
		}
	}
	if err == nil && request.Description != nil {
		err = validation.Validate(*request.Description, validation.RuneLength(0, 1024))
		if err != nil {
			err = errors.New("description: " + err.Error())
		}
	}
	if err != nil {
		return pkgError.ValidationError(err.Error())
	}

	return nil
}

// ValidateEditNewsletter checks an edit request: channel JID, message id and a text.
func ValidateEditNewsletter(ctx context.Context, request domainNewsletter.EditRequest) error {
	err := validation.ValidateStructWithContext(ctx, &request,
		validation.Field(&request.NewsletterID, validation.Required, validation.By(validateNewsletterJIDShape)),
		validation.Field(&request.MessageID, validation.Required, validation.Match(newsletterMessageIDShape).Error("must be 1 to 64 letters or digits")),
		validation.Field(&request.Text, validation.Required),
	)
	if err != nil {
		return pkgError.ValidationError(err.Error())
	}

	return nil
}

// ValidateRevokeNewsletter checks a revoke request: channel JID and message id.
func ValidateRevokeNewsletter(ctx context.Context, request domainNewsletter.RevokeRequest) error {
	err := validation.ValidateStructWithContext(ctx, &request,
		validation.Field(&request.NewsletterID, validation.Required, validation.By(validateNewsletterJIDShape)),
		validation.Field(&request.MessageID, validation.Required, validation.Match(newsletterMessageIDShape).Error("must be 1 to 64 letters or digits")),
	)
	if err != nil {
		return pkgError.ValidationError(err.Error())
	}

	return nil
}
