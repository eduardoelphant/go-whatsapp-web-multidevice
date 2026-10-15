package callrec

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanupRemovesOldRecordingsOnly(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	old := now.Add(-8 * 24 * time.Hour)
	for name, mtime := range map[string]time.Time{"old.ogg": old, "old.wav": old, "new.ogg": now, "notes.txt": old} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	n, err := Cleanup(dir, 7*24*time.Hour, now)
	if err != nil || n != 2 {
		t.Fatalf("removed %d (%v), want 2", n, err)
	}
	for name, want := range map[string]bool{"old.ogg": false, "old.wav": false, "new.ogg": true, "notes.txt": true} {
		_, err := os.Stat(filepath.Join(dir, name))
		if (err == nil) != want {
			t.Errorf("%s exists=%v, want %v", name, err == nil, want)
		}
	}
	if n, err := Cleanup(filepath.Join(dir, "missing"), time.Hour, now); n != 0 || err != nil {
		t.Fatalf("missing dir: %d, %v", n, err)
	}
}
