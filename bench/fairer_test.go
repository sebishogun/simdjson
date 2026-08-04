package bench

import (
	"testing"

	ours "github.com/sebishogun/simdjson"
	"github.com/tidwall/gjson"
)

// Does gjson notice a broken document at all?
func TestGjsonValidates(t *testing.T) {
	bad := []string{
		`{"a":1,}`, `{"a":}`, `{"a" 1}`, `{"a":01}`, `{"a":"\q"}`, `{"a":1`,
	}
	for _, in := range bad {
		got := gjson.Get(in, "a")
		_, ourErr := ours.Parse([]byte(in))
		t.Logf("%-14q gjson.Get -> %-8q exists=%-5v | gjson.Valid=%-5v | ours err=%v",
			in, got.String(), got.Exists(), gjson.Valid(in), ourErr != nil)
	}
}

// Like for like: both validating, then both not.
func BenchmarkEquivalentWork(b *testing.B) {
	const n = 10000
	data := genDoc(n)
	s := string(data)

	b.Run("validating/gjson-Valid+Get", func(b *testing.B) {
		for b.Loop() {
			if !gjson.Valid(s) {
				b.Fatal("invalid")
			}
			sinkF = gjson.Get(s, "meta.total").Float()
		}
	})
	b.Run("validating/ours-Parse+Get", func(b *testing.B) {
		var p ours.Parser
		for b.Loop() {
			doc, err := p.Parse(data)
			if err != nil {
				b.Fatal(err)
			}
			sinkF = doc.Get("meta", "total").Float()
		}
	})

	b.Run("unvalidated/gjson-Get", func(b *testing.B) {
		for b.Loop() {
			sinkF = gjson.Get(s, "meta.total").Float()
		}
	})
	b.Run("unvalidated/ours-Scan+Get", func(b *testing.B) {
		var p ours.Parser
		for b.Loop() {
			doc, _ := p.Scan(data)
			sinkF = doc.Get("meta", "total").Float()
		}
	})
}
