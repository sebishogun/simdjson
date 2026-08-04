package bench

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/bytedance/sonic"
	gojson "github.com/goccy/go-json"
	msj "github.com/minio/simdjson-go"
	ours "github.com/sebishogun/simdjson"
	"github.com/tidwall/gjson"
	"github.com/valyala/fastjson"
)

func BenchmarkCorpus(b *testing.B) {
	for _, name := range corpus {
		data := loadCorpus(b, name)
		s := string(data)

		b.Run(fmt.Sprintf("%s/ours-Parse", name), func(b *testing.B) {
			var p ours.Parser
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				if _, err := p.Parse(data); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("%s/ours-Scan", name), func(b *testing.B) {
			var p ours.Parser
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				if _, err := p.Scan(data); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("%s/fastjson", name), func(b *testing.B) {
			var p fastjson.Parser
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				if _, err := p.ParseBytes(data); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("%s/minio", name), func(b *testing.B) {
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
		b.Run(fmt.Sprintf("%s/gjson-Valid", name), func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				if !gjson.Valid(s) {
					b.Fatal("invalid")
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
