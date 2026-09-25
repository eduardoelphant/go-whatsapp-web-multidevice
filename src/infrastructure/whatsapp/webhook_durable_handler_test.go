package whatsapp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// quietMessageHandler turns off logging, auto-reply and auto-read for handler tests.
func quietMessageHandler(t *testing.T) {
	t.Helper()
	prevLog, prevReply, prevRead := log, config.WhatsappAutoReplyMessage, config.WhatsappAutoMarkRead
	log, config.WhatsappAutoReplyMessage, config.WhatsappAutoMarkRead = waLog.Noop, "", false
	t.Cleanup(func() {
		log, config.WhatsappAutoReplyMessage, config.WhatsappAutoMarkRead = prevLog, prevReply, prevRead
	})
}

func testInstance() *DeviceInstance {
	return NewDeviceInstance("dev-a", nil, &messageHandlerRepoSpy{})
}

func TestDurableHandlerQueuesTheMessageBeforeReturning(t *testing.T) {
	outbox := useTestOutbox(t)
	useGlobalWebhooks(t, "http://a.test/hook")
	quietMessageHandler(t)

	if !handleEventWithStatus(context.Background(), testInstance(), reactionEventForTest("R1", "M1", "\U0001f44d")) {
		t.Fatal("handler reported failure")
	}
	// No waiting: in durable mode the row exists when the handler returns.
	rows := pendingRows(t, outbox)
	if len(rows) != 1 || rows[0].EventName != EventTypeMessageReaction {
		t.Fatalf("rows %+v", rows)
	}
}

func TestDurableHandlerReportsFailureWhenTheQueueIsDown(t *testing.T) {
	outbox := useTestOutbox(t)
	useGlobalWebhooks(t, "http://a.test/hook")
	quietMessageHandler(t)
	outbox.Store().Close()

	if handleEventWithStatus(context.Background(), testInstance(), reactionEventForTest("R2", "M1", "\U0001f44d")) {
		t.Fatal("handler reported success with the outbox closed; WhatsApp would not redeliver")
	}
}

func TestDirectHandlerAlwaysReportsSuccess(t *testing.T) {
	useGlobalWebhooks(t, "http://a.test/hook")
	quietMessageHandler(t)
	previous := submitWebhookFn
	delivered := make(chan struct{}, 1)
	submitWebhookFn = func(context.Context, map[string]any, string, *domainChatStorage.DeviceWebhookConfig) error {
		delivered <- struct{}{}
		return errors.New("receiver down")
	}
	t.Cleanup(func() { submitWebhookFn = previous })

	if !handleEventWithStatus(context.Background(), testInstance(), reactionEventForTest("R3", "M1", "\U0001f44d")) {
		t.Fatal("direct mode must never withhold the ack")
	}
	select {
	case <-delivered: // still forwarded from a goroutine, as upstream does
	case <-time.After(5 * time.Second):
		t.Fatal("direct mode did not forward the webhook")
	}
}

func TestDurableReceiptIsQueuedBeforeReturning(t *testing.T) {
	outbox := useTestOutbox(t)
	useGlobalWebhooks(t, "http://a.test/hook")
	quietMessageHandler(t)
	contact := types.NewJID("5511999999999", types.DefaultUserServer)
	evt := &events.Receipt{
		MessageSource: types.MessageSource{Chat: contact, Sender: contact},
		MessageIDs:    []types.MessageID{"M1"},
		Timestamp:     time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC),
		Type:          types.ReceiptTypeRead,
	}

	if !handleEventWithStatus(context.Background(), testInstance(), evt) {
		t.Fatal("handler reported failure")
	}
	rows := pendingRows(t, outbox)
	if len(rows) != 1 || rows[0].EventName != "message.ack" {
		t.Fatalf("rows %+v", rows)
	}
}

func TestSessionStatusIsQueuedInTheCallerInDurableMode(t *testing.T) {
	useTestOutbox(t)
	ran := false
	dispatchSessionStatus(func() { ran = true })
	if !ran {
		t.Fatal("durable mode must queue session.status before returning")
	}
}

func TestSessionStatusRunsOffTheCallerInDirectMode(t *testing.T) {
	previous := durableOutbox
	durableOutbox = nil
	t.Cleanup(func() { durableOutbox = previous })

	block, done := make(chan struct{}), make(chan struct{})
	dispatchSessionStatus(func() {
		<-block
		close(done)
	}) // returns while deliver is still blocked
	close(block)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("delivery never ran")
	}
}

func TestRegisterEventHandlerEnablesTheDecryptedEventBufferOnlyInDurableMode(t *testing.T) {
	container := newTestSQLStore(t)
	instance := testInstance()

	direct := whatsmeow.NewClient(container.NewDevice(), nil)
	registerEventHandler(context.Background(), direct, instance)
	if direct.EnableDecryptedEventBuffer {
		t.Fatal("direct mode enabled the decrypted-event buffer")
	}

	useTestOutbox(t)
	durable := whatsmeow.NewClient(container.NewDevice(), nil)
	registerEventHandler(context.Background(), durable, instance)
	if !durable.EnableDecryptedEventBuffer {
		t.Fatal("durable mode must buffer decrypted events so a redelivered message can be read again")
	}
}
