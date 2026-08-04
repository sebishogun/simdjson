package bench

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/bytedance/sonic"
	gojson "github.com/goccy/go-json"
	ours "github.com/sebishogun/simdjson"
)

// Unmarshal into map[string]any, which every library supports identically.
func BenchmarkUnmarshalAny(b *testing.B) {
	for _, name := range corpus {
		data := loadCorpus(b, name)
		b.Run(fmt.Sprintf("%s/ours", name), func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				var v any
				if err := ours.Unmarshal(data, &v); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("%s/stdlib", name), func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				var v any
				if err := json.Unmarshal(data, &v); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("%s/sonic", name), func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				var v any
				if err := sonic.Unmarshal(data, &v); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("%s/goccy", name), func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				var v any
				if err := gojson.Unmarshal(data, &v); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
