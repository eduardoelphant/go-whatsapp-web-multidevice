package usecase

import (
	"testing"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/stretchr/testify/assert"
)

func TestNewsletterPollModeCaiEmPlain(t *testing.T) {
	old := config.WhatsappNewsletterPollMode
	t.Cleanup(func() { config.WhatsappNewsletterPollMode = old })
	for value, want := range map[string]string{"": "plain", "xyz": "plain", "plain": "plain", "node": "node", " Node ": "node"} {
		config.WhatsappNewsletterPollMode = value
		assert.Equal(t, want, newsletterPollMode(), "valor %q", value)
	}
}
