package whatsapp

import (
	"bytes"
	"context"
	"strings"
	"testing"

	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	"github.com/sirupsen/logrus"
)

func captureLogrus(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	previousOut, previousLevel := logrus.StandardLogger().Out, logrus.GetLevel()
	logrus.SetOutput(&logs)
	logrus.SetLevel(logrus.InfoLevel)
	t.Cleanup(func() { logrus.SetOutput(previousOut); logrus.SetLevel(previousLevel) })
	return &logs
}

func TestDurableForwardLogSaysQueued(t *testing.T) {
	useTestOutbox(t)
	useGlobalWebhooks(t, "http://a.test/hook")
	logs := captureLogrus(t)

	if err := forwardPayloadToConfiguredWebhooks(context.Background(), map[string]any{"event": "message"}, "message"); err != nil {
		t.Fatal(err)
	}
	out := logs.String()
	if strings.Contains(out, "forwarded") || strings.Contains(out, "Forwarding") ||
		!strings.Contains(out, "Queueing message for 1 configured webhook(s)") || !strings.Contains(out, "message queued for all webhook(s)") {
		t.Fatalf("log does not say the event was only queued:\n%s", out)
	}
}

func TestDirectForwardLogIsUnchanged(t *testing.T) {
	useGlobalWebhooks(t, "http://a.test/hook")
	previous := submitWebhookFn
	submitWebhookFn = func(context.Context, map[string]any, string, *domainChatStorage.DeviceWebhookConfig) error {
		return nil
	}
	t.Cleanup(func() { submitWebhookFn = previous })
	logs := captureLogrus(t)

	if err := forwardPayloadToConfiguredWebhooks(context.Background(), map[string]any{"event": "message"}, "message"); err != nil {
		t.Fatal(err)
	}
	if out := logs.String(); !strings.Contains(out, "Forwarding message to 1 configured webhook(s)") || !strings.Contains(out, "message forwarded to all webhook(s)") {
		t.Fatalf("direct log changed:\n%s", out)
	}
}

func TestSessionStatusQueueFailureDoesNotFailTheHandler(t *testing.T) {
	outbox := useTestOutbox(t)
	useGlobalWebhooks(t, "http://a.test/hook")
	previous := sessionStatusDispatch
	sessionStatusDispatch = dispatchSessionStatus
	t.Cleanup(func() { sessionStatusDispatch = previous })
	outbox.Store().Close()

	ctx, failed := withHandlerFailureFlag(context.Background())
	EmitSessionStatus(ctx, testInstance(), SessionStatus{Status: SessionStatusConnected})
	if failed.Load() {
		t.Fatal("a session.status queue failure must only be logged")
	}
}
