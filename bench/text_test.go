package bench

import (
	"bytes"
	stdjson "encoding/json"
	"testing"

	sonic "github.com/bytedance/sonic"
	goccy "github.com/goccy/go-json"
	ours "github.com/sebishogun/simdjson"
)

func BenchmarkTextOps(b *testing.B) {
	// The full shape corpus: Compact and Indent had only ever been measured
	// on the three classics, and five of this session's wins came from
	// shapes no table had measured.
	for _, name := range shapeNames {
		src := loadCorpus(b, name)
		b.Run(name+"/Valid/ours", func(b *testing.B) {
			b.SetBytes(int64(len(src)))
			for i := 0; i < b.N; i++ {
				if !ours.Valid(src) {
					b.Fatal("invalid")
				}
			}
		})
		b.Run(name+"/Valid/stdlib", func(b *testing.B) {
			b.SetBytes(int64(len(src)))
			for i := 0; i < b.N; i++ {
				if !stdjson.Valid(src) {
					b.Fatal("invalid")
				}
			}
		})
		b.Run(name+"/Valid/goccy", func(b *testing.B) {
			b.SetBytes(int64(len(src)))
			for i := 0; i < b.N; i++ {
				if !goccy.Valid(src) {
					b.Fatal("invalid")
				}
			}
		})
		b.Run(name+"/Valid/sonic", func(b *testing.B) {
			b.SetBytes(int64(len(src)))
			for i := 0; i < b.N; i++ {
				if !sonic.Valid(src) {
					b.Fatal("invalid")
				}
			}
		})

		var buf bytes.Buffer
		b.Run(name+"/Compact/ours", func(b *testing.B) {
			b.SetBytes(int64(len(src)))
			for i := 0; i < b.N; i++ {
				buf.Reset()
				if err := ours.Compact(&buf, src); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(name+"/Compact/stdlib", func(b *testing.B) {
			b.SetBytes(int64(len(src)))
			for i := 0; i < b.N; i++ {
				buf.Reset()
				if err := stdjson.Compact(&buf, src); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(name+"/Indent/ours", func(b *testing.B) {
			b.SetBytes(int64(len(src)))
			for i := 0; i < b.N; i++ {
				buf.Reset()
				if err := ours.Indent(&buf, src, "", "  "); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(name+"/Indent/stdlib", func(b *testing.B) {
			b.SetBytes(int64(len(src)))
			for i := 0; i < b.N; i++ {
				buf.Reset()
				if err := stdjson.Indent(&buf, src, "", "  "); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
