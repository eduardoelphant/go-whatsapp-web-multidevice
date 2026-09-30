package usecase

import (
	"context"
	"fmt"
	"sync"

	domainLID "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/lid"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/lidmap"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/validations"
	"github.com/sirupsen/logrus"
	"go.mau.fi/whatsmeow/types"
)

// lidStore is the part of whatsmeow's LID map the lookups use.
type lidStore interface {
	GetLIDForPN(ctx context.Context, pn types.JID) (types.JID, error)
	GetPNForLID(ctx context.Context, lid types.JID) (types.JID, error)
	GetManyLIDsForPNs(ctx context.Context, pns []types.JID) (map[types.JID]types.JID, error)
}

// pairLister lists every pair the gateway knows; global to the store database.
type pairLister interface {
	List(ctx context.Context, after string, limit int) ([]lidmap.Pair, error)
	PNsForLIDs(ctx context.Context, lids []string) (map[string]string, error)
}

type serviceLID struct {
	storeFor func(ctx context.Context) (lidStore, error)
	lister   pairLister
}

// NewLIDService lists through a second read-only handle opened on first use with the same
// driver and DSN as the whatsmeow store.
func NewLIDService(dbURI string) domainLID.ILIDUsecase {
	return serviceLID{storeFor: requestLIDStore, lister: &lazyLister{dbURI: dbURI}}
}

// sharedLIDStore is the map every device shares; a variable so tests can replace it.
var sharedLIDStore = func() lidStore {
	if shared := whatsapp.SharedLIDStore(); shared != nil {
		return shared
	}
	return nil
}

// requestLIDStore uses the request device's map, and the shared one when the device has no
// client yet (created, never paired): the pairs are the same table either way.
func requestLIDStore(ctx context.Context) (lidStore, error) {
	if client := whatsapp.ClientFromContext(ctx); client != nil && client.Store != nil && client.Store.LIDs != nil {
		return client.Store.LIDs, nil
	}
	if shared := sharedLIDStore(); shared != nil {
		return shared, nil
	}
	return nil, pkgError.ErrWaCLI
}

// lookupFailed logs the cause and answers a generic 500: driver text never reaches the client.
func lookupFailed(what string, err error) error {
	logrus.Errorf("LID lookup failed (%s): %v", what, err)
	return pkgError.InternalServerError("failed to look up lid mappings")
}

func userJID(digits string) types.JID   { return types.NewJID(digits, types.DefaultUserServer) }
func hiddenJID(digits string) types.JID { return types.NewJID(digits, types.HiddenUserServer) }

func (s serviceLID) PNToLID(ctx context.Context, phone string) (domainLID.PNItem, error) {
	digits, ok := validations.NormalizeBatchPhone(phone)
	if !ok {
		return domainLID.PNItem{}, pkgError.ValidationError("invalid phone")
	}
	store, err := s.storeFor(ctx)
	if err != nil {
		return domainLID.PNItem{}, err
	}
	lid, err := store.GetLIDForPN(ctx, userJID(digits))
	if err != nil {
		return domainLID.PNItem{}, lookupFailed("phone to lid", err)
	}
	return pnItem(digits, lid), nil
}

func (s serviceLID) LIDToPN(ctx context.Context, lid string) (domainLID.LIDItem, error) {
	digits, ok := validations.NormalizeLID(lid)
	if !ok {
		return domainLID.LIDItem{}, pkgError.ValidationError("invalid lid")
	}
	store, err := s.storeFor(ctx)
	if err != nil {
		return domainLID.LIDItem{}, err
	}
	pn, err := store.GetPNForLID(ctx, hiddenJID(digits))
	if err != nil {
		return domainLID.LIDItem{}, lookupFailed("lid to phone", err)
	}
	return lidItem(digits, pn), nil
}

func (s serviceLID) Lookup(ctx context.Context, request domainLID.LookupRequest) (domainLID.LookupResponse, error) {
	if err := validations.ValidateLookup(ctx, request); err != nil {
		return domainLID.LookupResponse{}, err
	}
	pns := make([]string, len(request.PNs))
	for i, raw := range request.PNs {
		digits, ok := validations.NormalizeBatchPhone(raw)
		if !ok {
			return domainLID.LookupResponse{}, pkgError.ValidationError(fmt.Sprintf("invalid phone at pns[%d]", i))
		}
		pns[i] = digits
	}
	lids := make([]string, len(request.LIDs))
	for i, raw := range request.LIDs {
		digits, ok := validations.NormalizeLID(raw)
		if !ok {
			return domainLID.LookupResponse{}, pkgError.ValidationError(fmt.Sprintf("invalid lid at lids[%d]", i))
		}
		lids[i] = digits
	}

	store, err := s.storeFor(ctx)
	if err != nil {
		return domainLID.LookupResponse{}, err
	}

	response := domainLID.LookupResponse{PNs: make([]domainLID.PNItem, len(pns)), LIDs: make([]domainLID.LIDItem, len(lids))}

	if len(pns) > 0 {
		jids := make([]types.JID, 0, len(pns))
		for _, digits := range pns {
			jids = append(jids, userJID(digits))
		}
		found, err := store.GetManyLIDsForPNs(ctx, jids)
		if err != nil {
			return domainLID.LookupResponse{}, lookupFailed("phones to lids", err)
		}
		for i, digits := range pns {
			response.PNs[i] = pnItem(digits, found[userJID(digits)])
		}
	}
	if len(lids) > 0 {
		// One query for every LID, straight from the table: a miss is never cached and no
		// per-entry query runs under whatsmeow's cache lock.
		found, err := s.lister.PNsForLIDs(ctx, lids)
		if err != nil {
			return domainLID.LookupResponse{}, lookupFailed("lids to phones", err)
		}
		for i, digits := range lids {
			var pn types.JID
			if user, ok := found[digits]; ok {
				pn = userJID(user)
			}
			response.LIDs[i] = lidItem(digits, pn)
		}
	}

	logrus.Infof("LID lookup: pns=%d lids=%d", len(pns), len(lids))
	return response, nil
}

func (s serviceLID) List(ctx context.Context, request domainLID.ListRequest) (domainLID.ListResponse, error) {
	limit := validations.ClampListLimit(request.Limit)
	// One extra row tells whether another page exists, so an exact multiple has no empty last page.
	pairs, err := s.lister.List(ctx, request.After, limit+1)
	if err != nil {
		logrus.Errorf("LID list failed: %v", err)
		return domainLID.ListResponse{}, pkgError.InternalServerError("failed to list lid mappings")
	}
	more := len(pairs) > limit
	if more {
		pairs = pairs[:limit]
	}

	response := domainLID.ListResponse{Items: make([]domainLID.ListItem, 0, len(pairs))}
	for _, p := range pairs {
		response.Items = append(response.Items, domainLID.ListItem{
			LID: hiddenJID(p.LID).String(),
			PN:  userJID(p.PN).String(),
		})
	}
	if more {
		next := pairs[len(pairs)-1].LID
		response.Next = &next
	}
	logrus.Infof("LID list: items=%d", len(response.Items))
	return response, nil
}

func pnItem(digits string, lid types.JID) domainLID.PNItem {
	item := domainLID.PNItem{PN: userJID(digits).String()}
	if !lid.IsEmpty() {
		s := lid.ToNonAD().String()
		item.LID = &s
	}
	return item
}

func lidItem(digits string, pn types.JID) domainLID.LIDItem {
	item := domainLID.LIDItem{LID: hiddenJID(digits).String()}
	if !pn.IsEmpty() {
		s := pn.ToNonAD().String()
		item.PN = &s
	}
	return item
}

// lazyLister opens the read-only handle on first use. A failure is not cached: the next call
// tries again, so a store that was not ready at startup does not break the endpoint for good.
type lazyLister struct {
	dbURI string
	mu    sync.Mutex
	r     *lidmap.Reader
}

func (l *lazyLister) reader() (*lidmap.Reader, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.r == nil {
		driver, dsn, err := whatsapp.ResolveDBDriver(l.dbURI)
		if err != nil {
			return nil, err
		}
		r, err := lidmap.Open(driver, dsn)
		if err != nil {
			return nil, err
		}
		l.r = r
	}
	return l.r, nil
}

func (l *lazyLister) List(ctx context.Context, after string, limit int) ([]lidmap.Pair, error) {
	r, err := l.reader()
	if err != nil {
		return nil, err
	}
	return r.List(ctx, after, limit)
}

func (l *lazyLister) PNsForLIDs(ctx context.Context, lids []string) (map[string]string, error) {
	r, err := l.reader()
	if err != nil {
		return nil, err
	}
	return r.PNsForLIDs(ctx, lids)
}

// Close closes the handle if it was opened.
func (l *lazyLister) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.r == nil {
		return nil
	}
	err := l.r.Close()
	l.r = nil
	return err
}
