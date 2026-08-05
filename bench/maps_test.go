package bench

// Marshalling maps with concrete value types.
//
// docs/competition.md has a row for marshalling a *decoded* document, which is
// map[string]any and goes through the reflect-free encoder. Nothing measured
// map[string]string, map[string]int or map[string]SomeStruct, which take the
// compiled reflect path instead -- a different encoder, with a different cost
// per entry, and a common shape in real code.
//
// It matters here because that path collects the map's keys with MapRange,
// sorts them, and then calls MapIndex to fetch each value again: a second hash
// of every key, for something the range already had in hand. The reflect-free
// encoder documents fixing exactly that and measuring it at 10%. Whether
// carrying the value through instead is a win depends on reflect.MapIter.Value
// boxing its result -- one allocation against one hash -- and that is a
// measurement, not an argument. There was nothing to measure it with.

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/bytedance/sonic"
	gojson "github.com/goccy/go-json"
	ours "github.com/sebishogun/simdjson"
)

type mapVal struct {
	ID   int64   `json:"id"`
	Name string  `json:"name"`
	N    float64 `json:"n"`
	OK   bool    `json:"ok"`
}

// Key sets of a few sizes, because the sort's share of the work depends on how
// many keys there are and the encoders differ most on small maps.
func mapKeys(n int) []string {
	out := make([]string, n)
	for i := range out {
		// Shared prefixes, which is what JSON keys look like and what the
		// radix sort exists for.
		out[i] = fmt.Sprintf("field_name_%04d", i)
	}
	return out
}

func BenchmarkMarshalMap(b *testing.B) {
	for _, n := range []int{4, 16, 64, 256} {
		keys := mapKeys(n)

		ms := make(map[string]string, n)
		mi := make(map[string]int, n)
		mv := make(map[string]mapVal, n)
		for i, k := range keys {
			ms[k] = "a value of some length for " + k
			mi[k] = i * 7
			mv[k] = mapVal{ID: int64(i), Name: k, N: float64(i) * 1.5, OK: i%2 == 0}
		}

		for _, c := range []struct {
			name string
			v    any
		}{
			{"string", ms},
			{"int", mi},
			{"struct", mv},
		} {
			// Sorted output, so every encoder is doing the same work. Checked
			// against encoding/json before it is timed: an encoder that skips
			// the sort produces different bytes and a smaller number.
			want, err := json.Marshal(c.v)
			if err != nil {
				b.Fatal(err)
			}
			got, err := ours.Marshal(c.v)
			if err != nil {
				b.Fatal(err)
			}
			if string(got) != string(want) {
				b.Fatalf("%s/%d: ours differs from encoding/json", c.name, n)
			}
			sgot, err := sonic.ConfigStd.Marshal(c.v)
			if err != nil {
				b.Fatal(err)
			}
			if string(sgot) != string(want) {
				b.Fatalf("%s/%d: sonic.ConfigStd differs from encoding/json", c.name, n)
			}

			prefix := fmt.Sprintf("%s/n=%d/", c.name, n)
			v := c.v
			b.Run(prefix+"ours", func(b *testing.B) {
				b.SetBytes(int64(len(want)))
				for b.Loop() {
					if _, err := ours.Marshal(v); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run(prefix+"sonic-std", func(b *testing.B) {
				b.SetBytes(int64(len(want)))
				for b.Loop() {
					if _, err := sonic.ConfigStd.Marshal(v); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run(prefix+"goccy", func(b *testing.B) {
				b.SetBytes(int64(len(want)))
				for b.Loop() {
					if _, err := gojson.Marshal(v); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run(prefix+"stdlib", func(b *testing.B) {
				b.SetBytes(int64(len(want)))
				for b.Loop() {
					if _, err := json.Marshal(v); err != nil {
						b.Fatal(err)
					}
				}
			})

			// The same encode without sorting, which is the only way to see
			// how much of the gap is the sort and how much is everything else.
			// Not byte-comparable with the rows above, and labelled so.
			unsorted := ours.Options{EscapeHTML: true, ValidateStrings: true}
			b.Run(prefix+"unsorted-ours", func(b *testing.B) {
				b.SetBytes(int64(len(want)))
				for b.Loop() {
					if _, err := unsorted.Marshal(v); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run(prefix+"unsorted-sonic", func(b *testing.B) {
				b.SetBytes(int64(len(want)))
				for b.Loop() {
					if _, err := sonic.Marshal(v); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// Allocations, which is the thing the double hash trades against: carrying the
// value through the sort means boxing a reflect.Value per entry.
func BenchmarkMarshalMapAllocs(b *testing.B) {
	keys := mapKeys(64)
	m := make(map[string]mapVal, len(keys))
	for i, k := range keys {
		m[k] = mapVal{ID: int64(i), Name: k, N: float64(i) * 1.5, OK: i%2 == 0}
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := ours.Marshal(m); err != nil {
			b.Fatal(err)
		}
	}
}
