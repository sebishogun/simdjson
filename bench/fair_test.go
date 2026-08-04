package bench

import (
	"fmt"
	"testing"

	"github.com/buger/jsonparser"
	ours "github.com/sebishogun/simdjson"
	"github.com/tidwall/gjson"
)

// Three shapes of access, because "fastest JSON library" is not a single
// number and the previous benchmark was measuring one library building an index
// against another reading thirty bytes.
//
//	early — one field at the front of the document
//	late  — one field at the very back
//	many  — every item's score, which is the whole document
//
// A lazy scanner wins "early" by not working. An index wins "many" by working
// once. "late" is where a lazy scanner has to read everything anyway.
func BenchmarkAccessShape(b *testing.B) {
	const n = 10000
	data := genDoc(n)
	s := string(data)
	lastPath := fmt.Sprintf("items.%d.nested.x", n-1)
	lastKeys := []string{"items", fmt.Sprint(n - 1), "nested", "x"}

	b.Run("early/gjson", func(b *testing.B) {
		for b.Loop() {
			sinkF = gjson.Get(s, "meta.total").Float()
		}
	})
	b.Run("early/jsonparser", func(b *testing.B) {
		for b.Loop() {
			f, _ := jsonparser.GetFloat(data, "meta", "total")
			sinkF = f
		}
	})
	b.Run("early/ours", func(b *testing.B) {
		var p ours.Parser
		for b.Loop() {
			doc, _ := p.Scan(data)
			sinkF = doc.Get("meta", "total").Float()
		}
	})

	b.Run("late/gjson", func(b *testing.B) {
		for b.Loop() {
			sinkF = gjson.Get(s, lastPath).Float()
		}
	})
	b.Run("late/jsonparser", func(b *testing.B) {
		for b.Loop() {
			f, _ := jsonparser.GetFloat(data, lastKeys...)
			sinkF = f
		}
	})
	b.Run("late/ours", func(b *testing.B) {
		var p ours.Parser
		for b.Loop() {
			doc, _ := p.Scan(data)
			sinkF = doc.Get("items").Index(n - 1).Key("nested").Key("x").Float()
		}
	})

	b.Run("many/gjson", func(b *testing.B) {
		for b.Loop() {
			var sum float64
			gjson.Get(s, "items").ForEach(func(_, v gjson.Result) bool {
				sum += v.Get("score").Float()
				return true
			})
			sinkF = sum
		}
	})
	b.Run("many/jsonparser", func(b *testing.B) {
		for b.Loop() {
			var sum float64
			jsonparser.ArrayEach(data, func(v []byte, _ jsonparser.ValueType, _ int, _ error) {
				f, _ := jsonparser.GetFloat(v, "score")
				sum += f
			}, "items")
			sinkF = sum
		}
	})
	b.Run("many/ours", func(b *testing.B) {
		var p ours.Parser
		for b.Loop() {
			doc, _ := p.Scan(data)
			var sum float64
			doc.Get("items").ForEach(func(v ours.Value) bool {
				sum += v.Key("score").Float()
				return true
			})
			sinkF = sum
		}
	})
}
