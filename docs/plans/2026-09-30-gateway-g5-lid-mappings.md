# G5 LID Mappings Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans (inline, chosen by the owner). Steps use checkbox (`- [ ]`) syntax.

**Goal:** `GET /lids/pn/:phone`, `GET /lids/:lid`, `POST /lids/lookup` and `GET /lids` expose the LID and phone pairs the gateway knows.

**Architecture:** Lookups go through the request device's `Store.LIDs` behind a small `lidStore` interface. The list reads `whatsmeow_lid_map` through a second read-only handle in a new `pkg/lidmap`, opened lazily with the store's own driver and DSN. The REST layer only parses and scopes the device.

**Tech Stack:** Go 1.26 (`GOTOOLCHAIN=auto`, from `src/`), Fiber v3, whatsmeow `sqlstore`, testify.

**Spec:** `docs/specs/2026-09-30-gateway-g5-lid-mappings-design.md`

## Global Constraints

- Run Go from `src/` with `GOTOOLCHAIN=auto`; `src/go.mod` stays unchanged.
- Transport in `ui/`, orchestration in `usecase/`, DTOs and interfaces in `domains/` (AGENTS.md).
- Read only: no write or delete of mappings. `docs/openapi.yaml` untouched.
- Batch lookup at most 500 entries; list `limit` default 100, clamped to 1000.
- Strings in answers: `<digits>@s.whatsapp.net` and `<digits>@lid`. Logs carry counts only.
- No `t.Parallel()` in tests that touch globals. Commits in English, no attribution. Push, tag and release only with the owner's OK.

## Review Focus

- `/lids/pn/x` must never be read as a LID, and `/lids/lookup` never as a LID (Task 4 route test).
- A `lid` or `pn` that is not known answers `null`, not an error (Task 3 test).
- The list must not skip or repeat a pair across pages (Task 1 keyset test).
- A whatsmeow upgrade that renames the table or its columns must fail a test (Task 1 contract test).
- More than 500 entries and an empty body answer `400` (Task 3 test).

## File map

| File | Responsibility |
|---|---|
| `pkg/lidmap/lidmap.go` (+ test) | `Open`, `Reader.List`: the only code that knows the table name |
| `domains/lid/lid.go` | DTOs and `ILIDUsecase` |
| `validations/lid_validation.go` (+ test) | `NormalizeLID`, `ValidateLookup`, `ClampListLimit` |
| `usecase/lid.go` (+ test) | lookups, list, lazy reader |
| `ui/rest/lid.go` (+ test) | routes |
| `cmd/rest.go`, `cmd/root.go` | wiring |
| `docs/reference/elphant-fork.md`, `docs/elphant-debt.md` | docs |

---

### Task 1: `pkg/lidmap`

**Produces:** `lidmap.Pair{LID, PN string}` (user parts), `lidmap.Open(driver, dsn string) (*Reader, error)`, `(*Reader).List(ctx, after string, limit int) ([]Pair, error)`, `(*Reader).Close() error`.

- [ ] **Step 1: tests that fail** (`pkg/lidmap/lidmap_test.go`)

```go
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
```

- [ ] **Step 2:** `cd src && GOTOOLCHAIN=auto go test ./pkg/lidmap/` → FAIL to compile.
- [ ] **Step 3: implement** `pkg/lidmap/lidmap.go`:

```go
// Package lidmap reads the LID to phone pairs whatsmeow keeps in whatsmeow_lid_map. The
// table has no device column: the pairs are shared by every device of the gateway.
package lidmap

import (
	"context"
	"database/sql"
	"fmt"
)

// Pair is one mapping, as the user parts stored by whatsmeow (no server suffix).
type Pair struct {
	LID string
	PN  string
}

// Reader is a read-only handle on the store database.
type Reader struct {
	db       *sql.DB
	postgres bool
}

// Open opens a handle with the driver and DSN the whatsmeow store uses.
func Open(driver, dsn string) (*Reader, error) {
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("open lid map: %w", err)
	}
	return &Reader{db: db, postgres: driver == "postgres"}, nil
}

func (r *Reader) Close() error { return r.db.Close() }

// List returns up to limit pairs ordered by lid, starting after the given lid (empty for the
// first page). The keyset on lid stays stable while the table grows.
func (r *Reader) List(ctx context.Context, after string, limit int) ([]Pair, error) {
	query := "SELECT lid, pn FROM whatsmeow_lid_map WHERE lid > ? ORDER BY lid LIMIT ?"
	if r.postgres {
		query = "SELECT lid, pn FROM whatsmeow_lid_map WHERE lid > $1 ORDER BY lid LIMIT $2"
	}
	rows, err := r.db.QueryContext(ctx, query, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list lid map: %w", err)
	}
	defer rows.Close()

	var pairs []Pair
	for rows.Next() {
		var p Pair
		if err := rows.Scan(&p.LID, &p.PN); err != nil {
			return nil, fmt.Errorf("scan lid map: %w", err)
		}
		pairs = append(pairs, p)
	}
	return pairs, rows.Err()
}
```

- [ ] **Step 4:** `go test ./pkg/lidmap/ -race` passes. **Step 5:** commit `feat(lid): read the whatsmeow LID map with keyset pagination`.

---

### Task 2: DTOs and validation

**Produces:** `domains/lid` types and `ILIDUsecase`; `validations.NormalizeLID(raw) (string, bool)`, `validations.ValidateLookup(ctx, domainLID.LookupRequest) error`, `validations.ClampListLimit(limit int) int`, constants `LookupMaxEntries = 500`, `ListDefaultLimit = 100`, `ListMaxLimit = 1000`.

- [ ] **Step 1: tests that fail** (`validations/lid_validation_test.go`)

```go
package validations

import (
	"context"
	"fmt"
	"testing"

	domainLID "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/lid"
	"github.com/stretchr/testify/assert"
)

func TestNormalizeLID(t *testing.T) {
	tests := []struct {
		raw    string
		digits string
		ok     bool
	}{
		{"100000000000001", "100000000000001", true},
		{"100000000000001@lid", "100000000000001", true},
		{"  100000000000001@lid ", "100000000000001", true},
		{"5511999999999@s.whatsapp.net", "", false},
		{"abc", "", false},
		{"", "", false},
		{"@lid", "", false},
		{"123456789012345678901", "", false}, // 21 digits
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			digits, ok := NormalizeLID(tt.raw)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.digits, digits)
		})
	}
}

func TestValidateLookup(t *testing.T) {
	ctx := context.Background()
	many := make([]string, LookupMaxEntries+1)
	for i := range many {
		many[i] = fmt.Sprintf("55119%08d", i)
	}
	assert.NoError(t, ValidateLookup(ctx, domainLID.LookupRequest{PNs: []string{"5511999999999"}}))
	assert.NoError(t, ValidateLookup(ctx, domainLID.LookupRequest{LIDs: []string{"100000000000001"}}))
	assert.NoError(t, ValidateLookup(ctx, domainLID.LookupRequest{PNs: many[:300], LIDs: many[:200]}))
	assert.Error(t, ValidateLookup(ctx, domainLID.LookupRequest{}))
	assert.Error(t, ValidateLookup(ctx, domainLID.LookupRequest{PNs: many[:300], LIDs: many[:201]}))
	assert.Error(t, ValidateLookup(ctx, domainLID.LookupRequest{PNs: many}))
}

func TestClampListLimit(t *testing.T) {
	assert.Equal(t, ListDefaultLimit, ClampListLimit(0))
	assert.Equal(t, ListDefaultLimit, ClampListLimit(-5))
	assert.Equal(t, 50, ClampListLimit(50))
	assert.Equal(t, ListMaxLimit, ClampListLimit(ListMaxLimit))
	assert.Equal(t, ListMaxLimit, ClampListLimit(ListMaxLimit+1))
}
```

- [ ] **Step 2:** `go test ./validations/ -run 'NormalizeLID|ValidateLookup|ClampListLimit'` → FAIL to compile.
- [ ] **Step 3: implement.** `domains/lid/lid.go`:

```go
// Package lid holds the DTOs of the LID and phone mapping endpoints (fork: elphant).
package lid

import "context"

// PNItem answers a phone lookup; LID is null when the pair is unknown.
type PNItem struct {
	PN  string  `json:"pn"`
	LID *string `json:"lid"`
}

// LIDItem answers a LID lookup; PN is null when the pair is unknown.
type LIDItem struct {
	LID string  `json:"lid"`
	PN  *string `json:"pn"`
}

type LookupRequest struct {
	PNs  []string `json:"pns"`
	LIDs []string `json:"lids"`
}

type LookupResponse struct {
	PNs  []PNItem  `json:"pns"`
	LIDs []LIDItem `json:"lids"`
}

type ListRequest struct {
	Limit int    `query:"limit"`
	After string `query:"after"`
}

type ListItem struct {
	LID string `json:"lid"`
	PN  string `json:"pn"`
}

// ListResponse pages through every pair the gateway knows. Next is null on the last page.
type ListResponse struct {
	Items []ListItem `json:"items"`
	Next  *string    `json:"next"`
}

type ILIDUsecase interface {
	PNToLID(ctx context.Context, phone string) (PNItem, error)
	LIDToPN(ctx context.Context, lid string) (LIDItem, error)
	Lookup(ctx context.Context, request LookupRequest) (LookupResponse, error)
	List(ctx context.Context, request ListRequest) (ListResponse, error)
}
```

`validations/lid_validation.go`:

```go
package validations

import (
	"context"
	"strings"

	domainLID "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/lid"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
)

const (
	LookupMaxEntries = 500
	ListDefaultLimit = 100
	ListMaxLimit     = 1000
	lidMaxDigits     = 20
)

// NormalizeLID reduces a LID to its digits, with or without the @lid suffix.
func NormalizeLID(raw string) (string, bool) {
	value := strings.TrimSpace(raw)
	value = strings.TrimSuffix(value, "@lid")
	if value == "" || len(value) > lidMaxDigits {
		return "", false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	return value, true
}

// ValidateLookup checks the size of a batch lookup; entries are normalized one by one later.
func ValidateLookup(_ context.Context, request domainLID.LookupRequest) error {
	total := len(request.PNs) + len(request.LIDs)
	if total == 0 {
		return pkgError.ValidationError("pns or lids is required")
	}
	if total > LookupMaxEntries {
		return pkgError.ValidationError("at most 500 entries per lookup")
	}
	return nil
}

// ClampListLimit applies the default and the maximum page size.
func ClampListLimit(limit int) int {
	if limit <= 0 {
		return ListDefaultLimit
	}
	if limit > ListMaxLimit {
		return ListMaxLimit
	}
	return limit
}
```

- [ ] **Step 4:** tests pass. **Step 5:** commit `feat(lid): DTOs and input validation for the LID endpoints`.

---

### Task 3: Usecase

**Consumes:** `lidmap.Pair`, `validations.*`, `domainLID.*`. **Produces:** `usecase.NewLIDService(dbURI string) domainLID.ILIDUsecase`, internal `lidStore` and `pairLister` interfaces.

- [ ] **Step 1: tests that fail** (`usecase/lid_test.go`)

```go
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
	pairs := []lidmap.Pair{{"101", "5511900000001"}, {"102", "5511900000002"}, {"103", "5511900000003"}}
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
```

- [ ] **Step 2:** `go test ./usecase/ -run 'LID|Lookup|ListPages|ListFailure|BadPhone|StoreError'` → FAIL to compile (`serviceLID` undefined).
- [ ] **Step 3: implement** `usecase/lid.go`:

```go
package usecase

import (
	"context"
	"errors"
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

func requestLIDStore(ctx context.Context) (lidStore, error) {
	client := whatsapp.ClientFromContext(ctx)
	if client == nil || client.Store == nil || client.Store.LIDs == nil {
		return nil, pkgError.ErrWaCLI
	}
	return client.Store.LIDs, nil
}

func pnJID(digits string) types.JID  { return types.NewJID(digits, types.DefaultUserServer) }
func lidJID(digits string) types.JID { return types.NewJID(digits, types.HiddenUserServer) }

func (s serviceLID) PNToLID(ctx context.Context, phone string) (domainLID.PNItem, error) {
	digits, ok := validations.NormalizeBatchPhone(phone)
	if !ok {
		return domainLID.PNItem{}, pkgError.ValidationError("invalid phone")
	}
	store, err := s.storeFor(ctx)
	if err != nil {
		return domainLID.PNItem{}, err
	}
	lid, err := store.GetLIDForPN(ctx, pnJID(digits))
	if err != nil {
		return domainLID.PNItem{}, fmt.Errorf("lookup lid for phone: %w", err)
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
	pn, err := store.GetPNForLID(ctx, lidJID(digits))
	if err != nil {
		return domainLID.LIDItem{}, fmt.Errorf("lookup phone for lid: %w", err)
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
			jids = append(jids, pnJID(digits))
		}
		found, err := store.GetManyLIDsForPNs(ctx, jids)
		if err != nil {
			return domainLID.LookupResponse{}, fmt.Errorf("lookup lids for phones: %w", err)
		}
		for i, digits := range pns {
			response.PNs[i] = pnItem(digits, found[pnJID(digits)])
		}
	}
	for i, digits := range lids {
		pn, err := store.GetPNForLID(ctx, lidJID(digits))
		if err != nil {
			return domainLID.LookupResponse{}, fmt.Errorf("lookup phone for lid: %w", err)
		}
		response.LIDs[i] = lidItem(digits, pn)
	}

	logrus.Infof("LID lookup: pns=%d lids=%d", len(pns), len(lids))
	return response, nil
}

func (s serviceLID) List(ctx context.Context, request domainLID.ListRequest) (domainLID.ListResponse, error) {
	limit := validations.ClampListLimit(request.Limit)
	pairs, err := s.lister.List(ctx, request.After, limit)
	if err != nil {
		logrus.Errorf("LID list failed: %v", err)
		return domainLID.ListResponse{}, pkgError.InternalServerError("failed to list lid mappings")
	}

	response := domainLID.ListResponse{Items: make([]domainLID.ListItem, 0, len(pairs))}
	for _, p := range pairs {
		response.Items = append(response.Items, domainLID.ListItem{
			LID: lidJID(p.LID).String(),
			PN:  pnJID(p.PN).String(),
		})
	}
	if len(pairs) == limit {
		next := pairs[len(pairs)-1].LID
		response.Next = &next
	}
	logrus.Infof("LID list: items=%d", len(response.Items))
	return response, nil
}

func pnItem(digits string, lid types.JID) domainLID.PNItem {
	item := domainLID.PNItem{PN: pnJID(digits).String()}
	if !lid.IsEmpty() {
		s := lid.ToNonAD().String()
		item.LID = &s
	}
	return item
}

func lidItem(digits string, pn types.JID) domainLID.LIDItem {
	item := domainLID.LIDItem{LID: lidJID(digits).String()}
	if !pn.IsEmpty() {
		s := pn.ToNonAD().String()
		item.PN = &s
	}
	return item
}

// lazyLister opens the read-only handle on first use. A failure is not cached: the next call
// tries again, so a store that was not ready at startup does not break the endpoint for good.
type lazyLister struct {
	dbURI  string
	mu     sync.Mutex
	reader *lidmap.Reader
}

func (l *lazyLister) List(ctx context.Context, after string, limit int) ([]lidmap.Pair, error) {
	l.mu.Lock()
	if l.reader == nil {
		driver, dsn, err := whatsapp.ResolveDBDriver(l.dbURI)
		if err != nil {
			l.mu.Unlock()
			return nil, err
		}
		reader, err := lidmap.Open(driver, dsn)
		if err != nil {
			l.mu.Unlock()
			return nil, err
		}
		l.reader = reader
	}
	reader := l.reader
	l.mu.Unlock()
	return reader.List(ctx, after, limit)
}

var _ = errors.New
```

(Drop the trailing `var _ = errors.New` and the `errors` import if unused after writing.)

- [ ] **Step 4:** `go test ./usecase/ -race` passes. **Step 5:** commit `feat(lid): usecase for the LID and phone lookups and the list`.

---

### Task 4: REST routes and wiring

**Consumes:** `domainLID.ILIDUsecase`. **Produces:** `rest.InitRestLID(app fiber.Router, service domainLID.ILIDUsecase) LID`.

- [ ] **Step 1: tests that fail** (`ui/rest/lid_test.go`)

```go
package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	domainLID "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/lid"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/ui/rest/middleware"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
)

type lidStub struct {
	domainLID.ILIDUsecase
	calls  []string
	device *whatsapp.DeviceInstance
	after  string
	limit  int
}

func (s *lidStub) PNToLID(ctx context.Context, phone string) (domainLID.PNItem, error) {
	s.calls = append(s.calls, "pn:"+phone)
	s.device, _ = whatsapp.DeviceFromContext(ctx)
	return domainLID.PNItem{PN: phone + "@s.whatsapp.net"}, nil
}

func (s *lidStub) LIDToPN(ctx context.Context, lid string) (domainLID.LIDItem, error) {
	s.calls = append(s.calls, "lid:"+lid)
	return domainLID.LIDItem{LID: lid + "@lid"}, nil
}

func (s *lidStub) Lookup(_ context.Context, _ domainLID.LookupRequest) (domainLID.LookupResponse, error) {
	s.calls = append(s.calls, "lookup")
	return domainLID.LookupResponse{PNs: []domainLID.PNItem{}, LIDs: []domainLID.LIDItem{}}, nil
}

func (s *lidStub) List(_ context.Context, request domainLID.ListRequest) (domainLID.ListResponse, error) {
	s.calls = append(s.calls, "list")
	s.after, s.limit = request.After, request.Limit
	return domainLID.ListResponse{Items: []domainLID.ListItem{}}, nil
}

func newLIDApp(stub *lidStub, device *whatsapp.DeviceInstance) *fiber.App {
	app := fiber.New()
	app.Use(middleware.Recovery())
	app.Use(func(c fiber.Ctx) error {
		c.Locals("device", device)
		return c.Next()
	})
	InitRestLID(app, stub)
	return app
}

func do(t *testing.T, app *fiber.App, method, path, body string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	assert.NoError(t, err)
	return resp
}

func TestLIDRoutesReachTheRightMethod(t *testing.T) {
	stub := &lidStub{}
	app := newLIDApp(stub, nil)

	assert.Equal(t, http.StatusOK, do(t, app, http.MethodGet, "/lids/pn/5511988887777", "").StatusCode)
	assert.Equal(t, http.StatusOK, do(t, app, http.MethodGet, "/lids/100000000000001@lid", "").StatusCode)
	assert.Equal(t, http.StatusOK, do(t, app, http.MethodPost, "/lids/lookup", `{"pns":["5511988887777"]}`).StatusCode)
	assert.Equal(t, http.StatusOK, do(t, app, http.MethodGet, "/lids?limit=2&after=101", "").StatusCode)

	// /lids/pn/x is a phone lookup and /lids/lookup a batch, never read as a LID.
	assert.Equal(t, []string{"pn:5511988887777", "lid:100000000000001@lid", "lookup", "list"}, stub.calls)
	assert.Equal(t, "101", stub.after)
	assert.Equal(t, 2, stub.limit)
}

func TestLIDEnvelope(t *testing.T) {
	app := newLIDApp(&lidStub{}, nil)

	resp := do(t, app, http.MethodGet, "/lids/pn/5511988887777", "")

	var body struct {
		Code    string                 `json:"code"`
		Results map[string]interface{} `json:"results"`
	}
	assert.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "SUCCESS", body.Code)
	assert.Contains(t, body.Results, "pn")
	assert.Contains(t, body.Results, "lid") // null, key present
}

func TestLIDMalformedLookupBodyIs400(t *testing.T) {
	app := newLIDApp(&lidStub{}, nil)

	resp := do(t, app, http.MethodPost, "/lids/lookup", `{"pns":`)

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestLIDLookupsScopeTheRequestDevice(t *testing.T) {
	stub := &lidStub{}
	device := &whatsapp.DeviceInstance{}
	app := newLIDApp(stub, device)

	do(t, app, http.MethodGet, "/lids/pn/5511988887777", "")

	assert.Same(t, device, stub.device)
}
```

- [ ] **Step 2:** `go test ./ui/rest/ -run TestLID` → FAIL to compile.
- [ ] **Step 3: implement** `ui/rest/lid.go`:

```go
package rest

import (
	domainLID "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/lid"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"github.com/gofiber/fiber/v3"
)

// LID serves the LID and phone mapping endpoints (fork: elphant, docs/reference/elphant-fork.md).
type LID struct {
	Service domainLID.ILIDUsecase
}

// InitRestLID registers the routes. `/lids/pn/:phone` and the POST lookup never collide with
// `/lids/:lid`, which is registered last.
func InitRestLID(app fiber.Router, service domainLID.ILIDUsecase) LID {
	rest := LID{Service: service}
	app.Get("/lids/pn/:phone", rest.PNToLID)
	app.Post("/lids/lookup", rest.Lookup)
	app.Get("/lids", rest.List)
	app.Get("/lids/:lid", rest.LIDToPN)
	return rest
}

func (controller *LID) PNToLID(c fiber.Ctx) error {
	ctx := whatsapp.ContextWithDevice(c.Context(), getDeviceFromCtx(c))
	response, err := controller.Service.PNToLID(ctx, c.Params("phone"))
	utils.PanicIfNeeded(err)
	return c.JSON(utils.ResponseData{Status: 200, Code: "SUCCESS", Message: "Success lookup lid", Results: response})
}

func (controller *LID) LIDToPN(c fiber.Ctx) error {
	ctx := whatsapp.ContextWithDevice(c.Context(), getDeviceFromCtx(c))
	response, err := controller.Service.LIDToPN(ctx, c.Params("lid"))
	utils.PanicIfNeeded(err)
	return c.JSON(utils.ResponseData{Status: 200, Code: "SUCCESS", Message: "Success lookup phone", Results: response})
}

func (controller *LID) Lookup(c fiber.Ctx) error {
	var request domainLID.LookupRequest
	if err := c.Bind().Body(&request); err != nil {
		return c.Status(400).JSON(utils.ResponseData{Status: 400, Code: "BAD_REQUEST", Message: "Invalid request body"})
	}
	ctx := whatsapp.ContextWithDevice(c.Context(), getDeviceFromCtx(c))
	response, err := controller.Service.Lookup(ctx, request)
	utils.PanicIfNeeded(err)
	return c.JSON(utils.ResponseData{Status: 200, Code: "SUCCESS", Message: "Success lookup lids", Results: response})
}

// List is global to the gateway: whatsmeow's LID map has no device column.
func (controller *LID) List(c fiber.Ctx) error {
	var request domainLID.ListRequest
	if err := c.Bind().Query(&request); err != nil {
		return c.Status(400).JSON(utils.ResponseData{Status: 400, Code: "BAD_REQUEST", Message: "Invalid query"})
	}
	response, err := controller.Service.List(c.Context(), request)
	utils.PanicIfNeeded(err)
	return c.JSON(utils.ResponseData{Status: 200, Code: "SUCCESS", Message: "Success list lids", Results: response})
}
```

Wiring: `cmd/root.go` adds `lidUsecase domainLID.ILIDUsecase` next to `userUsecase` (import `domainLID ".../domains/lid"`) and `lidUsecase = usecase.NewLIDService(config.DBURI)` after `userUsecase = ...`; `cmd/rest.go` adds `rest.InitRestLID(r, lidUsecase)` after `rest.InitRestUser(r, userUsecase)` inside `registerDeviceScopedRoutes`.

- [ ] **Step 4:** `go test ./ui/rest/ ./cmd/... ` and `go build ./...` pass. **Step 5:** commit `feat(lid): REST routes for the LID and phone mappings`.

---

### Task 5: Docs and final checks

- [ ] Add a section "LID and phone mappings" to `docs/reference/elphant-fork.md` (before "Specs and plans"): the four routes with an example each, `null` for unknown pairs, limits, the list being global (table has no device column), that unknown pairs are learned through `POST /user/check`, and that lookups read the store without calling WhatsApp.
- [ ] `docs/elphant-debt.md`: in D-4 remove G5 from the list (G8 and G9 stay); D-3 becomes "Timelock G6" (G7 is done). Keep the file's structure.
- [ ] `cd src && GOTOOLCHAIN=auto go vet ./... && GOTOOLCHAIN=auto go test ./...` → PASS.
- [ ] Commit `docs: document the LID and phone mapping endpoints`.
- [ ] Runtime check after the release (owner's OK for push, tag, image and stack): on devias look up the owner's phone and LID (from `POST /user/check`), list with `limit=2`, and confirm `next` pages.
