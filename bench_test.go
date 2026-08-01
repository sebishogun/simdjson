package simdjson

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

// A payload shaped like an API response: nested objects, arrays, strings and
// numbers, with more of it than any one caller wants.
func genDoc(n int) []byte {
	r := rand.New(rand.NewPCG(1, 2))
	var sb strings.Builder
	sb.WriteString(`{"meta":{"page":1,"total":`)
	fmt.Fprintf(&sb, "%d", n)
	sb.WriteString(`},"items":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, `{"id":%d,"name":"item-%d","score":%.4f,"active":%v,`,
			i, i, r.Float64(), i%2 == 0)
		fmt.Fprintf(&sb, `"tags":["a","b","c"],"nested":{"x":%d,"y":"%s"}}`,
			i*3, strings.Repeat("z", 8))
	}
	sb.WriteString(`]}`)
	return []byte(sb.String())
}

// The case this package is for: a few values out of a large document.
func BenchmarkGetFields(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		data := genDoc(n)
		b.Run(fmt.Sprintf("n=%d/encoding-json", n), func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				var m map[string]any
				if err := json.Unmarshal(data, &m); err != nil {
					b.Fatal(err)
				}
				_ = m["meta"].(map[string]any)["total"]
			}
		})
		b.Run(fmt.Sprintf("n=%d/simdjson", n), func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				doc, err := Parse(data)
				if err != nil {
					b.Fatal(err)
				}
				_ = doc.Get("meta", "total").Int()
			}
		})
		b.Run(fmt.Sprintf("n=%d/simdjson-scan", n), func(b *testing.B) {
			var p Parser
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				doc, err := p.Scan(data)
				if err != nil {
					b.Fatal(err)
				}
				_ = doc.Get("meta", "total").Int()
			}
		})
	}
}

// Walking everything, which is the case that favours the standard library
// least unfairly.
func BenchmarkWalkAll(b *testing.B) {
	data := genDoc(2000)
	b.Run("encoding-json", func(b *testing.B) {
		b.SetBytes(int64(len(data)))
		for b.Loop() {
			var m map[string]any
			if err := json.Unmarshal(data, &m); err != nil {
				b.Fatal(err)
			}
			var sum float64
			for _, it := range m["items"].([]any) {
				sum += it.(map[string]any)["score"].(float64)
			}
			sinkF = sum
		}
	})
	b.Run("simdjson-scan", func(b *testing.B) {
		var p Parser
		b.SetBytes(int64(len(data)))
		for b.Loop() {
			doc, err := p.Scan(data)
			if err != nil {
				b.Fatal(err)
			}
			var sum float64
			doc.Get("items").ForEach(func(v Value) bool {
				sum += v.Key("score").Float()
				return true
			})
			sinkF = sum
		}
	})
}

// Validation only: is this valid JSON?
func BenchmarkValidate(b *testing.B) {
	data := genDoc(2000)
	b.Run("encoding-json", func(b *testing.B) {
		b.SetBytes(int64(len(data)))
		for b.Loop() {
			sinkB = json.Valid(data)
		}
	})
	b.Run("simdjson", func(b *testing.B) {
		b.SetBytes(int64(len(data)))
		for b.Loop() {
			_, err := Parse(data)
			sinkB = err == nil
		}
	})
}

var (
	sinkF float64
	sinkB bool
)

// Does reusing the index buffers help, or does the allocator hand back memory
// that is just as hot? Same document repeatedly, which is the shape of a server
// handling a stream of similar payloads.
func BenchmarkReuse(b *testing.B) {
	data := genDoc(2000)
	b.Run("Parse/fresh", func(b *testing.B) {
		b.SetBytes(int64(len(data)))
		for b.Loop() {
			doc, err := Parse(data)
			if err != nil {
				b.Fatal(err)
			}
			sinkF = doc.Get("meta", "total").Float()
		}
	})
	b.Run("Parser/reused", func(b *testing.B) {
		var p Parser
		b.SetBytes(int64(len(data)))
		for b.Loop() {
			doc, err := p.Parse(data)
			if err != nil {
				b.Fatal(err)
			}
			sinkF = doc.Get("meta", "total").Float()
		}
	})
}
