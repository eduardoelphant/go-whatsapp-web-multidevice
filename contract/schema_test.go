package contract

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func compileSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	schema, err := c.Compile("stable.schema.json")
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	return schema
}

func loadJSON(t *testing.T, path string) any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return v
}

func TestFixturesMatchSchema(t *testing.T) {
	schema := compileSchema(t)
	files := fixtureFiles(t, "fixtures")
	synthetic := 0
	for _, f := range files {
		if strings.Contains(f, "synthetic") {
			synthetic++
		}
		t.Run(f, func(t *testing.T) {
			if err := schema.Validate(loadJSON(t, f)); err != nil {
				t.Errorf("%v", err)
			}
		})
	}
	if synthetic == 0 {
		t.Fatal("no synthetic fixtures found")
	}
}

func TestSchemaRejectsContractViolations(t *testing.T) {
	schema := compileSchema(t)
	mutate := func(fn func(doc map[string]any, stable map[string]any)) any {
		doc := loadJSON(t, filepath.Join("fixtures", "synthetic", "message-image.json")).(map[string]any)
		raw, _ := json.Marshal(doc)
		var copyDoc map[string]any
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		_ = dec.Decode(&copyDoc)
		fn(copyDoc, copyDoc["stable"].(map[string]any))
		return copyDoc
	}
	cases := map[string]any{
		"missing chat.lid":    mutate(func(_, s map[string]any) { delete(s["chat"].(map[string]any), "lid") }),
		"text is a number":    mutate(func(_, s map[string]any) { s["text"] = json.Number("5") }),
		"unknown key":         mutate(func(_, s map[string]any) { s["extra"] = true }),
		"unknown type":        mutate(func(_, s map[string]any) { s["type"] = "gif" }),
		"missing media":       mutate(func(_, s map[string]any) { delete(s, "media") }),
		"bad media url":       mutate(func(_, s map[string]any) { s["media"].(map[string]any)["url"] = "https://x/y" }),
		"unknown event":       mutate(func(d, _ map[string]any) { d["event"] = "message.deleted" }),
		"schema 2":            mutate(func(_, s map[string]any) { s["schema"] = json.Number("2") }),
		"missing interactive": mutate(func(_, s map[string]any) { delete(s, "interactive") }),
		"missing order":       mutate(func(_, s map[string]any) { delete(s, "order") }),
		"bad vote resolution": mutate(func(_, s map[string]any) {
			s["poll_vote"] = map[string]any{"poll_id": "P", "selected": []any{}, "resolution": "maybe"}
		}),
		"price as text": mutate(func(_, s map[string]any) {
			s["product"] = map[string]any{"id": nil, "title": nil, "description": nil, "retailer_id": nil, "url": nil,
				"currency": "BRL", "price_1000": "12,99", "sale_price_1000": nil}
		}),
		"bad interactive kind": mutate(func(_, s map[string]any) {
			s["interactive"] = map[string]any{"kind": "carousel", "header": nil, "footer": nil, "buttons": []any{}, "sections": []any{}}
		}),
		"bad button kind": mutate(func(_, s map[string]any) {
			s["interactive"] = map[string]any{"kind": "buttons", "header": nil, "footer": nil, "sections": []any{},
				"buttons": []any{map[string]any{"kind": "pay", "id": nil, "text": nil, "value": nil}}}
		}),
		"referral without entry_point": mutate(func(_, s map[string]any) {
			s["referral"] = map[string]any{"source_type": "ad", "source_app": nil, "source_id": nil, "source_url": nil, "ctwa_clid": nil,
				"ref": nil, "title": nil, "body": nil, "media_type": nil, "thumbnail_url": nil, "media_url": nil}
		}),
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if err := schema.Validate(doc); err == nil {
				t.Fatal("schema accepted a contract violation")
			}
		})
	}
}

func TestFixtureFilesIncludesNested(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "real", "message", "image")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "abc-1.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := fixtureFiles(t, dir); len(got) != 1 {
		t.Fatalf("fixtureFiles = %v, want the nested file", got)
	}
}

// fixtureFiles lists every .json under root, nested directories included.
func fixtureFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && filepath.Ext(path) == ".json" {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}
