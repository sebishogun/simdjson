package bench

import (
	"fmt"
	"testing"

	ours "github.com/sebishogun/simdjson"
	"github.com/tidwall/gjson"
)

// What each side actually does for one read of the whole document.
//
// gjson.Valid walks the bytes with a switch and returns a bool — it records
// nothing and allocates nothing. Scan walks them with vector instructions and
// materialises a structural index. Parse adds a grammar walk on top of that
// index. These are three different amounts of work and the point is to see how
// much each one costs.
func BenchmarkWhy(b *testing.B) {
	const n = 10000
	data := genDoc(n)
	s := string(data)
	b.Logf("document: %d bytes", len(data))

	b.Run("gjson.Valid (scalar, keeps nothing)", func(b *testing.B) {
		b.SetBytes(int64(len(data)))
		for b.Loop() {
			if !gjson.Valid(s) {
				b.Fatal("invalid")
			}
		}
	})
	b.Run("ours.Scan (vector, builds an index)", func(b *testing.B) {
		var p ours.Parser
		b.SetBytes(int64(len(data)))
		for b.Loop() {
			if _, err := p.Scan(data); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("ours.Parse (index + grammar walk)", func(b *testing.B) {
		var p ours.Parser
		b.SetBytes(int64(len(data)))
		for b.Loop() {
			if _, err := p.Parse(data); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("gjson.Get alone (stops at first match)", func(b *testing.B) {
		b.SetBytes(int64(len(data)))
		for b.Loop() {
			sinkF = gjson.Get(s, "meta.total").Float()
		}
	})

	// The case an index is supposed to win: many queries against one document.
	for _, q := range []int{1, 10, 100, 1000} {
		b.Run(fmt.Sprintf("q=%d/gjson-rescans", q), func(b *testing.B) {
			for b.Loop() {
				for i := 0; i < q; i++ {
					sinkF = gjson.Get(s, fmt.Sprintf("items.%d.score", i%n)).Float()
				}
			}
		})
		b.Run(fmt.Sprintf("q=%d/ours-index-once", q), func(b *testing.B) {
			var p ours.Parser
			for b.Loop() {
				doc, err := p.Scan(data)
				if err != nil {
					b.Fatal(err)
				}
				items := doc.Get("items")
				for i := 0; i < q; i++ {
					sinkF = items.Index(i % n).Key("score").Float()
				}
			}
		})
	}
}
