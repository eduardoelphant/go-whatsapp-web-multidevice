package usecase

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"

	domainLID "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/lid"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/lidmap"
	"github.com/stretchr/testify/assert"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"
)

type fakeLIDStore struct {
	pnToLID map[string]string // digits to digits
	lidToPN map[string]string
	err     error
}

func (f fakeLIDStore) GetLIDForPN(_ context.Context, pn types.JID) (types.JID, error) {
	if f.err != nil {
		return types.EmptyJID, f.err
	}
	if lid, ok := f.pnToLID[pn.User]; ok {
		return types.NewJID(lid, types.HiddenUserServer), nil
	}
	return types.EmptyJID, nil
}

func (f fakeLIDStore) GetPNForLID(_ context.Context, lid types.JID) (types.JID, error) {
	if f.err != nil {
		return types.EmptyJID, f.err
	}
	if pn, ok := f.lidToPN[lid.User]; ok {
		return types.NewJID(pn, types.DefaultUserServer), nil
	}
	return types.EmptyJID, nil
}

func (f fakeLIDStore) GetManyLIDsForPNs(ctx context.Context, pns []types.JID) (map[types.JID]types.JID, error) {
	out := map[types.JID]types.JID{}
	for _, pn := range pns {
		if lid, _ := f.GetLIDForPN(ctx, pn); !lid.IsEmpty() {
			out[pn] = lid
		}
	}
	return out, f.err
}

type fakeLister struct {
	pairs []lidmap.Pair
	err   error
}

func (f fakeLister) List(_ context.Context, after string, limit int) ([]lidmap.Pair, error) {
	if f.err != nil {
		return nil, f.err
	}
	var out []lidmap.Pair
	for _, p := range f.pairs {
		if p.LID > after && len(out) < limit {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f fakeLister) PNsForLIDs(_ context.Context, lids []string) (map[string]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]string{}
	for _, p := range f.pairs {
		for _, l := range lids {
			if p.LID == l {
				out[l] = p.PN
			}
		}
	}
	return out, nil
}

func newLIDTestService(store lidStore, lister pairLister) serviceLID {
	return serviceLID{storeFor: func(context.Context) (lidStore, error) { return store, nil }, lister: lister}
}

func sp(s string) *string { return &s }

func TestPNToLIDKnownAndUnknown(t *testing.T) {
	svc := newLIDTestService(fakeLIDStore{pnToLID: map[string]string{"5511988887777": "111"}}, nil)

	known, err := svc.PNToLID(context.Background(), "+55 11 98888-7777")
	assert.NoError(t, err)
	assert.Equal(t, domainLID.PNItem{PN: "5511988887777@s.whatsapp.net", LID: sp("111@lid")}, known)

	unknown, err := svc.PNToLID(context.Background(), "5511900000000")
	assert.NoError(t, err)
	assert.Equal(t, domainLID.PNItem{PN: "5511900000000@s.whatsapp.net", LID: nil}, unknown)
}

func TestLIDToPNKnownAndUnknown(t *testing.T) {
	svc := newLIDTestService(fakeLIDStore{lidToPN: map[string]string{"111": "5511988887777"}}, nil)

	known, err := svc.LIDToPN(context.Background(), "111@lid")
	assert.NoError(t, err)
	assert.Equal(t, domainLID.LIDItem{LID: "111@lid", PN: sp("5511988887777@s.whatsapp.net")}, known)

	unknown, err := svc.LIDToPN(context.Background(), "999")
	assert.NoError(t, err)
	assert.Equal(t, domainLID.LIDItem{LID: "999@lid", PN: nil}, unknown)
}

func TestBadPhoneOrLIDIsAValidationError(t *testing.T) {
	svc := newLIDTestService(fakeLIDStore{}, nil)

	_, err := svc.PNToLID(context.Background(), "abc")
	assert.Error(t, err)
	_, err = svc.LIDToPN(context.Background(), "5511999999999@s.whatsapp.net")
	assert.Error(t, err)
}

func TestLookupKeepsOrderAndAnswersNullForUnknown(t *testing.T) {
	svc := newLIDTestService(fakeLIDStore{
		pnToLID: map[string]string{"5511988887777": "111"},
	}, fakeLister{pairs: []lidmap.Pair{{LID: "222", PN: "5511977776666"}}})

	got, err := svc.Lookup(context.Background(), domainLID.LookupRequest{
		PNs:  []string{"5511900000000", "5511988887777", "5511988887777"},
		LIDs: []string{"222@lid", "333"},
	})

	assert.NoError(t, err)
	assert.Len(t, got.PNs, 3)
	assert.Nil(t, got.PNs[0].LID)
	assert.Equal(t, sp("111@lid"), got.PNs[1].LID)
	assert.Equal(t, sp("111@lid"), got.PNs[2].LID)
	assert.Equal(t, sp("5511977776666@s.whatsapp.net"), got.LIDs[0].PN)
	assert.Nil(t, got.LIDs[1].PN)
}

func TestLookupRejectsEmptyAndOversizedAndBadEntries(t *testing.T) {
	svc := newLIDTestService(fakeLIDStore{}, nil)

	_, err := svc.Lookup(context.Background(), domainLID.LookupRequest{})
	assert.Error(t, err)

	many := make([]string, 501)
	for i := range many {
		many[i] = "5511900000000"
	}
	_, err = svc.Lookup(context.Background(), domainLID.LookupRequest{PNs: many})
	assert.Error(t, err)

	_, err = svc.Lookup(context.Background(), domainLID.LookupRequest{PNs: []string{"abc"}})
	assert.Error(t, err)
}

func TestListPagesAndNext(t *testing.T) {
	pairs := []lidmap.Pair{{LID: "101", PN: "5511900000001"}, {LID: "102", PN: "5511900000002"}, {LID: "103", PN: "5511900000003"}}
	svc := newLIDTestService(fakeLIDStore{}, fakeLister{pairs: pairs})

	page1, err := svc.List(context.Background(), domainLID.ListRequest{Limit: 2})
	assert.NoError(t, err)
	assert.Equal(t, []domainLID.ListItem{
		{LID: "101@lid", PN: "5511900000001@s.whatsapp.net"},
		{LID: "102@lid", PN: "5511900000002@s.whatsapp.net"},
	}, page1.Items)
	assert.Equal(t, sp("102"), page1.Next)

	page2, err := svc.List(context.Background(), domainLID.ListRequest{Limit: 2, After: *page1.Next})
	assert.NoError(t, err)
	assert.Len(t, page2.Items, 1)
	assert.Nil(t, page2.Next)
}

func TestListFailureHasNoSQLInTheError(t *testing.T) {
	svc := newLIDTestService(fakeLIDStore{}, fakeLister{err: errors.New("SELECT lid FROM whatsmeow_lid_map boom")})

	_, err := svc.List(context.Background(), domainLID.ListRequest{})

	assert.Error(t, err)
	assert.NotContains(t, err.Error(), "SELECT")
}

func TestStoreErrorPropagatesOnLookups(t *testing.T) {
	svc := newLIDTestService(fakeLIDStore{err: errors.New("db down")}, nil)

	_, err := svc.PNToLID(context.Background(), "5511988887777")
	assert.Error(t, err)
}

// D-10 4: a table whose size is an exact multiple of the limit has no next page.
func TestListHasNoNextWhenTheTableEndsExactlyOnAPage(t *testing.T) {
	pairs := []lidmap.Pair{{LID: "101", PN: "5511900000001"}, {LID: "102", PN: "5511900000002"}}
	svc := newLIDTestService(fakeLIDStore{}, fakeLister{pairs: pairs})

	page, err := svc.List(context.Background(), domainLID.ListRequest{Limit: 2})

	assert.NoError(t, err)
	assert.Len(t, page.Items, 2)
	assert.Nil(t, page.Next)
}

func TestListNextIsTheLastItemOfAFullPageWithMoreBehindIt(t *testing.T) {
	pairs := []lidmap.Pair{{LID: "101", PN: "5511900000001"}, {LID: "102", PN: "5511900000002"}, {LID: "103", PN: "5511900000003"}}
	svc := newLIDTestService(fakeLIDStore{}, fakeLister{pairs: pairs})

	page, err := svc.List(context.Background(), domainLID.ListRequest{Limit: 2})

	assert.NoError(t, err)
	assert.Len(t, page.Items, 2)
	assert.Equal(t, sp("102"), page.Next)
}

// D-10 5: lookup failures do not leak driver text.
func TestLookupFailuresDoNotLeakDriverText(t *testing.T) {
	svc := newLIDTestService(fakeLIDStore{err: errors.New("pq: connection refused at 10.0.0.1")}, nil)

	_, err := svc.PNToLID(context.Background(), "5511988887777")

	assert.Error(t, err)
	assert.NotContains(t, err.Error(), "10.0.0.1")
	var generic pkgError.GenericError
	assert.ErrorAs(t, err, &generic)
	assert.Equal(t, http.StatusInternalServerError, generic.StatusCode())
}

// D-10 6: validation problems are validation errors (400), not plain errors (500).
func TestBadInputIsAValidationError(t *testing.T) {
	svc := newLIDTestService(fakeLIDStore{}, nil)

	_, err := svc.PNToLID(context.Background(), "abc")
	assertValidation(t, err)
	_, err = svc.LIDToPN(context.Background(), "5511999999999@s.whatsapp.net")
	assertValidation(t, err)
	_, err = svc.Lookup(context.Background(), domainLID.LookupRequest{})
	assertValidation(t, err)
	_, err = svc.Lookup(context.Background(), domainLID.LookupRequest{PNs: []string{"abc"}})
	assertValidation(t, err)
}

func assertValidation(t *testing.T, err error) {
	t.Helper()
	var generic pkgError.GenericError
	if assert.ErrorAs(t, err, &generic) {
		assert.Equal(t, http.StatusBadRequest, generic.StatusCode())
	}
}

// D-10 1: a device that was created but never paired has no client; the shared LID map still answers.
func TestRequestLIDStoreFallsBackToTheSharedMapWhenTheDeviceHasNoClient(t *testing.T) {
	shared := fakeLIDStore{pnToLID: map[string]string{"5511988887777": "111"}}
	previous := sharedLIDStore
	sharedLIDStore = func() lidStore { return shared }
	t.Cleanup(func() { sharedLIDStore = previous })

	got, err := requestLIDStore(context.Background())

	assert.NoError(t, err)
	lid, _ := got.GetLIDForPN(context.Background(), userJID("5511988887777"))
	assert.Equal(t, "111", lid.User)
}

func TestRequestLIDStoreWithNothingAvailableIsErrWaCLI(t *testing.T) {
	previous := sharedLIDStore
	sharedLIDStore = func() lidStore { return nil }
	t.Cleanup(func() { sharedLIDStore = previous })

	_, err := requestLIDStore(context.Background())

	assert.ErrorIs(t, err, pkgError.ErrWaCLI)
}

// D-10 6: a lookup and a list on a real whatsmeow store, not only on fakes.
func TestLIDServiceOnARealStore(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "whatsapp.db")
	uri := "file:" + dbPath
	driver, dsn, err := whatsapp.ResolveDBDriver(uri)
	assert.NoError(t, err)
	container, err := sqlstore.New(context.Background(), driver, dsn, waLog.Noop)
	assert.NoError(t, err)
	t.Cleanup(func() { _ = container.Close() })
	assert.NoError(t, container.LIDMap.PutLIDMapping(context.Background(), hiddenJID("111"), userJID("5511988887777")))
	assert.NoError(t, container.LIDMap.PutLIDMapping(context.Background(), hiddenJID("222"), userJID("5511977776666")))

	lister := &lazyLister{dbURI: uri}
	t.Cleanup(func() { _ = lister.Close() })
	svc := serviceLID{storeFor: func(context.Context) (lidStore, error) { return container.LIDMap, nil }, lister: lister}

	got, err := svc.Lookup(context.Background(), domainLID.LookupRequest{
		PNs:  []string{"5511988887777", "5511900000000"},
		LIDs: []string{"222@lid", "999"},
	})
	assert.NoError(t, err)
	assert.Equal(t, sp("111@lid"), got.PNs[0].LID)
	assert.Nil(t, got.PNs[1].LID)
	assert.Equal(t, sp("5511977776666@s.whatsapp.net"), got.LIDs[0].PN)
	assert.Nil(t, got.LIDs[1].PN)

	page, err := svc.List(context.Background(), domainLID.ListRequest{Limit: 1})
	assert.NoError(t, err)
	assert.Len(t, page.Items, 1)
	assert.Equal(t, sp("111"), page.Next)
}
