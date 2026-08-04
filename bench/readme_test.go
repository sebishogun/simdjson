package bench

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/buger/jsonparser"
	"github.com/bytedance/sonic"
	gojson "github.com/goccy/go-json"
	msj "github.com/minio/simdjson-go"
	ours "github.com/sebishogun/simdjson"
	"github.com/tidwall/gjson"
	"github.com/valyala/fastjson"
)

// The numbers the README quotes, in one benchmark so they are measured under
// the same conditions rather than assembled from separate runs.
//
// Every entry validates. `ours` uses Parse, not Scan, because minio's
// ParseND and encoding/json's Unmarshal both check the whole document, and a
// comparison against a non-validating path would be measuring two different
// operations — see the gjson section of the README for what that costs.
func BenchmarkReadme(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		data := genDoc(n)
		s := string(data)

		b.Run(fmt.Sprintf("n=%d/stdlib", n), func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				var m map[string]any
				if err := json.Unmarshal(data, &m); err != nil {
					b.Fatal(err)
				}
				sinkF = m["meta"].(map[string]any)["total"].(float64)
			}
		})
		b.Run(fmt.Sprintf("n=%d/minio", n), func(b *testing.B) {
			if !msj.SupportedCPU() {
				b.Skip("no AVX2")
			}
			b.SetBytes(int64(len(data)))
			var reuse *msj.ParsedJson
			for b.Loop() {
				pj, err := msj.Parse(data, reuse)
				if err != nil {
					b.Fatal(err)
				}
				reuse = pj
				it := pj.Iter()
				it.Advance()
				e, err := it.FindElement(nil, "meta", "total")
				if err != nil {
					b.Fatal(err)
				}
				f, _ := e.Iter.Float()
				sinkF = f
			}
		})
		b.Run(fmt.Sprintf("n=%d/ours-Parse", n), func(b *testing.B) {
			var p ours.Parser
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				doc, err := p.Parse(data)
				if err != nil {
					b.Fatal(err)
				}
				sinkF = doc.Get("meta", "total").Float()
			}
		})
		b.Run(fmt.Sprintf("n=%d/ours-Scan", n), func(b *testing.B) {
			var p ours.Parser
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				doc, err := p.Scan(data)
				if err != nil {
					b.Fatal(err)
				}
				sinkF = doc.Get("meta", "total").Float()
			}
		})
		b.Run(fmt.Sprintf("n=%d/sonic", n), func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				var m map[string]any
				if err := sonic.Unmarshal(data, &m); err != nil {
					b.Fatal(err)
				}
				sinkF = m["meta"].(map[string]any)["total"].(float64)
			}
		})
		b.Run(fmt.Sprintf("n=%d/goccy", n), func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				var m map[string]any
				if err := gojson.Unmarshal(data, &m); err != nil {
					b.Fatal(err)
				}
				sinkF = m["meta"].(map[string]any)["total"].(float64)
			}
		})
		b.Run(fmt.Sprintf("n=%d/fastjson", n), func(b *testing.B) {
			var p fastjson.Parser
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				v, err := p.ParseBytes(data)
				if err != nil {
					b.Fatal(err)
				}
				sinkF = v.GetFloat64("meta", "total")
			}
		})
		b.Run(fmt.Sprintf("n=%d/jsonparser", n), func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				f, err := jsonparser.GetFloat(data, "meta", "total")
				if err != nil {
					b.Fatal(err)
				}
				sinkF = f
			}
		})
		b.Run(fmt.Sprintf("n=%d/gjson-Valid+Get", n), func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				if !gjson.Valid(s) {
					b.Fatal("invalid")
				}
				sinkF = gjson.Get(s, "meta.total").Float()
			}
		})
		b.Run(fmt.Sprintf("n=%d/gjson-Get", n), func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				sinkF = gjson.Get(s, "meta.total").Float()
			}
		})
	}
}
