package whatsapp

import (
	"context"
	"sync/atomic"

	"go.mau.fi/whatsmeow/types/events"
)

// Fork (elphant): count failed full app state syncs (contacts, chat settings, labels) so they show
// on /metrics. whatsmeow keeps working without them; the data just stays stale until a later sync.
var appStateSyncErrors atomic.Int64

// AppStateSyncErrors returns how many full app state syncs failed since start.
func AppStateSyncErrors() int64 {
	return appStateSyncErrors.Load()
}

func handleAppStateSyncError(_ context.Context, evt *events.AppStateSyncError) {
	appStateSyncErrors.Add(1)
	log.Warnf("Full app state sync of %s failed: %v", evt.Name, evt.Error)
}
