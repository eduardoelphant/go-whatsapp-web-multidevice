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
	files, _ := filepath.Glob(filepath.Join("fixtures", "*", "*.json"))
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
		"missing chat.lid": mutate(func(_, s map[string]any) { delete(s["chat"].(map[string]any), "lid") }),
		"text is a number": mutate(func(_, s map[string]any) { s["text"] = json.Number("5") }),
		"unknown key":      mutate(func(_, s map[string]any) { s["extra"] = true }),
		"unknown type":     mutate(func(_, s map[string]any) { s["type"] = "gif" }),
		"missing media":    mutate(func(_, s map[string]any) { delete(s, "media") }),
		"bad media url":    mutate(func(_, s map[string]any) { s["media"].(map[string]any)["url"] = "https://x/y" }),
		"unknown event":    mutate(func(d, _ map[string]any) { d["event"] = "message.deleted" }),
		"schema 2":         mutate(func(_, s map[string]any) { s["schema"] = json.Number("2") }),
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if err := schema.Validate(doc); err == nil {
				t.Fatal("schema accepted a contract violation")
			}
		})
	}
}
