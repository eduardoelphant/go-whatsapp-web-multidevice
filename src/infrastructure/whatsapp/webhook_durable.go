package whatsapp

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/webhookoutbox"
	"github.com/sirupsen/logrus"
)

// Fork (elphant): durable webhook delivery (G3). With
// WHATSAPP_WEBHOOK_DELIVERY=durable every webhook event except chat_presence is
// written to a SQLite outbox and sent by webhookoutbox workers, in order per
// URL, retried for up to 72 hours. See docs/elphant-fork.md.

const (
	webhookDeliveryEnv = "WHATSAPP_WEBHOOK_DELIVERY"
	webhookOutboxDBEnv = "WHATSAPP_WEBHOOK_OUTBOX_DB"
	defaultOutboxDBURI = "file:storages/webhook-outbox.db"
)

// durableOutbox is nil in direct mode.
var durableOutbox *webhookoutbox.Outbox

func durableWebhooksEnabled() bool { return durableOutbox != nil }

// DurableWebhookOutbox returns the outbox, or nil in direct mode.
func DurableWebhookOutbox() *webhookoutbox.Outbox { return durableOutbox }

// StartDurableWebhooks opens the outbox and starts its workers when
// WHATSAPP_WEBHOOK_DELIVERY=durable; direct (the default) changes nothing. It
// must run before the first WhatsApp client is created.
func StartDurableWebhooks(ctx context.Context) error {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv(webhookDeliveryEnv)))
	switch mode {
	case "", "direct":
		return nil
	case "durable":
	default:
		return fmt.Errorf("%s=%q: want direct or durable", webhookDeliveryEnv, mode)
	}

	uri := strings.TrimSpace(os.Getenv(webhookOutboxDBEnv))
	if uri == "" {
		uri = defaultOutboxDBURI
	}
	store, err := webhookoutbox.OpenStore(uri)
	if err != nil {
		return fmt.Errorf("open webhook outbox: %w", err)
	}
	outbox := webhookoutbox.New(store, resolveWebhookSecret, webhookoutbox.DefaultPolicy)
	if err := outbox.Start(ctx); err != nil {
		store.Close()
		return fmt.Errorf("start webhook outbox: %w", err)
	}
	durableOutbox = outbox
	submitWebhookFn = submitWebhookDurable
	logrus.Infof("Webhook delivery: durable (outbox %s)", uri)
	return nil
}

// submitWebhookDurable replaces submitWebhook in durable mode: it queues one
// row for url and returns. A queue error marks the event handler as failed so
// whatsmeow does not ack the message (see handleEventWithStatus).
func submitWebhookDurable(ctx context.Context, payload map[string]any, url string, webhookConfig *domainChatStorage.DeviceWebhookConfig) error {
	eventName, _ := payload["event"].(string)
	if eventName == "chat_presence" {
		// A typing indicator is worthless hours later: send it directly.
		return submitWebhook(ctx, payload, url, webhookConfig)
	}
	if _, err := durableOutbox.Enqueue(ctx, url, webhookConfigRef(payload, webhookConfig), eventName, payload); err != nil {
		markHandlerFailed(ctx)
		return fmt.Errorf("queue webhook %s for %s: %w", eventName, url, err)
	}
	return nil
}

// webhookConfigRef names where the secret of this delivery comes from, so it
// is read again at send time and never stored in the outbox.
func webhookConfigRef(payload map[string]any, webhookConfig *domainChatStorage.DeviceWebhookConfig) string {
	if webhookConfig == nil {
		return "global"
	}
	if sessionID, _ := payload["session_id"].(string); sessionID != "" {
		return "device:" + sessionID
	}
	deviceID, _ := payload["device_id"].(string)
	return "jid:" + deviceID
}

// resolveWebhookSecret applies submitWebhook's rules to a config_ref: the
// device secret when set, else the global one; TLS verification is skipped
// when either the global or the device setting says so.
func resolveWebhookSecret(_ context.Context, ref string) (string, bool, error) {
	var (
		webhookConfig *domainChatStorage.DeviceWebhookConfig
		err           error
	)
	switch {
	case ref == "global":
	case strings.HasPrefix(ref, "device:"):
		webhookConfig, err = getWebhookConfigForSlot(map[string]any{"session_id": strings.TrimPrefix(ref, "device:")})
	case strings.HasPrefix(ref, "jid:"):
		webhookConfig, err = getWebhookConfigForDevice(strings.TrimPrefix(ref, "jid:"))
	default:
		return "", false, fmt.Errorf("unknown webhook config ref %q", ref)
	}
	if err != nil {
		return "", false, err
	}
	secret, insecure := config.WhatsappWebhookSecret, config.WhatsappWebhookInsecureSkipVerify
	if webhookConfig != nil {
		if webhookConfig.WebhookInsecureSkipVerify {
			insecure = true
		}
		if webhookConfig.WebhookSecret != "" {
			secret = webhookConfig.WebhookSecret
		}
	}
	return secret, insecure, nil
}

type handlerFailureKey struct{}

// withHandlerFailureFlag returns a context that markHandlerFailed can flag.
func withHandlerFailureFlag(ctx context.Context) (context.Context, *atomic.Bool) {
	failed := new(atomic.Bool)
	return context.WithValue(ctx, handlerFailureKey{}, failed), failed
}

// markHandlerFailed flags the event handler run that ctx belongs to; it is a
// no-op for contexts without the flag.
func markHandlerFailed(ctx context.Context) {
	if failed, ok := ctx.Value(handlerFailureKey{}).(*atomic.Bool); ok {
		failed.Store(true)
	}
}
