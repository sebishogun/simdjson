package bench

// Marshalling a decoded document, with and without sorted map keys.
//
// docs/competition.md claims a number for both and nothing measured the sorted
// one, which is the row where sonic leads. A claim with no benchmark behind it
// is a claim that cannot go stale in any visible way.
//
// The distinction matters because the two libraries disagree about the
// default. encoding/json always sorts, so this does too. sonic does not unless
// asked -- sonic.Marshal on the same map thirty times gives several different
// outputs -- so the comparison has to name which sonic it is measuring, and
// both are measured here.

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/bytedance/sonic"
	gojson "github.com/goccy/go-json"
	ours "github.com/sebishogun/simdjson"
)

// Sorting is not free, and the question is what it costs each library rather
// than whether it is on. Unsorted output is a set of bytes, not a value, so
// only the sorted column compares like with like.
func BenchmarkMarshalDecoded(b *testing.B) {
	for _, name := range corpus {
		data := loadCorpus(b, name)
		var v any
		if err := json.Unmarshal(data, &v); err != nil {
			b.Fatalf("%s: %v", name, err)
		}

		// Sorted output must agree byte for byte before the timings mean
		// anything -- an encoder that skips the sort is doing less work.
		std, err := json.Marshal(v)
		if err != nil {
			b.Fatalf("%s stdlib: %v", name, err)
		}
		mine, err := ours.Marshal(v)
		if err != nil {
			b.Fatalf("%s ours: %v", name, err)
		}
		if !bytes.Equal(std, mine) {
			b.Fatalf("%s: ours differs from encoding/json", name)
		}
		sstd, err := sonic.ConfigStd.Marshal(v)
		if err != nil {
			b.Fatalf("%s sonic: %v", name, err)
		}
		if !bytes.Equal(std, sstd) {
			b.Fatalf("%s: sonic.ConfigStd differs from encoding/json", name)
		}

		b.Run(name+"/sorted/ours", func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				if _, err := ours.Marshal(v); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(name+"/sorted/sonic-std", func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				if _, err := sonic.ConfigStd.Marshal(v); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(name+"/sorted/stdlib", func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				if _, err := json.Marshal(v); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(name+"/sorted/goccy", func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				if _, err := gojson.Marshal(v); err != nil {
					b.Fatal(err)
				}
			}
		})

		// Unsorted, where both libraries are allowed to skip it. Not
		// byte-comparable, and labelled so nobody reads it as though it were.
		unsorted := ours.Options{EscapeHTML: true, ValidateStrings: true}
		b.Run(name+"/unsorted/ours", func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				if _, err := unsorted.Marshal(v); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(name+"/unsorted/sonic", func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				if _, err := sonic.Marshal(v); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
