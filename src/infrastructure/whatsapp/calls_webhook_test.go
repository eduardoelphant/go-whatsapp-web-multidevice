package whatsapp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// callWebhookSink records the call webhooks the queue delivers.
type callWebhookSink struct {
	mu     sync.Mutex
	bodies []map[string]any
	events []string
}

func (s *callWebhookSink) forward(_ context.Context, body map[string]any, event string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bodies = append(s.bodies, body)
	s.events = append(s.events, event+":"+body["payload"].(map[string]any)["call_id"].(string))
	return nil
}

func (s *callWebhookSink) got() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.events...)
}

func (s *callWebhookSink) payloadOf(event, callID string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.bodies {
		p := b["payload"].(map[string]any)
		if b["event"] == event && p["call_id"] == callID {
			return p
		}
	}
	return nil
}

func stubCallWebhook(t *testing.T, fn func(context.Context, map[string]any, string) error) {
	t.Helper()
	callWebhookMu.Lock()
	orig := forwardCallWebhook
	forwardCallWebhook = fn
	callWebhookMu.Unlock()
	t.Cleanup(func() {
		callWebhookMu.Lock()
		forwardCallWebhook = orig
		callWebhookMu.Unlock()
	})
}

func TestCallEventBodiesMatchTheContractFixtures(t *testing.T) {
	secret, base, ttl := config.AppStaticsSecret, config.AppBasePath, config.WhatsappCallsRecordingURLTTLMinutes
	t.Cleanup(func() {
		config.AppStaticsSecret, config.AppBasePath, config.WhatsappCallsRecordingURLTTLMinutes = secret, base, ttl
	})
	config.AppStaticsSecret, config.AppBasePath, config.WhatsappCallsRecordingURLTTLMinutes = "test-secret", "", 1440

	at := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	peer := CallPeer{PN: "5511900000001@s.whatsapp.net", LID: "123456789012345@lid"}
	in := CallInfo{CallID: "A1B2C3D4E5F60718293A4B5C6D7E8F90", Direction: DirectionUser, Peer: peer}
	out := in
	out.Direction = DirectionBusiness
	missed := in
	missed.Peer = CallPeer{LID: "123456789012345@lid"}

	cases := map[string]CallEvent{
		"call-status.json":   {Type: EventCallStatus, Call: out, Status: "RINGING", At: at},
		"call-answered.json": {Type: EventCallAnswered, Call: in, AnsweredBy: "api", At: at},
		"call-ended.json": {Type: EventCallEnded, Call: in, Status: "COMPLETED", EndReason: "hangup_peer", Duration: 64, At: at,
			Recording: &RecordingInfo{Path: "storages/calls/A1B2C3D4E5F60718293A4B5C6D7E8F90.ogg", Bytes: 190464, Duration: 64 * time.Second}},
		"call-ended-missed.json": {Type: EventCallEnded, Call: missed, Status: "MISSED", EndReason: "timeout", At: at},
	}
	for name, ev := range cases {
		raw, err := os.ReadFile(filepath.Join("..", "..", "..", "contract", "call-fixtures", name))
		require.NoError(t, err)
		got, err := json.Marshal(callEventBody("5511900000000@s.whatsapp.net", ev))
		require.NoError(t, err)
		assert.JSONEq(t, string(raw), string(got), name)
	}
}

func TestCallRecordingURLWithoutSecretIsTheBarePath(t *testing.T) {
	secret, base := config.AppStaticsSecret, config.AppBasePath
	t.Cleanup(func() { config.AppStaticsSecret, config.AppBasePath = secret, base })
	config.AppStaticsSecret, config.AppBasePath = "", "/gowa"

	body := callEventBody("", CallEvent{Type: EventCallEnded, Call: CallInfo{CallID: "C1", Direction: DirectionUser},
		Status: "COMPLETED", EndReason: "hangup_agent", Duration: 3, At: time.Now(),
		Recording: &RecordingInfo{Bytes: 10, Duration: 3 * time.Second}})
	assert.NotContains(t, body, "device_id")
	rec := body["payload"].(map[string]any)["recording"].(map[string]any)
	assert.Equal(t, "/gowa/call/C1/recording", rec["url"])
}

func TestCallWebhooksKeepOrder(t *testing.T) {
	sink := &callWebhookSink{}
	stubCallWebhook(t, sink.forward)

	send := callEventSender(nil)
	for _, typ := range []string{EventCallStatus, EventCallAnswered, EventCallEnded} {
		send(CallEvent{Type: typ, Call: CallInfo{CallID: "ORDER-1"}, At: time.Now()})
	}
	require.Eventually(t, func() bool { return len(sink.got()) == 3 }, 2*time.Second, 5*time.Millisecond)
	assert.Equal(t, []string{"call.status:ORDER-1", "call.answered:ORDER-1", "call.ended:ORDER-1"}, sink.got())
}

func TestCallWebhookSenderNeverWaitsOnTheEndpoint(t *testing.T) {
	release := make(chan struct{})
	sink := &callWebhookSink{}
	stubCallWebhook(t, func(ctx context.Context, body map[string]any, event string) error {
		<-release
		return sink.forward(ctx, body, event)
	})

	send := callEventSender(nil)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 10; i++ {
			send(CallEvent{Type: EventCallStatus, Call: CallInfo{CallID: "SLOW-1"}, At: time.Now()})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the sender blocked on a slow webhook endpoint")
	}
	close(release)
	require.Eventually(t, func() bool { return len(sink.got()) == 10 }, 2*time.Second, 5*time.Millisecond)
}

func TestAttachCallsWiresTheWebhookSender(t *testing.T) {
	withCallsConfig(t, true, nil)
	client := &whatsmeow.Client{Store: &store.Device{}}
	attachCalls("slot-a", client)
	t.Cleanup(func() { forgetCalls(client) })
	v, ok := callsByClient.Load(client)
	require.True(t, ok)
	assert.NotNil(t, v.(*CallManager).opts.Emit)
}

func TestSwitchOffSendsCallEnded(t *testing.T) {
	withCallsConfig(t, true, nil)
	withCallsStore(t, newFakeCallsStore())
	client := &whatsmeow.Client{}
	withResolvedSlot(t, "slot-a", client)
	sink := &callWebhookSink{}
	stubCallWebhook(t, sink.forward)

	eng := &fakeEngine{}
	m := newCallManager(eng, callManagerOptions{MaxCalls: 2, Emit: callEventSender(client)})
	m.slotID = "slot-a"
	callsByClient.Store(client, m)
	t.Cleanup(func() { callsByClient.Delete(client) })
	eng.incoming(&fakeCall{id: "RING-1"})
	eng.incoming(&fakeCall{id: "LIVE-1"})
	require.NoError(t, m.Answer("LIVE-1", ""))

	_, err := SetDeviceCalls("slot-a", false)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return sink.payloadOf(EventCallEnded, "RING-1") != nil && sink.payloadOf(EventCallEnded, "LIVE-1") != nil
	}, 2*time.Second, 5*time.Millisecond)

	ring := sink.payloadOf(EventCallEnded, "RING-1")
	assert.Equal(t, "REJECTED", ring["status"])
	assert.Equal(t, "rejected", ring["end_reason"])
	live := sink.payloadOf(EventCallEnded, "LIVE-1")
	assert.Equal(t, "hangup_agent", live["end_reason"])
	assert.Equal(t, "FAILED", live["status"], "answered without media flowing")
}

func TestCallOfferFields(t *testing.T) {
	withCallsConfig(t, false, nil)
	evt := &events.CallOffer{}
	evt.CallID = "IN-1"
	evt.CallCreator = types.NewJID("123456789012345", types.HiddenUserServer)
	client := &whatsmeow.Client{}

	payload := map[string]any{}
	addCallOfferFields(context.Background(), payload, evt, client)
	assert.Empty(t, payload, "flag off: call.offer stays as upstream builds it")

	config.WhatsappCallsEnabled = true
	eng := &fakeEngine{}
	m := newCallManager(eng, callManagerOptions{Media: &fakeMediaFactory{}, OfferWait: 200 * time.Millisecond})
	callsByClient.Store(client, m)
	t.Cleanup(func() { callsByClient.Delete(client) })
	eng.incoming(&fakeCall{id: "IN-1"})

	addCallOfferFields(context.Background(), payload, evt, client)
	assert.Equal(t, true, payload["calls_enabled"])
	assert.Equal(t, DirectionUser, payload["direction"])
	assert.Equal(t, map[string]any{"pn": "", "lid": "123456789012345@lid"}, payload["peer"])
	assert.Equal(t, "v=0 gowa-offer", payload["offer_sdp"])

	group := &events.CallOffer{}
	group.CallID = "IN-1"
	group.CallCreator = evt.CallCreator
	group.GroupJID = types.NewJID("120363000000000000", types.GroupServer)
	grp := map[string]any{}
	addCallOfferFields(context.Background(), grp, group, client)
	assert.Equal(t, false, grp["calls_enabled"])
	assert.NotContains(t, grp, "offer_sdp")

	config.WhatsappCallsDevices = []string{"another-device"}
	resetDeviceCallsCache()
	off := map[string]any{}
	addCallOfferFields(context.Background(), off, evt, client)
	assert.Equal(t, false, off["calls_enabled"])
	assert.NotContains(t, off, "offer_sdp")
}
