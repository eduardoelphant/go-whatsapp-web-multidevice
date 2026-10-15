package whatsapp

import (
	"context"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/callrec"
)

// Fork (elphant): what voice calls need besides the clients (stage 2b).

// StartCallsServices runs once at start, before the first client is created. Retention runs even with calls
// off, so recordings of earlier calls still expire.
func StartCallsServices(ctx context.Context) {
	retention := time.Duration(config.WhatsappCallsRecordingRetentionDays) * 24 * time.Hour
	callrec.StartRetention(ctx, config.PathCalls, retention)
}
