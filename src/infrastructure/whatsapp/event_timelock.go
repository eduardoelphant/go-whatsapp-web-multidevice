package whatsapp

import (
	"context"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/sirupsen/logrus"
	"go.mau.fi/whatsmeow/types/events"
)

// Fork (elphant): the session.timelock webhook reports the account's reach-out timelock
// (spec 2026-09-30-gateway-g6). It is sent on transitions only.

// SessionTimelockEvent is the webhook event name for reach-out timelock changes.
const SessionTimelockEvent = "session.timelock"

// suspectWindow is how long a timelock with no known end is assumed to last.
func suspectWindow() time.Duration {
	minutes := config.WhatsappReachoutSuspectMinutes
	if minutes < 1 {
		minutes = 1
	}
	return time.Duration(minutes) * time.Minute
}

// ReachoutSnapshot returns the device's timelock state at now. Reading an expired state clears it.
func (d *DeviceInstance) ReachoutSnapshot(now time.Time) ReachoutSnapshot {
	snap, _ := d.reachout.read(now)
	return snap
}

// timelockPayload is the session.timelock payload; unknown values are JSON null.
func timelockPayload(snap ReachoutSnapshot) map[string]any {
	payload := map[string]any{
		"active":           snap.Active,
		"source":           nil,
		"enforcement_type": nil,
		"ends_at":          nil,
	}
	if snap.Active {
		payload["source"] = snap.Source
	}
	if snap.EnforcementType != "" {
		payload["enforcement_type"] = snap.EnforcementType
	}
	if snap.EndsAt != nil {
		payload["ends_at"] = snap.EndsAt.UTC().Format(time.RFC3339)
	}
	return payload
}

// EmitSessionTimelock sends a session.timelock webhook for the device without blocking the
// caller, through the same dispatch as session.status.
func EmitSessionTimelock(ctx context.Context, instance *DeviceInstance, snap ReachoutSnapshot) {
	if instance == nil {
		return
	}
	owner := canonicalInstance(instance)
	body := map[string]any{
		"event":      SessionTimelockEvent,
		"device_id":  owner.JID(),
		"session_id": owner.ID(),
		"timestamp":  time.Now().Format(time.RFC3339),
		"payload":    timelockPayload(snap),
	}
	sessionStatusDispatch(func() {
		webhookCtx, cancel := context.WithTimeout(withoutHandlerFailureFlag(context.WithoutCancel(ctx)), 30*time.Second)
		defer cancel()
		if err := forwardPayloadToConfiguredWebhooks(webhookCtx, body, SessionTimelockEvent); err != nil {
			logrus.Errorf("Failed to forward %s for device %s: %v", SessionTimelockEvent, owner.ID(), err)
		}
	})
}

// HandleReachoutTimelockEvent applies WhatsApp's notification and emits session.timelock when
// the visible state changed.
func HandleReachoutTimelockEvent(ctx context.Context, instance *DeviceInstance, evt *events.NotifyAccountReachoutTimelock, now time.Time) {
	if instance == nil || evt == nil {
		return
	}
	owner := canonicalInstance(instance)
	if snap, changed := owner.reachout.applyEvent(now, suspectWindow(), evt.IsActive, evt.EnforcementType, evt.TimeEnforcementEnds.Time); changed {
		EmitSessionTimelock(ctx, owner, snap)
	}
}

// NoteReachoutTimelock records a send refused with 463 and emits session.timelock when it
// turns the state active.
func NoteReachoutTimelock(ctx context.Context, instance *DeviceInstance) {
	if instance == nil {
		return
	}
	owner := canonicalInstance(instance)
	if snap, changed := owner.reachout.mark463(time.Now(), suspectWindow()); changed {
		EmitSessionTimelock(ctx, owner, snap)
	}
}

// resetReachout clears the timelock state when the session ends or a new one starts, and tells
// consumers when an active state was cleared.
func resetReachout(ctx context.Context, instance *DeviceInstance) {
	if instance == nil {
		return
	}
	if snap, was := instance.reachout.reset(); was {
		EmitSessionTimelock(ctx, instance, snap)
	}
}
