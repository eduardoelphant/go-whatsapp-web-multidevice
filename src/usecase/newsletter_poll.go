package usecase

import (
	"strings"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"go.mau.fi/whatsmeow/proto/waE2E"
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
