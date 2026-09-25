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

var (
	stableAuditMu sync.Mutex
	// stableAuditSlots bounds in-flight writes: with a slow or hung disk,
	// samples are dropped instead of piling up goroutines.
	stableAuditSlots = make(chan struct{}, 4)
	// stableAuditFull remembers shapes that already have stableAuditCap samples.
	stableAuditFull sync.Map
)

// Values kept verbatim: closed sets that carry no personal data.
var stableAuditKeep = map[string]bool{"schema": true, "type": true, "kind": true, "mime": true, "status": true}

// auditStable saves an anonymized sample of stable in the background. For
// type "unknown" messages, protoFields lists the populated proto field names
// (never values) so the sample says what the message actually was.
func auditStable(event string, stable any, protoFields []string) {
	dir := os.Getenv("WHATSAPP_STABLE_AUDIT_DIR")
	if dir == "" {
		return
	}
	select {
	case stableAuditSlots <- struct{}{}:
	default:
		logrus.Debugf("[STABLE_AUDIT] busy, dropping a %s sample", event)
		return
	}
	go func() {
		defer func() { <-stableAuditSlots }()
		defer func() {
			if r := recover(); r != nil {
				logrus.Warnf("[STABLE_AUDIT] panic: %v", r)
			}
		}()
		if err := writeStableAudit(dir, event, stable, protoFields); err != nil {
			logrus.Warnf("[STABLE_AUDIT] %s: %v", event, err)
		}
	}()
}

func writeStableAudit(dir, event string, stable any, protoFields []string) error {
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

	fullKey := filepath.Join(sub, key)
	if _, full := stableAuditFull.Load(fullKey); full {
		return nil
	}

	stableAuditMu.Lock()
	defer stableAuditMu.Unlock()
	if err := os.MkdirAll(sub, 0o750); err != nil {
		return err
	}
	existing, _ := filepath.Glob(filepath.Join(sub, key+"-*.json"))
	if len(existing) >= stableAuditCap {
		stableAuditFull.Store(fullKey, struct{}{})
		return nil
	}
	out, err := json.MarshalIndent(map[string]any{"event": event, "stable": anon}, "", "  ")
	if err != nil {
		return err
	}
	name := fmt.Sprintf("%s-%d", key, len(existing)+1)
	if err := os.WriteFile(filepath.Join(sub, name+".json"), append(out, '\n'), 0o640); err != nil {
		return err
	}
	if len(protoFields) > 0 {
		return os.WriteFile(filepath.Join(sub, name+".fields.txt"), []byte(strings.Join(protoFields, "\n")+"\n"), 0o640)
	}
	return nil
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
		case key == "emoji":
			return "<emoji>"
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
			// Same rule as contract/shape.Signature: array elements count as
			// "<path>[]" so [] and [x] are different shapes.
			for _, child := range x {
				if child != nil {
					paths = append(paths, prefix+"[]")
				}
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
