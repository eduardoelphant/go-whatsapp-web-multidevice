package callrec

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/safego"
	"github.com/sirupsen/logrus"
)

// Cleanup deletes the recordings in dir (.ogg, plus stage 2a's .wav) last written before
// now - maxAge. A recording in progress is written every 60 ms, so it is never old.
func Cleanup(dir string, maxAge time.Duration, now time.Time) (int, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	removed := 0
	var failed int
	var firstFail string
	fail := func(name string) {
		if failed == 0 {
			firstFail = name
		}
		failed++
	}
	for _, e := range entries {
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if e.IsDir() || (ext != ".ogg" && ext != ".wav") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			fail(e.Name())
			continue
		}
		if now.Sub(fi.ModTime()) <= maxAge {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err == nil {
			removed++
		} else {
			fail(e.Name())
		}
	}
	if failed > 0 {
		logrus.Warnf("[CALLS] recording retention: %d file(s) could not be inspected or removed (first: %s)", failed, firstFail)
	}
	return removed, nil
}

// StartRetention runs Cleanup at once and then every hour until ctx ends.
func StartRetention(ctx context.Context, dir string, maxAge time.Duration) {
	if maxAge <= 0 {
		return
	}
	safego.Loop("calls-recording-retention", func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			if n, err := Cleanup(dir, maxAge, time.Now()); err != nil {
				logrus.Warnf("[CALLS] recording retention: %v", err)
			} else if n > 0 {
				logrus.Infof("[CALLS] removed %d recording(s) past retention", n)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})
}
