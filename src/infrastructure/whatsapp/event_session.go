package whatsapp

import (
	"context"
	"time"

	"github.com/sirupsen/logrus"
	"go.mau.fi/whatsmeow/types/events"
)

// Fork (elphant): the session.status webhook reports device lifecycle changes
// (connection, logout, bans, pairing). See docs/elphant-fork.md for the contract.

// SessionStatusEvent is the webhook event name for device lifecycle changes.
const SessionStatusEvent = "session.status"

// Values of the session.status payload "status" key.
const (
	SessionStatusConnected           = "connected"
	SessionStatusDisconnected        = "disconnected"
	SessionStatusLoggedOut           = "logged_out"
	SessionStatusStreamReplaced      = "stream_replaced"
	SessionStatusTemporaryBan        = "temporary_ban"
	SessionStatusConnectFailure      = "connect_failure"
	SessionStatusKeepAliveTimeout    = "keepalive_timeout"
	SessionStatusKeepAliveRestored   = "keepalive_restored"
	SessionStatusClientOutdated      = "client_outdated"
	SessionStatusPairSuccess         = "pair_success"
	SessionStatusQR                  = "qr"
	SessionStatusQRTimeout           = "qr_timeout"
	SessionStatusPasskeyRequired     = "passkey_required"
	SessionStatusPasskeyConfirmation = "passkey_confirmation"
	SessionStatusPairError           = "pair_error"
)

// SessionStatus is one session.status payload. Nil fields are sent as JSON null.
type SessionStatus struct {
	Status    string
	Reason    *string
	Code      *int
	ExpiresAt *time.Time
	QRCode    *string
}

// Payload returns the webhook payload. All five keys are always present.
func (s SessionStatus) Payload() map[string]any {
	payload := map[string]any{
		"status":     s.Status,
		"reason":     nil,
		"code":       nil,
		"expires_at": nil,
		"qr_code":    nil,
	}
	if s.Reason != nil {
		payload["reason"] = *s.Reason
	}
	if s.Code != nil {
		payload["code"] = *s.Code
	}
	if s.ExpiresAt != nil {
		payload["expires_at"] = s.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if s.QRCode != nil {
		payload["qr_code"] = *s.QRCode
	}
	return payload
}

func sessionPtr[T any](v T) *T {
	return &v
}

// sessionStatusFromEvent maps a whatsmeow event to a session.status payload.
// ok is false for events that do not produce one.
func sessionStatusFromEvent(rawEvt any, now time.Time) (SessionStatus, bool) {
	switch evt := rawEvt.(type) {
	case *events.Connected:
		return SessionStatus{Status: SessionStatusConnected}, true
	case *events.Disconnected:
		return SessionStatus{Status: SessionStatusDisconnected}, true
	case *events.LoggedOut:
		return SessionStatus{
			Status: SessionStatusLoggedOut,
			Code:   sessionPtr(int(evt.Reason)),
			Reason: sessionPtr(evt.Reason.String()),
		}, true
	case *events.StreamReplaced:
		return SessionStatus{Status: SessionStatusStreamReplaced}, true
	case *events.TemporaryBan:
		status := SessionStatus{
			Status: SessionStatusTemporaryBan,
			Code:   sessionPtr(int(evt.Code)),
			Reason: sessionPtr(evt.Code.String()),
		}
		if evt.Expire > 0 {
			status.ExpiresAt = sessionPtr(now.Add(evt.Expire))
		}
		return status, true
	case *events.ConnectFailure:
		reason := evt.Message
		if reason == "" {
			reason = evt.Reason.String()
		}
		return SessionStatus{
			Status: SessionStatusConnectFailure,
			Code:   sessionPtr(int(evt.Reason)),
			Reason: &reason,
		}, true
	case *events.KeepAliveTimeout:
		// whatsmeow fires this on every failed keepalive; report only the first
		// failure and let keepalive_restored close it.
		if evt.ErrorCount != 1 {
			return SessionStatus{}, false
		}
		return SessionStatus{Status: SessionStatusKeepAliveTimeout}, true
	case *events.KeepAliveRestored:
		return SessionStatus{Status: SessionStatusKeepAliveRestored}, true
	case *events.ClientOutdated:
		return SessionStatus{
			Status: SessionStatusClientOutdated,
			Code:   sessionPtr(int(events.ConnectFailureClientOutdated)),
		}, true
	case *events.PairSuccess:
		return SessionStatus{Status: SessionStatusPairSuccess}, true
	}
	return SessionStatus{}, false
}

// buildSessionStatusBody builds the webhook body. session_id comes straight from
// the device slot so it is present even before pairing, when device_id is empty.
func buildSessionStatusBody(instance *DeviceInstance, status SessionStatus) map[string]any {
	return map[string]any{
		"event":      SessionStatusEvent,
		"device_id":  instance.JID(),
		"session_id": instance.ID(),
		"timestamp":  time.Now().Format(time.RFC3339),
		"payload":    status.Payload(),
	}
}

// EmitSessionStatus sends a session.status webhook for the device without
// blocking the caller. The body is built before the goroutine starts, so it
// captures the device JID even if a handler clears it right after.
func EmitSessionStatus(ctx context.Context, instance *DeviceInstance, status SessionStatus) {
	if instance == nil {
		return
	}
	body := buildSessionStatusBody(instance, status)
	go func() {
		webhookCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if err := forwardPayloadToConfiguredWebhooks(webhookCtx, body, SessionStatusEvent); err != nil {
			logrus.Errorf("Failed to forward %s %q for device %s: %v", SessionStatusEvent, status.Status, instance.ID(), err)
		}
	}()
}

// handleSessionEvent emits session.status for lifecycle events and ignores the rest.
func handleSessionEvent(ctx context.Context, instance *DeviceInstance, rawEvt any) {
	if status, ok := sessionStatusFromEvent(rawEvt, time.Now()); ok {
		EmitSessionStatus(ctx, instance, status)
	}
}
