package usecase

import (
	"errors"
	"net/http"
	"testing"

	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"go.mau.fi/whatsmeow"
)

func TestAvatarLookupErrorMapsMissingPictureToNotFound(t *testing.T) {
	for _, source := range []error{whatsmeow.ErrProfilePictureUnauthorized, whatsmeow.ErrProfilePictureNotSet} {
		err := avatarLookupError(source)
		genericErr, ok := err.(pkgError.GenericError)
		if !ok {
			t.Fatalf("%v: expected generic error, got %T", source, err)
		}
		if got := genericErr.StatusCode(); got != http.StatusNotFound {
			t.Fatalf("%v: expected status %d, got %d", source, http.StatusNotFound, got)
		}
	}
}

func TestAvatarLookupErrorKeepsOtherErrors(t *testing.T) {
	source := errors.New("websocket not connected")
	if got := avatarLookupError(source); got != source {
		t.Fatalf("expected the original error, got %v", got)
	}
}
