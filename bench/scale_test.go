package bench

import (
	"encoding/json"
	"fmt"
	"runtime"
	"testing"

	gojson "github.com/goccy/go-json"
	ours "github.com/sebishogun/simdjson"
)

// Throughput against document size. A structural index is memory proportional
// to the input, so the question this answers is where — if anywhere — it stops
// fitting and the approach falls off. Anything measured only at a megabyte is
// measured entirely inside cache.
func BenchmarkScale(b *testing.B) {
	for _, sz := range scaleSizes {
		data := scaleDoc(b, sz)
		name := fmt.Sprintf("%dMB", len(data)>>20)
		if len(data)>>20 == 0 {
			name = "1MB"
		}
		b.Run(name+"/ours-Parse", func(b *testing.B) {
			var p ours.Parser
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				if _, err := p.Parse(data); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(name+"/ours-Scan", func(b *testing.B) {
			var p ours.Parser
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				if _, err := p.Scan(data); err != nil {
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
	}
}

// Peak memory, which is the cost a structural index actually carries and which
// no throughput number shows.
func TestScaleMemory(t *testing.T) {
	for _, sz := range scaleSizes {
		data := scaleDoc(t, sz)
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)

		var p ours.Parser
		d, err := p.Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		runtime.ReadMemStats(&after)
		_ = d
		t.Logf("%6d MB document -> %6d MB retained by the index (%.2fx)",
			len(data)>>20,
			int(after.HeapAlloc-before.HeapAlloc)>>20,
			float64(after.HeapAlloc-before.HeapAlloc)/float64(len(data)))
		runtime.KeepAlive(d)
	}
}
