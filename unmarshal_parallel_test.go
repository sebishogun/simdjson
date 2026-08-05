package simdjson

// The gate on parallel Unmarshal: identical values AND identical errors to the
// serial decode, forced onto small documents. Errors are literal by
// construction -- the parallel path reruns serial on any anomaly -- so what
// the differential really guards is the commit path: values, order, lengths,
// and that no anomaly is missed.

import (
	stdjson "encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

func forceUnmarshalParallel(t *testing.T) {
	t.Helper()
	oldMin, oldSeg, oldEl := parallelMinBytes, parallelSegBytes, unmarshalParallelMinElems
	parallelMinBytes, parallelSegBytes, unmarshalParallelMinElems = 1, 1, 8
	t.Cleanup(func() {
		parallelMinBytes, parallelSegBytes, unmarshalParallelMinElems = oldMin, oldSeg, oldEl
	})
}

type upItem struct {
	N    int64          `json:"n"`
	S    string         `json:"s"`
	F    float64        `json:"f"`
	B    bool           `json:"b"`
	Tags []string       `json:"tags"`
	Kid  *upItem        `json:"kid,omitempty"`
	M    map[string]int `json:"m,omitempty"`
}

func unmarshalBoth[T any](t *testing.T, name string, doc []byte) {
	t.Helper()
	var got []T
	gErr := Unmarshal(doc, &got)
	var want []T
	oldMin := parallelMinBytes
	parallelMinBytes = 1 << 62
	wErr := Unmarshal(doc, &want)
	parallelMinBytes = oldMin
	if (gErr == nil) != (wErr == nil) || (gErr != nil && gErr.Error() != wErr.Error()) {
		t.Fatalf("%s: parallel err %v, serial err %v", name, gErr, wErr)
	}
	if gErr == nil && !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: values differ (len %d vs %d)", name, len(got), len(want))
	}
	// And against the standard library, so both agree with the contract.
	var std []T
	sErr := stdjson.Unmarshal(doc, &std)
	if (gErr == nil) != (sErr == nil) {
		t.Fatalf("%s: ours err %v, stdlib err %v", name, gErr, sErr)
	}
	if gErr == nil && !reflect.DeepEqual(got, std) {
		t.Fatalf("%s: differs from stdlib", name)
	}
}

func TestUnmarshalParallelMatchesSerial(t *testing.T) {
	forceUnmarshalParallel(t)
	rng := rand.New(rand.NewSource(99))
	strs := []string{"plain", `qu"ote`, "new\nline", "unié 🙂", "", "back\\slash",
		strings.Repeat("long ", 40)}
	mk := func(n int, brk int) []byte {
		var b strings.Builder
		b.WriteByte('[')
		for i := 0; i < n; i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			if i == brk {
				b.WriteString(`{"n":01}`)
				continue
			}
			s := strs[rng.Intn(len(strs))]
			enc, _ := stdjson.Marshal(s)
			fmt.Fprintf(&b, `{"n":%d,"s":%s,"f":%g,"b":%v,"tags":["a",%s]`,
				rng.Int63()-rng.Int63(), enc, rng.NormFloat64(), i%2 == 0, enc)
			if i%7 == 0 {
				fmt.Fprintf(&b, `,"kid":{"n":1,"s":"k","f":0.5,"b":false,"tags":[]}`)
			}
			if i%5 == 0 {
				fmt.Fprintf(&b, `,"m":{"x":1,"y":2}`)
			}
			b.WriteByte('}')
		}
		b.WriteByte(']')
		return []byte(b.String())
	}
	for trial := 0; trial < 25; trial++ {
		n := 8 + rng.Intn(500)
		unmarshalBoth[upItem](t, fmt.Sprintf("clean %d", trial), mk(n, -1))
		unmarshalBoth[upItem](t, fmt.Sprintf("broken %d", trial), mk(n, rng.Intn(n)))
	}
	// The shapes that must decline or stay exact.
	cases := map[string]string{
		"trailing junk":  `[` + strings.Repeat(`{"n":1},`, 40) + `{"n":2}]x`,
		"leading junk":   `x[` + strings.Repeat(`{"n":1},`, 40) + `{"n":2}]`,
		"ws pretty":      "[\n  " + strings.Repeat("{\"n\":1} ,\n  ", 40) + `{"n":2}` + "\n]  ",
		"scalar mixed":   `[` + strings.Repeat(`{"n":1},`, 40) + `7]`,
		"missing comma":  `[` + strings.Repeat(`{"n":1},`, 20) + `{"n":2} {"n":3}` + strings.Repeat(`,{"n":1}`, 20) + `]`,
		"unknown fields": `[` + strings.Repeat(`{"n":1,"zz":"ignored","deep":{"a":[1,2]}},`, 40) + `{"n":2}]`,
		"empty elements": `[` + strings.Repeat(`{},`, 60) + `{}]`,
		"root object":    `{"k":[` + strings.Repeat(`{"n":1},`, 40) + `{"n":2}]}`,
	}
	for name, doc := range cases {
		unmarshalBoth[upItem](t, name, []byte(doc))
	}
	// A nil/preexisting destination slice must behave as serial does.
	doc := mk(60, -1)
	pre := make([]upItem, 3)
	preSerial := make([]upItem, 3)
	if err := Unmarshal(doc, &pre); err != nil {
		t.Fatal(err)
	}
	oldMin := parallelMinBytes
	parallelMinBytes = 1 << 62
	if err := Unmarshal(doc, &preSerial); err != nil {
		t.Fatal(err)
	}
	parallelMinBytes = oldMin
	if !reflect.DeepEqual(pre, preSerial) {
		t.Fatal("preexisting destination diverges from serial")
	}
}
