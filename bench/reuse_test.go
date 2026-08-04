package bench

import (
	"fmt"
	"testing"

	ours "github.com/sebishogun/simdjson"
	"github.com/tidwall/gjson"
)

// Parse once, then look things up many times. This is the shape an index is
// supposed to win: the cost is paid up front and amortised over the queries,
// where a lazy scanner pays again on every one.
func BenchmarkRepeatedLookups(b *testing.B) {
	const n = 10000
	data := genDoc(n)
	s := string(data)

	for _, q := range []int{1, 10, 100, 1000} {
		paths := make([]string, q)
		idx := make([]int, q)
		for i := range paths {
			j := (i * 7919) % n
			idx[i] = j
			paths[i] = fmt.Sprintf("items.%d.score", j)
		}

		b.Run(fmt.Sprintf("q=%d/gjson", q), func(b *testing.B) {
			for b.Loop() {
				var sum float64
				for _, p := range paths {
					sum += gjson.Get(s, p).Float()
				}
				sinkF = sum
			}
		})
		b.Run(fmt.Sprintf("q=%d/gjson-parsed", q), func(b *testing.B) {
			for b.Loop() {
				r := gjson.Parse(s)
				var sum float64
				for _, p := range paths {
					sum += r.Get(p).Float()
				}
				sinkF = sum
			}
		})
		b.Run(fmt.Sprintf("q=%d/ours", q), func(b *testing.B) {
			var p ours.Parser
			for b.Loop() {
				doc, _ := p.Scan(data)
				items := doc.Get("items")
				var sum float64
				for _, j := range idx {
					sum += items.Index(j).Key("score").Float()
				}
				sinkF = sum
			}
		})
	}
}
