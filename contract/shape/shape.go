// Package shape compares the structure of stable payload fixtures.
package shape

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Fixture struct {
	File   string
	Event  string
	Stable map[string]any
}

type Report struct {
	Missing        []string
	KindMismatches []string
}

func walk(prefix string, v any, visit func(path string, value any)) {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			visit(p, child)
			walk(p, child, visit)
		}
	case []any:
		for _, child := range x {
			visit(prefix+"[]", child)
			walk(prefix+"[]", child, visit)
		}
	}
}

// Signature identifies a shape: event, message type and the sorted set of
// paths holding non-null values.
func Signature(event string, stable map[string]any) string {
	typ, _ := stable["type"].(string)
	seen := map[string]bool{}
	walk("", stable, func(p string, v any) {
		if v != nil {
			seen[p] = true
		}
	})
	paths := make([]string, 0, len(seen))
	for p := range seen {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return event + "|" + typ + "|" + strings.Join(paths, ",")
}

func kindOf(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64, json.Number:
		return "number"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	}
	return fmt.Sprintf("%T", v)
}

func Kinds(stable map[string]any) map[string]string {
	kinds := map[string]string{}
	walk("", stable, func(p string, v any) {
		if k := kindOf(v); k != "null" {
			kinds[p] = k
		}
	})
	return kinds
}

func Compare(synthetic, real []Fixture) Report {
	var report Report
	synSigs := map[string]bool{}
	byGroup := map[string][]Fixture{}
	for _, f := range synthetic {
		synSigs[Signature(f.Event, f.Stable)] = true
		typ, _ := f.Stable["type"].(string)
		byGroup[f.Event+"|"+typ] = append(byGroup[f.Event+"|"+typ], f)
	}
	seen := map[string]bool{}
	for _, r := range real {
		sig := Signature(r.Event, r.Stable)
		if !synSigs[sig] {
			report.Missing = append(report.Missing, fmt.Sprintf("real shape without synthetic fixture: %s (%s)", sig, r.File))
		}
		typ, _ := r.Stable["type"].(string)
		group := r.Event + "|" + typ
		realKinds := Kinds(r.Stable)
		for _, s := range byGroup[group] {
			for path, sk := range Kinds(s.Stable) {
				if rk, ok := realKinds[path]; ok && rk != sk {
					line := fmt.Sprintf("%s %s: real=%s synthetic=%s (%s vs %s)", group, path, rk, sk, r.File, s.File)
					if !seen[line] {
						seen[line] = true
						report.KindMismatches = append(report.KindMismatches, line)
					}
				}
			}
		}
	}
	sort.Strings(report.Missing)
	sort.Strings(report.KindMismatches)
	return report
}

// Load reads every {"event","stable"} fixture in dir.
func Load(dir string) ([]Fixture, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	fixtures := make([]Fixture, 0, len(files))
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		var doc struct {
			Event  string         `json:"event"`
			Stable map[string]any `json:"stable"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		fixtures = append(fixtures, Fixture{File: filepath.Base(file), Event: doc.Event, Stable: doc.Stable})
	}
	return fixtures, nil
}
