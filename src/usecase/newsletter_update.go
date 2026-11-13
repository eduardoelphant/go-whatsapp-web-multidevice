package usecase

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainNewsletter "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/newsletter"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/validations"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// mutationUpdateNewsletterQueryID is the GraphQL query id whatsmeow uses for the channel
// update mutation (mutationUpdateNewsletter, newsletter.go:123 of go.mau.fi/whatsmeow,
// not exported). The library swaps it for the desktop id when needed (convertQueryID).
const mutationUpdateNewsletterQueryID = "7150902998257522"

// newsletterClient adds the SendMexIQ call, which whatsmeow only exposes through
// DangerousInternals, to the real client so it satisfies newsletterAPI.
type newsletterClient struct {
	*whatsmeow.Client
}

func (c newsletterClient) SendMexIQ(ctx context.Context, queryID string, variables any) (json.RawMessage, error) {
	return c.Client.DangerousInternals().SendMexIQ(ctx, queryID, variables)
}

// realNewsletterClient returns the whatsmeow client behind the seam; false for a fake.
func realNewsletterClient(api newsletterAPI) (*whatsmeow.Client, bool) {
	wrapped, ok := api.(newsletterClient)
	if !ok {
		return nil, false
	}
	return wrapped.Client, true
}

// newsletterUpdateQueryID is the env id (WHATSAPP_NEWSLETTER_UPDATE_QUERY_ID) when set,
// otherwise the whatsmeow one.
func newsletterUpdateQueryID() string {
	if id := strings.TrimSpace(config.WhatsappNewsletterUpdateQueryID); id != "" {
		return id
	}
	return mutationUpdateNewsletterQueryID
}

// buildNewsletterUpdateVariables builds the mutation variables in the Baileys format:
// only the fields sent go into updates, settings is always present and null, the picture
// is base64 and removing it writes an empty picture.
func buildNewsletterUpdateVariables(jid string, name, description *string, picture []byte, removePicture bool) map[string]any {
	updates := map[string]any{"settings": nil}
	if name != nil {
		updates["name"] = *name
	}
	if description != nil {
		updates["description"] = *description
	}
	if len(picture) > 0 {
		updates["picture"] = base64.StdEncoding.EncodeToString(picture)
	} else if removePicture {
		updates["picture"] = ""
	}
	return map[string]any{"newsletter_id": jid, "updates": updates}
}

// Update edits name, description and picture of the channel (fork: elphant) and answers
// with the channel as WhatsApp now has it.
func (service serviceNewsletter) Update(ctx context.Context, request domainNewsletter.UpdateRequest) (response domainNewsletter.NewsletterInfoResponse, err error) {
	if err = validations.ValidateUpdateNewsletter(ctx, request); err != nil {
		return response, err
	}

	api := service.apiFromContext(ctx)
	if api == nil {
		return response, pkgError.ErrWaCLI
	}

	jid, err := types.ParseJID(request.NewsletterID)
	if err != nil || jid.Server != types.NewsletterServer {
		return response, pkgError.ValidationError("newsletter_id: invalid JID")
	}

	variables := buildNewsletterUpdateVariables(jid.String(), request.Name, request.Description, request.Picture, request.RemovePicture)
	data, err := api.SendMexIQ(ctx, newsletterUpdateQueryID(), variables)
	if err != nil {
		return response, mapNewsletterUpdateError(err)
	}

	var parsed struct {
		Metadata *types.NewsletterMetadata `json:"xwa2_newsletter_update"`
	}
	if len(data) > 0 {
		if jsonErr := json.Unmarshal(data, &parsed); jsonErr != nil {
			parsed.Metadata = nil
		}
	}

	pictureChecked := false
	if newsletterUpdateIncomplete(parsed.Metadata, request) {
		pictureChecked = len(request.Picture) > 0 || request.RemovePicture
		metadata, infoErr := api.GetNewsletterInfo(ctx, jid)
		if infoErr != nil {
			return response, mapNewsletterUpdateError(infoErr)
		}
		if metadata == nil {
			return response, pkgError.ErrNewsletterNotFound
		}
		parsed.Metadata = metadata
	}

	response = newsletterInfoResponse(parsed.Metadata)
	response.PictureChecked = pictureChecked
	// A mutation answer comes without the subscriber count, so zero would be a lie.
	response.SubscribersCount = nil
	return response, nil
}

// newsletterUpdateIncomplete says the mutation answer lacks something the caller needs: no
// metadata at all, no id or name, or any picture change (the answer never carries the
// picture state, so it is reread).
func newsletterUpdateIncomplete(metadata *types.NewsletterMetadata, request domainNewsletter.UpdateRequest) bool {
	if metadata == nil || metadata.ID.IsEmpty() || metadata.ThreadMeta.Name.Text == "" {
		return true
	}
	return len(request.Picture) > 0 || request.RemovePicture
}

// mapNewsletterUpdateError reads the GraphQL error code: missing role is 403, unknown
// channel 404, any other GraphQL refusal 422 with the server text. Other errors pass
// through the send mapping.
func mapNewsletterUpdateError(err error) error {
	var gqlErrors types.GraphQLErrors
	if errors.As(err, &gqlErrors) && len(gqlErrors) > 0 {
		for _, gqlErr := range gqlErrors {
			switch gqlErr.Extensions.ErrorCode {
			case 401, 403:
				return pkgError.NotAuthorized(err.Error())
			case 404:
				return pkgError.ErrNewsletterNotFound
			}
		}
		return pkgError.UpdateRefused(err.Error())
	}
	return mapNewsletterSendError(err, false)
}
