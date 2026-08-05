package simdjson

// The gate on parallel Parse: identical Docs and identical errors to the
// serial finish, forced small; then navigation over the parallel-built Doc,
// since a Doc with a wrong root extent looks fine until someone walks it.

import (
	stdjson "encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

func forceParseParallel(t *testing.T) {
	t.Helper()
	oldMin, oldSeg, oldEl := parallelMinBytes, parallelSegBytes, parseParallelMinElems
	parallelMinBytes, parallelSegBytes, parseParallelMinElems = 1, 1, 8
	t.Cleanup(func() {
		parallelMinBytes, parallelSegBytes, parseParallelMinElems = oldMin, oldSeg, oldEl
	})
}

func parseBoth(t *testing.T, name string, doc []byte) {
	t.Helper()
	gDoc, gErr := Parse(doc)
	oldMin := parallelMinBytes
	parallelMinBytes = 1 << 62
	wDoc, wErr := Parse(doc)
	parallelMinBytes = oldMin
	if (gErr == nil) != (wErr == nil) || (gErr != nil && gErr.Error() != wErr.Error()) {
		t.Fatalf("%s: parallel err %v, serial err %v", name, gErr, wErr)
	}
	if gErr != nil {
		return
	}
	gr, wr := gDoc.Root(), wDoc.Root()
	if gr.Kind() != wr.Kind() || gr.start != wr.start || gr.end != wr.end {
		t.Fatalf("%s: root %v[%d:%d] vs %v[%d:%d]",
			name, gr.Kind(), gr.start, gr.end, wr.Kind(), wr.start, wr.end)
	}
	// Navigation equivalence: decode both roots and compare, and spot-check
	// paths.
	var gv, wv any
	if err := gDoc.Unmarshal(&gv); err != nil {
		t.Fatalf("%s: decode parallel doc: %v", name, err)
	}
	if err := wDoc.Unmarshal(&wv); err != nil {
		t.Fatalf("%s: decode serial doc: %v", name, err)
	}
	if !reflect.DeepEqual(gv, wv) {
		t.Fatalf("%s: decoded values differ", name)
	}
	if n := gr.Len(); n > 2 {
		a := gDoc.Root().Index(n - 1)
		b := wDoc.Root().Index(n - 1)
		if a.Raw() == nil || string(a.Raw()) != string(b.Raw()) {
			t.Fatalf("%s: last element differs: %s vs %s", name, a.Raw(), b.Raw())
		}
	}
}

func TestParseParallelMatchesSerial(t *testing.T) {
	forceParseParallel(t)
	rng := rand.New(rand.NewSource(123))
	els := []string{
		`{"a":1,"b":"x","c":[1,2,{"d":null}]}`,
		`{"s":"with \"escape\" and unié"}`,
		`[true,false,{"n":-1.5e3}]`,
		`{}`,
		`{"deep":{"deeper":{"deepest":[{}]}}}`,
	}
	breaks := []string{`{"n":01}`, `{"a":tru}`, `{"a":}`, `{"a":"` + "\x02" + `"}`, `{"a":1]`}
	for trial := 0; trial < 30; trial++ {
		n := 8 + rng.Intn(600)
		brk := -1
		if trial%2 == 1 {
			brk = rng.Intn(n)
		}
		var b strings.Builder
		b.WriteByte('[')
		for i := 0; i < n; i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			if i == brk {
				b.WriteString(breaks[rng.Intn(len(breaks))])
			} else {
				b.WriteString(els[rng.Intn(len(els))])
			}
		}
		b.WriteByte(']')
		parseBoth(t, fmt.Sprintf("random %d", trial), []byte(b.String()))
	}
	cases := map[string]string{
		"trailing junk": `[` + strings.Repeat(`{"a":1},`, 40) + `{"a":2}]]`,
		"pretty":        "[\n " + strings.Repeat("{\"a\": 1} ,\n ", 40) + `{"a":2}` + " \n]\n",
		"scalar mixed":  `[` + strings.Repeat(`{"a":1},`, 40) + `3]`,
		"root object":   `{"k":` + strings.Repeat(`1,`, 1)[:0] + `[` + strings.Repeat(`{"a":1},`, 40) + `{"a":2}]}`,
		"parser reuse":  `[` + strings.Repeat(`{"r":1},`, 60) + `{"r":2}]`,
	}
	for name, doc := range cases {
		parseBoth(t, name, []byte(doc))
	}
	// Parser (buffer-reusing) goes through the same finish.
	var p Parser
	doc := []byte(`[` + strings.Repeat(`{"q":7},`, 80) + `{"q":8}]`)
	d1, err := p.Parse(doc)
	if err != nil {
		t.Fatal(err)
	}
	var v1 any
	if err := d1.Unmarshal(&v1); err != nil {
		t.Fatal(err)
	}
	var vs any
	if err := stdjson.Unmarshal(doc, &vs); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(v1, vs) {
		t.Fatal("Parser doc differs from stdlib")
	}
}
