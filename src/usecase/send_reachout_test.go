package usecase

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/stretchr/testify/assert"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

type noTokens struct{}

func (noTokens) PutPrivacyTokens(context.Context, ...store.PrivacyToken) error { return nil }
func (noTokens) GetPrivacyToken(context.Context, types.JID) (*store.PrivacyToken, error) {
	return nil, nil
}
func (noTokens) DeleteExpiredPrivacyTokens(context.Context, time.Time) (int64, error) { return 0, nil }

type noLIDs struct{}

func (noLIDs) PutManyLIDMappings(context.Context, []store.LIDMapping) error { return nil }
func (noLIDs) PutLIDMapping(context.Context, types.JID, types.JID) error    { return nil }
func (noLIDs) GetPNForLID(context.Context, types.JID) (types.JID, error)    { return types.EmptyJID, nil }
func (noLIDs) GetLIDForPN(context.Context, types.JID) (types.JID, error)    { return types.EmptyJID, nil }
func (noLIDs) GetManyLIDsForPNs(context.Context, []types.JID) (map[types.JID]types.JID, error) {
	return nil, nil
}

func err463() error {
	return fmt.Errorf("%w %d", whatsmeow.ErrServerReturnedError, 463)
}

func TestNoteReachoutFailureMarksOnlyA463(t *testing.T) {
	inst := whatsapp.NewDeviceInstance("send-463", nil, nil)
	ctx := whatsapp.ContextWithDevice(context.Background(), inst)

	noteReachoutFailure(ctx, nil)
	noteReachoutFailure(ctx, errors.New("boom"))
	noteReachoutFailure(ctx, fmt.Errorf("%w %d", whatsmeow.ErrServerReturnedError, 500))
	assert.False(t, inst.ReachoutSnapshot(time.Now()).Active)

	noteReachoutFailure(ctx, err463())
	snap := inst.ReachoutSnapshot(time.Now())
	assert.True(t, snap.Active)
	assert.Equal(t, whatsapp.ReachoutSourceSend463, snap.Source)
}

func TestNoteReachoutFailureWithoutADeviceIsANoOp(t *testing.T) {
	noteReachoutFailure(context.Background(), err463())
}

func TestWrapSendMessageRefusesWithTheGuardBeforeSending(t *testing.T) {
	previous := config.WhatsappReachoutGuard
	config.WhatsappReachoutGuard = true
	t.Cleanup(func() { config.WhatsappReachoutGuard = previous })

	inst := whatsapp.NewDeviceInstance("send-guard", nil, nil)
	ctx := whatsapp.ContextWithDevice(context.Background(), inst)
	whatsapp.NoteReachoutTimelock(ctx, inst) // the account is now timelocked
	client := &whatsmeow.Client{Store: &store.Device{PrivacyTokens: noTokens{}, LIDs: noLIDs{}}}

	_, err := serviceSend{}.wrapSendMessage(ctx, client, types.NewJID("5511988887777", types.DefaultUserServer), &waE2E.Message{}, "oi")

	var guard pkgError.WaReachoutGuardError
	assert.ErrorAs(t, err, &guard)
	assert.Equal(t, "WA_REACHOUT_GUARD", guard.ErrCode())
}
