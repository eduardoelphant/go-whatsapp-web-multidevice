package usecase

import (
	"errors"
	"testing"

	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	domainMessage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/message"
)

type skippedRepo struct {
	domainChatStorage.IChatStorageRepository
	skipped  *domainChatStorage.SkippedMessage
	deviceID string
}

func (r *skippedRepo) GetSkippedMessageByDevice(deviceID, _ string) (*domainChatStorage.SkippedMessage, error) {
	r.deviceID = deviceID
	return r.skipped, nil
}

func TestSkippedMessageLooksUpTheStorageDevice(t *testing.T) {
	repo := &skippedRepo{skipped: &domainChatStorage.SkippedMessage{ID: "M1", Reason: "album"}}
	res, err := serviceMessage{chatStorageRepo: repo}.SkippedMessage(mediaStreamCtx(), "M1")
	if err != nil {
		t.Fatal(err)
	}
	if repo.deviceID != "5511900000009@s.whatsapp.net" {
		t.Fatalf("device = %q, want the storage JID, not the slot alias", repo.deviceID)
	}
	if res.Reason != "album" || res.Fields == nil {
		t.Fatalf("unexpected response %+v (fields must be [] not null)", res)
	}
}

func TestSkippedMessageNotFound(t *testing.T) {
	_, err := serviceMessage{chatStorageRepo: &skippedRepo{}}.SkippedMessage(mediaStreamCtx(), "M1")
	if !errors.Is(err, domainMessage.ErrSkippedMessageNotFound) {
		t.Fatalf("err = %v, want ErrSkippedMessageNotFound", err)
	}
}
