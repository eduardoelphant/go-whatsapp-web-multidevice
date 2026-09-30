package whatsapp

import (
	"context"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

// Fork (elphant): send guard for the reach-out timelock (spec 2026-09-30-gateway-g6).

// A tctoken stays valid for four rolling 7-day buckets (about 28 days), the same rule whatsmeow
// applies to the tokens it attaches to a send. whatsmeow keeps its helpers private.
const (
	tcTokenBucketSeconds = 604800
	tcTokenBuckets       = 4
)

func tcTokenValid(token *store.PrivacyToken, now time.Time) bool {
	if token == nil || len(token.Token) == 0 || token.Timestamp.IsZero() {
		return false
	}
	cutoffBucket := now.Unix()/tcTokenBucketSeconds - (tcTokenBuckets - 1)
	return !token.Timestamp.Before(time.Unix(cutoffBucket*tcTokenBucketSeconds, 0))
}

// reachoutLookups are the store reads the guard needs; tests replace them.
type reachoutLookups struct {
	Token func(ctx context.Context, jid types.JID) (*store.PrivacyToken, error)
	LID   func(ctx context.Context, pn types.JID) (types.JID, error)
	Self  []types.JID // the account's own phone and LID: never refused (note to self)
}

// reachoutBlocks decides whether a send must be refused. It blocks only when the guard is on,
// the account is timelocked, the recipient is a user (phone or LID) and the store clearly has no
// valid tctoken for it. Any lookup error lets the send through.
func reachoutBlocks(ctx context.Context, enabled bool, snap ReachoutSnapshot, recipient types.JID, lookups reachoutLookups, now time.Time) error {
	if !enabled || !snap.Active {
		return nil
	}
	recipient = recipient.ToNonAD()
	if recipient.Server != types.DefaultUserServer && recipient.Server != types.HiddenUserServer {
		return nil
	}
	// Bots and PSA have no tctoken (whatsmeow exempts them too), and neither does the account itself.
	if recipient.IsBot() || recipient.User == types.PSAJID.User {
		return nil
	}
	for _, self := range lookups.Self {
		if !self.IsEmpty() && recipient.ToNonAD() == self.ToNonAD() {
			return nil
		}
	}

	key := recipient
	if recipient.Server == types.DefaultUserServer && lookups.LID != nil {
		if lid, err := lookups.LID(ctx, recipient); err == nil && !lid.IsEmpty() {
			key = lid.ToNonAD()
		}
	}

	token, err := lookups.Token(ctx, key)
	if err != nil {
		return nil
	}
	if tcTokenValid(token, now) {
		return nil
	}
	return pkgError.NewWaReachoutGuard(snap.EndsAt)
}

// CheckReachoutGuard is called before a message send. It returns a pkgError.WaReachoutGuardError
// (409) when the send must be refused, nil otherwise. A nil instance or client is a no-op.
func CheckReachoutGuard(ctx context.Context, instance *DeviceInstance, client *whatsmeow.Client, recipient types.JID) error {
	if instance == nil || client == nil || client.Store == nil || client.Store.PrivacyTokens == nil {
		return nil
	}
	owner := canonicalInstance(instance)
	now := time.Now()

	snap, expired := owner.reachout.read(now)
	if expired {
		EmitSessionTimelock(ctx, owner, snap)
	}

	lookups := reachoutLookups{Token: client.Store.PrivacyTokens.GetPrivacyToken}
	if client.Store.ID != nil {
		lookups.Self = append(lookups.Self, *client.Store.ID)
	}
	if !client.Store.LID.IsEmpty() {
		lookups.Self = append(lookups.Self, client.Store.LID)
	}
	if client.Store.LIDs != nil {
		lookups.LID = client.Store.LIDs.GetLIDForPN
	}
	return reachoutBlocks(ctx, config.WhatsappReachoutGuard, snap, recipient, lookups, now)
}
