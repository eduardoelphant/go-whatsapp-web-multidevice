package baileysimport

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"go.mau.fi/util/dbutil"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/store/sqlstore/upgrades"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// ErrDeviceExists is returned by Write when the target store already holds a
// device with the imported JID and Replace was not requested.
var ErrDeviceExists = errors.New("a device with this JID already exists in the store")

// Store is a whatsmeow SQL store opened for an import. It wraps the same
// schema sqlstore.New manages, and keeps the dbutil handle so that the whole
// import runs in one transaction and pre-keys can be inserted with their
// original ids (store.PreKeyStore has no method for that).
type Store struct {
	db        *dbutil.Database
	Container *sqlstore.Container
}

// NewStore wraps an open database/sql handle (driver "sqlite3", "sqlite" or
// "postgres") and upgrades the whatsmeow schema, like sqlstore.New does.
func NewStore(ctx context.Context, db *sql.DB, dialect string, log waLog.Logger) (*Store, error) {
	wrapped, err := dbutil.NewWithDB(db, dialect)
	if err != nil {
		return nil, err
	}
	wrapped.UpgradeTable = upgrades.Table
	wrapped.VersionTable = "whatsmeow_version"
	container := sqlstore.NewWithWrappedDB(wrapped, log)
	if err := container.Upgrade(ctx); err != nil {
		return nil, fmt.Errorf("upgrade whatsmeow schema: %w", err)
	}
	return &Store{db: wrapped, Container: container}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.Container.Close()
}

// WriteOptions controls Write.
type WriteOptions struct {
	// Replace deletes an existing device with the same JID (and, through
	// foreign keys, all of its keys) before importing.
	Replace bool
	// Keys is the optional separate keys database (GOWA's --db-keys-uri). When
	// set, identities, sessions, pre-keys and sender keys go there, matching
	// the stores GOWA redirects, and the device row is written to both.
	Keys *Store
}

// Direct SQL, isolated here because whatsmeow's PreKeyStore only generates
// new pre-keys: the imported ones must keep the ids peers and the server know.
const (
	insertPreKeyQuery        = `INSERT INTO whatsmeow_pre_keys (jid, key_id, key, uploaded) VALUES ($1, $2, $3, $4)`
	deletePrivacyTokensQuery = `DELETE FROM whatsmeow_privacy_tokens WHERE our_jid=$1`
)

// Write persists a Plan into target inside a single transaction (one per
// database when a separate keys store is used). Nothing is written if any
// step fails.
func Write(ctx context.Context, target *Store, plan *Plan, opts WriteOptions) error {
	if target == nil || plan == nil || plan.Device == nil {
		return fmt.Errorf("nothing to write")
	}
	keysStore := opts.Keys
	if keysStore == target {
		keysStore = nil
	}
	run := func(ctx context.Context) error {
		return writePlan(ctx, target, keysStore, plan, opts.Replace)
	}
	return target.db.DoTxn(ctx, nil, func(ctx context.Context) error {
		if keysStore == nil {
			return run(ctx)
		}
		return keysStore.db.DoTxn(ctx, nil, run)
	})
}

func writePlan(ctx context.Context, target, keysStore *Store, plan *Plan, replace bool) error {
	jid := plan.Device.ID
	if err := prepareSlot(ctx, target, jid, replace); err != nil {
		return err
	}
	if keysStore != nil {
		if err := prepareSlot(ctx, keysStore, jid, replace); err != nil {
			return fmt.Errorf("keys store: %w", err)
		}
	}

	dev := target.Container.NewDevice()
	plan.Device.apply(dev)
	if err := target.Container.PutDevice(ctx, dev); err != nil {
		return fmt.Errorf("put device: %w", err)
	}

	primary := sqlstore.NewSQLStore(target.Container, jid)
	sessionStore, sessionDB := primary, target.db
	if keysStore != nil {
		keysDev := *dev
		if err := keysStore.Container.PutDevice(ctx, &keysDev); err != nil {
			return fmt.Errorf("put device in keys store: %w", err)
		}
		sessionStore, sessionDB = sqlstore.NewSQLStore(keysStore.Container, jid), keysStore.db
	}

	for _, k := range plan.PreKeys {
		if _, err := sessionDB.Exec(ctx, insertPreKeyQuery, jid.String(), k.ID, k.Private[:], k.Uploaded); err != nil {
			return fmt.Errorf("put pre-key %d: %w", k.ID, err)
		}
	}
	if len(plan.Sessions) > 0 {
		if err := sessionStore.PutManySessions(ctx, plan.Sessions); err != nil {
			return fmt.Errorf("put sessions: %w", err)
		}
	}
	for address, key := range plan.Identities {
		if err := sessionStore.PutIdentity(ctx, address, key); err != nil {
			return fmt.Errorf("put identity %s: %w", address, err)
		}
	}
	for _, sk := range plan.SenderKeys {
		if err := sessionStore.PutSenderKey(ctx, sk.ChatID, sk.SenderID, sk.Record); err != nil {
			return fmt.Errorf("put sender key %s/%s: %w", sk.ChatID, sk.SenderID, err)
		}
	}
	for _, k := range plan.AppStateSyncKeys {
		if err := primary.PutAppStateSyncKey(ctx, k.ID, k.Key); err != nil {
			return fmt.Errorf("put app state sync key: %w", err)
		}
	}
	if len(plan.PrivacyTokens) > 0 {
		if err := primary.PutPrivacyTokens(ctx, plan.PrivacyTokens...); err != nil {
			return fmt.Errorf("put privacy tokens: %w", err)
		}
	}
	if len(plan.LIDMappings) > 0 {
		if err := target.Container.LIDMap.PutManyLIDMappings(ctx, plan.LIDMappings); err != nil {
			return fmt.Errorf("put lid mappings: %w", err)
		}
	}
	return nil
}

// prepareSlot refuses (or, with replace, clears) an existing device row.
// whatsmeow_privacy_tokens has no foreign key to the device, so it is
// cleared explicitly.
func prepareSlot(ctx context.Context, s *Store, jid types.JID, replace bool) error {
	existing, err := s.Container.GetDevice(ctx, jid)
	if err != nil {
		return fmt.Errorf("look up existing device: %w", err)
	}
	if existing == nil {
		return nil
	}
	if !replace {
		return fmt.Errorf("%w: %s", ErrDeviceExists, jid)
	}
	if err := s.Container.DeleteDevice(ctx, existing); err != nil {
		return fmt.Errorf("delete existing device: %w", err)
	}
	if _, err := s.db.Exec(ctx, deletePrivacyTokensQuery, jid.String()); err != nil {
		return fmt.Errorf("delete existing privacy tokens: %w", err)
	}
	return nil
}

// SiblingDevices lists other companion devices of the same phone number
// already in the store. GOWA leaves a store row alone when another slot
// already claims its number, so the caller should warn about them.
func SiblingDevices(ctx context.Context, s *Store, jid types.JID) ([]types.JID, error) {
	devices, err := s.Container.GetAllDevices(ctx)
	if err != nil {
		return nil, err
	}
	var out []types.JID
	for _, d := range devices {
		if d == nil || d.ID == nil {
			continue
		}
		if d.ID.User == jid.User && *d.ID != jid {
			out = append(out, *d.ID)
		}
	}
	return out, nil
}
