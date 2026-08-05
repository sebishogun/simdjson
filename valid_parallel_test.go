package simdjson

// The gate on the parallel grammar walk: bool agreement with the serial walk,
// forced onto small documents so element boundaries, gaps and fallbacks are
// all crossed.

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

func forceValidParallel(t *testing.T) {
	t.Helper()
	oldMin, oldSeg, oldEl := parallelMinBytes, parallelSegBytes, validParallelMinElems
	parallelMinBytes, parallelSegBytes, validParallelMinElems = 1, 1, 4
	t.Cleanup(func() {
		parallelMinBytes, parallelSegBytes, validParallelMinElems = oldMin, oldSeg, oldEl
	})
}

func validBoth(t *testing.T, name string, doc []byte) {
	t.Helper()
	got := Valid(doc)
	oldMin := parallelMinBytes
	parallelMinBytes = 1 << 62
	want := Valid(doc)
	parallelMinBytes = oldMin
	if got != want {
		t.Fatalf("%s: parallel Valid %v, serial %v\n%.200s", name, got, want, doc)
	}
}

func TestValidParallelWalk(t *testing.T) {
	forceValidParallel(t)
	seg := chunkBytes
	rep := func(el string, n int) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = el
		}
		return "[" + strings.Join(parts, ",") + "]"
	}
	cases := map[string]string{
		"objects":        rep(`{"k":"v","n":[1,2.5e3,true]}`, 3*seg/28),
		"arrays":         rep(`[1,"two",{"three":3}]`, 2000),
		"pretty gaps":    "[\n  " + strings.Repeat(`{"a":1},`+"\n  ", 3000) + `{"z":0}` + "\n]",
		"ws everywhere":  "  " + rep(`{ "a" : [ 1 , 2 ] }`, 2000) + "\t\n",
		"deep elements":  rep(strings.Repeat(`[`, 40)+`1`+strings.Repeat(`]`, 40), 1500),
		"strings heavy":  rep(`{"s":"with \"escapes\" and é and ]}, inside"}`, 1500),
		"bad number":     rep(`{"n":1}`, 500)[:0] + "[" + strings.Repeat(`{"n":1},`, 400) + `{"n":01}]`,
		"bad literal":    "[" + strings.Repeat(`{"b":true},`, 400) + `{"b":tru}]`,
		"junk in elem":   "[" + strings.Repeat(`{"a":1},`, 400) + `{"a":1 2}]`,
		"missing comma":  "[" + strings.Repeat(`{"a":1},`, 300) + `{"b":2} {"c":3}` + strings.Repeat(`,{"a":1}`, 300) + "]",
		"double comma":   "[" + strings.Repeat(`{"a":1},`, 300) + `,{"b":2}` + strings.Repeat(`,{"a":1}`, 300) + "]",
		"junk in gap":    "[" + strings.Repeat(`{"a":1},`, 300) + `x{"b":2}` + strings.Repeat(`,{"a":1}`, 300) + "]",
		"after close":    rep(`{"a":1}`, 600) + `x`,
		"before open":    `x` + rep(`{"a":1}`, 600),
		"trailing comma": "[" + strings.Repeat(`{"a":1},`, 600) + "]",
		"mixed scalars":  "[" + strings.Repeat(`{"a":1},`, 400) + `7,{"b":2}]`,
		"root object":    `{"k":` + rep(`{"a":1}`, 600) + `}`,
		"empty elems":    rep(`{}`, 3000),
		"unclosed elem":  "[" + strings.Repeat(`{"a":1},`, 400) + `{"b":2`,
	}
	for name, doc := range cases {
		validBoth(t, name, []byte(doc))
	}
	// Random valid-ish and broken-ish documents of the target shape.
	rng := rand.New(rand.NewSource(77))
	els := []string{`{"a":1,"b":[1,2]}`, `{"s":"x\ny"}`, `[true,null,1e4]`, `{"n":-0.5}`}
	breaks := []string{`{"n":01}`, `{"b":tru}`, `{"a":}`, `{"a":1`, `{"a":1}}`, `"s"`, `5`}
	for trial := 0; trial < 60; trial++ {
		var b strings.Builder
		b.WriteByte('[')
		n := 40 + rng.Intn(4000)
		brk := -1
		if trial%2 == 0 {
			brk = rng.Intn(n)
		}
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
		validBoth(t, fmt.Sprintf("random %d", trial), []byte(b.String()))
	}
}
