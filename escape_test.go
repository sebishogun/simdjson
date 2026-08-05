package simdjson

import (
	"encoding/binary"
	"encoding/json"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"

	"github.com/sebishogun/simd"
)

// escapedRef is the definition escapedMask has to meet: a byte is escaped if
// the run of backslashes immediately before it has odd length. Written as the
// loop the arithmetic replaces.
func escapedRef(data []byte) []bool {
	out := make([]bool, len(data))
	for i := 0; i < len(data); i++ {
		run := 0
		for j := i - 1; j >= 0 && data[j] == '\\'; j-- {
			run++
		}
		out[i] = run%2 == 1
	}
	return out
}

// The arithmetic carries state between words, so the cases that matter are runs
// of backslashes that straddle a sixty-four byte boundary — and specifically
// runs of every length either side of it, since the bug would be an off-by-one
// in the parity. Random data over a two-symbol alphabet hits those densely.
func TestEscapedMaskAgainstReference(t *testing.T) {
	r := rand.New(rand.NewPCG(19, 23))
	for _, n := range []int{64, 128, 192, 256, 1024} {
		for trial := 0; trial < 300; trial++ {
			data := make([]byte, n)
			for i := range data {
				// Heavily biased to backslashes so long runs are common.
				if r.IntN(3) != 0 {
					data[i] = '\\'
				} else {
					data[i] = 'a'
				}
			}
			want := escapedRef(data)

			mask := make([]byte, n/8)
			for i, c := range data {
				if c == '\\' {
					mask[i/8] |= 1 << (i % 8)
				}
			}
			var prev uint64
			for w := 0; w < n/64; w++ {
				got := escapedMask(binary.LittleEndian.Uint64(mask[w*8:]), &prev)
				for b := 0; b < 64; b++ {
					i := w*64 + b
					if (got>>b&1 == 1) != want[i] {
						t.Fatalf("n=%d trial=%d: byte %d of %q: escaped=%v, want %v",
							n, trial, i, data, got>>b&1 == 1, want[i])
					}
				}
			}
		}
	}
}

// Runs of every length at every offset around a word boundary, exhaustively.
func TestEscapedMaskRunsAcrossWords(t *testing.T) {
	const n = 192
	for start := 56; start < 72; start++ {
		for runLen := 1; runLen <= 12; runLen++ {
			data := make([]byte, n)
			for i := range data {
				data[i] = 'a'
			}
			for i := start; i < start+runLen && i < n; i++ {
				data[i] = '\\'
			}
			want := escapedRef(data)
			mask := make([]byte, n/8)
			for i, c := range data {
				if c == '\\' {
					mask[i/8] |= 1 << (i % 8)
				}
			}
			var prev uint64
			for w := 0; w < n/64; w++ {
				got := escapedMask(binary.LittleEndian.Uint64(mask[w*8:]), &prev)
				for b := 0; b < 64; b++ {
					i := w*64 + b
					if (got>>b&1 == 1) != want[i] {
						t.Fatalf("run of %d at %d: byte %d escaped=%v, want %v",
							runLen, start, i, got>>b&1 == 1, want[i])
					}
				}
			}
		}
	}
}

// The whole point of the escape mask is that a quote after an odd run of
// backslashes does not close a string. These are the documents where getting
// it wrong changes the answer rather than only the speed.
func TestEscapedQuotesDelimitStrings(t *testing.T) {
	docs := []string{
		`{"a":"x\"y","b":1}`,
		`{"a":"x\\","b":2}`,
		`{"a":"x\\\"y","b":3}`,
		`{"a":"\\\\","b":4}`,
		`{"a":"\\\\\"}","b":5}`,
		`{"a":"}{[]:,","b":6}`,
		`{"a":"\"\"\"","b":7}`,
		`["\\","\"","a"]`,
		`{"a":"` + strings.Repeat(`\\`, 40) + `","b":8}`,
		`{"a":"` + strings.Repeat(`\\`, 40) + `\"}","b":9}`,
		// A run that straddles the sixty-four byte word boundary.
		`{"pad":"` + strings.Repeat("z", 50) + `\\\\\"}","b":10}`,
		`{"pad":"` + strings.Repeat("z", 51) + `\\\"}","b":11}`,
		`{"pad":"` + strings.Repeat("z", 52) + `\"}","b":12}`,
	}
	for _, doc := range docs {
		var want any
		if err := json.Unmarshal([]byte(doc), &want); err != nil {
			t.Fatalf("the test document itself is not valid JSON: %s: %v", doc, err)
		}
		d, err := Parse([]byte(doc))
		if err != nil {
			t.Errorf("Parse(%s): %v", doc, err)
			continue
		}
		got := toAny(d.Root())
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Parse(%s) = %#v, encoding/json says %#v", doc, got, want)
		}
	}
}

// Documents that must be rejected. An escape bug shows up here as a document
// that parses when it should not, which is the failure that silently returns
// wrong data rather than an error.
func TestUnterminatedStringsRejected(t *testing.T) {
	bad := []string{
		`{"a":"x`,
		`{"a":"x\"}`,
		`{"a":"\\\"}`,
		`["a","b`,
		`"` + strings.Repeat("z", 100),
	}
	for _, doc := range bad {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("Parse(%q) succeeded; it is not valid JSON", doc)
		}
	}
}

// toAny renders a parsed value the way encoding/json would, so the two can be
// compared directly. Any disagreement about where a string ends shows up as a
// different key set or a different string, not merely a different speed.
func toAny(v Value) any {
	switch v.Kind() {
	case Object:
		m := map[string]any{}
		v.ForEachKey(func(k string, val Value) bool {
			m[k] = toAny(val)
			return true
		})
		return m
	case Array:
		a := []any{}
		v.ForEach(func(val Value) bool {
			a = append(a, toAny(val))
			return true
		})
		return a
	case String:
		return v.String()
	case Number:
		return v.Float()
	case Bool:
		return v.Bool()
	default:
		return nil
	}
}

// kernelScanMin mirrors the simd side's guard threshold for JSONCopyRun --
// one decision, previously written down in two repositories, and the drift
// between the two once put a Go byte loop behind a kernel's name for 35% of a
// Marshal (docs/wrong.md, "A kernel call below the kernel's own threshold").
// simd exports the number now, so the mirror is asserted rather than
// remembered.
func TestKernelScanMinMatchesSimd(t *testing.T) {
	if got := simd.KernelThreshold("JSONCopyRun"); got != kernelScanMin {
		t.Fatalf("kernelScanMin is %d; simd.KernelThreshold(JSONCopyRun) is %d — "+
			"one of the two moved without the other", kernelScanMin, got)
	}
}
