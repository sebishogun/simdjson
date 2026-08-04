package bench

import (
	"encoding/json"
	"fmt"
	"testing"

	gojson "github.com/goccy/go-json"
	msj "github.com/minio/simdjson-go"
	ours "github.com/sebishogun/simdjson"
	"github.com/valyala/fastjson"
)

// The sizes real JSON actually comes in.
//
// Everything else here is measured between 631 KB and 2.25 MB, which is the
// size a parser benchmark reaches for and not the size a service sees. An API
// response is a few hundred bytes to a few tens of kilobytes, and at that size
// a structural index has to pay for its fixed costs — five vector passes, the
// buffers, the pool — out of far less document.
var smallDocs = map[string]string{
	"tiny-64B":   `{"ok":true,"id":12345,"name":"a"}`,
	"small-200B": `{"id":"9f8e7d6c","ts":1699999999,"lvl":"info","msg":"request completed","dur_ms":12.5,"ok":true,"tags":["a","b"]}`,
}

func init() {
	// A ~2 KB config-shaped document and a ~20 KB list, built once.
	cfg := `{"server":{"host":"0.0.0.0","port":8080,"tls":{"cert":"/etc/c.pem","key":"/etc/k.pem","min":"1.2"}},` +
		`"limits":{"rps":1000,"burst":50,"timeout_ms":2500},"features":{`
	for i := 0; i < 40; i++ {
		if i > 0 {
			cfg += ","
		}
		cfg += fmt.Sprintf(`"feature_%d":%v`, i, i%2 == 0)
	}
	cfg += `},"upstreams":[`
	for i := 0; i < 12; i++ {
		if i > 0 {
			cfg += ","
		}
		cfg += fmt.Sprintf(`{"name":"svc-%d","url":"https://svc-%d.internal:8443","weight":%d}`, i, i, i*10)
	}
	cfg += `]}`
	smallDocs["config-2KB"] = cfg

	list := `{"page":1,"total":200,"items":[`
	for i := 0; i < 200; i++ {
		if i > 0 {
			list += ","
		}
		list += fmt.Sprintf(`{"id":%d,"sku":"SKU-%06d","price":%d.99,"in_stock":%v}`, i, i, i%97, i%3 != 0)
	}
	list += `]}`
	smallDocs["list-20KB"] = list
}

func BenchmarkSmall(b *testing.B) {
	names := []string{"tiny-64B", "small-200B", "config-2KB", "list-20KB"}
	for _, name := range names {
		data := []byte(smallDocs[name])
		b.Run(name+"/ours-Parse", func(b *testing.B) {
			var p ours.Parser
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				if _, err := p.Parse(data); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(name+"/fastjson", func(b *testing.B) {
			var p fastjson.Parser
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				if _, err := p.ParseBytes(data); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(name+"/goccy-Valid", func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				if !gojson.Valid(data) {
					b.Fatal("invalid")
				}
			}
		})
		b.Run(name+"/stdlib-Valid", func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				if !json.Valid(data) {
					b.Fatal("invalid")
				}
			}
		})
		b.Run(name+"/minio", func(b *testing.B) {
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
			}
		})
	}
}
