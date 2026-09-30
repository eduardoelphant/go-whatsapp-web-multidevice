package lidmap

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// newStore opens a real whatsmeow store in a temp SQLite file, so a whatsmeow upgrade that
// renames whatsmeow_lid_map or its columns fails here.
func newStore(t *testing.T) (*sqlstore.Container, string, string) {
	t.Helper()
	dsn := sqlite.FormatChatStorageURI("file:"+filepath.Join(t.TempDir(), "whatsapp.db"), true, true)
	container, err := sqlstore.New(context.Background(), sqlite.DriverName, dsn, waLog.Noop)
	require.NoError(t, err)
	t.Cleanup(func() { _ = container.Close() })
	return container, sqlite.DriverName, dsn
}

func put(t *testing.T, c *sqlstore.Container, lid, pn string) {
	t.Helper()
	require.NoError(t, c.LIDMap.PutLIDMapping(context.Background(),
		types.NewJID(lid, types.HiddenUserServer), types.NewJID(pn, types.DefaultUserServer)))
}

func TestListReadsTheRealWhatsmeowTable(t *testing.T) {
	c, driver, dsn := newStore(t)
	put(t, c, "333", "5511900000003")
	put(t, c, "111", "5511900000001")
	put(t, c, "222", "5511900000002")

	r, err := Open(driver, dsn)
	require.NoError(t, err)
	defer r.Close()

	got, err := r.List(context.Background(), "", 10)
	require.NoError(t, err)
	assert.Equal(t, []Pair{{"111", "5511900000001"}, {"222", "5511900000002"}, {"333", "5511900000003"}}, got)
}

func TestListKeysetPaginationNeitherSkipsNorRepeats(t *testing.T) {
	c, driver, dsn := newStore(t)
	for i, lid := range []string{"101", "102", "103", "104", "105"} {
		put(t, c, lid, "55119000000"+string(rune('0'+i)))
	}
	r, err := Open(driver, dsn)
	require.NoError(t, err)
	defer r.Close()

	var all []string
	after := ""
	for {
		page, err := r.List(context.Background(), after, 2)
		require.NoError(t, err)
		for _, p := range page {
			all = append(all, p.LID)
		}
		if len(page) < 2 {
			break
		}
		after = page[len(page)-1].LID
	}
	assert.Equal(t, []string{"101", "102", "103", "104", "105"}, all)
}

func TestListEmptyTable(t *testing.T) {
	_, driver, dsn := newStore(t)
	r, err := Open(driver, dsn)
	require.NoError(t, err)
	defer r.Close()

	got, err := r.List(context.Background(), "", 10)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestOpenFailsOnAnUnknownDriver(t *testing.T) {
	_, err := Open("nope", "x")
	assert.Error(t, err)
}
