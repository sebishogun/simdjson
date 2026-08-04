package simdjson

import (
	"bytes"
	"runtime"
	"testing"
)

// One call over a long stream, which is what this is for. The per-call
// benchmark measures pool setup 139 times over and says nothing about whether
// the pool works.
func TestParallelSteadyStateAllocation(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a 100 MB stream")
	}
	data := []byte(ndStream(1500000))
	t.Logf("stream is %d MB", len(data)/(1<<20))

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	n := int64(0)
	if err := ForEachLineReaderParallel(bytes.NewReader(data), func(v Value) bool {
		n += v.Key("id").Int()
		return true
	}); err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	alloc := after.TotalAlloc - before.TotalAlloc
	t.Logf("one call over %d MB allocated %d MB total, %.2f bytes per input byte",
		len(data)/(1<<20), alloc/(1<<20), float64(alloc)/float64(len(data)))
}
