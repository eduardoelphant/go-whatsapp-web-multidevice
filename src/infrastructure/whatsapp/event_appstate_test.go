package whatsapp

import (
	"context"
	"errors"
	"testing"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

func TestHandleAppStateSyncErrorCountsFailures(t *testing.T) {
	originalLog := log
	defer func() { log = originalLog }()
	log = waLog.Noop
	before := AppStateSyncErrors()

	handleAppStateSyncError(context.Background(), &events.AppStateSyncError{
		Name:     appstate.WAPatchRegularHigh,
		FullSync: true,
		Error:    errors.New("failed to verify patch v27: mismatching LTHash"),
	})

	if got := AppStateSyncErrors() - before; got != 1 {
		t.Fatalf("expected the counter to grow by 1, grew by %d", got)
	}
}
