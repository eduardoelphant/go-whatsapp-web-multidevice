package whatsapp

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sampleStable() map[string]any {
	return map[string]any{
		"schema": 1.0, "id": "3EB0SECRETID", "timestamp": "2026-09-25T12:00:00Z", "is_from_me": false,
		"chat":   map[string]any{"pn": "5511999998888@s.whatsapp.net", "lid": "123456789@lid", "is_group": false},
		"sender": map[string]any{"pn": "5511999998888@s.whatsapp.net", "lid": nil, "push_name": "Maria Segredo"},
		"type":   "image", "text": "senha 1234",
		"media": map[string]any{"kind": "image", "mime": "image/jpeg", "size": 2048.0, "sha256": "deadbeef",
			"filename": "rg.jpg", "duration": nil, "ptt": false, "width": 640.0, "height": 480.0, "url": "/message/3EB0SECRETID/media"},
		"contact":  map[string]any{"name": "Ana", "vcard": "BEGIN:VCARD", "phones": []any{"+55 11 90000-0002"}},
		"location": map[string]any{"latitude": -23.5, "longitude": -46.6, "name": "Casa", "address": nil},
		"quoted":   nil, "forwarded": false, "view_once": false,
	}
}

func TestAnonymizeStableRemovesPersonalData(t *testing.T) {
	anon := anonymizeStable(sampleStable(), "")
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // compare placeholders as written, not as \u003c escapes
	if err := enc.Encode(anon); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, secret := range []string{"5511999998888", "123456789", "Maria", "senha", "3EB0SECRETID", "deadbeef", "rg.jpg", "Ana", "90000", "Casa", "-23.5", "2048"} {
		if strings.Contains(out, secret) {
			t.Errorf("anonymized sample still contains %q: %s", secret, out)
		}
	}
	for _, kept := range []string{`"type":"image"`, `"kind":"image"`, `"mime":"image/jpeg"`, `"schema":1`, `"pn":"<jid>@s.whatsapp.net"`, `"lid":"<jid>@lid"`, `"url":"/message/<id>/media"`, `"text":"<text:10>"`, `"timestamp":"2000-01-01T00:00:00Z"`} {
		if !strings.Contains(out, kept) {
			t.Errorf("anonymized sample lost %s: %s", kept, out)
		}
	}
}

func TestWriteStableAuditCapsSamplesPerShape(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		if err := writeStableAudit(dir, "message", sampleStable()); err != nil {
			t.Fatal(err)
		}
	}
	files, _ := filepath.Glob(filepath.Join(dir, "message", "image", "*.json"))
	if len(files) != stableAuditCap {
		t.Fatalf("samples for one shape = %d, want %d", len(files), stableAuditCap)
	}

	other := sampleStable()
	other["text"] = nil
	if err := writeStableAudit(dir, "message", other); err != nil {
		t.Fatal(err)
	}
	files, _ = filepath.Glob(filepath.Join(dir, "message", "image", "*.json"))
	if len(files) != stableAuditCap+1 {
		t.Fatalf("a new shape must get its own sample; files = %d", len(files))
	}

	raw, _ := os.ReadFile(files[0])
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil || doc["event"] != "message" || doc["stable"] == nil {
		t.Fatalf("sample file = %s", raw)
	}
}

func TestAuditStableIsNoOpWithoutDir(t *testing.T) {
	t.Setenv("WHATSAPP_STABLE_AUDIT_DIR", "")
	auditStable("message", sampleStable())
}

func TestAuditStableReturnsImmediately(t *testing.T) {
	t.Setenv("WHATSAPP_STABLE_AUDIT_DIR", "/dev/null/not-a-dir")
	start := time.Now()
	auditStable("message", sampleStable())
	if time.Since(start) > 50*time.Millisecond {
		t.Fatal("auditStable must not wait for the disk")
	}
}
