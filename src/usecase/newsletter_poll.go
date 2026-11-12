package usecase

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

const (
	newsletterPollModePlain = "plain"
	newsletterPollModeNode  = "node"
)

// newsletterPollMode returns the configured channel poll mode; empty or unknown means plain.
func newsletterPollMode() string {
	if strings.ToLower(strings.TrimSpace(config.WhatsappNewsletterPollMode)) == newsletterPollModeNode {
		return newsletterPollModeNode
	}
	return newsletterPollModePlain
}

// buildNewsletterPoll builds a poll without MessageContextInfo: the channel server
// refuses the messageSecret that whatsmeow's BuildPollCreation adds (error 479).
func buildNewsletterPoll(question string, options []string, maxAnswers int) *waE2E.Message {
	if maxAnswers < 1 {
		maxAnswers = 1
	}
	poll := make([]*waE2E.PollCreationMessage_Option, 0, len(options))
	for _, name := range options {
		poll = append(poll, &waE2E.PollCreationMessage_Option{OptionName: proto.String(name)})
	}
	return &waE2E.Message{PollCreationMessage: &waE2E.PollCreationMessage{
		Name:                   proto.String(question),
		Options:                poll,
		SelectableOptionsCount: proto.Uint32(uint32(maxAnswers)),
	}}
}

// newsletterNodePollMessage builds the poll for the node variant: V3 when one answer is
// allowed, V1 otherwise. Neither carries MessageContextInfo.
func newsletterNodePollMessage(question string, options []string, maxAnswers int) *waE2E.Message {
	msg := buildNewsletterPoll(question, options, maxAnswers)
	if msg.PollCreationMessage.GetSelectableOptionsCount() == 1 {
		return &waE2E.Message{PollCreationMessageV3: msg.PollCreationMessage}
	}
	return msg
}

// buildNewsletterPollNode builds the raw message node of a channel poll: type="poll",
// the serialized proto in plaintext and a meta child with polltype="creation".
func buildNewsletterPollNode(to types.JID, id types.MessageID, msg *waE2E.Message) (waBinary.Node, error) {
	plaintext, err := proto.Marshal(msg)
	if err != nil {
		return waBinary.Node{}, fmt.Errorf("failed to marshal poll: %w", err)
	}
	return waBinary.Node{
		Tag:   "message",
		Attrs: waBinary.Attrs{"to": to, "id": id, "type": "poll"},
		Content: []waBinary.Node{
			{Tag: "plaintext", Attrs: waBinary.Attrs{}, Content: plaintext},
			{Tag: "meta", Attrs: waBinary.Attrs{"polltype": "creation"}},
		},
	}, nil
}

// parseNewsletterAck reads the server ack of a sent node the way whatsmeow's send.go does.
func parseNewsletterAck(node *waBinary.Node) (types.MessageServerID, time.Time, error) {
	ag := node.AttrGetter()
	serverID := types.MessageServerID(ag.OptionalInt("server_id"))
	timestamp := ag.UnixTime("t")
	if code := ag.Int("error"); code != 0 {
		return serverID, timestamp, fmt.Errorf("%w %d", whatsmeow.ErrServerReturnedError, code)
	}
	return serverID, timestamp, nil
}

// SendNewsletterNode sends a raw node and waits for its ack (DangerousInternals).
func (c newsletterClient) SendNewsletterNode(ctx context.Context, id types.MessageID, node waBinary.Node) (types.MessageServerID, time.Time, error) {
	internals := c.Client.DangerousInternals()
	ch := internals.WaitResponse(string(id))
	defer internals.CancelResponse(string(id), ch)
	if _, err := internals.SendNodeAndGetData(ctx, node); err != nil {
		return 0, time.Time{}, fmt.Errorf("failed to send message node: %w", err)
	}
	select {
	case ack := <-ch:
		return parseNewsletterAck(ack)
	case <-ctx.Done():
		return 0, time.Time{}, ctx.Err()
	}
}
