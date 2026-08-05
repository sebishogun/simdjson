package simdjson

// What is the most parallelism could buy on one document?
//
// Building a parallel structural index is real work: the quote mask depends on
// backslash runs that can straddle a chunk boundary, and the in-string parity
// is a prefix XOR over the whole document. Both are solvable -- the parity is a
// scan, which parallelises in a fan-in and a fan-out pass -- but the work only
// pays if the ceiling is high enough to be worth it.
//
// So the ceiling is measured before anything is built. Each goroutine scans its
// own slice of the document, which is NOT a correct parse -- a chunk boundary
// can fall inside a string, and every chunk here starts fresh with the parity
// it would have to be told. It is exactly the work a correct parallel index
// would do, minus the coordination, so it bounds what a correct one could
// reach.

import (
	"fmt"
	"runtime"
	"sync"
	"testing"
)

func BenchmarkParallelCeiling(b *testing.B) {
	for _, name := range []string{"twitter", "citm", "canada", "canada64MB"} {
		var data []byte
		if name == "canada64MB" {
			// The size the question is actually about. A 1 MB document is
			// entirely in L2 and its ceiling is set by goroutine overhead; a
			// document past last-level cache is set by memory bandwidth, and
			// those are different answers.
			one := gateCorpus(b, "canada")
			for len(data) < 64<<20 {
				data = append(data, one...)
			}
		} else {
			data = gateCorpus(b, name)
		}
		for _, n := range []int{1, 2, 4, 8, 16, 32} {
			if n > runtime.NumCPU() {
				continue
			}
			// Fixed split points, so every goroutine gets the same bytes every
			// iteration and the split cost is not in the measurement.
			chunks := make([][]byte, n)
			sz := len(data) / n
			for i := range chunks {
				lo := i * sz
				hi := lo + sz
				if i == n-1 {
					hi = len(data)
				}
				chunks[i] = data[lo:hi]
			}
			b.Run(fmt.Sprintf("%s/n=%d", name, n), func(b *testing.B) {
				b.SetBytes(int64(len(data)))
				ixs := make([]*index, n)
				var wg sync.WaitGroup
				for b.Loop() {
					wg.Add(n)
					for i := 0; i < n; i++ {
						go func(i int) {
							defer wg.Done()
							ix, _ := buildIndexMode(chunks[i], ixs[i], false, false, true)
							ixs[i] = ix
						}(i)
					}
					wg.Wait()
				}
			})
		}
	}
}
