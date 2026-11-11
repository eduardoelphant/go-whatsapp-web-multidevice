package whatsapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/safego"
)

// createGroupInfoPayload creates a webhook payload for group information events
func createGroupInfoPayload(ctx context.Context, evt *events.GroupInfo, actionType string, jids []types.JID, deviceID string, client *whatsmeow.Client) map[string]any {
	body := make(map[string]any)

	// Create payload structure matching the expected format
	payload := make(map[string]any)

	// Add group chat ID (groups use @g.us, not @lid, so no LID resolution needed)
	payload["chat_id"] = evt.JID.ToNonAD().String()

	// Add action type and affected users (with LID resolution)
	payload["type"] = actionType
	payload["jids"] = jidsToStrings(ctx, jids, client)

	// Wrap in payload structure
	body["payload"] = payload

	// Add metadata for webhook processing
	body["event"] = "group.participants"
	body["timestamp"] = evt.Timestamp.Format(time.RFC3339)
	if deviceID != "" {
		body["device_id"] = deviceID
	}

	return body
}

// jidsToStrings converts a slice of JIDs to a slice of strings, resolving LIDs to phone numbers
func jidsToStrings(ctx context.Context, jids []types.JID, client *whatsmeow.Client) []string {
	if len(jids) == 0 {
		return []string{} // Return empty array instead of nil for consistent JSON
	}

	result := make([]string, len(jids))
	for i, jid := range jids {
		// Resolve LID to phone number if possible
		normalizedJID := NormalizeJIDFromLID(ctx, jid, client)
		result[i] = normalizedJID.ToNonAD().String()
	}
	return result
}

// forwardGroupInfoToWebhook forwards group information events to the configured webhook URLs
func forwardGroupInfoToWebhook(ctx context.Context, evt *events.GroupInfo, deviceID string, client *whatsmeow.Client) error {
	// Send separate webhook events for each action type
	actions := []struct {
		actionType string
		jids       []types.JID
	}{
		{"join", evt.Join},
		{"leave", evt.Leave},
		{"promote", evt.Promote},
		{"demote", evt.Demote},
	}

	for _, action := range actions {
		if len(action.jids) > 0 {
			payload := createGroupInfoPayload(ctx, evt, action.actionType, action.jids, deviceID, client)

			if err := forwardGroupWebhook(ctx, payload, "group.participants"); err != nil {
				logrus.Warnf("Failed to forward group %s event to webhook: %v", action.actionType, err)
			}
		}
	}

	for _, change := range groupInfoChanges(evt) {
		payload := createGroupSettingPayload(ctx, evt, change, deviceID, client)

		if err := forwardGroupWebhook(ctx, payload, "group.info"); err != nil {
			logrus.Warnf("Failed to forward group.info %s event to webhook: %v", change.changeType, err)
		}
	}

	return nil
}

// forwardGroupWebhook is a seam so tests can capture group webhooks.
var forwardGroupWebhook = forwardPayloadToConfiguredWebhooks

type groupInfoChange struct {
	changeType string
	value      any
}

// groupInfoChanges lists one change per setting present in the event.
func groupInfoChanges(evt *events.GroupInfo) []groupInfoChange {
	var changes []groupInfoChange
	if evt.Name != nil {
		changes = append(changes, groupInfoChange{"name", evt.Name.Name})
	}
	if evt.Topic != nil {
		changes = append(changes, groupInfoChange{"topic", evt.Topic.Topic})
	}
	if evt.Locked != nil {
		changes = append(changes, groupInfoChange{"locked", evt.Locked.IsLocked})
	}
	if evt.Announce != nil {
		changes = append(changes, groupInfoChange{"announce", evt.Announce.IsAnnounce})
	}
	if evt.Ephemeral != nil {
		seconds := 0
		if evt.Ephemeral.IsEphemeral {
			seconds = int(evt.Ephemeral.DisappearingTimer)
		}
		changes = append(changes, groupInfoChange{"ephemeral", seconds})
	}
	if evt.MembershipApprovalMode != nil {
		changes = append(changes, groupInfoChange{"approval", evt.MembershipApprovalMode.IsJoinApprovalRequired})
	}
	if evt.NewInviteLink != nil {
		changes = append(changes, groupInfoChange{"invite_link_reset", inviteLinkValue(*evt.NewInviteLink)})
	}
	return changes
}

// inviteLinkValue is the hash of the new invite code, or nil when the link is empty.
func inviteLinkValue(link string) any {
	if link == "" {
		return nil
	}
	return inviteCodeSHA256(link)
}

// inviteCodeSHA256 strips the invite prefix and returns the lowercase hex SHA-256 of the code.
// The raw code is never forwarded: it is the credential to join the group.
func inviteCodeSHA256(link string) string {
	code := strings.TrimPrefix(link, whatsmeow.InviteLinkPrefix)
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// createGroupSettingPayload builds the group.info webhook body for one change.
func createGroupSettingPayload(ctx context.Context, evt *events.GroupInfo, change groupInfoChange, deviceID string, client *whatsmeow.Client) map[string]any {
	payload := map[string]any{
		"chat_id": evt.JID.ToNonAD().String(),
		"type":    change.changeType,
		"value":   change.value,
		"sender":  nil,
	}
	if sender := groupInfoSender(ctx, evt, client); sender != "" {
		payload["sender"] = sender
	}

	body := map[string]any{
		"event":     "group.info",
		"payload":   payload,
		"timestamp": evt.Timestamp.Format(time.RFC3339),
	}
	if deviceID != "" {
		body["device_id"] = deviceID
	}
	return body
}

// groupInfoSender returns the author as a phone JID, resolving LIDs like group.participants does.
func groupInfoSender(ctx context.Context, evt *events.GroupInfo, client *whatsmeow.Client) string {
	if evt.Sender == nil || evt.Sender.IsEmpty() {
		return ""
	}
	return NormalizeJIDFromLID(ctx, *evt.Sender, client).ToNonAD().String()
}

// handleJoinedGroup handles the event when the connected device is added to a new group
func handleJoinedGroup(ctx context.Context, evt *events.JoinedGroup, deviceID string, client *whatsmeow.Client) {
	log.Infof("Joined group %s (reason: %s, type: %s)", evt.JID, evt.Reason, evt.Type)

	// Forward joined group event to webhook
	go func(e *events.JoinedGroup, c *whatsmeow.Client) {
		defer safego.Recover("event_group#1")
		webhookCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if err := forwardJoinedGroupToWebhook(webhookCtx, e, deviceID, c); err != nil {
			logrus.Errorf("Failed to forward joined group event to webhook: %v", err)
		}
	}(evt, client)
}

// forwardJoinedGroupToWebhook forwards the JoinedGroup event to configured webhooks
func forwardJoinedGroupToWebhook(ctx context.Context, evt *events.JoinedGroup, deviceID string, client *whatsmeow.Client) error {
	// Get own JID to include in the payload
	ownJID := client.Store.ID
	if ownJID == nil {
		return fmt.Errorf("client store ID is nil")
	}

	payload := map[string]any{
		"chat_id": evt.JID.ToNonAD().String(),
		"type":    "join",
		"jids":    []string{ownJID.ToNonAD().String()},
		"reason":  evt.Reason, // "invite" if via invite link
	}

	// Include group name if available (GroupName.Name is embedded in GroupInfo)
	if evt.GroupName.Name != "" {
		payload["group_name"] = evt.GroupName.Name
	}

	body := map[string]any{
		"event":     "group.joined",
		"payload":   payload,
		"timestamp": time.Now().Format(time.RFC3339),
	}
	if deviceID != "" {
		body["device_id"] = deviceID
	}

	return forwardPayloadToConfiguredWebhooks(ctx, body, "group.joined")
}
