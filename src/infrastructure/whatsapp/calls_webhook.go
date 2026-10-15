package whatsapp

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/callrec"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/safego"
	"github.com/sirupsen/logrus"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types/events"
)

// Fork (elphant): call webhooks of voice calls stage 2b. Contract: contract/call.schema.json;
// docs/webhook-payload.md. Delivery is forwardPayloadToConfiguredWebhooks, so the durable mode
// (G3) applies.

// forwardCallWebhook is the delivery function; tests replace it under callWebhookMu.
var (
	callWebhookMu      sync.Mutex
	forwardCallWebhook = forwardPayloadToConfiguredWebhooks
)

type callWebhookJob struct {
	deviceID string
	ev       CallEvent
}

var (
	callWebhookQueue = make(chan callWebhookJob, 1024)
	callWebhookOnce  sync.Once
)

// callEventSender is a call manager's Emit. The manager calls it on engine goroutines while it
// holds the call's emit lock, so it only enqueues: it never blocks and never calls back into the
// manager. Events join one ordered queue and leave from one worker, so a call's status, answered
// and ended arrive in that order and a slow endpoint never holds up the call engine.
func callEventSender(client *whatsmeow.Client) func(CallEvent) {
	return func(ev CallEvent) {
		callWebhookOnce.Do(func() { safego.Loop("calls_webhooks", runCallWebhooks) })
		select {
		case callWebhookQueue <- callWebhookJob{deviceID: deviceJIDOf(client), ev: ev}:
		default:
			logrus.Errorf("[CALLS] webhook queue full; dropped %s of call %s", ev.Type, ev.Call.CallID)
		}
	}
}

func runCallWebhooks() {
	for job := range callWebhookQueue {
		deliverCallWebhook(job)
	}
}

func deliverCallWebhook(job callWebhookJob) {
	callWebhookMu.Lock()
	forward := forwardCallWebhook
	callWebhookMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := forward(ctx, callEventBody(job.deviceID, job.ev), job.ev.Type); err != nil {
		logrus.Errorf("[CALLS] webhook %s of call %s: %v", job.ev.Type, job.ev.Call.CallID, MaskDigits(err.Error()))
	}
}

func deviceJIDOf(client *whatsmeow.Client) string {
	if client == nil || client.Store == nil || client.Store.ID == nil {
		return ""
	}
	return client.Store.ID.ToNonAD().String()
}

func peerPayload(p CallPeer) map[string]any { return map[string]any{"pn": p.PN, "lid": p.LID} }

// callEventBody builds the webhook body of a call.status, call.answered or call.ended event.
func callEventBody(deviceID string, ev CallEvent) map[string]any {
	payload := map[string]any{
		"call_id":   ev.Call.CallID,
		"peer":      peerPayload(ev.Call.Peer),
		"direction": ev.Call.Direction,
	}
	switch ev.Type {
	case EventCallStatus:
		payload["status"] = ev.Status
	case EventCallAnswered:
		payload["answered_by"] = ev.AnsweredBy
	case EventCallEnded:
		payload["status"] = ev.Status
		payload["end_reason"] = ev.EndReason
		payload["duration"] = ev.Duration
		if ev.Recording != nil && ev.Recording.Bytes > 0 {
			payload["recording"] = recordingPayload(ev.Call.CallID, ev.Recording, ev.At)
		}
	}
	body := map[string]any{"event": ev.Type, "timestamp": ev.At.UTC().Format(time.RFC3339), "payload": payload}
	if deviceID != "" {
		body["device_id"] = deviceID
	}
	return body
}

// recordingPayload is call.ended's recording object (B6). The URL is relative to the server
// root, like stable.media.url.
func recordingPayload(callID string, rec *RecordingInfo, now time.Time) map[string]any {
	expires := now.Add(time.Duration(max(1, config.WhatsappCallsRecordingURLTTLMinutes)) * time.Minute)
	return map[string]any{
		"url":        callrec.URLPath(config.AppBasePath, callID, []byte(config.AppStaticsSecret), expires),
		"expires_at": expires.UTC().Format(time.RFC3339),
		"bytes":      rec.Bytes,
		"duration":   int(math.Round(rec.Duration.Seconds())),
		"format":     callrec.Format,
		"channels":   map[string]any{"left": "agent", "right": "customer"},
	}
}

// addCallOfferFields adds stage 2b's fields to call.offer: calls_enabled, direction, peer and,
// for a device with calls on, offer_sdp (GOWA's SDP offer, B3). With the master switch off the
// payload stays as upstream builds it. Group calls are left to the phone.
func addCallOfferFields(ctx context.Context, payload map[string]any, evt *events.CallOffer, client *whatsmeow.Client) {
	if !config.WhatsappCallsEnabled || evt == nil {
		return
	}
	var m *CallManager
	if client != nil {
		if v, ok := callsByClient.Load(client); ok {
			m = v.(*CallManager)
		}
	}
	enabled := m != nil && hasCallManager(client) && evt.GroupJID.IsEmpty()
	payload["calls_enabled"] = enabled
	payload["direction"] = DirectionUser
	payload["peer"] = peerPayload(resolvePeer(client, evt.CallCreator, ""))
	if !enabled {
		return
	}
	sdp, err := m.OfferSDP(ctx, evt.CallID)
	if err != nil {
		logrus.Warnf("[CALLS] call.offer %s without offer_sdp: %v", evt.CallID, MaskDigits(err.Error()))
		return
	}
	payload["offer_sdp"] = sdp
}
