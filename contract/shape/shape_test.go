package shape

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func stable(text any, media any) map[string]any {
	return map[string]any{"type": "image", "text": text, "media": media, "chat": map[string]any{"pn": "x@s.whatsapp.net", "lid": nil}}
}

func TestSignatureIgnoresValuesButNotNullness(t *testing.T) {
	a := Signature("message", stable("one", map[string]any{"kind": "image"}))
	b := Signature("message", stable("two", map[string]any{"kind": "image"}))
	c := Signature("message", stable(nil, map[string]any{"kind": "image"}))
	if a != b {
		t.Fatalf("same shape, different values: %q vs %q", a, b)
	}
	if a == c {
		t.Fatal("text null vs string must change the signature")
	}
	if !strings.HasPrefix(a, "message|image|") || strings.Contains(a, "chat.lid") {
		t.Fatalf("signature = %q", a)
	}
}

func TestCompareReportsCoverageGapsAndKindMismatches(t *testing.T) {
	synthetic := []Fixture{{File: "s1.json", Event: "message", Stable: stable("x", map[string]any{"kind": "image", "size": 1.0})}}
	real := []Fixture{
		// Only a nullability combination the synthetic set does not have: covered.
		{File: "r1.json", Event: "message", Stable: stable(nil, map[string]any{"kind": "image", "size": 2.0})},
		// A non-null path no synthetic fixture of the same event/type has.
		{File: "r2.json", Event: "message", Stable: stable("y", map[string]any{"kind": "image", "size": 3.0, "sha256": "<sha256>"})},
		// A kind conflict.
		{File: "r3.json", Event: "message", Stable: stable("z", map[string]any{"kind": "image", "size": "big"})},
		// An event/type with no synthetic fixture at all.
		{File: "r4.json", Event: "message.ack", Stable: map[string]any{"status": "read"}},
	}
	report := Compare(synthetic, real)
	if len(report.Missing) != 1 || !strings.Contains(report.Missing[0], "message.ack|") || !strings.Contains(report.Missing[0], "r4.json") {
		t.Fatalf("missing = %v, want only message.ack from r4.json", report.Missing)
	}
	if len(report.Uncovered) != 1 || !strings.Contains(report.Uncovered[0], "media.sha256") || !strings.Contains(report.Uncovered[0], "r2.json") {
		t.Fatalf("uncovered = %v, want media.sha256 from r2.json", report.Uncovered)
	}
	if len(report.KindMismatches) != 1 || !strings.Contains(report.KindMismatches[0], "media.size: real=string synthetic=number") {
		t.Fatalf("kind mismatches = %v", report.KindMismatches)
	}
}

func TestLoadReadsNestedDirectories(t *testing.T) {
	// The audit writes <event>/<type>/<key>-N.json; copying that tree into
	// fixtures/real must not be silently ignored.
	dir := t.TempDir()
	nested := filepath.Join(dir, "message", "image")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := `{"event":"message","stable":{"type":"image","text":null}}`
	if err := os.WriteFile(filepath.Join(nested, "abc-1.json"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	fixtures, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(fixtures) != 1 || fixtures[0].Event != "message" {
		t.Fatalf("Load = %+v, want the nested fixture", fixtures)
	}
}
