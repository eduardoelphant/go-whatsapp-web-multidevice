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

// Report lists what real traffic shows that the synthetic fixtures do not:
// Missing are event/type groups with no synthetic fixture, Uncovered are
// non-null paths no synthetic fixture of the group has, KindMismatches are
// paths whose JSON kind differs. Nullability combinations alone are not gaps.
type Report struct {
	Missing        []string
	Uncovered      []string
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

func nonNullPaths(stable map[string]any) map[string]bool {
	paths := map[string]bool{}
	walk("", stable, func(p string, v any) {
		if v != nil {
			paths[p] = true
		}
	})
	return paths
}

func Compare(synthetic, real []Fixture) Report {
	var report Report
	covered := map[string]map[string]bool{}
	byGroup := map[string][]Fixture{}
	for _, f := range synthetic {
		typ, _ := f.Stable["type"].(string)
		group := f.Event + "|" + typ
		byGroup[group] = append(byGroup[group], f)
		if covered[group] == nil {
			covered[group] = map[string]bool{}
		}
		for p := range nonNullPaths(f.Stable) {
			covered[group][p] = true
		}
	}
	seen := map[string]bool{}
	add := func(list *[]string, line string) {
		if !seen[line] {
			seen[line] = true
			*list = append(*list, line)
		}
	}
	for _, r := range real {
		typ, _ := r.Stable["type"].(string)
		group := r.Event + "|" + typ
		if covered[group] == nil {
			add(&report.Missing, fmt.Sprintf("%s has no synthetic fixture (%s)", group, r.File))
			continue
		}
		for p := range nonNullPaths(r.Stable) {
			if !covered[group][p] {
				add(&report.Uncovered, fmt.Sprintf("%s %s is never non-null in synthetic fixtures (%s)", group, p, r.File))
			}
		}
		realKinds := Kinds(r.Stable)
		for _, s := range byGroup[group] {
			for path, sk := range Kinds(s.Stable) {
				if rk, ok := realKinds[path]; ok && rk != sk {
					add(&report.KindMismatches, fmt.Sprintf("%s %s: real=%s synthetic=%s (%s vs %s)", group, path, rk, sk, r.File, s.File))
				}
			}
		}
	}
	sort.Strings(report.Missing)
	sort.Strings(report.Uncovered)
	sort.Strings(report.KindMismatches)
	return report
}

// Load reads every {"event","stable"} fixture under dir, including nested
// directories (the audit writes <event>/<type>/<key>-N.json). A missing dir
// yields no fixtures.
func Load(dir string) ([]Fixture, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && path == dir {
				return filepath.SkipDir
			}
			return err
		}
		if !d.IsDir() && filepath.Ext(path) == ".json" {
			files = append(files, path)
		}
		return nil
	})
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
		rel, _ := filepath.Rel(dir, file)
		fixtures = append(fixtures, Fixture{File: rel, Event: doc.Event, Stable: doc.Stable})
	}
	return fixtures, nil
}
