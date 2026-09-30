package usecase

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.mau.fi/whatsmeow/proto/waVnameCert"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

type fakeChecker struct {
	answers   []types.IsOnWhatsAppResponse
	err       error
	lidForPN  map[string]types.JID
	gotPhones []string
	calls     int
}

func (f *fakeChecker) IsOnWhatsApp(_ context.Context, phones []string) ([]types.IsOnWhatsAppResponse, error) {
	f.calls++
	f.gotPhones = phones
	return f.answers, f.err
}

func (f *fakeChecker) LIDForPN(_ context.Context, pn types.JID) (types.JID, error) {
	if lid, ok := f.lidForPN[pn.String()]; ok {
		return lid, nil
	}
	return types.EmptyJID, errors.New("no mapping")
}

func pnJID(digits string) types.JID  { return types.NewJID(digits, types.DefaultUserServer) }
func lidJID(digits string) types.JID { return types.NewJID(digits, types.HiddenUserServer) }

func str(s string) *string { return &s }

func TestRunCheckBatchStates(t *testing.T) {
	f := &fakeChecker{
		answers: []types.IsOnWhatsAppResponse{
			{Query: "+5511988887777", JID: lidJID("111"), PhoneNumber: pnJID("5511988887777"), IsIn: true},
			{Query: "+5511900000000", IsIn: false},
			// "5511911112222" is omitted by WhatsApp: not_exists.
		},
	}
	got := runCheckBatch(context.Background(), f, []string{"+55 11 98888-7777", "5511900000000", "5511911112222"})

	assert.Equal(t, []string{"+5511988887777", "+5511900000000", "+5511911112222"}, f.gotPhones)
	assert.Equal(t, "exists", got[0].Status)
	assert.Equal(t, "5511988887777", got[0].Query)
	assert.Equal(t, str("5511988887777@s.whatsapp.net"), got[0].PN)
	assert.Equal(t, str("111@lid"), got[0].LID)
	assert.Nil(t, got[0].Error)

	assert.Equal(t, "not_exists", got[1].Status)
	assert.Nil(t, got[1].PN)
	assert.Nil(t, got[1].LID)
	assert.Equal(t, "not_exists", got[2].Status)
}

func TestRunCheckBatchPNDifferentFromQuery(t *testing.T) {
	// Brazilian ninth digit: asked without the 9, WhatsApp answers with it.
	f := &fakeChecker{answers: []types.IsOnWhatsAppResponse{
		{Query: "+551188887777", JID: pnJID("5511988887777"), PhoneNumber: pnJID("5511988887777"), IsIn: true},
	}}
	got := runCheckBatch(context.Background(), f, []string{"551188887777"})

	assert.Equal(t, "exists", got[0].Status)
	assert.Equal(t, "551188887777", got[0].Query)
	assert.Equal(t, str("5511988887777@s.whatsapp.net"), got[0].PN)
}

func TestRunCheckBatchLIDSources(t *testing.T) {
	f := &fakeChecker{
		answers: []types.IsOnWhatsAppResponse{
			// pn JID answer with no LID: the local map fills it.
			{Query: "+5511911110001", JID: pnJID("5511911110001"), PhoneNumber: pnJID("5511911110001"), IsIn: true},
			// no LID anywhere: null.
			{Query: "+5511911110002", JID: pnJID("5511911110002"), IsIn: true},
		},
		lidForPN: map[string]types.JID{"5511911110001@s.whatsapp.net": lidJID("222")},
	}
	got := runCheckBatch(context.Background(), f, []string{"5511911110001", "5511911110002"})

	assert.Equal(t, str("222@lid"), got[0].LID)
	assert.Equal(t, "exists", got[1].Status)
	assert.Equal(t, str("5511911110002@s.whatsapp.net"), got[1].PN)
	assert.Nil(t, got[1].LID)
}

func TestRunCheckBatchVerifiedName(t *testing.T) {
	f := &fakeChecker{answers: []types.IsOnWhatsAppResponse{
		{Query: "+5511911110003", JID: pnJID("5511911110003"), IsIn: true,
			VerifiedName: &types.VerifiedName{Details: &waVnameCert.VerifiedNameCertificate_Details{VerifiedName: proto.String("Loja Exemplo")}}},
	}}
	got := runCheckBatch(context.Background(), f, []string{"5511911110003"})

	assert.Equal(t, str("Loja Exemplo"), got[0].VerifiedName)
}

func TestRunCheckBatchCallErrorNeverMeansNotExists(t *testing.T) {
	f := &fakeChecker{err: errors.New("usync timeout")}
	got := runCheckBatch(context.Background(), f, []string{"5511988887777", "abc", "5511977776666"})

	assert.Equal(t, "error", got[0].Status)
	assert.Equal(t, str("upstream"), got[0].Error)
	assert.Equal(t, "error", got[1].Status)
	assert.Equal(t, str("invalid_number"), got[1].Error) // stays invalid_number, not upstream
	assert.Equal(t, "error", got[2].Status)
	assert.Equal(t, str("upstream"), got[2].Error)
	for _, item := range got {
		assert.NotEqual(t, "not_exists", item.Status)
	}
}

func TestRunCheckBatchDuplicatesAndOrder(t *testing.T) {
	f := &fakeChecker{answers: []types.IsOnWhatsAppResponse{
		{Query: "+5511988887777", JID: pnJID("5511988887777"), PhoneNumber: pnJID("5511988887777"), IsIn: true},
	}}
	got := runCheckBatch(context.Background(), f, []string{"5511988887777", "12", "+5511988887777", "5511988887777@s.whatsapp.net"})

	assert.Equal(t, []string{"+5511988887777"}, f.gotPhones) // asked once
	assert.Len(t, got, 4)
	assert.Equal(t, "exists", got[0].Status)
	assert.Equal(t, "error", got[1].Status)
	assert.Equal(t, "12", got[1].Query)
	assert.Equal(t, "exists", got[2].Status)
	assert.Equal(t, "exists", got[3].Status)
}

func TestRunCheckBatchAllInvalidSkipsTheCall(t *testing.T) {
	f := &fakeChecker{}
	got := runCheckBatch(context.Background(), f, []string{"12", "x@lid"})

	assert.Equal(t, 0, f.calls)
	assert.Equal(t, "error", got[0].Status)
	assert.Equal(t, "error", got[1].Status)
}
