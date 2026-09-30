package validations

import (
	"context"
	"strings"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainUser "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/user"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	validation "github.com/go-ozzo/ozzo-validation/v4"
)

func ValidateUserInfo(ctx context.Context, request domainUser.InfoRequest) error {
	err := validation.ValidateStructWithContext(ctx, &request,
		validation.Field(&request.Phone, validation.Required),
	)

	if err != nil {
		return pkgError.ValidationError(err.Error())
	}

	return nil
}
func ValidateUserAvatar(ctx context.Context, request domainUser.AvatarRequest) error {
	err := validation.ValidateStructWithContext(ctx, &request,
		validation.Field(&request.Phone, validation.Required),
		validation.Field(&request.IsCommunity, validation.When(request.IsCommunity, validation.Required, validation.In(true, false))),
		validation.Field(&request.IsPreview, validation.When(request.IsPreview, validation.Required, validation.In(true, false))),
	)

	if err != nil {
		return pkgError.ValidationError(err.Error())
	}

	return nil
}

func ValidateBusinessProfile(ctx context.Context, request domainUser.BusinessProfileRequest) error {
	err := validation.ValidateStructWithContext(ctx, &request,
		validation.Field(&request.Phone, validation.Required),
	)

	if err != nil {
		return pkgError.ValidationError(err.Error())
	}

	return nil
}

// CheckBatchMaxPhones is the largest batch POST /user/check accepts.
const CheckBatchMaxPhones = 100

const (
	checkBatchMinDigits = 7
	checkBatchMaxDigits = 15
)

var batchPhoneCleaner = strings.NewReplacer("+", "", " ", "", "-", "", "(", "", ")", "", ".", "")

// ValidateCheckBatch checks the size of a batch. Entries are validated one by one by
// NormalizeBatchPhone, so a bad entry never rejects the whole batch.
func ValidateCheckBatch(ctx context.Context, request domainUser.CheckBatchRequest) error {
	err := validation.ValidateStructWithContext(ctx, &request,
		validation.Field(&request.Phones, validation.Required, validation.Length(1, CheckBatchMaxPhones)),
	)
	if err != nil {
		return pkgError.ValidationError(err.Error())
	}
	return nil
}

// NormalizeBatchPhone reduces an entry to its digits. It accepts digits with "+", spaces,
// dashes, dots and parentheses, and a plain user JID (@s.whatsapp.net). LID, group and
// device JIDs are rejected, as is anything outside 7 to 15 digits.
func NormalizeBatchPhone(raw string) (string, bool) {
	value := strings.TrimSpace(raw)
	if at := strings.Index(value, "@"); at >= 0 {
		if !strings.EqualFold(value[at:], config.WhatsappTypeUser) {
			return "", false
		}
		value = value[:at]
	}
	digits := batchPhoneCleaner.Replace(value)
	// E.164 numbers never start with 0 (a national trunk prefix or a 00 dialing prefix is not a number).
	if len(digits) < checkBatchMinDigits || len(digits) > checkBatchMaxDigits || digits[0] == '0' {
		return "", false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	return digits, true
}
