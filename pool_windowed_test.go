package simdjson

// The regression for the window-aliasing bug: a pooled index that last
// served the whole builder, handed a document big enough for the windowed
// builder, validated garbage -- the five window masks were views into one
// backing being treated as independent buffers. The sequence below was
// deterministic once the GC was out of the way; with the carve unified it
// must agree with stdlib every time, in every order.

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPooledIndexWholeThenWindowed(t *testing.T) {
	small := []byte(strings.Repeat("[", 200) + "1" + strings.Repeat("]", 200))
	big := []byte(`{"s":"` + strings.Repeat("all work and no play makes a dull payload ", 100000) + `"}`)
	if len(big) <= wholeDocMax {
		t.Fatalf("fixture no longer exercises the windowed builder (len %d, wholeDocMax %d)", len(big), wholeDocMax)
	}
	for i := 0; i < 8; i++ {
		var v any
		if err := Unmarshal(small, &v); err != nil {
			t.Fatal(err)
		}
		if got, want := Valid(big), json.Valid(big); got != want {
			t.Fatalf("round %d: Valid ours=%v stdlib=%v", i, got, want)
		}
		var a, b any
		if (Unmarshal(big, &a) == nil) != (json.Unmarshal(big, &b) == nil) {
			t.Fatalf("round %d: Unmarshal disagreement", i)
		}
	}
}
