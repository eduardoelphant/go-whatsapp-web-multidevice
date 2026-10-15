package whatsapp

import (
	"context"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/callrec"
)

// Fork (elphant): what voice calls need besides the clients (stage 2b).

// StartCallsServices runs once at start, before the first client is created. With calls off
// it does nothing.
func StartCallsServices(ctx context.Context) {
	if !config.WhatsappCallsEnabled {
		return
	}
	retention := time.Duration(config.WhatsappCallsRecordingRetentionDays) * 24 * time.Hour
	callrec.StartRetention(ctx, config.PathCalls, retention)
}
