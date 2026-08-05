package simdjson

// Byte-identity with the serial Compact, forced onto small documents so
// segment boundaries land everywhere: inside strings, inside whitespace runs,
// on kept-run edges.

import (
	"bytes"
	stdjson "encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

func compactBoth(t *testing.T, name string, doc []byte) {
	t.Helper()
	old := parallelMinBytes
	parallelMinBytes = 1
	var got bytes.Buffer
	gErr := Compact(&got, doc)
	parallelMinBytes = 1 << 62
	var want bytes.Buffer
	wErr := Compact(&want, doc)
	parallelMinBytes = old
	if (gErr == nil) != (wErr == nil) || (gErr != nil && gErr.Error() != wErr.Error()) {
		t.Fatalf("%s: parallel err %v, serial err %v", name, gErr, wErr)
	}
	if gErr == nil {
		if !bytes.Equal(got.Bytes(), want.Bytes()) {
			t.Fatalf("%s: outputs differ (len %d vs %d)", name, got.Len(), want.Len())
		}
		var std bytes.Buffer
		if err := stdjson.Compact(&std, doc); err == nil &&
			!bytes.Equal(got.Bytes(), std.Bytes()) {
			t.Fatalf("%s: differs from stdlib", name)
		}
	}
}

func TestCompactParallelMatchesSerial(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	atoms := []string{
		`{"k" : "v"}`, `[ 1 ,\n 2 ]`, `"a b\tc"`, `123`, `null`, `"  spaces  "`,
		`{ "s" : "with \" quote and \\ slash" }`, "\n\t ", `[[[ 5 ]]]`,
	}
	for trial := 0; trial < 40; trial++ {
		var b strings.Builder
		b.WriteString("[ ")
		n := 50 + rng.Intn(4000)
		for i := 0; i < n; i++ {
			if i > 0 {
				b.WriteString(" ,\n\t ")
			}
			a := atoms[rng.Intn(len(atoms))]
			a = strings.ReplaceAll(a, `\n`, "\n")
			if a == "\n\t " || a == "123" || a[0] == '"' {
				b.WriteString(`{"x":` + strings.TrimSpace(strings.ReplaceAll(a, "\n", " ")) + `}`)
			} else {
				b.WriteString(a)
			}
		}
		b.WriteString(" ]\n")
		compactBoth(t, fmt.Sprintf("random %d", trial), []byte(b.String()))
	}
	compactBoth(t, "no ws", []byte(`[`+strings.Repeat(`{"a":1},`, 5000)+`1]`))
	compactBoth(t, "all ws string", []byte(`{"k":"`+strings.Repeat(" \t", 8000)+`"}`))
	compactBoth(t, "pretty deep", []byte("[\n"+strings.Repeat("  [ 1 ,\n", 2000)+" 2"+strings.Repeat(" ]\n", 2000)+"]"))
}
