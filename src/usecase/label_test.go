package usecase

import (
	"errors"
	"testing"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types"
)

func TestNextLabelIDUsesKnownMaxPlusOne(t *testing.T) {
	synced := false
	id, err := nextLabelID(func() (int64, bool) { return 17, true }, func() error { synced = true; return nil })
	require.NoError(t, err)
	assert.Equal(t, "18", id)
	assert.False(t, synced)
}

func TestNextLabelIDSyncsWhenNoneKnown(t *testing.T) {
	var max int64
	known := false
	id, err := nextLabelID(
		func() (int64, bool) { return max, known },
		func() error { max, known = 41, true; return nil },
	)
	require.NoError(t, err)
	assert.Equal(t, "42", id)
}

func TestNextLabelIDEmptyAccountStartsAtOne(t *testing.T) {
	id, err := nextLabelID(func() (int64, bool) { return 0, false }, func() error { return nil })
	require.NoError(t, err)
	assert.Equal(t, "1", id)
}

func TestNextLabelIDSyncFailureIs502(t *testing.T) {
	_, err := nextLabelID(func() (int64, bool) { return 0, false }, func() error { return errors.New("boom") })
	require.Error(t, err)
	var sc interface{ StatusCode() int }
	require.True(t, errors.As(err, &sc))
	assert.Equal(t, 502, sc.StatusCode())
}

func TestLabelPatchBuilders(t *testing.T) {
	up := buildLabelUpsertPatch("18", "VIP", 4)
	require.Len(t, up.Mutations, 1)
	assert.Equal(t, []string{appstate.IndexLabelEdit, "18"}, up.Mutations[0].Index)
	action := up.Mutations[0].Value.GetLabelEditAction()
	assert.Equal(t, "VIP", action.GetName())
	assert.Equal(t, int32(4), action.GetColor())
	assert.False(t, action.GetDeleted())

	del := buildLabelDeletePatch("18", "VIP")
	assert.True(t, del.Mutations[0].Value.GetLabelEditAction().GetDeleted())
	assert.Equal(t, []string{appstate.IndexLabelEdit, "18"}, del.Mutations[0].Index)

	jid := types.NewJID("5511999999999", types.DefaultUserServer)
	ch := buildLabelChatPatch(jid, "18", true)
	assert.Equal(t, []string{appstate.IndexLabelAssociationChat, "18", jid.String()}, ch.Mutations[0].Index)
	assert.True(t, ch.Mutations[0].Value.GetLabelAssociationAction().GetLabeled())
}

func TestEmptyAccountSyncedOnceThenIncrements(t *testing.T) {
	client := &whatsmeow.Client{}
	syncs := 0
	known := func() (int64, bool) { return whatsapp.KnownMaxLabelID(client) }
	syncNow := func() error { syncs++; whatsapp.MarkLabelIDsKnown(client); return nil }

	id1, err := nextLabelID(known, syncNow)
	require.NoError(t, err)
	assert.Equal(t, "1", id1)
	whatsapp.NoteLabelID(client, id1)

	id2, err := nextLabelID(known, syncNow)
	require.NoError(t, err)
	assert.Equal(t, "2", id2)
	assert.Equal(t, 1, syncs)
}
