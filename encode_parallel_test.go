package simdjson

// The gate on parallel Marshal: byte-identity with the serial encode, errors
// included, across shapes and Options -- forced onto small values so every
// range boundary and worker count is exercised.

import (
	"bytes"
	stdjson "encoding/json"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"sync"
	"testing"
)

// forceParallelMarshal arms the path for any value and restores the tunables.
func forceParallelMarshal(t *testing.T) {
	t.Helper()
	oldMin, oldPer := parallelMarshalMin, parallelMarshalPerWork
	parallelMarshalMin, parallelMarshalPerWork = 1, 1
	t.Cleanup(func() { parallelMarshalMin, parallelMarshalPerWork = oldMin, oldPer })
}

// serialMarshal runs the same encode with the arm disarmed.
func serialMarshal(v any, o Options) ([]byte, error) {
	oldMin := parallelMarshalMin
	parallelMarshalMin = 1 << 62
	defer func() { parallelMarshalMin = oldMin }()
	return o.Marshal(v)
}

func compareMarshal(t *testing.T, name string, v any, o Options) {
	t.Helper()
	// Prime the pooled states' hints so the gate sees a size; the force
	// helper has already made any hint sufficient.
	got, gErr := o.Marshal(v)
	want, wErr := serialMarshal(v, o)
	if (gErr == nil) != (wErr == nil) || (gErr != nil && gErr.Error() != wErr.Error()) {
		t.Fatalf("%s: parallel err %v, serial err %v", name, gErr, wErr)
	}
	if !bytes.Equal(got, want) {
		i := 0
		for i < len(got) && i < len(want) && got[i] == want[i] {
			i++
		}
		lo := max(0, i-50)
		t.Fatalf("%s: differs at %d:\n par %.120q\n ser %.120q", name, i,
			string(got[lo:min(lo+120, len(got))]), string(want[lo:min(lo+120, len(want))]))
	}
}

func TestParallelMarshalMatchesSerial(t *testing.T) {
	forceParallelMarshal(t)
	rng := rand.New(rand.NewSource(31))
	strs := []string{"plain", `qu"ote`, "new\nline", "<html>&", "café é", "日本", "", "🙂",
		strings.Repeat("long ", 50)}
	// Two encodes of an UNSORTED map are not byte-identical even serial
	// against serial -- Go randomises map iteration per call -- so map-bearing
	// values are compared only under SortMapKeys, where identity is defined.
	anyVal := func(withMaps bool) any {
		k := rng.Intn(6)
		if !withMaps && k >= 4 {
			k = rng.Intn(4)
		}
		switch k {
		case 0:
			return strs[rng.Intn(len(strs))]
		case 1:
			return float64(rng.Intn(100000)) / 7
		case 2:
			return rng.Intn(2) == 0
		case 3:
			return nil
		case 4:
			return map[string]any{"a": strs[rng.Intn(len(strs))], "b": float64(rng.Intn(99))}
		default:
			return []any{float64(1), strs[rng.Intn(len(strs))], nil}
		}
	}
	opts := []struct {
		o        Options
		withMaps bool
	}{
		{Options{}, false},
		{Options{EscapeHTML: true}, false},
		{Options{EscapeHTML: true, ValidateStrings: true, SortMapKeys: true}, true},
	}
	for trial := 0; trial < 30; trial++ {
		n := 3 + rng.Intn(400)
		for oi, oc := range opts {
			vs := make([]any, n)
			for i := range vs {
				vs[i] = anyVal(oc.withMaps)
			}
			compareMarshal(t, fmt.Sprintf("slice %d opts %d", trial, oi), vs, oc.o)
			if oc.withMaps {
				doc := map[string]any{"statuses": vs, "meta": map[string]any{"count": float64(n)}}
				compareMarshal(t, fmt.Sprintf("doc %d opts %d", trial, oi), doc, oc.o)
			}
		}
	}
	// Typed slices at the top level take the reflect path.
	type item struct {
		N int    `json:"n"`
		S string `json:"s"`
	}
	items := make([]item, 137)
	for i := range items {
		items[i] = item{N: i, S: strs[i%len(strs)]}
	}
	compareMarshal(t, "typed slice", items, Std)
	compareMarshal(t, "typed nested", map[string]any{"items": any(nil), "s": "x"}, Std)
	// Registered/generated encoders inside a sharded range.
	regs := make([]registered, 8)
	for i := range regs {
		regs[i] = registered(loadFSearch(t))
	}
	compareMarshal(t, "registered elements", regs, Std)
}

func TestParallelMarshalErrors(t *testing.T) {
	forceParallelMarshal(t)
	bad := math.NaN()
	for _, pos := range []int{0, 3, 97, 199} {
		vs := make([]any, 200)
		for i := range vs {
			vs[i] = float64(i)
		}
		vs[pos] = bad
		got, gErr := Std.Marshal(vs)
		want, wErr := serialMarshal(vs, Std)
		if gErr == nil || wErr == nil {
			t.Fatalf("pos %d: NaN accepted: par %v ser %v", pos, gErr, wErr)
		}
		if gErr.Error() != wErr.Error() {
			t.Fatalf("pos %d: parallel err %q, serial %q", pos, gErr, wErr)
		}
		_, _ = got, want
	}
}

// []byte must never shard: it is base64, not a sequence.
func TestParallelMarshalByteSlice(t *testing.T) {
	forceParallelMarshal(t)
	b := bytes.Repeat([]byte{1, 2, 3, 250}, 300)
	got, err := Std.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := stdjson.Marshal(b)
	if !bytes.Equal(got, want) {
		t.Fatalf("byte slice:\n got %.80s\nwant %.80s", got, want)
	}
}

// TestConcurrentFirstEncode is the regression test for the encoder-cache
// waiter. The previous stub resolved itself through a sync.Once, and a second
// goroutine running the stub while the cache still held it pinned the stub to
// itself -- a permanent infinite recursion. The type here is declared inside
// the test so every `go test` process meets it fresh, and the barrier makes
// all goroutines hit encoderFor before any compile can finish.
func TestConcurrentFirstEncode(t *testing.T) {
	type fresh struct {
		A int     `json:"a"`
		B string  `json:"b"`
		C []fresh `json:"c"` // recursive, so the waiter path is embedded too
	}
	v := fresh{A: 1, B: "x", C: []fresh{{A: 2, B: "y"}}}
	want, err := serialMarshal(v, Std)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			got, err := Std.Marshal(v)
			if err != nil || !bytes.Equal(got, want) {
				t.Errorf("concurrent first encode: %v %s", err, got)
			}
		}()
	}
	close(start)
	wg.Wait()
}

// The payoff, reproducible: the decoded document through the public Marshal,
// arm off against arm on. Not a gate benchmark -- the gate corpora stay
// below the threshold by design -- but the row the README quotes.
func BenchmarkParallelMarshal(b *testing.B) {
	data := gateCorpus(b, "twitter")
	var doc map[string]any
	if err := stdjson.Unmarshal(data, &doc); err != nil {
		b.Fatal(err)
	}
	out, err := Marshal(doc)
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(out)))
	for _, mode := range []struct {
		name string
		min  int
	}{{"serial", 1 << 62}, {"parallel", 0}} {
		b.Run(mode.name, func(b *testing.B) {
			old := parallelMarshalMin
			if mode.min != 0 {
				parallelMarshalMin = mode.min
			}
			defer func() { parallelMarshalMin = old }()
			for b.Loop() {
				if _, err := Marshal(doc); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
