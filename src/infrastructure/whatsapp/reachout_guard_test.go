package whatsapp

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

var (
	guardNow   = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	guardLocks = ReachoutSnapshot{Active: true, Source: ReachoutSourceEvent}
	guardPN    = types.NewJID("5511988887777", types.DefaultUserServer)
	guardLID   = types.NewJID("100000000000001", types.HiddenUserServer)
)

// lookups builds fake lookups. tokenAt is when the stored token was issued (zero means no
// token); pnToLID maps the phone to a LID; asked records the JIDs the token lookup received.
func lookups(tokenAt time.Time, tokenErr error, pnToLID types.JID, asked *[]types.JID) reachoutLookups {
	return reachoutLookups{
		Token: func(_ context.Context, jid types.JID) (*store.PrivacyToken, error) {
			if asked != nil {
				*asked = append(*asked, jid)
			}
			if tokenErr != nil {
				return nil, tokenErr
			}
			if tokenAt.IsZero() {
				return nil, nil
			}
			return &store.PrivacyToken{User: jid, Token: []byte("tok"), Timestamp: tokenAt}, nil
		},
		LID: func(context.Context, types.JID) (types.JID, error) { return pnToLID, nil },
	}
}

func TestGuardBlocksLockedAccountWithoutToken(t *testing.T) {
	err := reachoutBlocks(context.Background(), true, guardLocks, guardPN, lookups(time.Time{}, nil, types.EmptyJID, nil), guardNow)

	var guard pkgError.WaReachoutGuardError
	if !errors.As(err, &guard) {
		t.Fatalf("err = %v, want WaReachoutGuardError", err)
	}
	if guard.ErrCode() != "WA_REACHOUT_GUARD" || guard.StatusCode() != http.StatusConflict {
		t.Fatalf("code=%s status=%d", guard.ErrCode(), guard.StatusCode())
	}
}

func TestGuardAllowsWithAValidToken(t *testing.T) {
	err := reachoutBlocks(context.Background(), true, guardLocks, guardPN, lookups(guardNow.Add(-24*time.Hour), nil, types.EmptyJID, nil), guardNow)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestGuardBlocksWithAnExpiredToken(t *testing.T) {
	// Older than the 28-day window of four 7-day buckets.
	err := reachoutBlocks(context.Background(), true, guardLocks, guardPN, lookups(guardNow.Add(-40*24*time.Hour), nil, types.EmptyJID, nil), guardNow)
	if err == nil {
		t.Fatal("an expired token must not count")
	}
}

func TestGuardAllowsGroupAndNewsletter(t *testing.T) {
	for _, jid := range []types.JID{
		types.NewJID("120363000000000000", types.GroupServer),
		types.NewJID("120363000000000001", types.NewsletterServer),
	} {
		if err := reachoutBlocks(context.Background(), true, guardLocks, jid, lookups(time.Time{}, nil, types.EmptyJID, nil), guardNow); err != nil {
			t.Errorf("%s: err = %v, want nil", jid.Server, err)
		}
	}
}

func TestGuardAllowsWhileClearedOrDisabled(t *testing.T) {
	none := lookups(time.Time{}, nil, types.EmptyJID, nil)
	if err := reachoutBlocks(context.Background(), true, ReachoutSnapshot{}, guardPN, none, guardNow); err != nil {
		t.Errorf("cleared: err = %v", err)
	}
	if err := reachoutBlocks(context.Background(), false, guardLocks, guardPN, none, guardNow); err != nil {
		t.Errorf("flag off: err = %v", err)
	}
}

func TestGuardAllowsOnATokenLookupError(t *testing.T) {
	err := reachoutBlocks(context.Background(), true, guardLocks, guardPN, lookups(time.Time{}, errors.New("db down"), types.EmptyJID, nil), guardNow)
	if err != nil {
		t.Fatalf("a lookup error must let the send through, got %v", err)
	}
}

func TestGuardLooksTheTokenUpByLIDWhenMapped(t *testing.T) {
	var asked []types.JID
	reachoutBlocks(context.Background(), true, guardLocks, guardPN, lookups(time.Time{}, nil, guardLID, &asked), guardNow)
	if len(asked) != 1 || asked[0] != guardLID {
		t.Fatalf("token asked for %v, want the LID %v", asked, guardLID)
	}

	asked = nil
	reachoutBlocks(context.Background(), true, guardLocks, guardLID, lookups(time.Time{}, nil, types.EmptyJID, &asked), guardNow)
	if len(asked) != 1 || asked[0] != guardLID {
		t.Fatalf("a LID recipient is looked up as is, asked %v", asked)
	}
}

func TestGuardMessageCarriesTheEndWhenKnown(t *testing.T) {
	ends := guardNow.Add(2 * time.Hour)
	snap := ReachoutSnapshot{Active: true, Source: ReachoutSourceEvent, EndsAt: &ends}

	err := reachoutBlocks(context.Background(), true, snap, guardPN, lookups(time.Time{}, nil, types.EmptyJID, nil), guardNow)

	if err == nil || !strings.Contains(err.Error(), ends.UTC().Format(time.RFC3339)) {
		t.Fatalf("message %v should carry the end %s", err, ends.UTC().Format(time.RFC3339))
	}
}

func TestCheckReachoutGuardIsANoOpWithoutInstanceOrClient(t *testing.T) {
	if err := CheckReachoutGuard(context.Background(), nil, nil, guardPN); err != nil {
		t.Fatalf("nil instance: %v", err)
	}
	if err := CheckReachoutGuard(context.Background(), NewDeviceInstance("guard-noop", nil, nil), nil, guardPN); err != nil {
		t.Fatalf("nil client: %v", err)
	}
}

// D-11 4: bots, PSA and the account's own JIDs have no tctoken and are never refused.
func TestGuardExemptsBotsPSAAndTheAccountItself(t *testing.T) {
	none := lookups(time.Time{}, nil, types.EmptyJID, nil)
	ownPN := types.NewJID("5511900000099", types.DefaultUserServer)
	ownLID := types.NewJID("200000000000009", types.HiddenUserServer)
	none.Self = []types.JID{ownPN, ownLID}

	for name, jid := range map[string]types.JID{
		"bot":     types.NewJID("867051314767696", types.BotServer),
		"psa":     types.PSAJID,
		"own pn":  ownPN,
		"own lid": ownLID,
	} {
		if err := reachoutBlocks(context.Background(), true, guardLocks, jid, none, guardNow); err != nil {
			t.Errorf("%s: err = %v, want nil", name, err)
		}
	}
	if err := reachoutBlocks(context.Background(), true, guardLocks, guardPN, none, guardNow); err == nil {
		t.Error("an ordinary recipient must still be refused")
	}
}
