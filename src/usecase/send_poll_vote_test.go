package usecase

import (
	"context"
	"testing"

	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mau.fi/whatsmeow/types"
)

type pollVoteRepoStub struct {
	domainChatStorage.IChatStorageRepository
	messages    map[string]*domainChatStorage.Message
	definitions map[string]*domainChatStorage.PollDefinition
}

func (r *pollVoteRepoStub) GetMessageByIDChatAndDevice(deviceID, chatJID, id string) (*domainChatStorage.Message, error) {
	return r.messages[deviceID+"|"+chatJID+"|"+id], nil
}

func (r *pollVoteRepoStub) GetPollDefinition(deviceID, chatJID, id string) (*domainChatStorage.PollDefinition, error) {
	return r.definitions[deviceID+"|"+chatJID+"|"+id], nil
}

type fakePollAddressing struct {
	pn       *types.JID
	lid      types.JID
	lidGroup bool
	lids     map[string]types.JID
}

func (f fakePollAddressing) OwnPN() *types.JID                          { return f.pn }
func (f fakePollAddressing) OwnLID() types.JID                          { return f.lid }
func (f fakePollAddressing) IsLIDGroup(context.Context, types.JID) bool { return f.lidGroup }
func (f fakePollAddressing) LIDForPN(_ context.Context, pn types.JID) (types.JID, error) {
	return f.lids[pn.User], nil
}

func TestResolvePollVoteTargetLIDGroup(t *testing.T) {
	device := "628000@s.whatsapp.net"
	group := types.NewJID("120363000000", types.GroupServer)
	sender := types.NewJID("628111", types.DefaultUserServer)
	senderLID := types.NewJID("9991", types.HiddenUserServer)
	noLID := types.NewJID("628222", types.DefaultUserServer)
	own := types.NewJID("628000", types.DefaultUserServer)
	ownLID := types.NewJID("8880", types.HiddenUserServer)
	ctx := whatsapp.ContextWithDevice(context.Background(), whatsapp.NewDeviceInstance(device, nil, nil))
	repo := &pollVoteRepoStub{
		messages: map[string]*domainChatStorage.Message{
			device + "|" + group.String() + "|IN":  {ID: "IN", Sender: sender.String()},
			device + "|" + group.String() + "|IN2": {ID: "IN2", Sender: noLID.String()},
			device + "|" + group.String() + "|OUT": {ID: "OUT", Sender: own.String(), IsFromMe: true},
		},
		definitions: map[string]*domainChatStorage.PollDefinition{
			device + "|" + group.String() + "|FRESH": {PollMessageID: "FRESH"},
		},
	}
	service := serviceSend{chatStorageRepo: repo}
	lidAddr := fakePollAddressing{pn: &own, lid: ownLID, lidGroup: true, lids: map[string]types.JID{"628111": senderLID}}
	pnAddr := fakePollAddressing{pn: &own, lid: ownLID, lids: lidAddr.lids}

	info, _, err := service.resolvePollVoteTarget(ctx, group, "OUT", lidAddr)
	require.NoError(t, err)
	assert.Equal(t, ownLID, info.Sender)

	info, _, err = service.resolvePollVoteTarget(ctx, group, "IN", lidAddr)
	require.NoError(t, err)
	assert.Equal(t, senderLID, info.Sender)

	info, _, err = service.resolvePollVoteTarget(ctx, group, "IN2", lidAddr)
	require.NoError(t, err)
	assert.Equal(t, noLID, info.Sender, "no mapping: keeps stored sender")

	info, _, err = service.resolvePollVoteTarget(ctx, group, "IN", pnAddr)
	require.NoError(t, err)
	assert.Equal(t, sender, info.Sender)
	info, _, err = service.resolvePollVoteTarget(ctx, group, "OUT", pnAddr)
	require.NoError(t, err)
	assert.Equal(t, own, info.Sender)

	// Message row not saved yet, definition exists: the poll is ours.
	info, def, err := service.resolvePollVoteTarget(ctx, group, "FRESH", lidAddr)
	require.NoError(t, err)
	assert.NotNil(t, def)
	assert.True(t, info.IsFromMe)
	assert.Equal(t, ownLID, info.Sender)
	info, _, err = service.resolvePollVoteTarget(ctx, group, "FRESH", pnAddr)
	require.NoError(t, err)
	assert.Equal(t, own, info.Sender)
}

func TestResolvePollVoteTarget(t *testing.T) {
	device := "628000@s.whatsapp.net"
	group := types.NewJID("120363000000", types.GroupServer)
	sender := types.NewJID("628111", types.DefaultUserServer)
	own := types.NewJID("628000", types.DefaultUserServer)
	ctx := whatsapp.ContextWithDevice(context.Background(), whatsapp.NewDeviceInstance(device, nil, nil))
	repo := &pollVoteRepoStub{
		messages: map[string]*domainChatStorage.Message{
			device + "|" + group.String() + "|IN":  {ID: "IN", Sender: sender.String()},
			device + "|" + group.String() + "|OUT": {ID: "OUT", Sender: own.String(), IsFromMe: true},
		},
		definitions: map[string]*domainChatStorage.PollDefinition{},
	}
	service := serviceSend{chatStorageRepo: repo}

	info, _, err := service.resolvePollVoteTarget(ctx, group, "IN", fakePollAddressing{pn: &own})
	require.NoError(t, err)
	assert.Equal(t, sender, info.Sender)
	assert.Equal(t, group, info.Chat)
	assert.False(t, info.IsFromMe)
	assert.True(t, info.IsGroup)

	info, _, err = service.resolvePollVoteTarget(ctx, group, "OUT", fakePollAddressing{pn: &own})
	require.NoError(t, err)
	assert.Equal(t, own, info.Sender)
	assert.True(t, info.IsFromMe)

	_, _, err = service.resolvePollVoteTarget(ctx, group, "NOPE", fakePollAddressing{pn: &own})
	assert.Equal(t, pkgError.ErrPollNotFound, err)

	// Another device's context cannot see this device's poll.
	other := whatsapp.ContextWithDevice(context.Background(), whatsapp.NewDeviceInstance("other@s.whatsapp.net", nil, nil))
	_, _, err = service.resolvePollVoteTarget(other, group, "IN", fakePollAddressing{pn: &own})
	assert.Equal(t, pkgError.ErrPollNotFound, err)
}

func TestValidatePollVoteOptions(t *testing.T) {
	definition := &domainChatStorage.PollDefinition{
		SelectableOptionCount: 1,
		Options:               []domainChatStorage.PollOption{{Name: "Sim"}, {Name: "Não"}},
	}
	assert.NoError(t, validatePollVoteOptions(definition, []string{"Sim"}))
	assert.NoError(t, validatePollVoteOptions(definition, nil), "empty retracts the vote")
	assert.IsType(t, pkgError.UnprocessableError(""), validatePollVoteOptions(definition, []string{"Talvez"}))
	assert.IsType(t, pkgError.UnprocessableError(""), validatePollVoteOptions(definition, []string{"Sim", "Não"}))
	assert.NoError(t, validatePollVoteOptions(nil, []string{"qualquer"}), "no definition: cannot check")
}
