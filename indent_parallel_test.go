package simdjson

// Byte-identity with the serial Indent, boundaries forced everywhere: inside
// strings, mid-number, mid-whitespace, mid-pending (right after an opener),
// across prefix/indent variants including the empty indent.

import (
	"bytes"
	stdjson "encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

func indentBoth(t *testing.T, name string, doc []byte, prefix, indent string) {
	t.Helper()
	old := parallelMinBytes
	parallelMinBytes = 1
	var got bytes.Buffer
	gErr := Indent(&got, doc, prefix, indent)
	parallelMinBytes = 1 << 62
	var want bytes.Buffer
	wErr := Indent(&want, doc, prefix, indent)
	parallelMinBytes = old
	if (gErr == nil) != (wErr == nil) || (gErr != nil && gErr.Error() != wErr.Error()) {
		t.Fatalf("%s: parallel err %v, serial err %v", name, gErr, wErr)
	}
	if gErr != nil {
		return
	}
	if !bytes.Equal(got.Bytes(), want.Bytes()) {
		g, w := got.Bytes(), want.Bytes()
		i := 0
		for i < len(g) && i < len(w) && g[i] == w[i] {
			i++
		}
		t.Fatalf("%s (%q,%q): differs at %d of %d/%d:\n got %.80q\nwant %.80q",
			name, prefix, indent, i, len(g), len(w),
			string(g[max(0, i-20):min(i+60, len(g))]),
			string(w[max(0, i-20):min(i+60, len(w))]))
	}
	var std bytes.Buffer
	if err := stdjson.Indent(&std, doc, prefix, indent); err == nil &&
		!bytes.Equal(got.Bytes(), std.Bytes()) {
		t.Fatalf("%s: differs from stdlib", name)
	}
}

func TestIndentParallelMatchesSerial(t *testing.T) {
	rng := rand.New(rand.NewSource(17))
	els := []string{
		`{"a":1,"b":[1,2,{"c":"x"}]}`, `[]`, `{}`, `[[],{}]`,
		`{"s":"with \" and , and { inside"}`, `12345.678e-2`, `true`,
		`{"long":"` + strings.Repeat("y", 300) + `"}`,
		`[ 1 ,   2,{ "k" :"v" } ]`,
	}
	variants := []struct{ prefix, indent string }{
		{"", "  "}, {"", "\t"}, {">>", " "}, {"", ""}, {"p", ""},
	}
	for trial := 0; trial < 25; trial++ {
		var b strings.Builder
		b.WriteString("[")
		n := 30 + rng.Intn(2500)
		for i := 0; i < n; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(els[rng.Intn(len(els))])
		}
		b.WriteString("]")
		v := variants[trial%len(variants)]
		indentBoth(t, fmt.Sprintf("random %d", trial), []byte(b.String()), v.prefix, v.indent)
	}
	deep := strings.Repeat(`[`, 300) + `1` + strings.Repeat(`]`, 300)
	indentBoth(t, "deep", []byte(`[`+deep+`,`+deep+`]`), "", " ")
	indentBoth(t, "already pretty", []byte("[\n  {\n    \"a\": 1\n  },\n  2\n]\n  "), "", "  ")
	indentBoth(t, "trailing ws kept", []byte(`[1,2]`+"\n\t "), "", "  ")
}
