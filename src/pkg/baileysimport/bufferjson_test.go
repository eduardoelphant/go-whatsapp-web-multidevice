package baileysimport

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestBytesDecodesAllBufferJSONShapes(t *testing.T) {
	want := []byte{0x00, 0x05, 0xff, 0x10}
	cases := map[string]string{
		"replacer base64":    `{"type":"Buffer","data":"AAX/EA=="}`,
		"Buffer.toJSON":      `{"type":"Buffer","data":[0,5,255,16]}`,
		"Uint8Array object":  `{"0":0,"1":5,"2":255,"3":16}`,
		"unordered keys":     `{"3":16,"1":5,"0":0,"2":255}`,
		"bare array":         `[0,5,255,16]`,
		"bare base64 string": `"AAX/EA=="`,
		"unpadded base64":    `"AAX/EA"`,
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			var b Bytes
			if err := json.Unmarshal([]byte(input), &b); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if !bytes.Equal(b, want) {
				t.Fatalf("got %x, want %x", []byte(b), want)
			}
		})
	}
}

func TestBytesNullAndErrors(t *testing.T) {
	var b Bytes
	if err := json.Unmarshal([]byte(`null`), &b); err != nil || b != nil {
		t.Fatalf("null: %v %x", err, []byte(b))
	}
	for _, bad := range []string{`{"type":"Buffer","data":[256]}`, `{"a":1}`, `{"0":"x"}`, `[-1]`, `"@@@"`, `true`} {
		if err := json.Unmarshal([]byte(bad), &b); err == nil {
			t.Errorf("%s: expected error", bad)
		}
	}
}

func TestUnwrapJSON(t *testing.T) {
	obj := `{"a":1}`
	doubled, _ := json.Marshal(obj)
	tripled, _ := json.Marshal(string(doubled))
	for name, input := range map[string]string{"object": obj, "double": string(doubled), "triple": string(tripled)} {
		out, err := unwrapJSON(json.RawMessage(input), false)
		if err != nil || string(out) != obj {
			t.Errorf("%s: got %s, %v", name, out, err)
		}
	}

	// A Buffer holding UTF-8 JSON (Baileys' sender-key storage).
	buffer := `{"type":"Buffer","data":"W3sieCI6MX1d"}` // [{"x":1}]
	out, err := unwrapJSON(json.RawMessage(buffer), true)
	if err != nil || string(out) != `[{"x":1}]` {
		t.Fatalf("buffer: got %s, %v", out, err)
	}
	if out, _ := unwrapJSON(json.RawMessage(buffer), false); string(out) != buffer {
		t.Fatalf("buffer without allowBuffer should be left alone, got %s", out)
	}
	if _, err := unwrapJSON(json.RawMessage(`"not json"`), false); err == nil {
		t.Fatal("expected error for non-JSON string")
	}
}

func TestUnwrapString(t *testing.T) {
	for input, want := range map[string]string{`"123"`: "123", `"\"123\""`: "123"} {
		got, err := unwrapString(json.RawMessage(input))
		if err != nil || got != want {
			t.Errorf("%s: got %q, %v", input, got, err)
		}
	}
}

func TestJSONInt64(t *testing.T) {
	cases := map[string]int64{
		`1758000000999`: 1758000000999,
		`"1758000000"`:  1758000000,
		`{"low":1358376059,"high":409,"unsigned":false}`: 1758000000123,
		`{"low":-1,"high":0,"unsigned":true}`:            4294967295,
		`null`:                                           0,
		`""`:                                             0,
	}
	for input, want := range cases {
		var n jsonInt64
		if err := json.Unmarshal([]byte(input), &n); err != nil || int64(n) != want {
			t.Errorf("%s: got %d, %v; want %d", input, n, err, want)
		}
	}
}
