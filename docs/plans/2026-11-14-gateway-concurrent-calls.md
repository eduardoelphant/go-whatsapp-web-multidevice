# Voice calls: concurrent calls per number and a gateway-wide cap Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Each number gets its own call limit at runtime (`max_calls` on
`PUT /devices/:device_id/calls`), bounded by the env ceiling `WHATSAPP_CALLS_MAX_PER_DEVICE`. A new
gateway-wide cap, `WHATSAPP_CALLS_MAX_TOTAL`, bounds the sum over every device. Both are enforced
with one counting rule and an atomic reservation, and refusals say which limit was hit.

**Architecture:**
- A package-level slot pool (`callSlots`, new file `calls_slots.go`) counts the slots held by
  every `CallManager`. Its mutex is a leaf: a manager takes it inside `m.mu`, and the pool never
  calls out while holding it.
- A manager's device count becomes `reserved + len(calls)`. Every tracked call holds a slot,
  including a ringing inbound call (D-21). Inbound and outbound both reserve under `m.mu`
  (device check, then pool), then track the call, then hand the reservation over to it.
  `finish` releases the pool slot when it removes the call from `calls`.
- The per-device limit is `min(stored max_calls or 1, ceiling)`. It is read before `m.mu`
  through a closure that follows the client to its registry slot (`callsSlotOf`). It comes from a
  per-slot cache next to the switch's cache in `calls_switch.go`.
- `elphant_device_calls` gains a nullable `max_calls INTEGER`, added idempotently on first use.
- A refused offer sends no `call.offer` (D-31). The manager records the refusal reason when it
  marks the id gone, and `addCallOfferFields` waits for the admission decision (the existing
  `waitTracked` wait) before it builds the webhook. A refused offer drops the whole payload.

**Tech Stack:** Go 1.26 (`GOTOOLCHAIN=auto`), whatsmeow, Fiber v3, testify, SQLite (chat storage).

**Spec:** `docs/specs/2026-11-14-gateway-concurrent-calls-design.md` (D-19 to D-31). Read it first.
It is binding. Stage 2b context: `docs/specs/2026-10-14-gateway-voice-calls-2b-design.md` (B7,
B12).

## Global Constraints

- **Workspace.** Work in the worktree `~/Documents/whatsapp-gateway-concurrent-calls`, branch
  `feature/concurrent-calls` (based on `elphant`). Never touch `~/Documents/whatsapp-gateway`.
  Local only: no push, tag, image, PR or server change without the owner's explicit OK.
- **Go commands.** Run Go from `src/` with `GOTOOLCHAIN=auto`. The `contract/` module runs from
  `contract/`.
- **Commits.** English, Conventional Commits, no attribution lines. Public repo: no IP, token,
  real phone number or QR content in code, tests or docs. Test numbers stay in the
  `55119000000xx` style that the existing tests use.
- **Upstream files.** In upstream files, add only single lines marked `// Fork (elphant)`, or edit
  inside existing fork blocks. Everything bigger goes into fork-owned files (`calls*.go`,
  `config/calls.go`, `cmd/calls_flags.go`, `ui/rest/call_device.go`).
- **Defaults keep today's behaviour, except D-21.** `WHATSAPP_CALLS_MAX_PER_DEVICE` defaults to
  `1` and `WHATSAPP_CALLS_MAX_TOTAL` to `0` (no gateway cap). The one intended behaviour change at
  the defaults is that a ringing inbound call now holds its slot.
- **Error texts are contract.** `ErrCallLimit` stays exactly "too many calls on this device". The
  new `ErrGatewayCallLimit` is exactly "call capacity of this gateway reached" and must never
  contain "too many calls", because the CRM matches substrings.
- **`end_reason` values.** `device_limit` and `gateway_limit`, always with `status` `MISSED`.
- **Lock order.** Take `tc.emitMu` first, then `m.mu`, then `callSlots.mu`. Never call
  `m.opts.Limit`, the store or `GetDeviceManager()` while holding `m.mu`.
- **Tests that mutate config or package globals** stay serial (no `t.Parallel()`) and restore
  state with `t.Cleanup`.
- **Background goroutines.** None are new. If one is added anyway, it uses `safego.Go` or starts
  with `defer safego.Recover(name)` (`TestEveryBackgroundGoroutineIsProtected`).

## Review Focus

1. **A slot leaks.** Every end path must release the device slot and the pool slot exactly once:
   hangup, peer end, reject, ring timeout, max duration, browser loss, failed `Answer`, switch off,
   client discarded, a synchronous end during tracking, a bad SDP and an engine error. If one
   leaks, the gateway slowly refuses every call. Pinned in Task 4
   (`TestEveryEndPathReleasesTheGatewaySlot`).
2. **Two devices take the last slot.** Pinned in Task 4
   (`TestConcurrentStartsAcrossManagersRespectTheGatewayCap`), run with `-race`.
3. **A refusal sends the wrong reason or the wrong text.** Device first, then gateway. The gateway
   text never contains "too many calls". Pinned in Task 4 (`TestGatewayPoolIsSharedAcrossManagers`,
   `TestDeviceLimitIsCheckedBeforeTheGatewayCap`).
4. **The limit is read from the wrong slot.** The startup client moves from its AD JID to its
   ULID slot. Pinned in Task 5 (`TestCallLimitFollowsTheStartupClientToItsRegistrySlot`).
5. **A refused offer rings the CRM.** No `call.offer` for an offer refused by a limit; an
   admitted offer, a device with calls off and the master switch off keep it. Pinned in Task 8
   (`TestRefusedOfferSendsNoCallOffer`, `TestCallOfferFields`).
6. **An old database.** A table created by 2b has no `max_calls`. Pinned in Task 2
   (`TestDeviceCallsMaxCallsColumnIsAddedToAnOldTable`).

## Decisions taken in this plan (spec silences)

- **Keep `MaxCalls` in `callManagerOptions`** as the ceiling, and add two optional fields: `Limit
  func() int` (nil means the ceiling) and `Pool *callSlots` (nil means no gateway accounting). The
  roughly 40 existing tests that build `callManagerOptions{MaxCalls: n}` stay valid.
- **The pool counts even with no cap** (`WHATSAPP_CALLS_MAX_TOTAL=0`). The count is cheap, and the
  leak tests can then run at the default.
- **Keep `SetDeviceCalls(deviceID, enabled) (bool, error)` and `DeviceCalls(deviceID) (bool,
  error)`** as thin wrappers over the new `ConfigureDeviceCalls` and `DeviceCallsSettings`, so
  existing switch tests stay unchanged. The usecase moves to the new functions.
- **`max_calls` validation lives in `ConfigureDeviceCalls`** (`calls_switch.go`), not in
  `validations/`, because the range depends on the env ceiling and the master-switch check runs
  first there. Order: master switch, `max_calls` range, slot resolution.
- **A stored `max_calls` below 1** (only possible by hand in SQLite) reads as 1.
- **A failed limit read is not cached.** It falls back to 1 and logs a warning, the same as the
  switch's read path.
- **`device_manager.go:282`** (`deviceCallsCache.Delete(id)` on `RemoveDevice`) becomes
  `forgetDeviceCallsCache(id)`, which drops both caches.
- **Schema check once per database handle**, behind a package mutex. The tolerance for "duplicate
  column name" stays, for safety.

## File map

| File | Responsibility | Task |
|---|---|---|
| `src/config/calls.go` (+`calls_test.go`) | `WhatsappCallsMaxTotal`, `CallsMaxPerDeviceCeiling()`, `CallsMaxTotal()` | 1 |
| `src/cmd/calls_flags.go` (+test) | `WHATSAPP_CALLS_MAX_TOTAL` env and `--calls-max-total` flag | 1 |
| `src/infrastructure/chatstorage/sqlite_repository_calls.go` (+test) | `max_calls` column, `SetDeviceCallsConfig`, `GetDeviceCallsMaxCalls` | 2 |
| `src/infrastructure/whatsapp/calls_slots.go` (new, +test) | Gateway slot pool | 3 |
| `src/infrastructure/whatsapp/calls.go`, `calls_test.go` | Reservation, counting rule, refusal reasons, errors, logs | 4 |
| `src/infrastructure/whatsapp/calls_concurrent_test.go` (new) | Multi-manager, leak and limit tests | 4 |
| `src/infrastructure/whatsapp/calls_switch.go`, `calls_switch_test.go`, `calls_slot_test.go` | Limit cache, `ConfigureDeviceCalls`, `DeviceCallsSettings` | 5 |
| `src/infrastructure/whatsapp/calls_registry.go`, `calls_registry_test.go` | `attachCalls` wires `Limit` and `Pool` | 5 |
| `src/infrastructure/whatsapp/device_manager.go` | One fork line: `forgetDeviceCallsCache(id)` | 5 |
| `src/domains/call/call.go`, `src/usecase/call.go`, `src/ui/rest/call_device.go`, `src/ui/rest/call_device_test.go`, `src/ui/rest/call_test.go` | REST shape | 6 |
| `docs/openapi.yaml` | `/devices/{device_id}/calls`, `/call/start` 400 | 6 |
| `contract/call.schema.json`, `contract/call-fixtures/call-ended-limit.json`, `contract/call_schema_test.go`, `src/infrastructure/whatsapp/calls_webhook_test.go`, `docs/webhook-payload.md` | Webhook contract | 7 |
| `src/infrastructure/whatsapp/calls.go`, `calls_webhook.go`, `event_call.go` (2 fork lines), `calls_webhook_test.go` | No `call.offer` for a refused offer | 8 |
| `docs/reference/elphant-fork.md`, `src/.env.example`, `docs/elphant-debt.md` | Docs and debt | 9 |
| `docs/reference/elphant-fork.md` | Measurement results | 10 |

Task order: 1 → 2 → 3 → 4 → 5 → 6 → 7 → 8 → 9 → 10. Every task leaves `go build ./...` and its
package's tests green. Task 5 needs Tasks 1 to 4. Task 6 needs Task 5. Tasks 7 and 8 need Task 4.

---

### Task 1: Config: the ceiling and `WHATSAPP_CALLS_MAX_TOTAL` (D-19, D-20)

**Files:**
- Modify: `src/config/calls.go`, `src/config/calls_test.go`
- Modify: `src/cmd/calls_flags.go`, `src/cmd/calls_flags_test.go`

**Interfaces:**
- `config.WhatsappCallsMaxTotal int` (default `0`)
- `config.CallsMaxPerDeviceCeiling() int`: `WhatsappCallsMaxPerDevice`, at least 1
- `config.CallsMaxTotal() int`: `WhatsappCallsMaxTotal`, negative read as 0

- [ ] **Step 1: Write the failing config test**

Append to `src/config/calls_test.go`:

```go
func TestCallsLimitsHelpers(t *testing.T) {
	origPer, origTotal := WhatsappCallsMaxPerDevice, WhatsappCallsMaxTotal
	t.Cleanup(func() { WhatsappCallsMaxPerDevice, WhatsappCallsMaxTotal = origPer, origTotal })

	for _, c := range []struct{ per, want int }{{1, 1}, {3, 3}, {0, 1}, {-2, 1}} {
		WhatsappCallsMaxPerDevice = c.per
		if got := CallsMaxPerDeviceCeiling(); got != c.want {
			t.Fatalf("ceiling for %d: got %d, want %d", c.per, got, c.want)
		}
	}
	for _, c := range []struct{ total, want int }{{0, 0}, {6, 6}, {-1, 0}} {
		WhatsappCallsMaxTotal = c.total
		if got := CallsMaxTotal(); got != c.want {
			t.Fatalf("total for %d: got %d, want %d", c.total, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run it and see it fail**

Run: `cd src && GOTOOLCHAIN=auto go test ./config/ -run TestCallsLimitsHelpers`
Expected: compile error, `undefined: WhatsappCallsMaxTotal`.

- [ ] **Step 3: Implement in `src/config/calls.go`**

In the `var (...)` block, change the comment of `WhatsappCallsMaxPerDevice` and add the new
variable after it:

```go
	WhatsappCallsMaxPerDevice           = 1      // ceiling of simultaneous calls per number; each device's own max_calls is capped by it (concurrent calls, D-19)
	WhatsappCallsMaxTotal               = 0      // simultaneous calls summed over every device of this gateway; 0: no cap (concurrent calls, D-20)
```

Append these functions to the file:

```go
// CallsMaxPerDeviceCeiling is the effective per-number ceiling: WHATSAPP_CALLS_MAX_PER_DEVICE,
// at least 1 (concurrent calls, D-19).
func CallsMaxPerDeviceCeiling() int {
	if WhatsappCallsMaxPerDevice < 1 {
		return 1
	}
	return WhatsappCallsMaxPerDevice
}

// CallsMaxTotal is the effective gateway-wide cap: WHATSAPP_CALLS_MAX_TOTAL, where 0 and
// negative values mean no cap (concurrent calls, D-20).
func CallsMaxTotal() int {
	if WhatsappCallsMaxTotal < 0 {
		return 0
	}
	return WhatsappCallsMaxTotal
}
```

- [ ] **Step 4: Run the config test**

Run: `cd src && GOTOOLCHAIN=auto go test ./config/ -run TestCallsLimitsHelpers`
Expected: `ok`.

- [ ] **Step 5: Write the failing env test**

Append to `src/cmd/calls_flags_test.go`:

```go
func TestInitCallsEnvMaxTotal(t *testing.T) {
	orig := config.WhatsappCallsMaxTotal
	t.Cleanup(func() {
		config.WhatsappCallsMaxTotal = orig
		viper.Set("whatsapp_calls_max_total", nil)
	})
	viper.Set("whatsapp_calls_max_total", 6)

	initCallsEnv()
	if config.WhatsappCallsMaxTotal != 6 {
		t.Fatalf("got %d, want 6", config.WhatsappCallsMaxTotal)
	}
	if rootCmd.PersistentFlags().Lookup("calls-max-total") == nil {
		t.Fatal("flag not registered")
	}
}
```

Run: `cd src && GOTOOLCHAIN=auto go test ./cmd/ -run TestInitCallsEnvMaxTotal`
Expected: FAIL (`got 0, want 6`).

- [ ] **Step 6: Implement in `src/cmd/calls_flags.go`**

In `initCallsEnv`, add at the end:

```go
	if viper.IsSet("whatsapp_calls_max_total") {
		config.WhatsappCallsMaxTotal = viper.GetInt("whatsapp_calls_max_total")
	}
```

In `initCallsFlags`, add at the end:

```go
	flags.IntVarP(&config.WhatsappCallsMaxTotal, "calls-max-total", "", config.WhatsappCallsMaxTotal,
		`simultaneous calls summed over every device (0: no cap) --calls-max-total <n> | example: --calls-max-total=6`)
```

In `src/cmd/root.go`, the existing `--calls-max-per-device` help text says "simultaneous calls per
device". Change only its text, inside the existing fork block, to:
`` `ceiling of simultaneous calls per device; each device's max_calls is capped by it --calls-max-per-device <n> | example: --calls-max-per-device=3` ``.

- [ ] **Step 7: Run the cmd and config tests**

Run: `cd src && GOTOOLCHAIN=auto go test ./cmd/ ./config/`
Expected: `ok` for both.

- [ ] **Step 8: Commit**

```bash
git add src/config/calls.go src/config/calls_test.go src/cmd/calls_flags.go src/cmd/calls_flags_test.go src/cmd/root.go
git commit -m "feat(calls): WHATSAPP_CALLS_MAX_TOTAL and the per-device ceiling helpers"
```

---

### Task 2: Storage: the `max_calls` column (D-22)

**Files:**
- Modify: `src/infrastructure/chatstorage/sqlite_repository_calls.go`
- Modify: `src/infrastructure/chatstorage/sqlite_repository_calls_test.go`

**Interfaces (new methods on `*SQLiteRepository`):**
- `SetDeviceCallsConfig(deviceID string, enabled bool, maxCalls *int) error`: an upsert. A nil
  `maxCalls` keeps the stored value (or `NULL` on insert).
- `GetDeviceCallsMaxCalls(deviceID string) (maxCalls int, set bool, err error)`: `set` is false
  for a missing row or `NULL`.
- `SetDeviceCallsEnabled` keeps its signature and behaviour, now through
  `SetDeviceCallsConfig(id, enabled, nil)`.

The compile-time assertion `var _ whatsapp.DeviceCallsStore = (*SQLiteRepository)(nil)` in the
test file must keep compiling. Task 5 adds the two methods to that interface.

- [ ] **Step 1: Write the failing tests**

Append to `src/infrastructure/chatstorage/sqlite_repository_calls_test.go`:

```go
func intPtr(v int) *int { return &v }

func TestDeviceCallsMaxCallsRoundTrip(t *testing.T) {
	repo := newTestSQLiteRepository(t)

	if n, set, err := repo.GetDeviceCallsMaxCalls("slot-a"); err != nil || set || n != 0 {
		t.Fatalf("no row: got (%d, %v, %v), want (0, false, nil)", n, set, err)
	}
	if err := repo.SetDeviceCallsConfig("slot-a", true, nil); err != nil {
		t.Fatal(err)
	}
	if n, set, err := repo.GetDeviceCallsMaxCalls("slot-a"); err != nil || set || n != 0 {
		t.Fatalf("NULL: got (%d, %v, %v), want (0, false, nil)", n, set, err)
	}
	if err := repo.SetDeviceCallsConfig("slot-a", true, intPtr(3)); err != nil {
		t.Fatal(err)
	}
	if n, set, err := repo.GetDeviceCallsMaxCalls("slot-a"); err != nil || !set || n != 3 {
		t.Fatalf("after 3: got (%d, %v, %v)", n, set, err)
	}
	// A nil max_calls keeps the stored value; enabled still changes.
	if err := repo.SetDeviceCallsConfig("slot-a", false, nil); err != nil {
		t.Fatal(err)
	}
	if n, set, _ := repo.GetDeviceCallsMaxCalls("slot-a"); !set || n != 3 {
		t.Fatalf("nil must keep 3, got (%d, %v)", n, set)
	}
	if on, found, _ := repo.GetDeviceCallsEnabled("slot-a"); !found || on {
		t.Fatalf("enabled must be false, got (%v, %v)", on, found)
	}
	// SetDeviceCallsEnabled leaves max_calls alone.
	if err := repo.SetDeviceCallsEnabled("slot-a", true); err != nil {
		t.Fatal(err)
	}
	if n, _, _ := repo.GetDeviceCallsMaxCalls("slot-a"); n != 3 {
		t.Fatalf("SetDeviceCallsEnabled must keep 3, got %d", n)
	}
	if err := repo.SetDeviceCallsConfig(" ", true, intPtr(2)); err == nil {
		t.Fatal("a blank device id must fail")
	}
}

func TestDeviceCallsMaxCallsColumnIsAddedToAnOldTable(t *testing.T) {
	repo := newTestSQLiteRepository(t)
	// The table as stage 2b created it, with a row.
	if _, err := repo.db.Exec(`CREATE TABLE elphant_device_calls (
		device_id VARCHAR(255) PRIMARY KEY,
		enabled BOOLEAN NOT NULL,
		updated_at TIMESTAMP NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.Exec(`INSERT INTO elphant_device_calls (device_id, enabled, updated_at) VALUES ('slot-old', 1, CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}

	if n, set, err := repo.GetDeviceCallsMaxCalls("slot-old"); err != nil || set || n != 0 {
		t.Fatalf("old row reads NULL: got (%d, %v, %v)", n, set, err)
	}
	if on, found, err := repo.GetDeviceCallsEnabled("slot-old"); err != nil || !found || !on {
		t.Fatalf("old row keeps enabled: got (%v, %v, %v)", on, found, err)
	}
	if err := repo.SetDeviceCallsConfig("slot-old", true, intPtr(2)); err != nil {
		t.Fatal(err)
	}
	if n, set, _ := repo.GetDeviceCallsMaxCalls("slot-old"); !set || n != 2 {
		t.Fatalf("after 2: got (%d, %v)", n, set)
	}
	var columns int
	if err := repo.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('elphant_device_calls') WHERE name = 'max_calls'`).Scan(&columns); err != nil || columns != 1 {
		t.Fatalf("max_calls columns: got %d (%v), want 1", columns, err)
	}
}
```

- [ ] **Step 2: Run them and see them fail**

Run: `cd src && GOTOOLCHAIN=auto go test ./infrastructure/chatstorage/ -run 'TestDeviceCallsMaxCalls'`
Expected: compile error, `repo.GetDeviceCallsMaxCalls undefined`.

- [ ] **Step 3: Implement**

Rewrite `src/infrastructure/chatstorage/sqlite_repository_calls.go` as:

```go
package chatstorage

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Fork (elphant): per-device voice calls switch (stage 2b, B7) and call limit (concurrent calls,
// D-22). It lives in its own table, created on first use, so upstream's numbered migrations stay
// untouched.

const createDeviceCallsTable = `CREATE TABLE IF NOT EXISTS elphant_device_calls (
	device_id VARCHAR(255) PRIMARY KEY,
	enabled BOOLEAN NOT NULL,
	max_calls INTEGER,
	updated_at TIMESTAMP NOT NULL
)`

var (
	deviceCallsSchemaMu    sync.Mutex
	deviceCallsSchemaReady = map[*sql.DB]bool{}
)

// ensureDeviceCallsTable creates the table on first use and adds max_calls to a table created
// before the column existed (stage 2b). Existing rows keep max_calls NULL, which means 1.
func (r *SQLiteRepository) ensureDeviceCallsTable() error {
	deviceCallsSchemaMu.Lock()
	defer deviceCallsSchemaMu.Unlock()
	if deviceCallsSchemaReady[r.db] {
		return nil
	}
	if _, err := r.db.Exec(createDeviceCallsTable); err != nil {
		return err
	}
	var columns int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('elphant_device_calls') WHERE name = 'max_calls'`).Scan(&columns); err != nil {
		return err
	}
	if columns == 0 {
		_, err := r.db.Exec(`ALTER TABLE elphant_device_calls ADD COLUMN max_calls INTEGER`)
		if err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column name") {
			return err
		}
	}
	deviceCallsSchemaReady[r.db] = true
	return nil
}

// SetDeviceCallsEnabled stores whether calls are on for a device slot; its call limit is kept.
func (r *SQLiteRepository) SetDeviceCallsEnabled(deviceID string, enabled bool) error {
	return r.SetDeviceCallsConfig(deviceID, enabled, nil)
}

// SetDeviceCallsConfig stores a slot's calls switch and, when maxCalls is not nil, its call
// limit. A nil maxCalls keeps the stored limit (NULL for a new row).
func (r *SQLiteRepository) SetDeviceCallsConfig(deviceID string, enabled bool, maxCalls *int) error {
	if strings.TrimSpace(deviceID) == "" {
		return fmt.Errorf("device id is required")
	}
	if err := r.ensureDeviceCallsTable(); err != nil {
		return err
	}
	var limit any
	if maxCalls != nil {
		limit = *maxCalls
	}
	_, err := r.db.Exec(`INSERT INTO elphant_device_calls (device_id, enabled, max_calls, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(device_id) DO UPDATE SET enabled = excluded.enabled,
			max_calls = COALESCE(excluded.max_calls, elphant_device_calls.max_calls),
			updated_at = excluded.updated_at`, deviceID, enabled, limit, time.Now())
	return err
}

// SeedDeviceCallsEnabled stores a slot's first value; it never overwrites an existing one.
func (r *SQLiteRepository) SeedDeviceCallsEnabled(deviceID string, enabled bool) error {
	if strings.TrimSpace(deviceID) == "" {
		return fmt.Errorf("device id is required")
	}
	if err := r.ensureDeviceCallsTable(); err != nil {
		return err
	}
	_, err := r.db.Exec(`INSERT INTO elphant_device_calls (device_id, enabled, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(device_id) DO NOTHING`, deviceID, enabled, time.Now())
	return err
}

// GetDeviceCallsEnabled reads a device slot's calls switch; found is false when the slot
// has never been set.
func (r *SQLiteRepository) GetDeviceCallsEnabled(deviceID string) (enabled, found bool, err error) {
	if err := r.ensureDeviceCallsTable(); err != nil {
		return false, false, err
	}
	err = r.db.QueryRow(`SELECT enabled FROM elphant_device_calls WHERE device_id = ?`, deviceID).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return enabled, true, nil
}

// GetDeviceCallsMaxCalls reads a device slot's call limit; set is false when the slot has no
// row or its limit was never stored (NULL).
func (r *SQLiteRepository) GetDeviceCallsMaxCalls(deviceID string) (maxCalls int, set bool, err error) {
	if err := r.ensureDeviceCallsTable(); err != nil {
		return 0, false, err
	}
	var v sql.NullInt64
	err = r.db.QueryRow(`SELECT max_calls FROM elphant_device_calls WHERE device_id = ?`, deviceID).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if !v.Valid {
		return 0, false, nil
	}
	return int(v.Int64), true, nil
}
```

- [ ] **Step 4: Run the storage tests**

Run: `cd src && GOTOOLCHAIN=auto go test ./infrastructure/chatstorage/ -run 'DeviceCalls'`
Expected: `ok`. That includes the existing `TestDeviceCallsEnabledRoundTrip` and
`TestSeedDeviceCallsEnabledNeverOverwrites`.

- [ ] **Step 5: Run the package**

Run: `cd src && GOTOOLCHAIN=auto go test ./infrastructure/chatstorage/`
Expected: `ok`.

- [ ] **Step 6: Commit**

```bash
git add src/infrastructure/chatstorage/sqlite_repository_calls.go src/infrastructure/chatstorage/sqlite_repository_calls_test.go
git commit -m "feat(calls): nullable max_calls column in elphant_device_calls"
```

---

### Task 3: The gateway slot pool (D-20)

**Files:**
- Create: `src/infrastructure/whatsapp/calls_slots.go`
- Create: `src/infrastructure/whatsapp/calls_slots_test.go`

**Interfaces:**
- `type callSlots struct` with `newCallSlots(max func() int) *callSlots`
- `(*callSlots).tryAcquire() bool`, `release()`, `inUse() int`, `limit() int`. Every method is
  nil-safe: a nil pool never refuses and counts nothing.
- `var gatewayCallSlots = newCallSlots(config.CallsMaxTotal)`

- [ ] **Step 1: Write the failing tests**

Create `src/infrastructure/whatsapp/calls_slots_test.go`:

```go
package whatsapp

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCallSlotsCap(t *testing.T) {
	limit := 2
	p := newCallSlots(func() int { return limit })
	assert.True(t, p.tryAcquire())
	assert.True(t, p.tryAcquire())
	assert.False(t, p.tryAcquire(), "the third slot is over the cap")
	assert.Equal(t, 2, p.inUse())
	assert.Equal(t, 2, p.limit())

	p.release()
	assert.True(t, p.tryAcquire())

	limit = 0 // read at use time; 0 means no cap
	assert.True(t, p.tryAcquire())
	assert.Equal(t, 3, p.inUse())
	assert.Equal(t, 0, p.limit())
}

func TestCallSlotsReleaseNeverGoesNegative(t *testing.T) {
	p := newCallSlots(func() int { return 1 })
	p.release()
	assert.Equal(t, 0, p.inUse())
	assert.True(t, p.tryAcquire())
	assert.False(t, p.tryAcquire())
}

func TestNilCallSlotsNeverRefuse(t *testing.T) {
	var p *callSlots
	assert.True(t, p.tryAcquire())
	p.release()
	assert.Equal(t, 0, p.inUse())
	assert.Equal(t, 0, p.limit())
}

func TestCallSlotsConcurrentAcquire(t *testing.T) {
	p := newCallSlots(func() int { return 5 })
	var wg sync.WaitGroup
	var mu sync.Mutex
	won := 0
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if p.tryAcquire() {
				mu.Lock()
				won++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, 5, won)
	assert.Equal(t, 5, p.inUse())
}
```

- [ ] **Step 2: Run them and see them fail**

Run: `cd src && GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/... -race -run 'TestCallSlots|TestNilCallSlots'`
Expected: compile error, `undefined: newCallSlots`.

- [ ] **Step 3: Implement**

Create `src/infrastructure/whatsapp/calls_slots.go`:

```go
package whatsapp

import (
	"sync"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/sirupsen/logrus"
)

// Fork (elphant): the gateway-wide call slot pool (concurrent calls, D-20, D-21). See
// docs/specs/2026-11-14-gateway-concurrent-calls-design.md.

// callSlots counts the call slots held by every CallManager of the process. Its mutex is a
// leaf: a manager takes it inside m.mu, and the pool never calls out while holding it. A nil
// pool never refuses and counts nothing.
type callSlots struct {
	mu   sync.Mutex
	held int
	max  func() int // the cap, read at use time; <= 0: no cap
}

func newCallSlots(max func() int) *callSlots { return &callSlots{max: max} }

// gatewayCallSlots is the pool shared by every manager that attachCalls creates.
var gatewayCallSlots = newCallSlots(config.CallsMaxTotal)

// tryAcquire takes one slot unless the cap is reached.
func (p *callSlots) tryAcquire() bool {
	if p == nil {
		return true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if limit := p.limitLocked(); limit > 0 && p.held >= limit {
		return false
	}
	p.held++
	return true
}

// release gives one slot back. A release with nothing held is a bug elsewhere: it is logged
// and ignored, so the count never goes negative.
func (p *callSlots) release() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.held == 0 {
		logrus.Warn("[CALLS] released a gateway call slot that was not held")
		return
	}
	p.held--
}

func (p *callSlots) inUse() int {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.held
}

// limit is the cap in force (0: none).
func (p *callSlots) limit() int {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.limitLocked()
}

func (p *callSlots) limitLocked() int {
	if p.max == nil {
		return 0
	}
	if v := p.max(); v > 0 {
		return v
	}
	return 0
}
```

- [ ] **Step 4: Run the tests**

Run: `cd src && GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/... -race -run 'TestCallSlots|TestNilCallSlots'`
Expected: `ok`.

- [ ] **Step 5: Commit**

```bash
git add src/infrastructure/whatsapp/calls_slots.go src/infrastructure/whatsapp/calls_slots_test.go
git commit -m "feat(calls): gateway-wide call slot pool"
```

---

### Task 4: The manager: one counting rule, atomic reservation, refusal reasons (D-21, D-24 to D-28)

**Files:**
- Modify: `src/infrastructure/whatsapp/calls.go`
- Modify: `src/infrastructure/whatsapp/calls_test.go` (one existing assertion)
- Create: `src/infrastructure/whatsapp/calls_concurrent_test.go`

**Interfaces:**
- `callManagerOptions` gains `Limit func() int` (the device's own limit, called without `m.mu`;
  nil means `MaxCalls`) and `Pool *callSlots` (nil means no gateway accounting). `MaxCalls` is
  now the ceiling.
- New error: `ErrGatewayCallLimit = pkgError.ValidationError("call capacity of this gateway reached")`.
- New constants: `refusalDevice = "device_limit"`, `refusalGateway = "gateway_limit"` (used as
  `end_reason`).
- `activeCountLocked` is removed and replaced by `heldLocked`, `reserveLocked`,
  `unreserveLocked` and `settleLocked`.

**The rule (spec D-21):** a device's count is `m.reserved + len(m.calls)`. Every tracked call
holds one device slot and one pool slot. A slot is reserved under `m.mu` (device check first,
then `Pool.tryAcquire`, then `reserved++`) before the call is tracked. `settleLocked` then hands it
over: `reserved--` and `insertLocked`. If the call ended during tracking, it is not inserted, so
the pool slot is released right there. `finish` releases the pool slot when it deletes the call
from `m.calls`. `Answer` no longer checks any limit (D-27).

- [ ] **Step 1: Write the failing tests**

Create `src/infrastructure/whatsapp/calls_concurrent_test.go`:

```go
package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Fork (elphant): concurrent calls (docs/specs/2026-11-14-gateway-concurrent-calls-design.md).

func newPooledManager(t *testing.T, ceiling int, limit func() int, pool *callSlots, now *time.Time, media mediaFactory) (*CallManager, *fakeEngine, *eventLog) {
	t.Helper()
	eng := &fakeEngine{}
	log := &eventLog{}
	m := newCallManager(eng, callManagerOptions{
		MaxCalls: ceiling, Limit: limit, Pool: pool,
		RingWindow: time.Minute, OfferWait: 200 * time.Millisecond,
		Now: func() time.Time { return *now }, Emit: log.add, Media: media,
	})
	return m, eng, log
}

func heldOf(m *CallManager) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.heldLocked()
}

func TestRingingInboundHoldsTheDeviceSlot(t *testing.T) {
	now := time.Now()
	m, eng, log := newTestManager(t, 1, &now, nil)
	eng.incoming(&fakeCall{id: "IN-1"}) // rings, not answered

	second := &fakeCall{id: "IN-2"}
	eng.incoming(second)
	assert.True(t, second.rejected, "a ringing call holds the only slot")
	ended, ok := log.last(EventCallEnded)
	require.True(t, ok)
	assert.Equal(t, "IN-2", ended.Call.CallID)
	assert.Equal(t, "MISSED", ended.Status)
	assert.Equal(t, "device_limit", ended.EndReason)

	_, _, err := m.Start(context.Background(), "+5511900000000", "")
	assert.ErrorIs(t, err, ErrCallLimit)
	require.NoError(t, m.Answer("IN-1", ""), "the admitted call is still answered")
}

func TestDeviceLimitFollowsTheLimitFunc(t *testing.T) {
	now := time.Now()
	limit := 2
	m, _, _ := newPooledManager(t, 3, func() int { return limit }, nil, &now, nil)

	first, _, err := m.Start(context.Background(), "+5511900000001", "")
	require.NoError(t, err)
	_, _, err = m.Start(context.Background(), "+5511900000002", "")
	require.NoError(t, err)
	_, _, err = m.Start(context.Background(), "+5511900000003", "")
	assert.ErrorIs(t, err, ErrCallLimit)

	limit = 3
	_, _, err = m.Start(context.Background(), "+5511900000003", "")
	require.NoError(t, err)

	limit = 1 // lowering never ends live calls (D-24)
	assert.Len(t, m.Active(), 3)
	require.NoError(t, m.Hangup(first.CallID))
	_, _, err = m.Start(context.Background(), "+5511900000004", "")
	assert.ErrorIs(t, err, ErrCallLimit, "still above the lowered limit")

	limit = 9 // capped by the ceiling
	_, _, err = m.Start(context.Background(), "+5511900000004", "")
	require.NoError(t, err)
	_, _, err = m.Start(context.Background(), "+5511900000005", "")
	assert.ErrorIs(t, err, ErrCallLimit)

	limit = 0 // a bad stored value reads as 1, and 3 calls are live
	_, _, err = m.Start(context.Background(), "+5511900000006", "")
	assert.ErrorIs(t, err, ErrCallLimit)
}

func TestGatewayPoolIsSharedAcrossManagers(t *testing.T) {
	now := time.Now()
	pool := newCallSlots(func() int { return 2 })
	a, _, _ := newPooledManager(t, 3, nil, pool, &now, nil)
	b, engB, logB := newPooledManager(t, 3, nil, pool, &now, nil)

	callA, _, err := a.Start(context.Background(), "+5511900000001", "")
	require.NoError(t, err)
	_, _, err = b.Start(context.Background(), "+5511900000002", "")
	require.NoError(t, err)
	assert.Equal(t, 2, pool.inUse())

	_, _, err = a.Start(context.Background(), "+5511900000003", "")
	assert.ErrorIs(t, err, ErrGatewayCallLimit)
	assert.NotContains(t, err.Error(), "too many calls", "the CRM matches substrings")
	assert.Equal(t, "call capacity of this gateway reached", err.Error())

	in := &fakeCall{id: "IN-B"}
	engB.incoming(in)
	assert.True(t, in.rejected)
	ended, ok := logB.last(EventCallEnded)
	require.True(t, ok)
	assert.Equal(t, "MISSED", ended.Status)
	assert.Equal(t, "gateway_limit", ended.EndReason)

	require.NoError(t, a.Hangup(callA.CallID))
	assert.Equal(t, 1, pool.inUse())
	_, _, err = b.Start(context.Background(), "+5511900000003", "")
	require.NoError(t, err)
}

func TestDeviceLimitIsCheckedBeforeTheGatewayCap(t *testing.T) {
	now := time.Now()
	pool := newCallSlots(func() int { return 2 })
	a, _, _ := newPooledManager(t, 1, nil, pool, &now, nil)
	b, engB, logB := newPooledManager(t, 1, nil, pool, &now, nil)
	_, _, err := a.Start(context.Background(), "+5511900000001", "")
	require.NoError(t, err)
	_, _, err = b.Start(context.Background(), "+5511900000002", "")
	require.NoError(t, err)

	_, _, err = b.Start(context.Background(), "+5511900000003", "")
	assert.ErrorIs(t, err, ErrCallLimit, "device and gateway are both full: the device wins")
	engB.incoming(&fakeCall{id: "IN-B"})
	ended, _ := logB.last(EventCallEnded)
	assert.Equal(t, "device_limit", ended.EndReason)
}

func TestConcurrentStartsAcrossManagersRespectTheGatewayCap(t *testing.T) {
	now := time.Now()
	pool := newCallSlots(func() int { return 3 })
	a, _, _ := newPooledManager(t, 3, nil, pool, &now, nil)
	b, _, _ := newPooledManager(t, 3, nil, pool, &now, nil)
	const n = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, limited := 0, 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			m := a
			if i%2 == 1 {
				m = b
			}
			_, _, err := m.Start(context.Background(), fmt.Sprintf("+55119000000%02d", i), "")
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, ErrCallLimit), errors.Is(err, ErrGatewayCallLimit):
				limited++
			}
		}(i)
	}
	wg.Wait()
	assert.Equal(t, 3, ok)
	assert.Equal(t, n-3, limited)
	assert.Equal(t, 3, pool.inUse())
	assert.Equal(t, 3, heldOf(a)+heldOf(b))
}

func TestAnswerIgnoresALoweredLimit(t *testing.T) {
	now := time.Now()
	limit := 2
	m, eng, _ := newPooledManager(t, 2, func() int { return limit }, nil, &now, nil)
	eng.incoming(&fakeCall{id: "IN-1"})
	eng.incoming(&fakeCall{id: "IN-2"})
	limit = 1
	require.NoError(t, m.Answer("IN-1", ""))
	require.NoError(t, m.Answer("IN-2", ""), "an admitted call can be answered (D-27)")
}

// Every way a call can end, or fail to start, gives back its device slot and its gateway slot
// exactly once.
func TestEveryEndPathReleasesTheGatewaySlot(t *testing.T) {
	now := time.Now()
	pool := newCallSlots(func() int { return 0 }) // no cap: the pool still counts
	media := &fakeMediaFactory{}
	m, eng, _ := newPooledManager(t, 8, nil, pool, &now, media)
	free := func(path string) {
		t.Helper()
		assert.Equal(t, 0, pool.inUse(), path)
		assert.Equal(t, 0, heldOf(m), path)
	}
	ctx := context.Background()

	// Outbound: bad SDP, engine error, synchronous end during tracking.
	media.err = errors.New("bad sdp")
	_, _, err := m.Start(ctx, "+5511900000001", "v=0 browser")
	require.Error(t, err)
	media.err = nil
	free("bad sdp")
	eng.err = errors.New("dial failed")
	_, _, err = m.Start(ctx, "+5511900000001", "v=0 browser")
	require.Error(t, err)
	free("engine error")
	eng.syncEnd = true
	_, _, err = m.Start(ctx, "+5511900000001", "v=0 browser")
	require.ErrorIs(t, err, ErrCallNotFound)
	eng.syncEnd = false
	free("outbound synchronous end")

	// Outbound: agent hangup and peer end.
	info, _, err := m.Start(ctx, "+5511900000002", "v=0 browser")
	require.NoError(t, err)
	require.NoError(t, m.Hangup(info.CallID))
	free("hangup")
	_, _, err = m.Start(ctx, "+5511900000003", "v=0 browser")
	require.NoError(t, err)
	eng.mu.Lock()
	out := eng.placed[len(eng.placed)-1]
	eng.mu.Unlock()
	out.end("")
	free("peer end")

	// Inbound: synchronous end during tracking, reject, ring timeout, failed answer, browser loss.
	eng.incoming(&fakeCall{id: "IN-SYNC", syncEnd: true})
	free("inbound synchronous end")
	eng.incoming(&fakeCall{id: "IN-REJ"})
	require.NoError(t, m.Reject("IN-REJ"))
	free("reject")
	eng.incoming(&fakeCall{id: "IN-TIMEOUT"})
	now = now.Add(2 * time.Minute)
	m.Active() // prune rejects the expired offer
	free("ring timeout")
	eng.incoming(&fakeCall{id: "IN-FAIL", answerErr: errors.New("answer failed")})
	_, err = m.OfferSDP(ctx, "IN-FAIL")
	require.NoError(t, err)
	require.Error(t, m.Answer("IN-FAIL", "v=0 answer"))
	free("failed answer")
	eng.incoming(&fakeCall{id: "IN-LOST"})
	_, err = m.OfferSDP(ctx, "IN-LOST")
	require.NoError(t, err)
	require.NoError(t, m.Answer("IN-LOST", "v=0 answer"))
	media.last().ev.Lost()
	free("browser leg lost")

	// Several live calls, then closeAll (switch off, client discarded, shutdown).
	eng.incoming(&fakeCall{id: "IN-A"})
	_, _, err = m.Start(ctx, "+5511900000004", "v=0 browser")
	require.NoError(t, err)
	assert.Equal(t, 2, pool.inUse())
	m.closeAll()
	free("closeAll")
}

func TestRefusalLogNamesTheReason(t *testing.T) {
	now := time.Now()
	pool := newCallSlots(func() int { return 1 })
	a, _, _ := newPooledManager(t, 1, nil, pool, &now, nil)
	assert.True(t, strings.Contains(a.refusalText(refusalDevice, 1), "device at its call limit (1)"))
	assert.True(t, strings.Contains(a.refusalText(refusalGateway, 1), "gateway at its call capacity (1)"))
}
```

In `src/infrastructure/whatsapp/calls_test.go`, `TestIncomingAboveCapIsRejectedAndMissed`, change
`assert.Equal(t, "rejected", ended.EndReason)` to `assert.Equal(t, "device_limit", ended.EndReason)`.

- [ ] **Step 2: Run them and see them fail**

Run: `cd src && GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/... -race -run 'TestRingingInbound|TestDeviceLimit|TestGatewayPool|TestConcurrentStartsAcross|TestAnswerIgnores|TestEveryEndPath|TestRefusalLog|TestIncomingAboveCap'`
Expected: compile error (`unknown field Limit`, `undefined: ErrGatewayCallLimit`).

- [ ] **Step 3: Implement in `src/infrastructure/whatsapp/calls.go`**

1. Add `"fmt"` to the imports.
2. In the error `var` block, add after `ErrCallLimit`:

```go
	ErrGatewayCallLimit = pkgError.ValidationError("call capacity of this gateway reached") // never "too many calls": the CRM matches substrings (D-26)
```

3. After the `local*` constants, add:

```go
// Why an offer or a start was refused for capacity; also call.ended's end_reason (D-25).
const (
	refusalDevice  = "device_limit"
	refusalGateway = "gateway_limit"
)
```

4. In `callManagerOptions`, replace the `MaxCalls int` line with:

```go
	MaxCalls    int        // ceiling per device (WHATSAPP_CALLS_MAX_PER_DEVICE)
	Limit       func() int // the device's own limit (stored max_calls); nil: MaxCalls. Called without m.mu (D-24)
	Pool        *callSlots // the gateway-wide slot pool; nil: no gateway accounting (D-20)
```

5. In `CallManager`, change the comment of `reserved` to
   `// slots claimed for calls about to be tracked (inbound and outbound, D-21)`.
6. Replace `onIncoming` with:

```go
func (m *CallManager) onIncoming(h callHandle) {
	m.prune()
	limit := m.deviceLimit()
	id := h.ID()
	m.mu.Lock()
	refusal := m.reserveLocked(limit)
	if refusal != "" {
		m.gone[id] = m.opts.Now()
		m.notifyLocked(id)
	}
	m.mu.Unlock()
	if refusal != "" {
		info := CallInfo{CallID: id, Direction: DirectionUser, Peer: h.Peer(), State: stateRinging, StartedAt: m.opts.Now()}
		logrus.Infof("[CALLS] rejecting %s from %s: %s", id, maskPeer(info.Peer), m.refusalText(refusal, limit))
		_ = h.Reject()
		m.emit(CallEvent{Type: EventCallEnded, Call: info, Status: "MISSED", EndReason: refusal, At: m.opts.Now()})
		return
	}
	tc := m.newTracked(h, DirectionUser, stateRinging, nil)
	m.mu.Lock()
	m.settleLocked(tc)
	m.mu.Unlock()
}
```

7. Replace `activeCountLocked` with:

```go
// deviceLimit is the device's effective limit now: min(own limit, ceiling), the own limit read
// as at least 1 (D-24). Limit may read the store, so callers must not hold m.mu.
func (m *CallManager) deviceLimit() int {
	limit := m.opts.MaxCalls
	if m.opts.Limit != nil {
		own := m.opts.Limit()
		if own < 1 {
			own = 1
		}
		if own < limit {
			limit = own
		}
	}
	return limit
}

// heldLocked counts the device's slots: reservations plus tracked calls. Every tracked call
// holds one, ringing inbound calls included (D-21).
func (m *CallManager) heldLocked() int { return m.reserved + len(m.calls) }

// reserveLocked claims a device slot and a gateway slot for a call about to be tracked, or
// returns why it cannot. The device check runs first (D-25, D-26).
func (m *CallManager) reserveLocked(limit int) string {
	if m.heldLocked() >= limit {
		return refusalDevice
	}
	if !m.opts.Pool.tryAcquire() {
		return refusalGateway
	}
	m.reserved++
	return ""
}

// unreserveLocked gives back a reservation that never became a tracked call.
func (m *CallManager) unreserveLocked() {
	m.reserved--
	m.opts.Pool.release()
}

// settleLocked hands a reservation over to tc. A call that ended while it was being tracked is
// not inserted, so its gateway slot is given back here; otherwise finish gives it back.
func (m *CallManager) settleLocked(tc *trackedCall) {
	m.reserved--
	m.insertLocked(tc)
	if tc.ended {
		m.opts.Pool.release()
	}
}

// refusalText is the log text of a refusal (D-28).
func (m *CallManager) refusalText(refusal string, limit int) string {
	if refusal == refusalGateway {
		return fmt.Sprintf("gateway at its call capacity (%d)", m.opts.Pool.limit())
	}
	return fmt.Sprintf("device at its call limit (%d)", limit)
}
```

8. In `Answer`, delete this block (D-27):

```go
	if m.activeCountLocked() >= m.opts.MaxCalls {
		m.mu.Unlock()
		return ErrCallLimit
	}
```

9. In `Start`, replace everything from `m.prune()` to the end of the `release` closure with:

```go
	m.prune()
	limit := m.deviceLimit()
	m.mu.Lock()
	refusal := m.reserveLocked(limit)
	m.mu.Unlock()
	if refusal != "" {
		logrus.Infof("[CALLS] refusing an outbound call on device %s: %s", maskSlot(m.slotID), m.refusalText(refusal, limit))
		if refusal == refusalGateway {
			return CallInfo{}, "", ErrGatewayCallLimit
		}
		return CallInfo{}, "", ErrCallLimit
	}
	release := func() {
		m.mu.Lock()
		m.unreserveLocked()
		m.mu.Unlock()
	}
```

   Further down in `Start`, replace

```go
	m.mu.Lock()
	m.reserved--
	m.insertLocked(tc)
```

   with

```go
	m.mu.Lock()
	m.settleLocked(tc)
```

10. In `finish`, replace

```go
	if m.calls[id] == tc {
		delete(m.calls, id)
	}
```

    with

```go
	if m.calls[id] == tc {
		delete(m.calls, id)
		m.opts.Pool.release() // a tracked call holds a gateway slot until it ends (D-21)
	}
```

- [ ] **Step 4: Run the new tests**

Run: `cd src && GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/... -race -run 'TestRingingInbound|TestDeviceLimit|TestGatewayPool|TestConcurrentStartsAcross|TestAnswerIgnores|TestEveryEndPath|TestRefusalLog|TestIncomingAboveCap|TestConcurrentStartRespectsCap|TestStartRespectsCap|TestEndBeforeTrackLeavesNoEntry|TestStartEngineErrorFreesSlot'`
Expected: `ok`.

If `TestEveryEndPathReleasesTheGatewaySlot` fails on the "browser leg lost" step because
`media.last()` is not the inbound leg, check that `OfferSDP` created it. `fakeMediaFactory.Inbound`
appends to `made`, so `last()` is the latest leg. Do not weaken the assertion.

- [ ] **Step 5: Run the whole package with the race detector**

Run: `cd src && GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/... -race`
Expected: `ok`. An older test can fail only because it keeps two inbound calls ringing on a
manager built with `MaxCalls: 1` (a slot is now held while ringing). Then raise that test's
`MaxCalls`/`newTestManager` value to the number of calls it holds at once, and change nothing
else in it. Name every test changed this way in the commit body. `TestMaxDurationFinishesSilentEngine`
is a known flake (D-18): rerun it alone before treating it as a regression.

- [ ] **Step 6: Commit**

```bash
git add src/infrastructure/whatsapp/calls.go src/infrastructure/whatsapp/calls_test.go src/infrastructure/whatsapp/calls_concurrent_test.go
git commit -m "feat(calls): count ringing calls, reserve device and gateway slots atomically"
```

---

### Task 5: Per-device limit at runtime and the wiring (D-22 to D-24)

**Files:**
- Modify: `src/infrastructure/whatsapp/calls_switch.go`, `calls_switch_test.go`
- Modify: `src/infrastructure/whatsapp/calls_slot_test.go`
- Modify: `src/infrastructure/whatsapp/calls_registry.go`, `calls_registry_test.go`
- Modify: `src/infrastructure/whatsapp/device_manager.go` (one line)

**Interfaces:**
- `DeviceCallsStore` gains `SetDeviceCallsConfig(deviceID string, enabled bool, maxCalls *int) error`
  and `GetDeviceCallsMaxCalls(deviceID string) (maxCalls int, set bool, err error)`. Task 2
  implemented both on `*SQLiteRepository`.
- `type DeviceCallsState struct { Enabled bool; MaxCalls, MaxCallsCeiling, MaxCallsTotal int }`
- `ConfigureDeviceCalls(deviceID string, enabled bool, maxCalls *int) (DeviceCallsState, error)`
- `DeviceCallsSettings(deviceID string) (DeviceCallsState, error)`
- `deviceMaxCalls(slotID string) int` (stored value, 1 when unset), `forgetDeviceCallsCache(slotID string)`
- `SetDeviceCalls` and `DeviceCalls` keep their signatures as wrappers.

- [ ] **Step 1: Extend the fake store**

In `src/infrastructure/whatsapp/calls_switch_test.go`, add a `limits map[string]int` field to
`fakeCallsStore`. Initialize it in `newFakeCallsStore` (`limits: map[string]int{}`), and add:

```go
func (s *fakeCallsStore) SetDeviceCallsConfig(id string, on bool, maxCalls *int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[id] = on
	if maxCalls != nil {
		s.limits[id] = *maxCalls
	}
	return nil
}

func (s *fakeCallsStore) GetDeviceCallsMaxCalls(id string) (int, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.limits[id]
	return n, ok, nil
}

func intPtr(v int) *int { return &v }

// withCallsLimits sets the two env limits for one test.
func withCallsLimits(t *testing.T, ceiling, total int) {
	per, tot := config.WhatsappCallsMaxPerDevice, config.WhatsappCallsMaxTotal
	t.Cleanup(func() { config.WhatsappCallsMaxPerDevice, config.WhatsappCallsMaxTotal = per, tot })
	config.WhatsappCallsMaxPerDevice, config.WhatsappCallsMaxTotal = ceiling, total
}
```

- [ ] **Step 2: Write the failing tests**

Append to `src/infrastructure/whatsapp/calls_switch_test.go`:

```go
func TestConfigureDeviceCallsStoresTheLimit(t *testing.T) {
	withCallsConfig(t, true, nil)
	withCallsLimits(t, 3, 6)
	reg := newFakeCallsStore()
	withCallsStore(t, reg)
	withResolvedSlot(t, "slot-a", &whatsmeow.Client{Store: &store.Device{}})

	got, err := DeviceCallsSettings("slot-a")
	require.NoError(t, err)
	assert.Equal(t, DeviceCallsState{Enabled: true, MaxCalls: 1, MaxCallsCeiling: 3, MaxCallsTotal: 6}, got, "never set reads 1")

	got, err = ConfigureDeviceCalls("slot-a", true, intPtr(3))
	require.NoError(t, err)
	assert.Equal(t, DeviceCallsState{Enabled: true, MaxCalls: 3, MaxCallsCeiling: 3, MaxCallsTotal: 6}, got)
	assert.Equal(t, 3, deviceMaxCalls("slot-a"))

	got, err = ConfigureDeviceCalls("slot-a", true, nil) // omitted: unchanged
	require.NoError(t, err)
	assert.Equal(t, 3, got.MaxCalls)

	got, err = ConfigureDeviceCalls("slot-a", false, intPtr(2)) // both stored
	require.NoError(t, err)
	assert.False(t, got.Enabled)
	assert.Equal(t, 2, got.MaxCalls)
	assert.Equal(t, 2, reg.limits["slot-a"])
}

func TestConfigureDeviceCallsValidatesTheRange(t *testing.T) {
	withCallsConfig(t, true, nil)
	withCallsLimits(t, 3, 0)
	reg := newFakeCallsStore()
	withCallsStore(t, reg)
	withResolvedSlot(t, "slot-a", &whatsmeow.Client{Store: &store.Device{}})

	for _, v := range []int{0, -1, 4} {
		_, err := ConfigureDeviceCalls("slot-a", true, intPtr(v))
		require.Error(t, err, "max_calls %d", v)
		assert.Equal(t, "max_calls must be between 1 and 3", err.Error())
		var ge pkgError.GenericError
		require.ErrorAs(t, err, &ge)
		assert.Equal(t, 400, ge.StatusCode())
	}
	assert.Empty(t, reg.snapshot(), "a refused value stores nothing")

	config.WhatsappCallsEnabled = false
	_, err := ConfigureDeviceCalls("slot-a", true, intPtr(9))
	assert.ErrorIs(t, err, ErrCallsMasterOff, "the master switch is checked first")
}

func TestDeviceMaxCallsClampsAndCaches(t *testing.T) {
	withCallsConfig(t, true, nil)
	reg := newFakeCallsStore()
	reg.limits["slot-bad"] = 0
	withCallsStore(t, reg)
	assert.Equal(t, 1, deviceMaxCalls("slot-none"))
	assert.Equal(t, 1, deviceMaxCalls("slot-bad"), "a stored value below 1 reads 1")

	reg.limits["slot-none"] = 3 // written behind the cache's back
	assert.Equal(t, 1, deviceMaxCalls("slot-none"), "cached")
	forgetDeviceCallsCache("slot-none")
	assert.Equal(t, 3, deviceMaxCalls("slot-none"))
}
```

Add `pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"` to that test file's
imports.

Append to `src/infrastructure/whatsapp/calls_slot_test.go` (it follows
`TestCallsSwitchFollowsTheStartupClientToItsRegistrySlot`):

```go
// The call limit is keyed by the registry slot too; the manager's Limit closure must follow the
// startup client there (D-24).
func TestCallLimitFollowsTheStartupClientToItsRegistrySlot(t *testing.T) {
	withCallsConfig(t, true, nil)
	withCallsLimits(t, 3, 0)
	reg := newFakeCallsStore()
	withCallsStore(t, reg)
	dm := NewDeviceManager(nil, nil, nil)
	withDeviceManager(t, dm)

	ad := types.NewADJID("5511900000002", 0, 3)
	client := &whatsmeow.Client{Store: &store.Device{ID: &ad}}
	adSlot := ad.String()
	dm.EnsureDefault(NewDeviceInstance(adSlot, client, nil))

	eng := &fakeEngine{}
	m := newCallManager(eng, callManagerOptions{
		MaxCalls: config.CallsMaxPerDeviceCeiling(),
		Limit:    func() int { return deviceMaxCalls(callsSlotOf(client, adSlot)) },
	})
	m.slotID = adSlot

	const ulid = "01JTESTSLOT0000000000000001"
	dm.loadFromRegistry([]*domainChatStorage.DeviceRecord{{DeviceID: ulid, JID: ad.ToNonAD().String(), ADJID: adSlot}})
	_, err := ConfigureDeviceCalls(ulid, true, intPtr(2))
	require.NoError(t, err)

	_, _, err = m.Start(context.Background(), "+5511900000001", "")
	require.NoError(t, err)
	_, _, err = m.Start(context.Background(), "+5511900000003", "")
	require.NoError(t, err, "the ULID slot's limit of 2 applies")
	_, _, err = m.Start(context.Background(), "+5511900000004", "")
	assert.ErrorIs(t, err, ErrCallLimit)
	_, onAD := reg.limits[adSlot]
	assert.False(t, onAD, "no limit row under the AD JID")
}
```

Add `"context"` and `"github.com/aldinokemal/go-whatsapp-web-multidevice/config"` to that file's
imports if they are missing.

Append to `src/infrastructure/whatsapp/calls_registry_test.go`:

```go
func TestAttachCallsWiresTheLimitAndThePool(t *testing.T) {
	withCallsConfig(t, true, nil)
	withCallsLimits(t, 3, 6)
	reg := newFakeCallsStore()
	reg.limits["slot-a"] = 2
	withCallsStore(t, reg)

	client := &whatsmeow.Client{Store: &store.Device{}}
	attachCalls("slot-a", client)
	t.Cleanup(func() { forgetCalls(client) })
	v, ok := callsByClient.Load(client)
	require.True(t, ok)
	m := v.(*CallManager)
	assert.Equal(t, 3, m.opts.MaxCalls)
	assert.Same(t, gatewayCallSlots, m.opts.Pool)
	require.NotNil(t, m.opts.Limit)
	assert.Equal(t, 2, m.deviceLimit())
}
```

- [ ] **Step 3: Run them and see them fail**

Run: `cd src && GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/... -race -run 'TestConfigureDeviceCalls|TestDeviceMaxCalls|TestCallLimitFollows|TestAttachCallsWires'`
Expected: compile error (`undefined: DeviceCallsSettings`, `undefined: deviceMaxCalls`).

- [ ] **Step 4: Implement in `calls_switch.go`**

1. Add `"fmt"` to the imports.
2. Add the two methods to `DeviceCallsStore`:

```go
	// SetDeviceCallsConfig stores the switch and, when maxCalls is not nil, the call limit
	// (concurrent calls, D-22, D-23); a nil maxCalls keeps the stored limit.
	SetDeviceCallsConfig(deviceID string, enabled bool, maxCalls *int) error
	// GetDeviceCallsMaxCalls reads the call limit; set is false when it was never stored.
	GetDeviceCallsMaxCalls(deviceID string) (maxCalls int, set bool, err error)
```

3. Next to `deviceCallsCache`, add the limit cache and make the reset drop both:

```go
var deviceMaxCallsCache sync.Map // slot id -> int (stored max_calls, 1 when unset)

func resetDeviceCallsCache() {
	for _, c := range []*sync.Map{&deviceCallsCache, &deviceMaxCallsCache} {
		c.Range(func(k, _ any) bool {
			c.Delete(k)
			return true
		})
	}
}

// forgetDeviceCallsCache drops a slot's cached switch and limit (the slot was removed).
func forgetDeviceCallsCache(slotID string) {
	deviceCallsCache.Delete(slotID)
	deviceMaxCallsCache.Delete(slotID)
}

// deviceMaxCalls returns a slot's stored call limit, 1 when it was never set or reads below 1
// (D-22). A store error falls back to 1 and is not cached.
func deviceMaxCalls(slotID string) int {
	if v, ok := deviceMaxCallsCache.Load(slotID); ok {
		return v.(int)
	}
	deviceCallsMu.Lock()
	defer deviceCallsMu.Unlock()
	if v, ok := deviceMaxCallsCache.Load(slotID); ok {
		return v.(int)
	}
	reg := callsStoreFn()
	if reg == nil {
		return 1
	}
	n, set, err := reg.GetDeviceCallsMaxCalls(slotID)
	if err != nil {
		logrus.Warnf("[CALLS] read the call limit of %s: %v", maskSlot(slotID), err)
		return 1
	}
	if !set || n < 1 {
		n = 1
	}
	deviceMaxCallsCache.Store(slotID, n)
	return n
}
```

   (The old `resetDeviceCallsCache` body is replaced by the one above.)

4. Replace `SetDeviceCalls` and `DeviceCalls` with:

```go
// DeviceCallsState is what GET and PUT /devices/:device_id/calls report (D-23).
type DeviceCallsState struct {
	Enabled         bool
	MaxCalls        int // stored max_calls, 1 when never set
	MaxCallsCeiling int // WHATSAPP_CALLS_MAX_PER_DEVICE, at least 1
	MaxCallsTotal   int // WHATSAPP_CALLS_MAX_TOTAL, 0: no gateway cap
}

func deviceCallsState(slotID string, enabled bool) DeviceCallsState {
	return DeviceCallsState{
		Enabled:         enabled,
		MaxCalls:        deviceMaxCalls(slotID),
		MaxCallsCeiling: config.CallsMaxPerDeviceCeiling(),
		MaxCallsTotal:   config.CallsMaxTotal(),
	}
}

// ConfigureDeviceCalls switches calls on or off for a device and, when maxCalls is not nil,
// stores its call limit. Off ends the device's live calls; its next offers are left to the phone
// (the offer filter reads the switch on every offer). A new limit applies to the next offer or
// start and never ends a live call (D-24).
func ConfigureDeviceCalls(deviceID string, enabled bool, maxCalls *int) (DeviceCallsState, error) {
	if !config.WhatsappCallsEnabled {
		return DeviceCallsState{}, ErrCallsMasterOff
	}
	ceiling := config.CallsMaxPerDeviceCeiling()
	if maxCalls != nil && (*maxCalls < 1 || *maxCalls > ceiling) {
		return DeviceCallsState{}, pkgError.ValidationError(fmt.Sprintf("max_calls must be between 1 and %d", ceiling))
	}
	slotID, client, err := resolveCallsSlot(deviceID)
	if err != nil {
		return DeviceCallsState{}, err
	}
	reg := callsStoreFn()
	if reg == nil {
		return DeviceCallsState{}, pkgError.InternalServerError("device registry unavailable")
	}
	deviceCallsMu.Lock()
	err = reg.SetDeviceCallsConfig(slotID, enabled, maxCalls)
	if err == nil {
		deviceCallsCache.Store(slotID, enabled)
		if maxCalls != nil {
			deviceMaxCallsCache.Store(slotID, *maxCalls)
		}
	}
	deviceCallsMu.Unlock()
	if err != nil {
		return DeviceCallsState{}, err
	}
	if !enabled {
		endCallsOfSlot(slotID, client)
	}
	state := deviceCallsState(slotID, enabled) // after the unlock: deviceMaxCalls takes deviceCallsMu
	logrus.Infof("[CALLS] calls switched %s for device %s (max_calls %d, ceiling %d)", onOff(enabled), maskSlot(slotID), state.MaxCalls, state.MaxCallsCeiling)
	return state, nil
}

// SetDeviceCalls switches calls on or off and keeps the stored call limit.
func SetDeviceCalls(deviceID string, enabled bool) (bool, error) {
	state, err := ConfigureDeviceCalls(deviceID, enabled, nil)
	return state.Enabled, err
}

// DeviceCallsSettings reports a device's switch and limits right now.
func DeviceCallsSettings(deviceID string) (DeviceCallsState, error) {
	slotID, client, err := resolveCallsSlot(deviceID)
	if err != nil {
		return DeviceCallsState{}, err
	}
	return deviceCallsState(slotID, callsAllowedNow(slotID, client)), nil
}

// DeviceCalls reports whether calls are on for a device right now.
func DeviceCalls(deviceID string) (bool, error) {
	state, err := DeviceCallsSettings(deviceID)
	return state.Enabled, err
}
```

- [ ] **Step 5: Wire `attachCalls` and the device manager**

In `src/infrastructure/whatsapp/calls_registry.go`, `attachCalls`, replace the `MaxCalls:` line of
the options with:

```go
		MaxCalls:    config.CallsMaxPerDeviceCeiling(),
		Limit:       func() int { return deviceMaxCalls(callsSlotOf(client, slotID)) }, // concurrent calls, D-24
		Pool:        gatewayCallSlots,                                                  // concurrent calls, D-20
```

Extend the attach log line with the limits:

```go
	logrus.Infof("[CALLS] calls enabled for device %s (codec %s, early media %s, ceiling %d, gateway cap %d)", maskSlot(slotID), effectiveCodecName, parseCallsEarlyMedia(config.WhatsappCallsEarlyMedia), config.CallsMaxPerDeviceCeiling(), config.CallsMaxTotal())
```

In `src/infrastructure/whatsapp/device_manager.go:282`, replace
`deviceCallsCache.Delete(id) // Fork (elphant): the slot's calls switch goes with it.` with
`forgetDeviceCallsCache(id) // Fork (elphant): the slot's calls switch and limit go with it.`

- [ ] **Step 6: Run the tests**

Run: `cd src && GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/... -race -run 'TestConfigureDeviceCalls|TestDeviceMaxCalls|TestCallLimitFollows|TestAttachCallsWires|TestCallsSwitch|TestSetDeviceCalls|TestSwitchOff|TestDeviceCalls|TestRemoveDeviceDropsItsCallsSwitchCache'`
Expected: `ok`.

Then run: `cd src && GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/... ./infrastructure/chatstorage/... -race`
Expected: `ok`. This includes `var _ whatsapp.DeviceCallsStore = (*SQLiteRepository)(nil)` and
`TestCallsStoreOfUnwrapsTheDeviceWrapper`. If the type assertion in `callsStoreOf` fails, the real
store would silently be nil.

- [ ] **Step 7: Commit**

```bash
git add src/infrastructure/whatsapp/calls_switch.go src/infrastructure/whatsapp/calls_switch_test.go src/infrastructure/whatsapp/calls_slot_test.go src/infrastructure/whatsapp/calls_registry.go src/infrastructure/whatsapp/calls_registry_test.go src/infrastructure/whatsapp/device_manager.go
git commit -m "feat(calls): per-device max_calls applied at runtime, gateway pool wired"
```

---

### Task 6: REST: `max_calls` on `PUT/GET /devices/:device_id/calls` (D-23, D-26)

**Files:**
- Modify: `src/domains/call/call.go`, `src/usecase/call.go`
- Modify: `src/ui/rest/call_device.go`, `src/ui/rest/call_device_test.go`, `src/ui/rest/call_test.go`
- Modify: `docs/openapi.yaml`

**Interfaces:**
- `domainCall.DeviceCallsRequest` gains `MaxCalls *int` (`json:"max_calls" form:"max_calls"`).
- `domainCall.DeviceCallsResponse` becomes
  `{Enabled bool "enabled"; MaxCalls int "max_calls"; MaxCallsCeiling int "max_calls_ceiling"; MaxCallsTotal int "max_calls_total"}`.
- `ICallUsecase.SetDeviceCalls(ctx context.Context, deviceID string, enabled bool, maxCalls *int) (DeviceCallsResponse, error)`.
  `GetDeviceCalls` keeps its signature.

All three layers (domain, usecase, REST and its stub) change in this one task, so the build stays
green.

- [ ] **Step 1: Write the failing REST test**

In `src/ui/rest/call_test.go`, add the field `switchMax *int` to `callServiceStub`.

In `src/ui/rest/call_device_test.go`, replace the two stub methods with:

```go
func (s *callServiceStub) SetDeviceCalls(_ context.Context, deviceID string, enabled bool, maxCalls *int) (domainCall.DeviceCallsResponse, error) {
	if s.err != nil {
		return domainCall.DeviceCallsResponse{}, s.err
	}
	s.switchDevice, s.switchValue, s.switchMax = deviceID, enabled, maxCalls
	limit := 1
	if maxCalls != nil {
		limit = *maxCalls
	}
	return domainCall.DeviceCallsResponse{Enabled: enabled, MaxCalls: limit, MaxCallsCeiling: 3, MaxCallsTotal: 6}, nil
}

func (s *callServiceStub) GetDeviceCalls(_ context.Context, deviceID string) (domainCall.DeviceCallsResponse, error) {
	s.switchDevice = deviceID
	return domainCall.DeviceCallsResponse{Enabled: s.switchValue, MaxCalls: 2, MaxCallsCeiling: 3, MaxCallsTotal: 6}, s.err
}
```

Append:

```go
func TestDeviceCallsRoutesCarryMaxCalls(t *testing.T) {
	service := &callServiceStub{}
	app := fiber.New()
	app.Use(middleware.Recovery())
	InitRestDeviceCalls(app, service)

	put := func(body string) (int, map[string]any) {
		req := httptest.NewRequest("PUT", "/devices/slot-a/calls", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		require.NoError(t, err)
		raw, _ := io.ReadAll(resp.Body)
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		return resp.StatusCode, out
	}

	code, out := put(`{"enabled":true,"max_calls":3}`)
	assert.Equal(t, 200, code)
	require.NotNil(t, service.switchMax)
	assert.Equal(t, 3, *service.switchMax)
	assert.Equal(t, map[string]any{"enabled": true, "max_calls": float64(3), "max_calls_ceiling": float64(3), "max_calls_total": float64(6)}, out["results"])

	code, _ = put(`{"enabled":true}`)
	assert.Equal(t, 200, code)
	assert.Nil(t, service.switchMax, "omitted max_calls is passed as nil (unchanged)")
	code, _ = put(`{"enabled":true,"max_calls":null}`)
	assert.Equal(t, 200, code)
	assert.Nil(t, service.switchMax)

	service.err = pkgError.ValidationError("max_calls must be between 1 and 3")
	code, out = put(`{"enabled":true,"max_calls":4}`)
	assert.Equal(t, 400, code)
	assert.Equal(t, "max_calls must be between 1 and 3", out["message"])
	service.err = nil

	resp, err := app.Test(httptest.NewRequest("GET", "/devices/slot-a/calls", nil))
	require.NoError(t, err)
	raw, _ := io.ReadAll(resp.Body)
	var got struct {
		Results domainCall.DeviceCallsResponse `json:"results"`
	}
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Equal(t, domainCall.DeviceCallsResponse{Enabled: true, MaxCalls: 2, MaxCallsCeiling: 3, MaxCallsTotal: 6}, got.Results)
}
```

Add `pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"` to that test file's
imports.

- [ ] **Step 2: Run it and see it fail**

Run: `cd src && GOTOOLCHAIN=auto go test ./ui/rest/ -run TestDeviceCallsRoutes`
Expected: compile error (`unknown field MaxCalls`, the stub does not implement `ICallUsecase`).

- [ ] **Step 3: Implement**

In `src/domains/call/call.go`:

```go
	SetDeviceCalls(ctx context.Context, deviceID string, enabled bool, maxCalls *int) (DeviceCallsResponse, error)
```

```go
type DeviceCallsRequest struct {
	Enabled  *bool `json:"enabled" form:"enabled"`
	MaxCalls *int  `json:"max_calls" form:"max_calls"` // nil: keep the stored limit (concurrent calls, D-23)
}

type DeviceCallsResponse struct {
	Enabled         bool `json:"enabled"`
	MaxCalls        int  `json:"max_calls"`
	MaxCallsCeiling int  `json:"max_calls_ceiling"`
	MaxCallsTotal   int  `json:"max_calls_total"`
}
```

In `src/usecase/call.go`, replace `SetDeviceCalls` and `GetDeviceCalls` with:

```go
func (service serviceCall) SetDeviceCalls(_ context.Context, deviceID string, enabled bool, maxCalls *int) (domainCall.DeviceCallsResponse, error) {
	state, err := whatsapp.ConfigureDeviceCalls(deviceID, enabled, maxCalls)
	if err != nil {
		return domainCall.DeviceCallsResponse{}, err
	}
	return toDeviceCallsResponse(state), nil
}

func (service serviceCall) GetDeviceCalls(_ context.Context, deviceID string) (domainCall.DeviceCallsResponse, error) {
	state, err := whatsapp.DeviceCallsSettings(deviceID)
	if err != nil {
		return domainCall.DeviceCallsResponse{}, err
	}
	return toDeviceCallsResponse(state), nil
}

func toDeviceCallsResponse(s whatsapp.DeviceCallsState) domainCall.DeviceCallsResponse {
	return domainCall.DeviceCallsResponse{
		Enabled: s.Enabled, MaxCalls: s.MaxCalls, MaxCallsCeiling: s.MaxCallsCeiling, MaxCallsTotal: s.MaxCallsTotal,
	}
}
```

In `src/ui/rest/call_device.go`, `SetDeviceCalls`, change the service call to:

```go
	res, err := controller.Service.SetDeviceCalls(c.Context(), pathDeviceID(c), *request.Enabled, request.MaxCalls)
```

Change the file's header comment to also name concurrent calls:
`// Fork (elphant): the per-device voice calls switch (stage 2b, B7) and call limit (concurrent calls, D-23).`

- [ ] **Step 4: Run the tests**

Run: `cd src && GOTOOLCHAIN=auto go test ./ui/rest/ ./usecase/ -run 'Call'`
Expected: `ok`.

- [ ] **Step 5: Update `docs/openapi.yaml`**

Under `/devices/{device_id}/calls`:
- `get.summary`: `Calls switch and call limits of a device`. Both `200` descriptions:
  ``'`results`: `enabled`, `max_calls` (stored limit, 1 when never set), `max_calls_ceiling` (`WHATSAPP_CALLS_MAX_PER_DEVICE`), `max_calls_total` (`WHATSAPP_CALLS_MAX_TOTAL`, 0 = no gateway cap)'``.
- `put.summary`: `Switch calls and set the call limit of a device`. Add this paragraph to
  `put.description`: "`max_calls` (optional) is the device's limit of simultaneous calls, from 1
  to `WHATSAPP_CALLS_MAX_PER_DEVICE`. Omitted or null keeps the stored value. It applies to the
  next offer or start and never ends a live call."
- Add to the `put` request schema `properties`:

```yaml
                max_calls:
                  type: integer
                  minimum: 1
                  nullable: true
```

- The `put` `400` description: `Bad Request (missing enabled, max_calls out of range ("max_calls must be between 1 and N"), unknown device, or calls off on the gateway)`.

Under `/call/start`, append to the `400` description:
`; "too many calls on this device" (device limit) or "call capacity of this gateway reached" (WHATSAPP_CALLS_MAX_TOTAL)`.

Check the YAML still parses:
`python3 -c "import yaml; yaml.safe_load(open('docs/openapi.yaml'))" && echo ok` (from the
worktree root). If `python3` has no `yaml` module, check the diff by eye and keep the indentation
of the surrounding keys.

- [ ] **Step 6: Run the build and the REST package**

Run: `cd src && GOTOOLCHAIN=auto go build ./... && GOTOOLCHAIN=auto go test ./ui/rest/ ./usecase/`
Expected: build succeeds, both `ok`.

- [ ] **Step 7: Commit**

```bash
git add src/domains/call/call.go src/usecase/call.go src/ui/rest/call_device.go src/ui/rest/call_device_test.go src/ui/rest/call_test.go docs/openapi.yaml
git commit -m "feat(calls): max_calls and the limits on PUT/GET /devices/:device_id/calls"
```

---

### Task 7: Webhook contract: `device_limit` and `gateway_limit` (D-25)

**Files:**
- Modify: `contract/call.schema.json`, `contract/call_schema_test.go`
- Create: `contract/call-fixtures/call-ended-limit.json`
- Modify: `src/infrastructure/whatsapp/calls_webhook_test.go`
- Modify: `docs/webhook-payload.md`

- [ ] **Step 1: Write the failing fixture and test cases**

Create `contract/call-fixtures/call-ended-limit.json`:

```json
{
  "event": "call.ended",
  "timestamp": "2026-10-15T12:00:00Z",
  "device_id": "5511900000000@s.whatsapp.net",
  "payload": {
    "call_id": "A1B2C3D4E5F60718293A4B5C6D7E8F90",
    "peer": { "pn": "5511900000001@s.whatsapp.net", "lid": "123456789012345@lid" },
    "direction": "USER_INITIATED",
    "status": "MISSED",
    "end_reason": "gateway_limit",
    "duration": 0
  }
}
```

In `contract/call_schema_test.go`, `TestCallFixturesMatchSchema`, change `if len(files) < 5`
and its message to `6`. In `TestCallSchemaRejectsViolations`, add to `cases`:

```go
		"limit reason with another status": mutate("call-ended-limit.json", func(p map[string]any) { p["status"] = "REJECTED" }),
```

In `src/infrastructure/whatsapp/calls_webhook_test.go`, the fixture test with the `cases` map,
add:

```go
		"call-ended-limit.json": {Type: EventCallEnded, Call: in, Status: "MISSED", EndReason: "gateway_limit", At: at},
```

Run: `cd contract && GOTOOLCHAIN=auto go test ./...`
Expected: FAIL. The fixture violates the `end_reason` enum, and the status case is not rejected.

- [ ] **Step 2: Update the schema**

In `contract/call.schema.json`, in the `call.ended` payload definition (the one with
`"required": ["status", "end_reason", "duration"]`):
- Add `"device_limit", "gateway_limit"` to the `end_reason` enum.
- Add this rule so that both reasons only come with `MISSED` (an `allOf` entry, or an `if`/`then`
  at that level, depending on what the definition already uses):

```json
"if": { "properties": { "end_reason": { "enum": ["device_limit", "gateway_limit"] } }, "required": ["end_reason"] },
"then": { "properties": { "status": { "const": "MISSED" } } }
```

Keep `"unevaluatedProperties": false` working: `if`/`then` are applicators, so it does. Run the
existing violation cases to confirm.

- [ ] **Step 3: Run the contract and webhook tests**

Run: `cd contract && GOTOOLCHAIN=auto go test ./...`
Expected: `ok`.
Run: `cd src && GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/... -race -run 'Fixture|CallEvent'`
Expected: `ok`.

- [ ] **Step 4: Update `docs/webhook-payload.md`**

In the `call.ended` table:
- `status`: replace "or refused by `WHATSAPP_CALLS_MAX_PER_DEVICE`" with "or refused for
  capacity (`end_reason` `device_limit` or `gateway_limit`)".
- `end_reason`: add after `error (...)`:
  "`device_limit` (refused: the number is at its call limit, `max_calls` capped by
  `WHATSAPP_CALLS_MAX_PER_DEVICE`), `gateway_limit` (refused: the gateway is at
  `WHATSAPP_CALLS_MAX_TOTAL`). Both always come with `MISSED`, `duration` 0 and no recording."

Add to the bullet list after the example:
"- A ringing inbound call holds its slot until it is answered, rejected, answered elsewhere or
  rings out."

(Task 8 rewrites the bullet about `call.offer` and refused calls.)

- [ ] **Step 5: Commit**

```bash
git add contract/call.schema.json contract/call_schema_test.go contract/call-fixtures/call-ended-limit.json src/infrastructure/whatsapp/calls_webhook_test.go docs/webhook-payload.md
git commit -m "feat(calls): device_limit and gateway_limit in the call.ended contract"
```

---

### Task 8: No `call.offer` for an offer refused by a limit (D-31)

Today `handleCallOffer` (`event_call.go`, called from `event_handler.go:93`) forwards `call.offer`
on its own goroutine. `addCallOfferFields` calls `m.OfferSDP`, which waits in `waitTracked` up to
`OfferWait` for the engine to report the offer. A refused id is `gone`, so `OfferSDP` returns
`ErrCallNotFound`, and `call.offer` is sent anyway with `calls_enabled: true` and no `offer_sdp`.
That happens today with `MaxCalls=1`. After this task, the manager records why it refused, and
`addCallOfferFields` waits for the admission decision first. When the offer was refused, the
whole `call.offer` is dropped.

**Files:**
- Modify: `src/infrastructure/whatsapp/calls.go`
- Modify: `src/infrastructure/whatsapp/calls_webhook.go`
- Modify: `src/infrastructure/whatsapp/event_call.go` (two single fork lines)
- Modify: `src/infrastructure/whatsapp/calls_webhook_test.go`
- Modify: `docs/webhook-payload.md`

**Interfaces:**
- `CallManager.refused map[string]string`: call id → refusal reason. It is set in `onIncoming`'s
  refusal section, under the same `m.mu` hold that sets `gone[id]`, and pruned with `gone`.
- `(*CallManager).admission(ctx, callID string) string`: waits like `waitTracked` and returns ""
  (tracked, unknown, or wait expired) or the refusal reason.
- `addCallOfferFields(...) (suppress bool)`: it now returns true for a refused offer.
- `createCallOfferPayload` returns nil when `addCallOfferFields` suppresses. `forwardCallOfferToWebhook`
  sends nothing for a nil payload.

- [ ] **Step 1: Write the failing tests**

Append to `src/infrastructure/whatsapp/calls_webhook_test.go`:

```go
// An offer refused by a limit sends no call.offer, only call.ended (D-31). The webhook can run
// before the engine reports the offer, so the refusal is decided while it waits.
func TestRefusedOfferSendsNoCallOffer(t *testing.T) {
	withCallsConfig(t, true, nil)
	client := &whatsmeow.Client{}
	eng := &fakeEngine{}
	m := newCallManager(eng, callManagerOptions{MaxCalls: 1, Media: &fakeMediaFactory{}, OfferWait: time.Second})
	callsByClient.Store(client, m)
	t.Cleanup(func() { callsByClient.Delete(client) })
	eng.incoming(&fakeCall{id: "IN-1"}) // holds the only slot

	evt := &events.CallOffer{}
	evt.CallID = "IN-2"
	evt.CallCreator = types.NewJID("123456789012345", types.HiddenUserServer)

	done := make(chan bool, 1)
	go func() { done <- addCallOfferFields(context.Background(), map[string]any{}, evt, client) }()
	time.Sleep(50 * time.Millisecond) // the webhook runs first and waits
	eng.incoming(&fakeCall{id: "IN-2"})
	select {
	case suppress := <-done:
		assert.True(t, suppress, "a refused offer is not sent")
	case <-time.After(2 * time.Second):
		t.Fatal("addCallOfferFields did not return")
	}
	assert.Nil(t, createCallOfferPayload(context.Background(), evt, "dev", client, false), "no call.offer body")

	// Refused before the webhook runs: same result, without waiting.
	evt3 := &events.CallOffer{}
	evt3.CallID = "IN-3"
	evt3.CallCreator = evt.CallCreator
	eng.incoming(&fakeCall{id: "IN-3"})
	assert.True(t, addCallOfferFields(context.Background(), map[string]any{}, evt3, client))

	// The admitted offer is still sent, with its offer_sdp.
	first := &events.CallOffer{}
	first.CallID = "IN-1"
	first.CallCreator = evt.CallCreator
	payload := map[string]any{}
	assert.False(t, addCallOfferFields(context.Background(), payload, first, client))
	assert.Equal(t, "v=0 gowa-offer", payload["offer_sdp"])
	assert.NotNil(t, createCallOfferPayload(context.Background(), first, "dev", client, false))
}

// Without a browser leg, OfferSDP fails at once; the refusal must still be waited for.
func TestRefusedOfferSendsNoCallOfferWithoutMedia(t *testing.T) {
	withCallsConfig(t, true, nil)
	client := &whatsmeow.Client{}
	eng := &fakeEngine{}
	m := newCallManager(eng, callManagerOptions{MaxCalls: 1, OfferWait: time.Second})
	callsByClient.Store(client, m)
	t.Cleanup(func() { callsByClient.Delete(client) })
	eng.incoming(&fakeCall{id: "IN-1"})

	evt := &events.CallOffer{}
	evt.CallID = "IN-2"
	evt.CallCreator = types.NewJID("123456789012345", types.HiddenUserServer)
	done := make(chan bool, 1)
	go func() { done <- addCallOfferFields(context.Background(), map[string]any{}, evt, client) }()
	time.Sleep(50 * time.Millisecond)
	eng.incoming(&fakeCall{id: "IN-2"})
	assert.True(t, <-done)
}
```

In `TestCallOfferFields`, each `addCallOfferFields(...)` call returns a value now. Wrap each one
in `assert.False(t, addCallOfferFields(...))`. Those cases (flag off, admitted, group, device off)
are never suppressed, so upstream's `call.offer` is pinned.

- [ ] **Step 2: Run them and see them fail**

Run: `cd src && GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/... -race -run 'TestRefusedOffer|TestCallOfferFields'`
Expected: compile error (`addCallOfferFields (no value) used as value`).

- [ ] **Step 3: Implement in `calls.go`**

1. Add a field to `CallManager`, after `gone`:
   `refused map[string]string // call id -> refusal reason (device_limit, gateway_limit); pruned with gone (D-31)`.
   Initialize it in `newCallManager` (`refused: map[string]string{}`).
2. In `onIncoming`'s refusal section (the `if refusal != ""` block under `m.mu`, next to
   `m.gone[id] = ...`), add `m.refused[id] = refusal` before `m.notifyLocked(id)`.
3. In `prune`, inside the loop that deletes old `gone` ids, also `delete(m.refused, id)`.
4. Add:

```go
// admission waits, like waitTracked, for the engine to report an offer, and returns why the
// manager refused it ("" when it was tracked, is unknown, or the wait ran out). call.offer uses
// it so that a refused offer never reaches the webhook (D-31).
func (m *CallManager) admission(ctx context.Context, callID string) string {
	if _, err := m.waitTracked(ctx, callID); err == nil {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.refused[callID]
}
```

- [ ] **Step 4: Implement in `calls_webhook.go` and `event_call.go`**

Change `addCallOfferFields` to return `suppress bool`. Update its doc comment: "It returns true
when the manager refused the offer for a limit; the caller then sends no call.offer (D-31)." The
body becomes:

```go
func addCallOfferFields(ctx context.Context, payload map[string]any, evt *events.CallOffer, client *whatsmeow.Client) (suppress bool) {
	if !config.WhatsappCallsEnabled || evt == nil {
		return false
	}
	var m *CallManager
	if client != nil {
		if v, ok := callsByClient.Load(client); ok {
			m = v.(*CallManager)
		}
	}
	enabled := m != nil && hasCallManager(client) && evt.GroupJID.IsEmpty()
	payload["calls_enabled"] = enabled
	payload["direction"] = DirectionUser
	payload["peer"] = peerPayload(resolvePeer(client, evt.CallCreator, ""))
	if !enabled {
		return false
	}
	if reason := m.admission(ctx, evt.CallID); reason != "" {
		logrus.Infof("[CALLS] call.offer %s not sent: refused (%s)", evt.CallID, reason)
		return true
	}
	sdp, err := m.OfferSDP(ctx, evt.CallID)
	if err != nil {
		logrus.Warnf("[CALLS] call.offer %s without offer_sdp: %v", evt.CallID, MaskDigits(err.Error()))
		return false
	}
	payload["offer_sdp"] = sdp
	return false
}
```

In `src/infrastructure/whatsapp/event_call.go`, `createCallOfferPayload`, replace the line

```go
	addCallOfferFields(ctx, payload, evt, client) // Fork (elphant): calls_enabled, direction, peer and offer_sdp (voice calls 2b).
```

with

```go
	if addCallOfferFields(ctx, payload, evt, client) { // Fork (elphant): voice calls 2b fields; nil when a limit refused the offer (D-31).
		return nil
	}
```

In `forwardCallOfferToWebhook`, after `payload := createCallOfferPayload(...)`, add:

```go
	if payload == nil { // Fork (elphant): refused by a call limit; call.ended alone reports it (D-31).
		return nil
	}
```

(The `return nil` inside `createCallOfferPayload` is a short `if` block; it counts as the
fork's single-purpose edit in that upstream file.)

- [ ] **Step 5: Run the tests**

Run: `cd src && GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/... -race -run 'TestRefusedOffer|TestCallOfferFields|TestCreateCallOfferPayload|SenderDisplayName|TestOfferSDP'`
Expected: `ok`. Then run `cd src && GOTOOLCHAIN=auto go test ./infrastructure/whatsapp/... -race`.
Expected: `ok`.

- [ ] **Step 6: Update `docs/webhook-payload.md`**

Replace the bullet that starts with "A call's events arrive in order (status, answered, ended).
`call.offer` leaves from its own path ..." with:

"- A call's events arrive in order (status, answered, ended). `call.offer` leaves from its own
  path and is not ordered with them (for example a call cancelled at once); match events by
  `call_id`. An offer refused by a call limit sends no `call.offer` at all, only `call.ended`
  `MISSED` with `end_reason` `device_limit` or `gateway_limit`. Devices with calls off, group
  calls and `WHATSAPP_CALLS_ENABLED=false` keep upstream's `call.offer`."

- [ ] **Step 7: Commit**

```bash
git add src/infrastructure/whatsapp/calls.go src/infrastructure/whatsapp/calls_webhook.go src/infrastructure/whatsapp/event_call.go src/infrastructure/whatsapp/calls_webhook_test.go docs/webhook-payload.md
git commit -m "feat(calls): no call.offer webhook for an offer refused by a call limit"
```

---

### Task 9: Docs, debt and full verification

**Files:**
- Modify: `docs/reference/elphant-fork.md` (the "Voice calls" section)
- Modify: `src/.env.example`
- Modify: `docs/elphant-debt.md`

- [ ] **Step 1: The fork page**

In `docs/reference/elphant-fork.md`, section `## Voice calls (stages 2a and 2b, off by default)`:
1. Rename the heading to `## Voice calls (stages 2a and 2b, concurrent calls; off by default)` and
   add the spec link to the "Designs:" line:
   `[concurrent calls](../specs/2026-11-14-gateway-concurrent-calls-design.md)`.
2. In the "Per device" bullet, replace `PUT /devices/:device_id/calls {"enabled": true|false}` with
   `PUT /devices/:device_id/calls {"enabled": true|false, "max_calls": n}`. Add this sentence:
   "`max_calls` (optional, from 1 to the ceiling; omitted keeps the stored value, `NULL` reads 1)
   is the number's own limit of simultaneous calls, applied at the next offer or start. Both
   routes return `{enabled, max_calls, max_calls_ceiling, max_calls_total}`."
3. In the settings bullet, replace
   `` `WHATSAPP_CALLS_MAX_PER_DEVICE` (`1`; above it the offer is rejected and `call.ended` is `MISSED`) ``
   with:
   "`WHATSAPP_CALLS_MAX_PER_DEVICE` (`1`): the ceiling per number; each device's `max_calls` is
   capped by it. `WHATSAPP_CALLS_MAX_TOTAL` (`0`: none): simultaneous calls summed over every
   device of the gateway. Every tracked call holds a slot, a ringing inbound call included. An
   offer over a limit is rejected with `call.ended` `MISSED` and `end_reason` `device_limit` or
   `gateway_limit`. A start over a limit gets 400 "too many calls on this device" or "call
   capacity of this gateway reached"."
4. In "Infrastructure", replace the last part of the CPU bullet ("Measure before raising
   `WHATSAPP_CALLS_MAX_PER_DEVICE`.") with "See \"Concurrent calls: measurement\" below before
   raising the two caps."
5. Add a subsection at the end of the voice calls section:

```markdown
### Concurrent calls: measurement

Pending (plan 2026-11-14, Task 10, owner present). Until it runs, the deploy values
(`WHATSAPP_CALLS_MAX_PER_DEVICE=3`, `WHATSAPP_CALLS_MAX_TOTAL=6`) are estimates. They rest on 2a's
10% to 25% of a core per call with one leg; the browser bridge's cost is unmeasured. The host has
4 vCPU shared with other services, and the GOWA container is limited to 1 GiB.
```

- [ ] **Step 2: `.env.example`**

In `src/.env.example`, replace the line `WHATSAPP_CALLS_MAX_PER_DEVICE=1` with:

```
# Ceiling of simultaneous calls per number; each device sets its own max_calls up to it with
# PUT /devices/:device_id/calls {"enabled": true, "max_calls": n} (never set = 1)
WHATSAPP_CALLS_MAX_PER_DEVICE=1
# Simultaneous calls summed over every device of this gateway (0: no cap). A ringing call counts.
WHATSAPP_CALLS_MAX_TOTAL=0
```

- [ ] **Step 3: Debt**

In `docs/elphant-debt.md`, under `## Open`, add a new entry after the last open one:

```markdown
### D-32 Concurrent calls: open items after the code

- Live measurement on the gateway host (plan `2026-11-14-gateway-concurrent-calls.md`, Task 10),
  owner present. Until then `3`/`6` are estimates.
- What WhatsApp does with a second offer to a number already on a call is unknown (busy, phone
  only, or delivered).
- A ringing spam call holds a slot for up to the 1-minute ring window.
- No metric for slots in use; `GET /calls/active` is per device.
```

(The spec's decisions take D-19 to D-31, so this entry is D-32. The next debt entry starts at
D-33.)

- [ ] **Step 4: Full verification**

Run:

```bash
cd ~/Documents/whatsapp-gateway-concurrent-calls/src
gofmt -l . | grep -v '^vendor/' ; echo "gofmt done"
GOTOOLCHAIN=auto go vet ./...
GOTOOLCHAIN=auto go test ./...
GOTOOLCHAIN=auto go test -race ./infrastructure/whatsapp/... ./infrastructure/chatstorage/... ./ui/rest/ ./usecase/ ./cmd/ ./config/
CGO_ENABLED=0 GOTOOLCHAIN=auto go build -tags purego -o /dev/null .
GOTOOLCHAIN=auto go build -o whatsapp . && rm whatsapp
cd ../contract && GOTOOLCHAIN=auto go test ./...
```

Expected:
- gofmt lists nothing before "gofmt done", and vet is clean.
- Every package is `ok`, including `pkg/safego` (`TestEveryBackgroundGoroutineIsProtected`).
- The race run is clean, and the CGO-free build and the default build succeed.
- The contract tests pass.

A failure in `TestMaxDurationFinishesSilentEngine` alone is the known flake D-18: rerun that test
alone (`go test ./infrastructure/whatsapp/ -run TestMaxDurationFinishesSilentEngine -count=5`)
before treating it as a regression. Fix any other failure here before going on.

- [ ] **Step 5: Check the diff of upstream files**

Run: `cd ~/Documents/whatsapp-gateway-concurrent-calls && git diff elphant --stat -- src ':!src/infrastructure/whatsapp/calls*' ':!src/infrastructure/chatstorage/sqlite_repository_calls*' ':!src/cmd/calls_flags*' ':!src/config/calls*' ':!src/ui/rest/call_device*'`
Expected: in upstream files, only the `device_manager.go` fork line, the two `event_call.go` fork lines
(Task 8), the `root.go` flag help text
inside the fork block, and edits inside the existing fork blocks of `domains/call/call.go`,
`usecase/call.go` and `ui/rest/call_test.go`.

- [ ] **Step 6: Commit**

```bash
git add docs/reference/elphant-fork.md src/.env.example docs/elphant-debt.md
git commit -m "docs(calls): concurrent calls settings, limits and open items"
```

---

### Task 10: Live measurement on the gateway host (OWNER PRESENT; never run by a subagent alone) (D-30)

Nothing in this task runs without the owner in the session. Every server step (image build, stack
change, env change) needs the owner's explicit OK at that moment. Use the dedicated test number
only, never a customer number. Write no IP, token or phone number into the repo.

**Files:**
- Modify: `docs/reference/elphant-fork.md` ("Concurrent calls: measurement")
- Modify: `docs/elphant-debt.md` (D-32)

- [ ] **Step 1: Image and env (owner's OK)**

With the OK, build the image from this branch on the gateway host, the same way as the earlier
`.elphant.N` builds (Actions is disabled on the gateway repository). Set
`WHATSAPP_CALLS_MAX_PER_DEVICE=3` and `WHATSAPP_CALLS_MAX_TOTAL=6` on the stack. Then
`PUT /devices/<test device>/calls {"enabled": true, "max_calls": 3}`, and check that `GET` returns
`{enabled: true, max_calls: 3, max_calls_ceiling: 3, max_calls_total: 6}`.

- [ ] **Step 2: Baseline**

With no call running, record `docker stats --no-stream` for the GOWA container (CPU %, memory)
and the host's `uptime` load average over 1 minute.

- [ ] **Step 3: Inbound, 1, 2 and 3 at a time**

From other phones, call the test number. Answer each call in a separate browser tab of
`/calls/phone` (or the CRM beta). At each level (1, 2, 3 live calls), hold for 2 minutes and
record:
- container CPU % (peak and typical) and memory from `docker stats`;
- audio both ways on every call (clear, choppy, delayed, one-way), and the recording's channels and
  length against the call;
- whether messages still flow on the device during the calls.

Then place a 4th inbound call. Expected: rejected, with `call.ended` `MISSED`/`device_limit`, and
the reason in the log.

- [ ] **Step 4: Outbound, 1, 2 and 3 at a time**

Repeat Step 3 with calls placed from `/calls/phone`. Expected for the 4th: 400 "too many calls on
this device".

- [ ] **Step 5: A second offer to a number on a call**

While the test number is on one call with phone A, have phone B call the test number with
`max_calls: 3`, and note what reaches GOWA (`call.offer` or nothing) and what phone B sees
(ringing, busy). Repeat with `max_calls: 1`, and note what phone B sees when GOWA rejects
(`device_limit`).

- [ ] **Step 6: Gateway cap (if a second call-enabled device is available)**

With a temporary `WHATSAPP_CALLS_MAX_TOTAL=2` (owner's OK), hold 2 calls on one device and place
or receive a call on another. Expected: `gateway_limit` or 400 "call capacity of this gateway
reached". Restore `6` afterwards.

- [ ] **Step 7: Record the results**

Replace the "Pending" paragraph of "Concurrent calls: measurement" in
`docs/reference/elphant-fork.md` with a dated table: calls (1, 2, 3, inbound and outbound), CPU %,
memory, audio. Add the second-offer behaviour and a recommended `WHATSAPP_CALLS_MAX_PER_DEVICE` /
`WHATSAPP_CALLS_MAX_TOTAL` for this host. Update or close D-32 in `docs/elphant-debt.md`. Then
commit:

```bash
git add docs/reference/elphant-fork.md docs/elphant-debt.md
git commit -m "docs(calls): concurrent calls measured on the gateway host"
```

---

## After the plan: release (not a code task; owner's OK only)

Version target `v9.7.0-elphant.12` (D-29). The tag, the image and the stack change follow the
fork's usual release steps, only after the owner's explicit OK, and only after Task 10 confirms or
corrects the deploy values.
