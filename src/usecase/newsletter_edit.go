package usecase

import (
	"context"
	"errors"
	"strings"

	domainNewsletter "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/newsletter"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/validations"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// Edit replaces the text of a text post with the admin edit
// (fork: elphant). Neither CheckPermissions nor the reach-out guard applies.
func (service serviceNewsletter) Edit(ctx context.Context, request domainNewsletter.EditRequest) (response domainNewsletter.EditResponse, err error) {
	if err = validations.ValidateEditNewsletter(ctx, request); err != nil {
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

	content := &waE2E.Message{Conversation: proto.String(request.Text)}
	msg := api.BuildEdit(recipient, types.MessageID(request.MessageID), content)
	if _, err = api.SendMessage(ctx, recipient, msg); err != nil {
		return response, mapNewsletterEditError(err)
	}

	return domainNewsletter.EditResponse{MessageID: request.MessageID}, nil
}

// Revoke deletes a published post for everyone with the admin revoke (fork: elphant).
// The sender is the empty JID, the form whatsmeow documents for the device's own posts.
func (service serviceNewsletter) Revoke(ctx context.Context, request domainNewsletter.RevokeRequest) (response domainNewsletter.EditResponse, err error) {
	if err = validations.ValidateRevokeNewsletter(ctx, request); err != nil {
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

	msg := api.BuildRevoke(recipient, types.EmptyJID, types.MessageID(request.MessageID))
	if _, err = api.SendMessage(ctx, recipient, msg); err != nil {
		return response, mapNewsletterSendError(err, false)
	}

	return domainNewsletter.EditResponse{MessageID: request.MessageID}, nil
}

// mapNewsletterEditError reads a bad-request or not-acceptable answer to an edit as a
// refusal (for instance the edit window is over), keeping the server text. Other
// answers go through the send mapping.
func mapNewsletterEditError(err error) error {
	text := err.Error()
	if errors.Is(err, whatsmeow.ErrIQBadRequest) || errors.Is(err, whatsmeow.ErrServerReturnedError) || strings.Contains(text, "bad-request") || strings.Contains(text, "not-acceptable") {
		return pkgError.EditRefused(text)
	}
	return mapNewsletterSendError(err, false)
}
