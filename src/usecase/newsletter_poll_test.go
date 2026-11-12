package usecase

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainNewsletter "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/newsletter"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

func TestNewsletterPollModeCaiEmPlain(t *testing.T) {
	old := config.WhatsappNewsletterPollMode
	t.Cleanup(func() { config.WhatsappNewsletterPollMode = old })
	for value, want := range map[string]string{"": "plain", "xyz": "plain", "plain": "plain", "node": "node", " Node ": "node"} {
		config.WhatsappNewsletterPollMode = value
		assert.Equal(t, want, newsletterPollMode(), "valor %q", value)
	}
}

func pollNodeChild(t *testing.T, node waBinary.Node, tag string) waBinary.Node {
	t.Helper()
	children, ok := node.Content.([]waBinary.Node)
	require.True(t, ok)
	for _, child := range children {
		if child.Tag == tag {
			return child
		}
	}
	t.Fatalf("filho %q ausente", tag)
	return waBinary.Node{}
}

func TestBuildNewsletterPollNode(t *testing.T) {
	to := types.NewJID("120363000000000001", types.NewsletterServer)
	cases := map[string]struct {
		max    int
		wantV3 bool
	}{"uma resposta usa V3": {1, true}, "várias usam V1": {2, false}}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			msg := newsletterNodePollMessage("Qual?", []string{"A", "B", "C"}, tc.max)
			node, err := buildNewsletterPollNode(to, "ID1", msg)
			require.NoError(t, err)
			assert.Equal(t, "message", node.Tag)
			assert.Equal(t, "poll", node.Attrs["type"])
			assert.Equal(t, "ID1", node.Attrs["id"])
			assert.Equal(t, to, node.Attrs["to"])
			assert.Equal(t, "creation", pollNodeChild(t, node, "meta").Attrs["polltype"])
			var decoded waE2E.Message
			require.NoError(t, proto.Unmarshal(pollNodeChild(t, node, "plaintext").Content.([]byte), &decoded))
			assert.Nil(t, decoded.GetMessageContextInfo())
			assert.Equal(t, tc.wantV3, decoded.GetPollCreationMessageV3() != nil)
			assert.Equal(t, !tc.wantV3, decoded.GetPollCreationMessage() != nil)
		})
	}
}

func TestParseNewsletterAck(t *testing.T) {
	ok := &waBinary.Node{Tag: "ack", Attrs: waBinary.Attrs{"server_id": "77", "t": "1700000000"}}
	serverID, ts, err := parseNewsletterAck(ok)
	require.NoError(t, err)
	assert.Equal(t, types.MessageServerID(77), serverID)
	assert.Equal(t, int64(1700000000), ts.Unix())

	_, _, err = parseNewsletterAck(&waBinary.Node{Tag: "ack", Attrs: waBinary.Attrs{"error": "479", "t": "1700000000"}})
	assert.ErrorIs(t, err, whatsmeow.ErrServerReturnedError)
}

func TestNewsletterSendEnqueteModoNo(t *testing.T) {
	old := config.WhatsappNewsletterPollMode
	t.Cleanup(func() { config.WhatsappNewsletterPollMode = old })
	config.WhatsappNewsletterPollMode = "node"
	req := domainNewsletter.SendRequest{NewsletterID: sendTestJID, Type: "poll", PollQuestion: "Qual?", PollOptions: []string{"A", "B"}}

	t.Run("ack com server_id vira a resposta", func(t *testing.T) {
		api := &fakeNewsletterAPI{nodeServerID: 55, nodeAt: time.Unix(1700000000, 0).UTC()}
		got, err := newSendService(api).Send(context.Background(), req)
		require.NoError(t, err)
		assert.Equal(t, "GEN-ID", got.MessageID)
		assert.Equal(t, 55, got.ServerID)
		assert.Equal(t, "2023-11-14T22:13:20Z", got.Timestamp)
		assert.Equal(t, "poll", api.nodeSent.Attrs["type"])
		assert.Equal(t, "creation", pollNodeChild(t, api.nodeSent, "meta").Attrs["polltype"])
		assert.Nil(t, api.sendMsg, "não usa o SendMessage")
	})
	t.Run("message_id do pedido é mantido", func(t *testing.T) {
		api := &fakeNewsletterAPI{}
		withID := req
		withID.MessageID = "MINE"
		got, err := newSendService(api).Send(context.Background(), withID)
		require.NoError(t, err)
		assert.Equal(t, "MINE", got.MessageID)
	})
	t.Run("erro 479 vira SEND_REFUSED", func(t *testing.T) {
		api := &fakeNewsletterAPI{nodeErr: fmt.Errorf("%w 479", whatsmeow.ErrServerReturnedError)}
		_, err := newSendService(api).Send(context.Background(), req)
		assert.Equal(t, pkgError.SendRefused(api.nodeErr.Error()), err)
	})
}

func (f *fakeNewsletterAPI) GenerateMessageID() types.MessageID { return "GEN-ID" }

func (f *fakeNewsletterAPI) SendNewsletterNode(_ context.Context, _ types.MessageID, node waBinary.Node) (types.MessageServerID, time.Time, error) {
	f.calls = append(f.calls, "node")
	f.nodeSent = node
	return f.nodeServerID, f.nodeAt, f.nodeErr
}
