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
		if err := writeStableAudit(dir, "message", sampleStable(), nil); err != nil {
			t.Fatal(err)
		}
	}
	files, _ := filepath.Glob(filepath.Join(dir, "message", "image", "*.json"))
	if len(files) != stableAuditCap {
		t.Fatalf("samples for one shape = %d, want %d", len(files), stableAuditCap)
	}

	other := sampleStable()
	other["text"] = nil
	if err := writeStableAudit(dir, "message", other, nil); err != nil {
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
	auditStable("message", sampleStable(), nil)
}

func TestAuditStableReturnsImmediately(t *testing.T) {
	t.Setenv("WHATSAPP_STABLE_AUDIT_DIR", "/dev/null/not-a-dir")
	start := time.Now()
	auditStable("message", sampleStable(), nil)
	if time.Since(start) > 50*time.Millisecond {
		t.Fatal("auditStable must not wait for the disk")
	}
}

func TestAnonymizeStableReplacesReactionText(t *testing.T) {
	anon := anonymizeStable(map[string]any{"emoji": "secret words"}, "").(map[string]any)
	if anon["emoji"] != "<emoji>" {
		t.Fatalf("emoji = %v, want <emoji>", anon["emoji"])
	}
}

func TestStableShapeKeyCountsArrayElements(t *testing.T) {
	withPhone := map[string]any{"type": "contact", "contact": map[string]any{"phones": []any{"<phone>"}}}
	without := map[string]any{"type": "contact", "contact": map[string]any{"phones": []any{}}}
	if stableShapeKey("message", withPhone) == stableShapeKey("message", without) {
		t.Fatal("phones [] and [x] must be different shapes")
	}
}

func TestWriteStableAuditWritesProtoFieldsSidecar(t *testing.T) {
	dir := t.TempDir()
	unknown := map[string]any{"type": "unknown", "text": nil}
	if err := writeStableAudit(dir, "message", unknown, []string{"buttonsMessage", "messageContextInfo"}); err != nil {
		t.Fatal(err)
	}
	sidecars, _ := filepath.Glob(filepath.Join(dir, "message", "unknown", "*.fields.txt"))
	if len(sidecars) != 1 {
		t.Fatalf("sidecars = %v, want one", sidecars)
	}
	raw, _ := os.ReadFile(sidecars[0])
	if string(raw) != "buttonsMessage\nmessageContextInfo\n" {
		t.Fatalf("sidecar = %q", raw)
	}
}

func TestAuditStableDropsWhenBusy(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WHATSAPP_STABLE_AUDIT_DIR", dir)
	for i := 0; i < cap(stableAuditSlots); i++ {
		stableAuditSlots <- struct{}{}
	}
	t.Cleanup(func() {
		for i := 0; i < cap(stableAuditSlots); i++ {
			<-stableAuditSlots
		}
	})
	auditStable("message", sampleStable(), nil)
	time.Sleep(50 * time.Millisecond)
	if files, _ := filepath.Glob(filepath.Join(dir, "*", "*", "*.json")); len(files) != 0 {
		t.Fatalf("a busy audit must drop the sample, wrote %v", files)
	}
}

func TestAnonymizeStableKeepsAdSourceButNotTrackingIDs(t *testing.T) {
	referral := map[string]any{"referral": map[string]any{
		"source_type": "ad", "source_app": "instagram", "media_type": "image", "source_id": "120211234567890123",
		"ctwa_clid": "ARAkLclid", "source_url": "https://fb.me/xyz", "title": "Promo da Maria",
		"entry_point": map[string]any{"source": "ctwa_ad", "app": "instagram", "external_source": "cliente_joao", "external_medium": "email"},
	}}
	anon := anonymizeStable(referral, "").(map[string]any)["referral"].(map[string]any)
	for key, want := range map[string]string{"source_type": "ad", "source_app": "instagram", "media_type": "image"} {
		if anon[key] != want {
			t.Errorf("%s = %v, want %s kept", key, anon[key], want)
		}
	}
	ep := anon["entry_point"].(map[string]any)
	if ep["source"] != "ctwa_ad" || ep["app"] != "instagram" {
		t.Errorf("entry_point source/app = %v/%v, want kept", ep["source"], ep["app"])
	}
	for _, key := range []string{"source_id", "ctwa_clid", "source_url", "title"} {
		if v, _ := anon[key].(string); !strings.HasPrefix(v, "<") {
			t.Errorf("%s = %v, want a placeholder", key, anon[key])
		}
	}
	if v, _ := ep["external_source"].(string); !strings.HasPrefix(v, "<") {
		t.Errorf("external_source = %v, want a placeholder", ep["external_source"])
	}
}

func TestAnonymizeStableKeepsCommerceAndCallEnums(t *testing.T) {
	in := map[string]any{
		"call":      map[string]any{"outcome": "missed", "call_type": "regular"},
		"poll_vote": map[string]any{"poll_id": "P1", "selected": []any{"Sim"}, "resolution": "resolved"},
		"product":   map[string]any{"currency": "BRL", "title": "Camiseta da Ana", "price_1000": 59900.0},
		"poll":      map[string]any{"options": []any{"Sim", "Não"}},
	}
	anon := anonymizeStable(in, "").(map[string]any)
	call := anon["call"].(map[string]any)
	vote := anon["poll_vote"].(map[string]any)
	product := anon["product"].(map[string]any)
	if call["outcome"] != "missed" || call["call_type"] != "regular" || vote["resolution"] != "resolved" || product["currency"] != "BRL" {
		t.Fatalf("closed-set values lost: call=%v vote=%v product=%v", call, vote, product)
	}
	if product["price_1000"] != 0.0 || vote["poll_id"] == "P1" || product["title"] == "Camiseta da Ana" {
		t.Fatalf("personal values survived: vote=%v product=%v", vote, product)
	}
	for _, v := range append(vote["selected"].([]any), anon["poll"].(map[string]any)["options"].([]any)...) {
		if s, _ := v.(string); !strings.HasPrefix(s, "<") {
			t.Fatalf("poll option or vote kept verbatim: %v", v)
		}
	}
}
