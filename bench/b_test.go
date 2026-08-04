package bench

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/buger/jsonparser"
	"github.com/bytedance/sonic"
	gojson "github.com/goccy/go-json"
	msj "github.com/minio/simdjson-go"
	ours "github.com/sebishogun/simdjson"
	"github.com/tidwall/gjson"
	"github.com/valyala/fastjson"
)

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
		fmt.Fprintf(&sb, `{"id":%d,"name":"item-%d","score":%.4f,"active":%v,`, i, i, r.Float64(), i%2 == 0)
		fmt.Fprintf(&sb, `"tags":["a","b","c"],"nested":{"x":%d,"y":"%s"}}`, i*3, strings.Repeat("z", 8))
	}
	sb.WriteString(`]}`)
	return []byte(sb.String())
}

var sinkF float64

// Every library doing the same job: pull one nested field out of a document.
func BenchmarkGetField(b *testing.B) {
	for _, n := range []int{1000, 10000} {
		data := genDoc(n)
		s := string(data)

		b.Run(fmt.Sprintf("n=%d/gjson", n), func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				sinkF = gjson.Get(s, "meta.total").Float()
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
		b.Run(fmt.Sprintf("n=%d/minio", n), func(b *testing.B) {
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
		b.Run(fmt.Sprintf("n=%d/ours", n), func(b *testing.B) {
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
	}
}
