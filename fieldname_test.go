package simdjson

import (
	"bytes"
	stdjson "encoding/json"
	"testing"
)

// A field name longer than maxFieldName is not in the length table, so the
// lookup has to fall back to the map for it. The fast path added for keys
// longer than the longest table entry must not swallow those.
//
// Without the longName flag this test decodes zero fields: every key is longer
// than the table, and the table is empty because the only name did not fit it.
type longNameStruct struct {
	// 70 characters, past maxFieldName.
	A int `json:"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`
	B int `json:"b"`
}

func TestUnmarshalFieldNameLongerThanTable(t *testing.T) {
	const long = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if len(long) <= maxFieldName {
		t.Fatalf("test name is %d bytes, needs to exceed maxFieldName (%d)", len(long), maxFieldName)
	}
	src := []byte(`{"` + long + `":7,"b":9,"` + long + `x":1}`)

	var got longNameStruct
	if err := Unmarshal(src, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	var want longNameStruct
	if err := stdjson.Unmarshal(src, &want); err != nil {
		t.Fatalf("encoding/json: %v", err)
	}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if got.A != 7 || got.B != 9 {
		t.Fatalf("got %+v, want {A:7 B:9}", got)
	}
}

// The mirror: a struct whose names all fit the table must reject a longer key
// without consulting the map, and must still reject it -- not accept it as some
// other field.
func TestUnmarshalKeyLongerThanEveryFieldName(t *testing.T) {
	type small struct {
		A int `json:"a"`
		B int `json:"bb"`
	}
	src := []byte(`{"a":1,"bb":2,"bbb":3,"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa":4}`)

	var got small
	if err := Unmarshal(src, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.A != 1 || got.B != 2 {
		t.Fatalf("got %+v, want {A:1 B:2}", got)
	}

	// DisallowUnknownFields must still see them.
	var d small
	dec := NewDecoder(bytes.NewReader(src))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err == nil {
		t.Fatal("DisallowUnknownFields accepted a key longer than every field name")
	}
}
