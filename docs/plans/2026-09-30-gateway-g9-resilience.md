# G9 Resilience Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans (inline, chosen by the owner). Steps use checkbox (`- [ ]`) syntax.

**Goal:** contain panics in every goroutine, reconnect every paired device on its own schedule, and expose per-device health and metrics.

**Architecture:** `pkg/safego` (recover helpers plus a source-walking test), a `ReconnectWatchdog` in `infrastructure/whatsapp` over small interfaces, and two Basic Auth routes that read the watchdog's snapshot.

**Tech Stack:** Go 1.26 (`GOTOOLCHAIN=auto`, from `src/`), Fiber v3, logrus, stdlib tests.

**Spec:** `docs/specs/2026-09-30-gateway-g9-resilience-design.md`

## Global Constraints

- Run Go from `src/` with `GOTOOLCHAIN=auto`; `src/go.mod` unchanged (no Prometheus library).
- A goroutine is either `safego.Go(name, fn)` or a closure whose first statement is `defer safego.Recover(name)`; the source-walking test enforces it.
- The watchdog never connects a device that is not paired, has no client, or is in `StreamReplaced`.
- Device ids never appear in `/metrics`. `/health` stays public and unchanged; `/health/devices` and `/metrics` sit behind Basic Auth (mounted after the auth middleware, before the device group).
- Tests that swap globals restore them. Commits in English, no attribution. Push, tag and release only with the owner's OK.

## Review Focus

- A `go` statement added later without protection fails the walking test (Task 1).
- Named-function goroutines keep the arguments they had when launched (Task 1, call sites).
- An unpaired device is never connected by the watchdog (Task 2).
- The wait grows per device and is capped; a success resets it (Task 2).
- A panic inside a tick does not stop later ticks (Task 2).

---

### Task 1: `pkg/safego` and the 39 sites

- [ ] Tests (`pkg/safego/safego_test.go`): `Go` runs `fn` and contains a panic (counter +1, the name in the log, the process alive); `Recover` as a deferred call contains a panic and does nothing on a normal return; `Panics()` counts only recovered panics; the walking test (`lint_test.go`) parses every non-test `.go` file under `src/` (skipping `testdata`) and fails on a `go` statement that is neither a func literal whose first statement is `defer safego.Recover(...)` nor inside `pkg/safego`; it is also run on a temporary file with an unprotected `go` to prove it can fail.
- [ ] Implement `pkg/safego/safego.go`:

```go
// Package safego keeps a panic in a background goroutine from ending the whole process
// (fork: elphant, spec 2026-09-30-gateway-g9).
package safego

import (
	"runtime/debug"
	"sync/atomic"

	"github.com/sirupsen/logrus"
)

var panics atomic.Int64

// Panics is how many panics were recovered since the process started.
func Panics() int64 { return panics.Load() }

// Recover is deferred as the first statement of a goroutine closure: `defer safego.Recover("name")`.
func Recover(name string) {
	if r := recover(); r != nil {
		panics.Add(1)
		logrus.Errorf("panic recovered in goroutine %q: %v\n%s", name, r, debug.Stack())
	}
}

// Go runs fn in a goroutine that contains its panics.
func Go(name string, fn func()) {
	go func() {
		defer Recover(name)
		fn()
	}()
}
```

- [ ] Apply to the sites: every `go func(...) {` closure gets `defer safego.Recover("<file>#<n>")` as its first line; every `go namedFunc(args)` becomes `safego.Go("<name>", func() { namedFunc(args) })`, with any argument that could change after the launch copied into a local first. Add the `safego` import.
- [ ] `go build ./... && go vet ./...`, `go test ./pkg/safego/ -race`, then the full suite; commit `feat(safego): contain panics in every background goroutine`.

---

### Task 2: Reconnect watchdog

**Produces:** `whatsapp.DeviceHealth`, `whatsapp.NewReconnectWatchdog(list func() []*DeviceInstance, interval time.Duration) *ReconnectWatchdog`, `(*ReconnectWatchdog).Start(ctx)`, `.Tick(now)`, `.Snapshot() []DeviceHealth`, `.Counters() (attempts, successes int64)`; internal `watchedDevice` interface (`ID() string`, `Paired() bool`, `HasClient() bool`, `Connected() bool`, `Replaced() bool`, `State() string`, `LoggedIn() bool`, `Connect() error`).

- [ ] Tests (`device_watchdog_test.go`, fake devices and clock): connected resets attempts and records the last connected time; unpaired, replaced and client-less devices are skipped; a due device is connected and counted; the wait doubles per failed attempt from the interval and is capped at 15 minutes; a success resets the wait; a device not yet due is skipped; a removed device is forgotten; a panic in `Connect` does not stop the tick (the next device is still handled); `Snapshot` reports attempts and the next attempt time.
- [ ] Implement `device_watchdog.go` as in the spec rules; `DeviceInstance` gets small accessors (`Paired()`: `client != nil && client.Store != nil && client.Store.ID != nil`; `Replaced()`: `!ShouldAutoReconnect(client)`); the production `Connect` is `client.Connect()`; `Start` runs `Tick(time.Now())` every interval inside `safego.Go`, each device attempt under `safego.Recover` so one panic skips only that device.
- [ ] Config: `WhatsappWatchdogIntervalSeconds = 120` (`<= 0` disables), viper key `whatsapp_watchdog_interval_seconds`, flag `--whatsapp-watchdog-interval-seconds`, `.env.example`.
- [ ] Run tests with `-race`; commit `feat(whatsapp): reconnect every paired device with a growing wait`.

---

### Task 3: Health and metrics routes, wiring

**Produces:** `rest.InitRestHealth(app fiber.Router, provider HealthProvider)` with `HealthProvider{ Snapshot() []whatsapp.DeviceHealth; Counters() (int64, int64) }`.

- [ ] Tests (`ui/rest/health_test.go`): `/health/devices` JSON shape (`devices`, `panics_recovered`, null `last_connected_at` and `next_attempt_at` when unknown); `/metrics` text format (`# HELP`/`# TYPE`, the four metric names, the `state` label per state, no device id), `Content-Type: text/plain; version=0.0.4`; a nil provider answers an empty list and zero counters.
- [ ] Implement `ui/rest/health.go`; in `cmd/rest.go` create the watchdog after the device manager exists, mount `rest.InitRestHealth(apiGroup, watchdog)` next to `InitRestWebhookOutbox` (before the device group; the auth middleware is already installed), start it where the default-client loop was started, and delete `startAutoReconnectCheckerIfClientAvailable`, `getValidWhatsAppClient` if unused, and `helpers.SetAutoReconnectChecking`.
- [ ] `go vet ./... && go test ./...`; commit `feat(rest): add /health/devices and /metrics`.

---

### Task 4: Docs and final checks

- [ ] `docs/reference/elphant-fork.md`: a section "Resilience" (safego rule, watchdog rules and the env var, the two routes with examples, the metric names).
- [ ] `docs/elphant-debt.md`: D-4 keeps only G8.
- [ ] `cd src && GOTOOLCHAIN=auto go vet ./... && GOTOOLCHAIN=auto go test ./...` → PASS. Commit `docs: document the G9 resilience work`.
