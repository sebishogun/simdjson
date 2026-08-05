package simdjson

import (
	"fmt"
	"math/rand"
	"testing"
)

func BenchmarkFloatSliceModes(b *testing.B) {
	rng := rand.New(rand.NewSource(3))
	for _, n := range []int{20000, 150000, 1000000} {
		vs := make([]float64, n)
		for i := range vs {
			vs[i] = rng.NormFloat64() * 1e6
		}
		out, _ := Marshal(vs)
		for _, mode := range []struct {
			name string
			min  int
		}{{"serial", 1 << 62}, {"parallel", 0}} {
			b.Run(fmt.Sprintf("n=%d/%s", n, mode.name), func(b *testing.B) {
				old := parallelMarshalMin
				if mode.min != 0 {
					parallelMarshalMin = mode.min
				}
				defer func() { parallelMarshalMin = old }()
				b.SetBytes(int64(len(out)))
				for b.Loop() {
					if _, err := Marshal(vs); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
