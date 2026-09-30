# G7 Batch User Check Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans (inline, chosen by the owner) to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `POST /user/check` checks up to 100 numbers in one call and returns `exists`, `not_exists` or `error` per number, with the WhatsApp `pn` and `lid`.

**Architecture:** A new usecase method builds the items from one `client.IsOnWhatsApp` call behind a small `phoneChecker` interface (fake in tests). A per-device pacer serializes calls and spaces them. The REST handler scopes the device from the request and returns the items under the usual envelope.

**Tech Stack:** Go 1.26 (`GOTOOLCHAIN=auto`, run from `src/`), Fiber v3, whatsmeow, ozzo-validation, testify.

**Spec:** `docs/specs/2026-09-29-gateway-g7-batch-user-check-design.md`

## Global Constraints

- Run Go from `src/` with `GOTOOLCHAIN=auto`; `src/go.mod` stays unchanged.
- Transport parsing in `ui/`, orchestration in `usecase/`, DTOs and interfaces in `domains/` (AGENTS.md).
- `GET /user/check` and `docs/openapi.yaml` stay untouched.
- Batch size 1 to 100. Phone digits 7 to 15. Timeout 20 s per call. Default minimum interval `500` ms (`WHATSAPP_USER_CHECK_MIN_INTERVAL_MS`).
- Logs carry counts only, never numbers.
- Tests that mutate config or package globals restore them and stay serial (no `t.Parallel()`).
- Commit messages in English, no attribution line. Push, tag and release only with the owner's OK.

## Review Focus

- Brazilian ninth digit: WhatsApp returns a `pn` different from the query; the item must carry the returned `pn` and keep `query` (Task 3 test).
- A whole-call error must never produce `not_exists` (Task 3 test).
- Duplicate entries and invalid entries in the same batch keep their positions (Task 3 test).
- A waiter whose request is cancelled must not call WhatsApp (Task 2 test).
- `X-Device-Id` must select the client, not the default one (Task 4 test).

## File map

| File | Responsibility |
|---|---|
| `domains/user/account.go` | `CheckBatchRequest`, `CheckBatchItem`, `CheckBatchResponse` |
| `domains/user/interfaces.go` | `IsOnWhatsAppBatch` on `IUserInfo` |
| `validations/user_validation.go` (+ test) | `ValidateCheckBatch`, `NormalizeBatchPhone` |
| `config/settings.go`, `cmd/root.go`, `.env.example` | `WhatsappUserCheckMinIntervalMs` |
| `usecase/user_check_pacer.go` (+ test) | per-key serialized, spaced execution |
| `usecase/user_check_batch.go` (+ test) | `phoneChecker`, item building, `IsOnWhatsAppBatch` |
| `ui/rest/user.go`, `ui/rest/user_check_batch_test.go` | `POST /user/check` |
| `docs/reference/elphant-fork.md` | endpoint documentation |

---

### Task 1: DTOs, interface and input validation

**Files:**
- Modify: `src/domains/user/account.go`, `src/domains/user/interfaces.go`, `src/validations/user_validation.go`
- Test: `src/validations/user_validation_test.go`

**Interfaces:**
- Produces: `domainUser.CheckBatchRequest{Phones []string}`, `domainUser.CheckBatchItem`, `domainUser.CheckBatchResponse` (`[]CheckBatchItem`), `IUserInfo.IsOnWhatsAppBatch(ctx, CheckBatchRequest) (CheckBatchResponse, error)`, `validations.ValidateCheckBatch(ctx, request) error`, `validations.NormalizeBatchPhone(raw string) (string, bool)`, `validations.CheckBatchMaxPhones = 100`.

- [ ] **Step 1: Write the failing tests** (append to `validations/user_validation_test.go`; add `strings` and `fmt` to its imports)

```go
func TestValidateCheckBatch(t *testing.T) {
	ctx := context.Background()
	many := make([]string, CheckBatchMaxPhones+1)
	for i := range many {
		many[i] = fmt.Sprintf("55119%08d", i)
	}
	assert.NoError(t, ValidateCheckBatch(ctx, domainUser.CheckBatchRequest{Phones: []string{"5511999999999"}}))
	assert.NoError(t, ValidateCheckBatch(ctx, domainUser.CheckBatchRequest{Phones: many[:CheckBatchMaxPhones]}))
	assert.Error(t, ValidateCheckBatch(ctx, domainUser.CheckBatchRequest{}))
	assert.Error(t, ValidateCheckBatch(ctx, domainUser.CheckBatchRequest{Phones: []string{}}))
	assert.Error(t, ValidateCheckBatch(ctx, domainUser.CheckBatchRequest{Phones: many}))
}

func TestNormalizeBatchPhone(t *testing.T) {
	tests := []struct {
		raw    string
		digits string
		ok     bool
	}{
		{"5511999999999", "5511999999999", true},
		{"+55 11 98888-7777", "5511988887777", true},
		{"(55) 11 97777.6666", "5511977776666", true},
		{"5511977776666@s.whatsapp.net", "5511977776666", true},
		{"  5511977776666  ", "5511977776666", true},
		{"123456", "", false},                      // 6 digits
		{"1234567", "1234567", true},               // 7 digits
		{"123456789012345", "123456789012345", true}, // 15 digits
		{"1234567890123456", "", false},            // 16 digits
		{"123456789@lid", "", false},
		{"120363000000000000@g.us", "", false},
		{"5511999999999:12@s.whatsapp.net", "", false}, // device suffix
		{"55119999abc99", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			digits, ok := NormalizeBatchPhone(tt.raw)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.digits, digits)
		})
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd src && GOTOOLCHAIN=auto go test ./validations/ -run 'TestValidateCheckBatch|TestNormalizeBatchPhone'`
Expected: FAIL to compile (`CheckBatchRequest`, `ValidateCheckBatch`, `NormalizeBatchPhone` undefined).

- [ ] **Step 3: Implement**

`domains/user/account.go`, after `CheckResponse`:

```go
// CheckBatchRequest is the body of POST /user/check.
type CheckBatchRequest struct {
	Phones []string `json:"phones"`
}

// CheckBatchItem is the answer for one entry of a batch check. Pointer fields are
// null in JSON when there is no value.
type CheckBatchItem struct {
	Query        string  `json:"query"`
	Status       string  `json:"status"` // exists | not_exists | error
	PN           *string `json:"pn"`
	LID          *string `json:"lid"`
	VerifiedName *string `json:"verified_name"`
	Error        *string `json:"error"` // invalid_number | upstream
}

// CheckBatchResponse holds one item per input entry, in input order.
type CheckBatchResponse []CheckBatchItem
```

`domains/user/interfaces.go`, in `IUserInfo`:

```go
	IsOnWhatsAppBatch(ctx context.Context, request CheckBatchRequest) (response CheckBatchResponse, err error)
```

`validations/user_validation.go` (add `strings` and `config` imports; `config` is `github.com/aldinokemal/go-whatsapp-web-multidevice/config`):

```go
// CheckBatchMaxPhones is the largest batch POST /user/check accepts.
const CheckBatchMaxPhones = 100

const (
	checkBatchMinDigits = 7
	checkBatchMaxDigits = 15
)

var batchPhoneCleaner = strings.NewReplacer("+", "", " ", "", "-", "", "(", "", ")", "", ".", "")

// ValidateCheckBatch checks the size of a batch. Entries are validated one by one by
// NormalizeBatchPhone, so a bad entry never rejects the whole batch.
func ValidateCheckBatch(ctx context.Context, request domainUser.CheckBatchRequest) error {
	err := validation.ValidateStructWithContext(ctx, &request,
		validation.Field(&request.Phones, validation.Required, validation.Length(1, CheckBatchMaxPhones)),
	)
	if err != nil {
		return pkgError.ValidationError(err.Error())
	}
	return nil
}

// NormalizeBatchPhone reduces an entry to its digits. It accepts digits with "+", spaces,
// dashes, dots and parentheses, and a plain user JID (@s.whatsapp.net). LID, group and
// device JIDs are rejected, as is anything outside 7 to 15 digits.
func NormalizeBatchPhone(raw string) (string, bool) {
	value := strings.TrimSpace(raw)
	if at := strings.Index(value, "@"); at >= 0 {
		if value[at:] != config.WhatsappTypeUser {
			return "", false
		}
		value = value[:at]
	}
	digits := batchPhoneCleaner.Replace(value)
	if len(digits) < checkBatchMinDigits || len(digits) > checkBatchMaxDigits {
		return "", false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	return digits, true
}
```

`serviceUser` now lacks `IsOnWhatsAppBatch`, so the package `usecase` will not compile until Task 3; that is expected. Run only the validations package here.

- [ ] **Step 4: Run to verify it passes**

Run: `cd src && GOTOOLCHAIN=auto go test ./validations/ ./domains/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add src/domains/user src/validations
git commit -m "feat(user): add batch check DTOs and input validation"
```

---

### Task 2: Config and pacer

**Files:**
- Modify: `src/config/settings.go`, `src/cmd/root.go`, `src/.env.example`
- Create: `src/usecase/user_check_pacer.go`
- Test: `src/usecase/user_check_pacer_test.go`

**Interfaces:**
- Produces: `config.WhatsappUserCheckMinIntervalMs int` (default `500`); `newCheckPacer() *checkPacer`; `(*checkPacer).run(ctx context.Context, key string, interval time.Duration, fn func() error) error`.

- [ ] **Step 1: Write the failing tests**

```go
package usecase

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestCheckPacerSerializesAndSpacesOneKey(t *testing.T) {
	p := newCheckPacer()
	interval := 60 * time.Millisecond
	var mu sync.Mutex
	var starts []time.Time
	var running int32
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = p.run(context.Background(), "dev", interval, func() error {
				assert.Equal(t, int32(1), atomic.AddInt32(&running, 1))
				mu.Lock()
				starts = append(starts, time.Now())
				mu.Unlock()
				time.Sleep(10 * time.Millisecond)
				atomic.AddInt32(&running, -1)
				return nil
			})
		}()
	}
	wg.Wait()
	assert.Len(t, starts, 3)
	// each start is at least interval after the previous call finished (10 ms of work).
	for i := 1; i < len(starts); i++ {
		assert.GreaterOrEqual(t, starts[i].Sub(starts[i-1]), interval+10*time.Millisecond-5*time.Millisecond)
	}
}

func TestCheckPacerKeysDoNotBlockEachOther(t *testing.T) {
	p := newCheckPacer()
	release := make(chan struct{})
	started := make(chan struct{})
	go func() {
		_ = p.run(context.Background(), "a", time.Second, func() error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	done := make(chan struct{})
	go func() {
		_ = p.run(context.Background(), "b", time.Second, func() error { return nil })
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("key b was blocked by key a")
	}
	close(release)
}

func TestCheckPacerCancelledWaiterNeverRuns(t *testing.T) {
	p := newCheckPacer()
	release := make(chan struct{})
	started := make(chan struct{})
	go func() {
		_ = p.run(context.Background(), "dev", 0, func() error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	var called int32
	errCh := make(chan error, 1)
	go func() {
		errCh <- p.run(ctx, "dev", 0, func() error {
			atomic.AddInt32(&called, 1)
			return nil
		})
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	assert.ErrorIs(t, <-errCh, context.Canceled)
	close(release)
	assert.Equal(t, int32(0), atomic.LoadInt32(&called))
}

func TestCheckPacerCancelDuringIntervalWaitNeverRuns(t *testing.T) {
	p := newCheckPacer()
	assert.NoError(t, p.run(context.Background(), "dev", time.Hour, func() error { return nil }))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	var called int32
	err := p.run(ctx, "dev", time.Hour, func() error {
		atomic.AddInt32(&called, 1)
		return nil
	})
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, int32(0), atomic.LoadInt32(&called))
}

func TestCheckPacerReturnsFunctionError(t *testing.T) {
	p := newCheckPacer()
	boom := errors.New("boom")
	assert.ErrorIs(t, p.run(context.Background(), "dev", 0, func() error { return boom }), boom)
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd src && GOTOOLCHAIN=auto go vet ./usecase/ 2>&1 | head` then note that the package does not build until Task 3 defines `IsOnWhatsAppBatch`. To run this task's tests alone, first add a temporary stub in `usecase/user_check_batch.go`:

```go
package usecase

import (
	"context"

	domainUser "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/user"
)

func (service serviceUser) IsOnWhatsAppBatch(ctx context.Context, request domainUser.CheckBatchRequest) (domainUser.CheckBatchResponse, error) {
	return nil, nil
}
```

Run: `cd src && GOTOOLCHAIN=auto go test ./usecase/ -run TestCheckPacer`
Expected: FAIL to compile (`newCheckPacer` undefined).

- [ ] **Step 3: Implement**

`usecase/user_check_pacer.go`:

```go
package usecase

import (
	"context"
	"sync"
	"time"
)

// checkPacer runs one function at a time per key and waits a minimum interval after the
// previous function of that key finished. WhatsApp bans accounts that hammer the contact
// sync, and a broadcast audience is thousands of numbers.
type checkPacer struct {
	mu    sync.Mutex
	slots map[string]*checkSlot
}

type checkSlot struct {
	sem  chan struct{} // capacity 1: whoever holds it runs
	last time.Time     // finish time of the previous run; only touched while holding sem
}

func newCheckPacer() *checkPacer {
	return &checkPacer{slots: make(map[string]*checkSlot)}
}

func (p *checkPacer) slot(key string) *checkSlot {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.slots[key]
	if !ok {
		s = &checkSlot{sem: make(chan struct{}, 1)}
		p.slots[key] = s
	}
	return s
}

// run waits for the key's turn and for the interval, then calls fn. A cancelled ctx while
// waiting returns ctx.Err() without calling fn.
func (p *checkPacer) run(ctx context.Context, key string, interval time.Duration, fn func() error) error {
	s := p.slot(key)
	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-s.sem }()

	if wait := interval - time.Since(s.last); !s.last.IsZero() && wait > 0 {
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
	// Deferred calls run last-in first-out: last is set before the semaphore is released.
	defer func() { s.last = time.Now() }()
	return fn()
}
```

`config/settings.go`, next to the other `Whatsapp*` vars:

```go
	WhatsappUserCheckMinIntervalMs             = 500 // Fork (elphant): minimum wait between POST /user/check batches on one device
```

`cmd/root.go`, in the viper block after `whatsapp_account_validation`:

```go
	if viper.IsSet("whatsapp_user_check_min_interval_ms") {
		config.WhatsappUserCheckMinIntervalMs = viper.GetInt("whatsapp_user_check_min_interval_ms")
	}
```

and next to the other `IntVarP` flags:

```go
	rootCmd.PersistentFlags().IntVarP(
		&config.WhatsappUserCheckMinIntervalMs,
		"whatsapp-user-check-min-interval-ms", "",
		config.WhatsappUserCheckMinIntervalMs,
		`minimum wait in milliseconds between batch number checks on one device --whatsapp-user-check-min-interval-ms <int> | example: --whatsapp-user-check-min-interval-ms=500`,
	)
```

`.env.example`, after `WHATSAPP_ACCOUNT_VALIDATION=true`:

```
# Fork (elphant): minimum wait between POST /user/check batches on one device, in milliseconds.
WHATSAPP_USER_CHECK_MIN_INTERVAL_MS=500
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd src && GOTOOLCHAIN=auto go test ./usecase/ -run TestCheckPacer -race -count=3 && GOTOOLCHAIN=auto go build ./...`
Expected: PASS, build OK.

- [ ] **Step 5: Commit**

```bash
git add src/config src/cmd/root.go src/.env.example src/usecase
git commit -m "feat(user): add per-device pacer and interval setting for batch checks"
```

---

### Task 3: Usecase

**Files:**
- Modify (replace the Task 2 stub): `src/usecase/user_check_batch.go`
- Test: `src/usecase/user_check_batch_test.go`

**Interfaces:**
- Consumes: `validations.NormalizeBatchPhone`, `validations.ValidateCheckBatch`, `newCheckPacer`, `checkPacer.run`, `config.WhatsappUserCheckMinIntervalMs`.
- Produces: `phoneChecker`, `runCheckBatch(ctx, checker, raw []string) domainUser.CheckBatchResponse`, and the real `serviceUser.IsOnWhatsAppBatch`.

- [ ] **Step 1: Write the failing tests**

```go
package usecase

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.mau.fi/whatsmeow/types"
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
```

Add to the test imports: `"go.mau.fi/whatsmeow/proto/waVnameCert"` and `"google.golang.org/protobuf/proto"`.

- [ ] **Step 2: Run to verify it fails**

Run: `cd src && GOTOOLCHAIN=auto go test ./usecase/ -run TestRunCheckBatch`
Expected: FAIL to compile (`runCheckBatch` undefined).

- [ ] **Step 3: Implement** (replace the stub file `usecase/user_check_batch.go`)

```go
package usecase

import (
	"context"
	"strings"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	domainUser "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/user"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/utils"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/validations"
	"github.com/sirupsen/logrus"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

const (
	checkStatusExists     = "exists"
	checkStatusNotExists  = "not_exists"
	checkStatusError      = "error"
	checkErrInvalidNumber = "invalid_number"
	checkErrUpstream      = "upstream"
	checkBatchTimeout     = 20 * time.Second
)

// userCheckPacer spaces batch checks per device.
var userCheckPacer = newCheckPacer()

// phoneChecker is the part of whatsmeow the batch check needs; tests use a fake.
type phoneChecker interface {
	IsOnWhatsApp(ctx context.Context, phones []string) ([]types.IsOnWhatsAppResponse, error)
	LIDForPN(ctx context.Context, pn types.JID) (types.JID, error)
}

type whatsmeowChecker struct{ client *whatsmeow.Client }

func (c whatsmeowChecker) IsOnWhatsApp(ctx context.Context, phones []string) ([]types.IsOnWhatsAppResponse, error) {
	return c.client.IsOnWhatsApp(ctx, phones)
}

func (c whatsmeowChecker) LIDForPN(ctx context.Context, pn types.JID) (types.JID, error) {
	if c.client.Store == nil || c.client.Store.LIDs == nil {
		return types.EmptyJID, nil
	}
	return c.client.Store.LIDs.GetLIDForPN(ctx, pn)
}

func (service serviceUser) IsOnWhatsAppBatch(ctx context.Context, request domainUser.CheckBatchRequest) (domainUser.CheckBatchResponse, error) {
	if err := validations.ValidateCheckBatch(ctx, request); err != nil {
		return nil, err
	}
	client := whatsapp.ClientFromContext(ctx)
	if client == nil {
		return nil, pkgError.ErrWaCLI
	}
	utils.MustLogin(client)

	key := "default"
	if inst, ok := whatsapp.DeviceFromContext(ctx); ok && inst != nil {
		key = inst.ID()
	}
	interval := time.Duration(config.WhatsappUserCheckMinIntervalMs) * time.Millisecond
	if interval < 0 {
		interval = 0
	}

	var items domainUser.CheckBatchResponse
	started := time.Now()
	err := userCheckPacer.run(ctx, key, interval, func() error {
		callCtx, cancel := context.WithTimeout(ctx, checkBatchTimeout)
		defer cancel()
		items = runCheckBatch(callCtx, whatsmeowChecker{client: client}, request.Phones)
		return nil
	})
	if err != nil {
		return nil, err
	}

	counts := map[string]int{}
	for _, item := range items {
		counts[item.Status]++
	}
	logrus.Infof("Batch user check: entries=%d exists=%d not_exists=%d error=%d duration=%s",
		len(items), counts[checkStatusExists], counts[checkStatusNotExists], counts[checkStatusError], time.Since(started).Round(time.Millisecond))
	return items, nil
}

// runCheckBatch builds one item per raw entry, in order. Valid entries are deduplicated and
// asked in a single call; invalid entries never reach WhatsApp.
func runCheckBatch(ctx context.Context, checker phoneChecker, raw []string) domainUser.CheckBatchResponse {
	items := make(domainUser.CheckBatchResponse, len(raw))
	var ask []string
	asked := map[string]bool{}
	for i, entry := range raw {
		digits, ok := validations.NormalizeBatchPhone(entry)
		if !ok {
			items[i] = errorItem(strings.TrimSpace(entry), checkErrInvalidNumber)
			continue
		}
		items[i].Query = digits
		if !asked[digits] {
			asked[digits] = true
			ask = append(ask, "+"+digits)
		}
	}
	if len(ask) == 0 {
		return items
	}

	resolved := make(map[string]domainUser.CheckBatchItem, len(ask))
	answers, err := checker.IsOnWhatsApp(ctx, ask)
	if err != nil {
		logrus.Warnf("Batch user check call failed: %v", err)
		for digits := range asked {
			resolved[digits] = errorItem(digits, checkErrUpstream)
		}
	} else {
		for digits := range asked {
			resolved[digits] = domainUser.CheckBatchItem{Query: digits, Status: checkStatusNotExists}
		}
		for _, answer := range answers {
			digits := onlyDigits(answer.Query)
			if _, wanted := asked[digits]; !wanted || !answer.IsIn {
				continue
			}
			resolved[digits] = existsItem(ctx, checker, digits, answer)
		}
	}

	for i := range items {
		if items[i].Status == "" {
			items[i] = resolved[items[i].Query]
		}
	}
	return items
}

func existsItem(ctx context.Context, checker phoneChecker, digits string, answer types.IsOnWhatsAppResponse) domainUser.CheckBatchItem {
	item := domainUser.CheckBatchItem{Query: digits, Status: checkStatusExists}

	pn := answer.PhoneNumber
	if pn.IsEmpty() && answer.JID.Server == types.DefaultUserServer {
		pn = answer.JID
	}
	if !pn.IsEmpty() {
		s := pn.ToNonAD().String()
		item.PN = &s
	}

	lid := types.EmptyJID
	if answer.JID.Server == types.HiddenUserServer {
		lid = answer.JID
	} else if !pn.IsEmpty() {
		if mapped, err := checker.LIDForPN(ctx, pn.ToNonAD()); err == nil {
			lid = mapped
		}
	}
	if !lid.IsEmpty() {
		s := lid.ToNonAD().String()
		item.LID = &s
	}

	if answer.VerifiedName != nil && answer.VerifiedName.Details != nil {
		if name := answer.VerifiedName.Details.GetVerifiedName(); name != "" {
			item.VerifiedName = &name
		}
	}
	return item
}

func errorItem(query, code string) domainUser.CheckBatchItem {
	return domainUser.CheckBatchItem{Query: query, Status: checkStatusError, Error: &code}
}

func onlyDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd src && GOTOOLCHAIN=auto go test ./usecase/ -race && GOTOOLCHAIN=auto go build ./...`
Expected: PASS, build OK.

- [ ] **Step 5: Commit**

```bash
git add src/usecase
git commit -m "feat(user): batch number check usecase with pn, lid and three states"
```

---

### Task 4: REST endpoint

**Files:**
- Modify: `src/ui/rest/user.go`
- Test: `src/ui/rest/user_check_batch_test.go`

**Interfaces:**
- Consumes: `IUserInfo.IsOnWhatsAppBatch`, `whatsapp.ContextWithDevice`, `getDeviceFromCtx`.
- Produces: route `POST /user/check`.

- [ ] **Step 1: Write the failing tests**

```go
package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	domainUser "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/user"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/ui/rest/middleware"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
)

type checkBatchStub struct {
	domainUser.IUserUsecase
	got       domainUser.CheckBatchRequest
	gotDevice *whatsapp.DeviceInstance
}

func (s *checkBatchStub) IsOnWhatsAppBatch(ctx context.Context, request domainUser.CheckBatchRequest) (domainUser.CheckBatchResponse, error) {
	s.got = request
	s.gotDevice, _ = whatsapp.DeviceFromContext(ctx)
	code := "upstream"
	return domainUser.CheckBatchResponse{{Query: "5511988887777", Status: "error", Error: &code}}, nil
}

func newCheckBatchApp(stub *checkBatchStub, device *whatsapp.DeviceInstance) *fiber.App {
	app := fiber.New()
	app.Use(middleware.Recovery())
	app.Use(func(c fiber.Ctx) error {
		c.Locals("device", device)
		return c.Next()
	})
	controller := User{Service: stub}
	app.Post("/user/check", controller.UserCheckBatch)
	return app
}

func TestUserCheckBatchReturnsItemsInTheEnvelope(t *testing.T) {
	stub := &checkBatchStub{}
	app := newCheckBatchApp(stub, nil)

	req := httptest.NewRequest(http.MethodPost, "/user/check", strings.NewReader(`{"phones":["5511988887777"]}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var body struct {
		Code    string                   `json:"code"`
		Results []map[string]interface{} `json:"results"`
	}
	assert.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "SUCCESS", body.Code)
	assert.Len(t, body.Results, 1)
	assert.Equal(t, "error", body.Results[0]["status"])
	assert.Nil(t, body.Results[0]["pn"]) // null, key present
	assert.Contains(t, body.Results[0], "lid")
	assert.Equal(t, []string{"5511988887777"}, stub.got.Phones)
}

func TestUserCheckBatchScopesTheRequestDevice(t *testing.T) {
	stub := &checkBatchStub{}
	device := &whatsapp.DeviceInstance{}
	app := newCheckBatchApp(stub, device)

	req := httptest.NewRequest(http.MethodPost, "/user/check", strings.NewReader(`{"phones":["5511988887777"]}`))
	req.Header.Set("Content-Type", "application/json")
	_, err := app.Test(req)
	assert.NoError(t, err)
	assert.Same(t, device, stub.gotDevice)
}

func TestUserCheckBatchRejectsMalformedBody(t *testing.T) {
	stub := &checkBatchStub{}
	app := newCheckBatchApp(stub, nil)

	req := httptest.NewRequest(http.MethodPost, "/user/check", strings.NewReader(`{"phones":`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, resp.StatusCode, 400)
	assert.Less(t, resp.StatusCode, 500)
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd src && GOTOOLCHAIN=auto go test ./ui/rest/ -run TestUserCheckBatch`
Expected: FAIL to compile (`UserCheckBatch` undefined).

- [ ] **Step 3: Implement**

`ui/rest/user.go`, in `InitRestUser` after the GET route:

```go
	app.Post("/user/check", rest.UserCheckBatch)
```

and the handler after `UserCheck`:

```go
// UserCheckBatch checks up to 100 numbers in one call (fork: elphant, see docs/reference/elphant-fork.md).
func (controller *User) UserCheckBatch(c fiber.Ctx) error {
	var request domainUser.CheckBatchRequest
	err := c.Bind().Body(&request)
	utils.PanicIfNeeded(err)

	ctx := whatsapp.ContextWithDevice(c.Context(), getDeviceFromCtx(c))

	response, err := controller.Service.IsOnWhatsAppBatch(ctx, request)
	utils.PanicIfNeeded(err)

	return c.JSON(utils.ResponseData{
		Status:  200,
		Code:    "SUCCESS",
		Message: "Success check users",
		Results: response,
	})
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd src && GOTOOLCHAIN=auto go test ./ui/rest/ -run TestUserCheckBatch`
Expected: PASS. If the malformed-body test gets 500, check how `utils.PanicIfNeeded` maps a bind error; wrap the bind error with `pkgError.ValidationError(err.Error())` so it answers 400, and keep the test.

- [ ] **Step 5: Commit**

```bash
git add src/ui/rest
git commit -m "feat(user): add POST /user/check for batch number checks"
```

---

### Task 5: Documentation and final checks

**Files:**
- Modify: `docs/reference/elphant-fork.md`, `docs/elphant-debt.md`

- [ ] **Step 1: Document the endpoint** in `docs/reference/elphant-fork.md`, as a new section before "Specs and plans", following the page's style: request body, the response item table (`query`, `status`, `pn`, `lid`, `verified_name`, `error`), the ninth-digit note (use the returned `pn`), error semantics (`upstream` is retryable, never `not_exists`), limits (100 entries, one call at a time per device, `WHATSAPP_USER_CHECK_MIN_INTERVAL_MS`, 20 s timeout), and that `GET /user/check` is unchanged.

- [ ] **Step 2: Run the checks**

Run: `cd src && GOTOOLCHAIN=auto go vet ./... && GOTOOLCHAIN=auto go test ./validations/ ./usecase/ ./ui/rest/ ./domains/... ./config/... ./cmd/... -race`
Expected: PASS.

Run: `cd src && GOTOOLCHAIN=auto go test ./...`
Expected: PASS (full suite, shared contract touched).

- [ ] **Step 3: Commit**

```bash
git add docs/reference/elphant-fork.md
git commit -m "docs: document POST /user/check"
```

- [ ] **Step 4: Runtime check (after the release, owner's OK needed)**

After tag `v9.5.0-elphant.7` and the stack update (separate approvals), call `POST /user/check` on devias with 3 numbers: the owner's number, a number that does not exist, and a malformed one. Confirm `Query` matching works with the real answer (the numbers come from the owner; never print them in logs or docs). If `Query` differs from the assumed `+digits` form, fix `onlyDigits` matching and add a test with the real format.
