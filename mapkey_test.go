package simdjson

// Map key types, because compileMapEncoder now decides once per type what it
// used to decide once per key.
//
// The old code asked whether a key needed MarshalText by calling k.Interface()
// and asserting on the result — a per-key allocation to answer a question about
// the type. The check is hoisted to kt.Implements(textMarshalerType), and the
// two disagree in exactly the places worth testing: an interface-typed key,
// where the assertion sees the dynamic type and Implements sees the static one,
// and a type whose MarshalText is on the pointer receiver.

import (
	"bytes"
	stdjson "encoding/json"
	"testing"
)

type textKey struct{ N int }

func (t textKey) MarshalText() ([]byte, error) { return []byte{'k', byte('0' + t.N)}, nil }

type ptrTextKey struct{ N int }

func (t *ptrTextKey) MarshalText() ([]byte, error) { return []byte{'p', byte('0' + t.N)}, nil }

type stringKey string

func TestMarshalMapKeyTypes(t *testing.T) {
	cases := []struct {
		name string
		v    any
	}{
		{"string", map[string]int{"b": 2, "a": 1}},
		{"named string", map[stringKey]int{"b": 2, "a": 1}},
		{"int", map[int]string{2: "b", 1: "a", -3: "c"}},
		{"int64", map[int64]string{2: "b", 1: "a"}},
		{"uint", map[uint]string{2: "b", 1: "a"}},
		// MarshalText on the value receiver: both the old assertion and the
		// hoisted Implements say yes.
		{"TextMarshaler", map[textKey]int{{2}: 2, {1}: 1}},
		// MarshalText on the pointer receiver only: both say no, and both fall
		// through to the same UnsupportedTypeError.
		{"pointer TextMarshaler", map[*ptrTextKey]int{}},
		{"empty", map[string]int{}},
		{"nil", map[string]int(nil)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			want, wErr := stdjson.Marshal(c.v)
			got, gErr := Marshal(c.v)
			if (wErr == nil) != (gErr == nil) {
				t.Fatalf("error mismatch: encoding/json %v, ours %v", wErr, gErr)
			}
			if wErr != nil {
				return
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("got %s, want %s", got, want)
			}
		})
	}
}

// A map whose value type contains the map type itself. The value encoder is now
// resolved once per call rather than per entry, and resolving it too early --
// while compileMapEncoder is still returning -- would not terminate.
type recur struct {
	Name string           `json:"name"`
	Sub  map[string]recur `json:"sub,omitempty"`
}

func TestMarshalRecursiveMapValue(t *testing.T) {
	v := map[string]recur{
		"a": {Name: "a", Sub: map[string]recur{
			"b": {Name: "b", Sub: map[string]recur{"c": {Name: "c"}}},
		}},
		"z": {Name: "z"},
	}
	want, err := stdjson.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %s, want %s", got, want)
	}
}

// map[string]any keeps taking the dynamic type per entry, since that is the
// whole point of an interface value type.
func TestMarshalMapInterfaceValues(t *testing.T) {
	v := map[string]any{
		"s": "x", "n": 1.5, "b": true, "z": nil,
		"a": []any{1.0, "two"}, "m": map[string]any{"k": "v"},
	}
	want, err := stdjson.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %s, want %s", got, want)
	}
}
