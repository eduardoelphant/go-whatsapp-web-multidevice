package usecase

import (
	"encoding/json"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/types"
)

func TestParticipantStatuses(t *testing.T) {
	own := types.NewJID("5511900000000", types.DefaultUserServer)
	pn := types.NewJID("5511911111111", types.DefaultUserServer)
	lid := types.NewJID("99887766", types.HiddenUserServer)
	ownLID := types.NewJID("55443322", types.HiddenUserServer)
	req := &types.GroupParticipantAddRequest{}

	cases := []struct {
		name    string
		in      types.GroupParticipant
		want    int
		status  string
		message string
		code    int
		phone   string
	}{
		{"sucesso", types.GroupParticipant{JID: pn}, 1, "success", "Action success", 0, ""},
		{"403 com AddRequest", types.GroupParticipant{JID: pn, Error: 403, AddRequest: req}, 1, "error", "Failed to add participant", 403, ""},
		{"403 sem AddRequest", types.GroupParticipant{JID: pn, Error: 403}, 1, "error", "Failed to add participant (code 403)", 403, ""},
		{"408", types.GroupParticipant{JID: pn, Error: 408}, 1, "error", "Failed to add participant (code 408)", 408, ""},
		{"409", types.GroupParticipant{JID: pn, Error: 409}, 1, "error", "Failed to add participant (code 409)", 409, ""},
		{"com telefone", types.GroupParticipant{JID: pn, PhoneNumber: pn}, 1, "success", "Action success", 0, pn.String()},
		{"LID com telefone", types.GroupParticipant{JID: lid, PhoneNumber: pn}, 1, "success", "Action success", 0, pn.String()},
		{"próprio por JID", types.GroupParticipant{JID: own}, 0, "", "", 0, ""},
		{"próprio por telefone", types.GroupParticipant{JID: lid, PhoneNumber: own}, 0, "", "", 0, ""},
		{"próprio só por LID", types.GroupParticipant{JID: ownLID}, 0, "", "", 0, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := participantStatuses([]types.GroupParticipant{c.in}, own, ownLID)
			if len(got) != c.want {
				t.Fatalf("len = %d, want %d", len(got), c.want)
			}
			if c.want == 0 {
				return
			}
			g := got[0]
			if g.Status != c.status || g.Message != c.message || g.Code != c.code || g.PhoneNumber != c.phone {
				t.Fatalf("got %+v", g)
			}
		})
	}
}

func TestParticipantStatusesSerializaCodeNoSucesso(t *testing.T) {
	got := participantStatuses([]types.GroupParticipant{{JID: types.NewJID("5511911111111", types.DefaultUserServer)}}, types.EmptyJID)
	b, _ := json.Marshal(got[0])
	if !strings.Contains(string(b), `"code":0`) {
		t.Fatalf("code ausente: %s", b)
	}
	if strings.Contains(string(b), "phone_number") {
		t.Fatalf("phone_number deveria sumir: %s", b)
	}
}

func TestRequestParticipantStatuses(t *testing.T) {
	pn := types.NewJID("5511911111111", types.DefaultUserServer)
	got := requestParticipantStatuses([]types.GroupParticipant{{JID: pn}, {JID: pn, Error: 409}}, "approve")
	if got[0].Status != "success" || got[0].Code != 0 || got[0].Message != "Action approve success" {
		t.Fatalf("got %+v", got[0])
	}
	if got[1].Status != "error" || got[1].Code != 409 || got[1].Message != "Action approve failed (code 409)" {
		t.Fatalf("got %+v", got[1])
	}
	rej := requestParticipantStatuses([]types.GroupParticipant{{JID: pn}, {JID: pn, Error: 409}}, "reject")
	if rej[0].Message != "Action reject success" || rej[1].Code != 409 || rej[1].Message != "Action reject failed (code 409)" {
		t.Fatalf("got %+v", rej)
	}
}
