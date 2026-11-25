package validations

import (
	"context"
	"regexp"
	"strings"
	"unicode/utf8"

	domainLabel "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/label"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"go.mau.fi/whatsmeow/types"
)

const (
	labelNameMaxLength = 100
	labelColorMax      = 19
)

var labelIDPattern = regexp.MustCompile(`^[0-9]{1,18}$`)

func validateLabelName(name string) error {
	if name == "" {
		return pkgError.ValidationError("name: cannot be blank.")
	}
	if utf8.RuneCountInString(name) > labelNameMaxLength {
		return pkgError.ValidationError("name: the length must be no more than 100.")
	}

	return nil
}

func validateLabelColor(color int32) error {
	if color < 0 || color > labelColorMax {
		return pkgError.ValidationError("color: must be between 0 and 19.")
	}

	return nil
}

func validateLabelID(id string) error {
	if !labelIDPattern.MatchString(id) {
		return pkgError.ValidationError("label_id: must be a numeric string.")
	}

	return nil
}

func ValidateCreateLabel(_ context.Context, request *domainLabel.CreateLabelRequest) error {
	request.Name = strings.TrimSpace(request.Name)
	if err := validateLabelName(request.Name); err != nil {
		return err
	}

	return validateLabelColor(request.Color)
}

func ValidateUpdateLabel(_ context.Context, request *domainLabel.UpdateLabelRequest) error {
	if err := validateLabelID(request.LabelID); err != nil {
		return err
	}
	request.Name = strings.TrimSpace(request.Name)
	if err := validateLabelName(request.Name); err != nil {
		return err
	}

	return validateLabelColor(request.Color)
}

func ValidateDeleteLabel(_ context.Context, request *domainLabel.DeleteLabelRequest) error {
	if err := validateLabelID(request.LabelID); err != nil {
		return err
	}
	request.Name = strings.TrimSpace(request.Name)

	return validateLabelName(request.Name)
}

func ValidateLabelChat(_ context.Context, request *domainLabel.LabelChatRequest) error {
	if err := validateLabelID(request.LabelID); err != nil {
		return err
	}
	request.ChatJID = strings.TrimSpace(request.ChatJID)
	if request.ChatJID == "" {
		return pkgError.ValidationError("chat_jid: cannot be blank.")
	}
	jid, err := types.ParseJID(request.ChatJID)
	if err != nil || jid.User == "" || !strings.Contains(request.ChatJID, "@") {
		return pkgError.ValidationError("chat_jid: invalid JID.")
	}
	switch jid.Server {
	case types.DefaultUserServer, types.GroupServer, types.HiddenUserServer:
		return nil
	}

	return pkgError.ValidationError("chat_jid: must be a user or group JID.")
}
