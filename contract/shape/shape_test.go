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

func TestCompareReportsMissingShapesAndKindMismatches(t *testing.T) {
	synthetic := []Fixture{{File: "s1.json", Event: "message", Stable: stable("x", map[string]any{"kind": "image", "size": 1.0})}}
	real := []Fixture{
		{File: "r1.json", Event: "message", Stable: stable("y", map[string]any{"kind": "image", "size": 2.0})},
		{File: "r2.json", Event: "message", Stable: stable(nil, map[string]any{"kind": "image", "size": "big"})},
	}
	report := Compare(synthetic, real)
	if len(report.Missing) != 1 || !strings.Contains(report.Missing[0], "r2.json") {
		t.Fatalf("missing = %v, want only r2.json", report.Missing)
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
