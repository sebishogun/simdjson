package simdjson

import (
	"fmt"
	"testing"
)

// BenchmarkBucketThreshold sweeps bucketMin itself, on whole sorts.
//
// An earlier version compared "counting at every level" against "branchy at
// every level" per n, and read a crossover out of it. That is not the choice
// the code makes: bucketMin applies to every group the recursion produces, so
// the sub-groups of a large sort take whichever path their own size selects.
// Sweeping the parameter measures the thing that is actually set.
func BenchmarkBucketThreshold(b *testing.B) {
	const k = 64
	for _, n := range []int{64, 128, 256, 1024} {
		ps := permSets(n, k)
		buf := make([]pair[int], n)
		for _, min := range []int{2, 8, 16, 32, 64, 96, 128, 256, 1 << 30} {
			name := fmt.Sprint(min)
			if min == 1<<30 {
				name = "never"
			}
			b.Run(fmt.Sprintf("n=%d/min=%s", n, name), func(b *testing.B) {
				oldT, oldG := bucketTotalMin, bucketGroupMin
				bucketTotalMin, bucketGroupMin = min, min
				defer func() { bucketTotalMin, bucketGroupMin = oldT, oldG }()
				i := 0
				for b.Loop() {
					copy(buf, ps[i%k])
					i++
					sortPairs(buf)
				}
			})
		}
	}
}
