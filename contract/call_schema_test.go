package contract

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func compileCallSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	schema, err := jsonschema.NewCompiler().Compile("call.schema.json")
	if err != nil {
		t.Fatalf("compile call schema: %v", err)
	}
	return schema
}

func TestCallFixturesMatchSchema(t *testing.T) {
	schema := compileCallSchema(t)
	files, _ := filepath.Glob(filepath.Join("call-fixtures", "*.json"))
	if len(files) < 5 {
		t.Fatalf("found %d call fixtures, want 5", len(files))
	}
	for _, f := range files {
		t.Run(f, func(t *testing.T) {
			if err := schema.Validate(loadJSON(t, f)); err != nil {
				t.Errorf("%v", err)
			}
		})
	}
}

func TestCallSchemaRejectsViolations(t *testing.T) {
	schema := compileCallSchema(t)
	mutate := func(name string, fn func(p map[string]any)) any {
		doc := loadJSON(t, filepath.Join("call-fixtures", name))
		raw, _ := json.Marshal(doc)
		var out map[string]any
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		_ = dec.Decode(&out)
		fn(out["payload"].(map[string]any))
		return out
	}
	ended := func(fn func(p map[string]any)) any { return mutate("call-ended.json", fn) }
	cases := map[string]any{
		"unknown status": ended(func(p map[string]any) { p["status"] = "DONE" }),
		"unknown reason": ended(func(p map[string]any) { p["end_reason"] = "bye" }),
		"2a direction":   ended(func(p map[string]any) { p["direction"] = "inbound" }),
		"extra key":      ended(func(p map[string]any) { p["reason"] = "x" }),
		"masked peer":    ended(func(p map[string]any) { p["peer"].(map[string]any)["pn"] = "…7777@s.whatsapp.net" }),
		"missing lid":    ended(func(p map[string]any) { delete(p["peer"].(map[string]any), "lid") }),
		"wav recording":  ended(func(p map[string]any) { p["recording"].(map[string]any)["format"] = "wav" }),
		"absolute url":   ended(func(p map[string]any) { p["recording"].(map[string]any)["url"] = "https://x/call/A/recording" }),
		"channels swapped": ended(func(p map[string]any) {
			p["recording"].(map[string]any)["channels"] = map[string]any{"left": "customer", "right": "agent"}
		}),
		"negative duration": ended(func(p map[string]any) { p["duration"] = json.Number("-1") }),
		"status extra key":  mutate("call-status.json", func(p map[string]any) { p["answered_by"] = "api" }),
		"ended status":      mutate("call-status.json", func(p map[string]any) { p["status"] = "COMPLETED" }),
		"answered by phone": mutate("call-answered.json", func(p map[string]any) { p["answered_by"] = "phone" }),
		"offer no switch":   mutate("call-offer.json", func(p map[string]any) { delete(p, "calls_enabled") }),
		"offer outbound":    mutate("call-offer.json", func(p map[string]any) { p["direction"] = "BUSINESS_INITIATED" }),
		"offer lf sdp":      mutate("call-offer.json", func(p map[string]any) { p["offer_sdp"] = "v=0\no=-\n" }),
	}
	for name, doc := range cases {
		if err := schema.Validate(doc); err == nil {
			t.Errorf("%s: the schema accepted it", name)
		}
	}
}
