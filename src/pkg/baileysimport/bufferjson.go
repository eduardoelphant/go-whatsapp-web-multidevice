package baileysimport

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Bytes is a byte slice decoded from any of the shapes Baileys' BufferJSON
// reviver (or a plain JSON.stringify of a Node Buffer) can produce:
//
//   - {"type":"Buffer","data":"<base64>"}   BufferJSON.replacer output
//   - {"type":"Buffer","data":[1,2,3]}      Buffer.prototype.toJSON output
//   - {"0":1,"1":2,"2":3}                   JSON.stringify of a Uint8Array
//
// A bare JSON array of numbers and a bare base64 string are accepted too, and
// null decodes to nil.
type Bytes []byte

// UnmarshalJSON implements json.Unmarshaler.
func (b *Bytes) UnmarshalJSON(data []byte) error {
	decoded, err := decodeBufferJSON(data)
	if err != nil {
		return err
	}
	*b = decoded
	return nil
}

func decodeBufferJSON(data []byte) ([]byte, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return nil, nil
	}
	switch data[0] {
	case '"':
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return nil, err
		}
		return decodeBase64(s)
	case '[':
		return decodeByteArray(data)
	case '{':
		var probe struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(data, &probe); err == nil && probe.Type == "Buffer" && len(probe.Data) > 0 {
			return decodeBufferJSON(probe.Data)
		}
		return decodeNumericKeyObject(data)
	default:
		return nil, fmt.Errorf("unsupported buffer encoding %.32q", data)
	}
}

func decodeBase64(s string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if out, err := enc.DecodeString(s); err == nil {
			return out, nil
		}
	}
	return nil, fmt.Errorf("invalid base64 buffer data")
}

func decodeByteArray(data []byte) ([]byte, error) {
	var values []int
	if err := json.Unmarshal(data, &values); err != nil {
		return nil, fmt.Errorf("invalid buffer byte array: %w", err)
	}
	out := make([]byte, len(values))
	for i, v := range values {
		if v < 0 || v > 255 {
			return nil, fmt.Errorf("buffer byte %d out of range", v)
		}
		out[i] = byte(v)
	}
	return out, nil
}

// decodeNumericKeyObject mirrors the BufferJSON reviver branch for objects
// whose keys are all integers and whose values are all numbers. JavaScript
// iterates integer keys in ascending order, so the bytes are ordered by key.
func decodeNumericKeyObject(data []byte) ([]byte, error) {
	var obj map[string]int
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, fmt.Errorf("object is not a buffer: %w", err)
	}
	if len(obj) == 0 {
		return nil, fmt.Errorf("object is not a buffer")
	}
	type entry struct {
		index int
		value int
	}
	entries := make([]entry, 0, len(obj))
	for k, v := range obj {
		idx, err := strconv.Atoi(k)
		if err != nil {
			return nil, fmt.Errorf("object is not a buffer: key %q", k)
		}
		if v < 0 || v > 255 {
			return nil, fmt.Errorf("buffer byte %d out of range", v)
		}
		entries = append(entries, entry{idx, v})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].index < entries[j].index })
	out := make([]byte, len(entries))
	for i, e := range entries {
		out[i] = byte(e.value)
	}
	return out, nil
}

// unwrapJSON removes the layers that auth stores add around a value: a JSON
// document stored as a JSON string (double encoding) and, when allowBuffer is
// set, a Buffer that holds UTF-8 JSON (how Baileys persists sender keys).
// Values that are already objects or arrays are returned unchanged.
func unwrapJSON(raw json.RawMessage, allowBuffer bool) (json.RawMessage, error) {
	for range 4 {
		raw = bytes.TrimSpace(raw)
		if len(raw) == 0 {
			return nil, fmt.Errorf("empty value")
		}
		switch raw[0] {
		case '"':
			var s string
			if err := json.Unmarshal(raw, &s); err != nil {
				return nil, err
			}
			inner := strings.TrimSpace(s)
			if !json.Valid([]byte(inner)) {
				if allowBuffer {
					if decoded, err := decodeBase64(inner); err == nil && json.Valid(decoded) {
						raw = decoded
						continue
					}
				}
				return nil, fmt.Errorf("string value is not JSON")
			}
			raw = json.RawMessage(inner)
		case '{':
			if allowBuffer {
				if decoded, err := decodeBufferJSON(raw); err == nil && json.Valid(decoded) {
					trimmed := bytes.TrimSpace(decoded)
					if len(trimmed) > 0 && (trimmed[0] == '[' || trimmed[0] == '{') {
						raw = trimmed
						continue
					}
				}
			}
			return raw, nil
		default:
			return raw, nil
		}
	}
	return nil, fmt.Errorf("value is nested too deeply")
}

// unwrapString decodes a JSON string value, peeling any extra levels of string
// encoding (a JSON string that itself contains a JSON string). Some auth stores
// stringify before persisting and again when exporting, so a plain value such
// as a LID mapping can arrive wrapped two or three times.
func unwrapString(raw json.RawMessage) (string, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("expected a string: %w", err)
	}
	for range 4 {
		trimmed := strings.TrimSpace(s)
		if !strings.HasPrefix(trimmed, `"`) {
			break
		}
		var inner string
		if err := json.Unmarshal([]byte(trimmed), &inner); err != nil {
			break
		}
		s = inner
	}
	return s, nil
}

// jsonInt64 decodes an integer written as a number, a numeric string or a
// long.js Long object ({"low":..,"high":..,"unsigned":..}).
type jsonInt64 int64

// UnmarshalJSON implements json.Unmarshaler.
func (n *jsonInt64) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		*n = 0
		return nil
	}
	switch data[0] {
	case '"':
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		if s == "" {
			*n = 0
			return nil
		}
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid integer string %q", s)
		}
		*n = jsonInt64(v)
	case '{':
		var long struct {
			Low  int32 `json:"low"`
			High int32 `json:"high"`
		}
		if err := json.Unmarshal(data, &long); err != nil {
			return fmt.Errorf("invalid Long value: %w", err)
		}
		*n = jsonInt64(int64(long.High)<<32 | int64(uint32(long.Low)))
	default:
		var f json.Number
		if err := json.Unmarshal(data, &f); err != nil {
			return err
		}
		if v, err := f.Int64(); err == nil {
			*n = jsonInt64(v)
			return nil
		}
		fv, err := f.Float64()
		if err != nil {
			return err
		}
		*n = jsonInt64(int64(fv))
	}
	return nil
}
