// Package safego keeps a panic in a background goroutine from ending the whole process
// (fork: elphant, spec 2026-09-30-gateway-g9).
//
// Every goroutine GOWA starts is either safego.Go(name, fn) or a closure whose first statement
// is `defer safego.Recover(name)`; TestEveryBackgroundGoroutineIsProtected enforces it.
package safego

import (
	"runtime/debug"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"
)

var panics atomic.Int64

// Panics is how many panics were recovered since the process started.
func Panics() int64 { return panics.Load() }

// Recover is deferred as the first statement of a goroutine closure:
//
//	go func() {
//		defer safego.Recover("name")
//		...
//	}()
//
// A panic is logged with its stack and counted, and the goroutine ends.
func Recover(name string) {
	if r := recover(); r != nil {
		logrus.Errorf("panic recovered in goroutine %q: %v\n%s", name, r, debug.Stack())
		panics.Add(1)
	}
}

// Go runs fn in a goroutine that contains its panics.
func Go(name string, fn func()) {
	go func() {
		defer Recover(name)
		fn()
	}()
}

// loopDelay is the wait before restarting a loop that panicked: 1s, doubling, capped at 30s.
var loopDelay = func(panicsInARow int) time.Duration {
	d := time.Second << (panicsInARow - 1)
	if panicsInARow > 5 || d > 30*time.Second {
		return 30 * time.Second
	}
	return d
}

// Loop runs a long-lived service loop (one that is meant to run until the process ends). A panic
// is contained like in Go, and the loop is started again after a growing wait, because a dead
// loop would leave the feature silently down while the process stays up. A normal return ends it.
func Loop(name string, fn func()) {
	go func() {
		defer Recover(name + ":final")
		for attempt := 1; ; attempt++ {
			if runOnce(name, fn) {
				return
			}
			time.Sleep(loopDelay(attempt))
		}
	}()
}

// runOnce runs fn and reports whether it returned without a panic.
func runOnce(name string, fn func()) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			logrus.Errorf("panic recovered in loop %q: %v\n%s", name, r, debug.Stack())
			panics.Add(1)
			ok = false
		}
	}()
	fn()
	return true
}
