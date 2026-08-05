package gentest

import (
	"bytes"
	stdjson "encoding/json"
	"fmt"
	"math"
	"math/rand"
	"testing"

	"github.com/sebishogun/simdjson"
)

func TestGeneratedMatchesReflect(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	strs := []string{
		"", "plain", `has "quotes"`, "has\nnewline", "<html>&</html>",
		"café", "日本語", "🙂", "\xff invalid", "tab\there", "\x00control",
	}
	pick := func() string { return strs[rng.Intn(len(strs))] }

	var vals []Outer
	for i := 0; i < 400; i++ {
		o := Outer{
			ID: rng.Int63() - rng.Int63(), Name: pick(), Ok: i%2 == 0,
			Ratio: rng.NormFloat64() * math.Pow(10, float64(rng.Intn(12)-6)),
			Small: int32(rng.Int31() - rng.Int31()), U: uint16(rng.Intn(1 << 16)),
			Kid:     Inner{N: rng.Intn(1000) - 500, S: pick()},
			Skipped: pick(),
		}
		for j := rng.Intn(4); j > 0; j-- {
			o.Kids = append(o.Kids, Inner{N: rng.Intn(100), S: pick()})
		}
		for j := rng.Intn(4); j > 0; j-- {
			o.Words = append(o.Words, pick())
		}
		vals = append(vals, o)
	}
	// The awkward floats explicitly, not left to chance.
	vals = append(vals,
		Outer{Ratio: 0}, Outer{Ratio: -0}, Outer{Ratio: 1e21}, Outer{Ratio: 1e-7},
		Outer{Ratio: math.MaxFloat64}, Outer{Ratio: math.SmallestNonzeroFloat64},
		Outer{Kids: []Inner{}}, Outer{Words: []string{}},
	)

	opts := []simdjson.Options{
		{},
		{EscapeHTML: true},
		{ValidateStrings: true},
		{EscapeHTML: true, ValidateStrings: true},
		{EscapeHTML: true, ValidateStrings: true, SortMapKeys: true},
	}
	for oi, o := range opts {
		for i, v := range vals {
			p := Plain(v)
			got, err := o.Marshal(v)
			if err != nil {
				t.Fatalf("opts %d value %d: generated: %v", oi, i, err)
			}
			want, err := o.Marshal(p)
			if err != nil {
				t.Fatalf("opts %d value %d: reflect: %v", oi, i, err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("opts %+v value %d:\n gen %s\nrefl %s", o, i, got, want)
			}
		}
	}

	// And the default surface against encoding/json, which is the contract
	// users actually rely on.
	for i, v := range vals {
		got, err := simdjson.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		want, err := stdjson.Marshal(Plain(v))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("value %d:\n gen %s\n std %s", i, got, want)
		}
	}
}

func TestGeneratedNested(t *testing.T) {
	// Registered encoders must be picked up wherever the type appears, not
	// only at the top level.
	v := []Outer{{ID: 1, Name: "a"}, {ID: 2, Name: "b"}}
	got, err := simdjson.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var p []Plain
	for _, x := range v {
		p = append(p, Plain(x))
	}
	want, err := stdjson.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("in a slice:\n gen %s\n std %s", got, want)
	}
	m := map[string]Outer{"k": {ID: 3, Name: "c"}}
	got, err = simdjson.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := stdjson.Marshal(map[string]Plain{"k": Plain(m["k"])}); !bytes.Equal(got, want) {
		t.Fatalf("in a map:\n gen %s\n std %s", got, want)
	}
	fmt.Fprint(&bytes.Buffer{}, "")
}

// TestDeclinedTypesAreDeclined: the generator must refuse what it cannot do
// exactly, and a type it refuses keeps the reflect encoder. If it ever starts
// accepting one of these, the differential above has to grow to cover it
// first -- accepting silently is how wrong JSON ships.
func TestDeclinedTypeStillEncodes(t *testing.T) {
	v := Declined{M: map[string]int{"a": 1}, P: &Inner{N: 1, S: "x"}, Any: 3, Omi: "", B: []byte("hi")}
	got, err := simdjson.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	want, err := stdjson.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("declined type:\n got %s\nwant %s", got, want)
	}
}

func BenchmarkGeneratedVsReflect(b *testing.B) {
	v := Outer{ID: 12345, Name: "a name with some length to it", Ok: true,
		Ratio: 1.5, Small: -7, U: 42, Kid: Inner{N: 3, S: "kid"},
		Kids:  []Inner{{N: 1, S: "one"}, {N: 2, S: "two"}, {N: 3, S: "three"}},
		Words: []string{"alpha", "beta", "gamma", "delta"}}
	p := Plain(v)
	buf := make([]byte, 0, 4096)
	b.Run("generated", func(b *testing.B) {
		for b.Loop() {
			var err error
			if buf, err = simdjson.MarshalTo(buf[:0], v); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("reflect", func(b *testing.B) {
		for b.Loop() {
			var err error
			if buf, err = simdjson.MarshalTo(buf[:0], p); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("stdlib", func(b *testing.B) {
		for b.Loop() {
			if _, err := stdjson.Marshal(p); err != nil {
				b.Fatal(err)
			}
		}
	})
}
