package bench

// The generated-encoder row, and the agreement that makes it publishable.
//
// GSearch mirrors tSearch field for field and tag for tag, so the two marshal
// to identical bytes -- asserted here, against the reflect path AND against
// encoding/json, before the row is worth timing. The row is fair to publish
// next to sonic's: sonic's JIT emits per-type code at run time, structgen
// emits it at build time; both rows measure "the per-type code path".

import (
	"encoding/json"
	"testing"

	ours "github.com/sebishogun/simdjson"
)

func loadGSearch(tb testing.TB) GSearch {
	tb.Helper()
	var g GSearch
	if err := json.Unmarshal(loadCorpus(tb, "twitter"), &g); err != nil {
		tb.Fatal(err)
	}
	return g
}

func TestGeneratedRowMatches(t *testing.T) {
	data := loadCorpus(t, "twitter")
	var v tSearch
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	g := loadGSearch(t)

	gen, err := ours.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	refl, err := ours.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if string(gen) != string(refl) {
		t.Fatalf("generated differs from reflect:\n gen  %.200s\n refl %.200s", gen, refl)
	}
	std, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if string(gen) != string(std) {
		t.Fatalf("generated differs from encoding/json:\n gen %.200s\n std %.200s", gen, std)
	}
}

func BenchmarkMarshalStructGenerated(b *testing.B) {
	g := loadGSearch(b)
	out, err := ours.Marshal(g)
	if err != nil {
		b.Fatal(err)
	}
	b.Run("ours-generated", func(b *testing.B) {
		b.SetBytes(int64(len(out)))
		for b.Loop() {
			if _, err := ours.Marshal(g); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("ours-generated-MarshalTo", func(b *testing.B) {
		buf := make([]byte, 0, len(out))
		b.SetBytes(int64(len(out)))
		for b.Loop() {
			var err error
			buf, err = ours.MarshalTo(buf[:0], g)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}
