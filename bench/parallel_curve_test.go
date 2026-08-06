package bench

// Aggregate throughput under concurrency: every goroutine decodes its own
// twitter into its own struct -- the many-requests server shape sonic
// publishes -cpu curves for. (The other axis, ONE document sharded across
// cores, is the at-scale family in the README; nothing else has it.)

import (
	"encoding/json"
	"testing"

	sonic "github.com/bytedance/sonic"
	goccy "github.com/goccy/go-json"
	ours "github.com/sebishogun/simdjson"
)

func BenchmarkParallelTwitterStruct(b *testing.B) {
	data := loadCorpus(b, "twitter")
	libs := []struct {
		name string
		f    func([]byte, any) error
	}{
		{"ours", func(d []byte, v any) error { return ours.Unmarshal(d, v) }},
		{"sonic", func(d []byte, v any) error { return sonic.ConfigStd.Unmarshal(d, v) }},
		{"goccy", func(d []byte, v any) error { return goccy.Unmarshal(d, v) }},
		{"stdlib", func(d []byte, v any) error { return json.Unmarshal(d, v) }},
	}
	for _, l := range libs {
		b.Run(l.name, func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					var v tSearch
					if err := l.f(data, &v); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}
