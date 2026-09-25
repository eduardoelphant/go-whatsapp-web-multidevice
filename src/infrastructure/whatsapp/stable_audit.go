package whatsapp

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/sirupsen/logrus"
)

// Fork (elphant): production audit of payload.stable. With
// WHATSAPP_STABLE_AUDIT_DIR set, each new shape (event, type, non-null keys)
// is saved anonymized, up to stableAuditCap samples, for contract/cmd/compare.
// It never blocks or fails webhook delivery.

const stableAuditCap = 3

var stableAuditMu sync.Mutex

// Values kept verbatim: closed sets that carry no personal data.
var stableAuditKeep = map[string]bool{"schema": true, "type": true, "kind": true, "mime": true, "status": true, "emoji": true}

func auditStable(event string, stable any) {
	dir := os.Getenv("WHATSAPP_STABLE_AUDIT_DIR")
	if dir == "" {
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logrus.Warnf("[STABLE_AUDIT] panic: %v", r)
			}
		}()
		if err := writeStableAudit(dir, event, stable); err != nil {
			logrus.Warnf("[STABLE_AUDIT] %s: %v", event, err)
		}
	}()
}

func writeStableAudit(dir, event string, stable any) error {
	raw, err := json.Marshal(stable)
	if err != nil {
		return err
	}
	var tree map[string]any
	if err := json.Unmarshal(raw, &tree); err != nil {
		return err
	}
	anon, _ := anonymizeStable(tree, "").(map[string]any)
	typ, _ := tree["type"].(string)
	if typ == "" {
		typ = "-"
	}
	key := stableShapeKey(event, anon)
	sub := filepath.Join(dir, event, typ)

	stableAuditMu.Lock()
	defer stableAuditMu.Unlock()
	if err := os.MkdirAll(sub, 0o750); err != nil {
		return err
	}
	existing, _ := filepath.Glob(filepath.Join(sub, key+"-*.json"))
	if len(existing) >= stableAuditCap {
		return nil
	}
	out, err := json.MarshalIndent(map[string]any{"event": event, "stable": anon}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(sub, fmt.Sprintf("%s-%d.json", key, len(existing)+1)), append(out, '\n'), 0o640)
}

// anonymizeStable replaces personal values with type-preserving placeholders.
func anonymizeStable(v any, key string) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, child := range x {
			out[k] = anonymizeStable(child, k)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, child := range x {
			out[i] = anonymizeStable(child, key)
		}
		return out
	case string:
		switch {
		case stableAuditKeep[key]:
			return x
		case key == "pn" || key == "lid":
			if i := strings.LastIndex(x, "@"); i >= 0 {
				return "<jid>" + x[i:]
			}
			return "<jid>"
		case key == "id" || key == "target_id" || key == "ids":
			return "<id>"
		case key == "url":
			return "/message/<id>/media"
		case key == "sha256":
			return "<sha256>"
		case key == "phones":
			return "<phone>"
		case key == "timestamp":
			return "2000-01-01T00:00:00Z"
		default:
			return fmt.Sprintf("<text:%d>", utf8.RuneCountInString(x))
		}
	case float64:
		if stableAuditKeep[key] {
			return x
		}
		return 0.0
	default:
		return x
	}
}

// stableShapeKey is a short file-name key of the shape: event, type and the
// sorted non-null paths.
func stableShapeKey(event string, tree map[string]any) string {
	var paths []string
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, child := range x {
				p := strings.TrimPrefix(prefix+"."+k, ".")
				if child != nil {
					paths = append(paths, p)
				}
				walk(p, child)
			}
		case []any:
			for _, child := range x {
				walk(prefix+"[]", child)
			}
		}
	}
	walk("", tree)
	sort.Strings(paths)
	typ, _ := tree["type"].(string)
	sum := sha1.Sum([]byte(event + "|" + typ + "|" + strings.Join(paths, ",")))
	return hex.EncodeToString(sum[:])[:12]
}
