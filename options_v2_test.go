package simdjson

import (
	"bytes"
	stdjson "encoding/json"
	"strings"
	"testing"
	"time"
)

type omitRow struct {
	A int            `json:"a"`
	B string         `json:"b"`
	C []int          `json:"c"`
	D map[string]int `json:"d"`
	E *int           `json:"e"`
	F bool           `json:"f"`
	G float64        `json:"g"`
	H time.Time      `json:"h"`
	I int            `json:"i,omitempty"`
	J int            `json:"j"`
}

func TestOmitZeroStructFields(t *testing.T) {
	var v omitRow
	v.J = 7
	// Default: everything is written, matching encoding/json.
	got, err := Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	want, err := stdjson.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("default\n got %s\nwant %s", got, want)
	}

	o := Std
	o.OmitZeroStructFields = true
	got, err = o.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"j":7}` {
		t.Errorf("OmitZeroStructFields = %s, want {\"j\":7}", got)
	}

	// A non-zero value of each kind survives.
	full := omitRow{A: 1, B: "x", C: []int{1}, D: map[string]int{"k": 1},
		F: true, G: 1.5, H: time.Unix(1, 0).UTC(), I: 2, J: 3}
	n := 5
	full.E = &n
	got, err = o.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{`"a"`, `"b"`, `"c"`, `"d"`, `"e"`, `"f"`, `"g"`, `"h"`, `"i"`, `"j"`} {
		if !strings.Contains(string(got), k) {
			t.Errorf("OmitZeroStructFields dropped %s from %s", k, got)
		}
	}
}

func TestSortMapKeys(t *testing.T) {
	m := map[string]int{"z": 1, "a": 2, "m": 3, "b": 4}
	got, err := Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	want, err := stdjson.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("default map order\n got %s\nwant %s", got, want)
	}
	// Sorted output is stable across calls; that is the property being kept.
	for i := 0; i < 20; i++ {
		again, _ := Marshal(m)
		if string(again) != string(got) {
			t.Fatalf("sorted output differed between calls: %s then %s", got, again)
		}
	}

	// Unsorted still produces every key, and valid JSON.
	o := Std
	o.SortMapKeys = false
	un, err := o.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if !Valid(un) {
		t.Fatalf("unsorted output is not valid JSON: %s", un)
	}
	var back map[string]int
	if err := Unmarshal(un, &back); err != nil {
		t.Fatal(err)
	}
	if len(back) != 4 || back["z"] != 1 || back["b"] != 4 {
		t.Errorf("unsorted round trip = %v", back)
	}
}

func TestMarshalWrite(t *testing.T) {
	v := map[string]any{"a": 1, "b": "two"}
	var buf bytes.Buffer
	if err := MarshalWrite(&buf, v); err != nil {
		t.Fatal(err)
	}
	want, _ := Marshal(v)
	if buf.String() != string(want) {
		t.Errorf("MarshalWrite = %q, Marshal = %q", buf.String(), want)
	}
	// Unlike Encoder.Encode, no trailing newline.
	if strings.HasSuffix(buf.String(), "\n") {
		t.Error("MarshalWrite added a newline")
	}
	// A failing writer surfaces its error.
	if err := MarshalWrite(errWriter{}, v); err == nil {
		t.Error("write error was swallowed")
	}
}

type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, errSyntax("no") }
