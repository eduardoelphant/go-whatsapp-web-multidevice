package usecase

import (
	"context"
	"errors"
	"testing"

	domainLID "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/lid"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/lidmap"
	"github.com/stretchr/testify/assert"
	"go.mau.fi/whatsmeow/types"
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
		lidToPN: map[string]string{"222": "5511977776666"},
	}, nil)

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
