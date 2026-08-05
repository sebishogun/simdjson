//go:build goexperiment.jsonv2

package bench

// The stdlib rows under the engine Go 1.27 intends to make default.
// Build and run with GOEXPERIMENT=jsonv2 (make bench-v2); without the
// experiment this file does not exist and nothing changes.
//
// Two things are measured: v1-via-v2 (the encoding/json surface reimplemented
// on the v2 engine — what existing programs get for free) comes out of the
// ordinary stdlib rows when this build is active, and the native v2 API
// (jsonv2.Marshal/Unmarshal) gets its own rows here. v2 semantics differ:
// case-sensitive field matching and duplicate-key rejection, so its numbers
// are labeled, not merged.

import (
	jsonv2 "encoding/json/v2"
	"testing"
)

func BenchmarkJSONv2Native(b *testing.B) {
	twitter := loadCorpus(b, "twitter")
	citm := loadCorpus(b, "citm")
	canada := loadCorpus(b, "canada")

	b.Run("unmarshal-twitter-struct", func(b *testing.B) {
		b.SetBytes(int64(len(twitter)))
		for b.Loop() {
			var v tSearch
			if err := jsonv2.Unmarshal(twitter, &v); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("unmarshal-citm-struct", func(b *testing.B) {
		b.SetBytes(int64(len(citm)))
		for b.Loop() {
			var v citmDoc
			if err := jsonv2.Unmarshal(citm, &v); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("unmarshal-canada-struct", func(b *testing.B) {
		b.SetBytes(int64(len(canada)))
		for b.Loop() {
			var v canadaFC
			if err := jsonv2.Unmarshal(canada, &v); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("marshal-twitter-struct", func(b *testing.B) {
		var v tSearch
		if err := jsonv2.Unmarshal(twitter, &v); err != nil {
			b.Fatal(err)
		}
		out, err := jsonv2.Marshal(&v)
		if err != nil {
			b.Fatal(err)
		}
		b.SetBytes(int64(len(out)))
		for b.Loop() {
			if _, err := jsonv2.Marshal(&v); err != nil {
				b.Fatal(err)
			}
		}
	})
}
