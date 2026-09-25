package baileysimport

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	groupRecord "go.mau.fi/libsignal/groups/state/record"
	"go.mau.fi/whatsmeow/store"
)

func loadState(t *testing.T) *AuthState {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "bob_auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	state, err := Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

// A record whose sessions are all closed is skipped, not imported.
func TestSessionWithoutOpenStateIsSkipped(t *testing.T) {
	state := loadState(t)
	name := "session-5511900000002.3"
	closed := regexp.MustCompile(`"closed":\s*-1`).ReplaceAll(state.Keys[name], []byte(`"closed": 1758000000000`))
	state.Keys[name] = closed
	plan, err := Convert(state, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := plan.Sessions["5511900000002:3"]; ok || plan.Skipped["session without open state"] != 1 {
		t.Fatalf("closed-only session imported: skipped=%v", plan.Skipped)
	}
	raw, _ := unwrapJSON(closed, true)
	if _, err := convertSessionRecord(raw, localIdentity{}); !errors.Is(err, ErrNoOpenSession) {
		t.Fatalf("got %v, want ErrNoOpenSession", err)
	}
}

// Baileys keeps sender key states oldest first; go.mau.fi/libsignal newest
// first (state 0 encrypts). Alice rotated her key in the fixture.
func TestSenderKeyStatesAreReversed(t *testing.T) {
	state := loadState(t)
	raw, err := unwrapJSON(state.Keys["sender-key-120363000000000001@g.us::5511900000002::3"], true)
	if err != nil {
		t.Fatal(err)
	}
	var baileysStates []nodeSenderKeyState
	if err := json.Unmarshal(raw, &baileysStates); err != nil || len(baileysStates) != 2 {
		t.Fatalf("fixture states: %d, %v", len(baileysStates), err)
	}
	serialized, _, err := convertSenderKeyRecord(raw)
	if err != nil {
		t.Fatal(err)
	}
	ser := store.SignalProtobufSerializer
	rec, err := groupRecord.NewSenderKeyFromBytes(serialized, ser.SenderKeyRecord, ser.SenderKeyState)
	if err != nil {
		t.Fatal(err)
	}
	current, err := rec.SenderKeyState()
	if err != nil {
		t.Fatal(err)
	}
	if current.KeyID() != baileysStates[1].SenderKeyID {
		t.Fatalf("current state %d, want newest Baileys state %d", current.KeyID(), baileysStates[1].SenderKeyID)
	}
	if _, err := rec.GetSenderKeyStateByID(baileysStates[0].SenderKeyID); err != nil {
		t.Fatalf("older state lost: %v", err)
	}
}

func TestConvertRejectsBrokenKeys(t *testing.T) {
	cases := map[string]string{
		"pre-key-abc":            `{"public":"AA==","private":"AA=="}`,
		"pre-key-4":              `{"public":{"type":"Buffer","data":"AA=="},"private":{"type":"Buffer","data":"AA=="}}`,
		"session-5511.x":         `{}`,
		"sender-key-bad":         `[]`,
		"lid-mapping-5511":       `"not-a-lid"`,
		"app-state-sync-key-!!!": `{"keyData":"AA=="}`,
	}
	for name, value := range cases {
		state := loadState(t)
		state.Keys[name] = json.RawMessage(value)
		if _, err := Convert(state, Options{}); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
