package simdjson

import "testing"

// simd.MaskBitsAny takes at most eight bytes, packed one per byte of a word.
// A longer set is not an error there — it silently falls back to the portable
// path, which is correct and several times slower. That happened once, when an
// escaping mistake made a set nine characters instead of eight, and it showed
// up only as a performance regression that looked like something else.
func TestStructSetFitsOnePass(t *testing.T) {
	if len(structSet) > 8 {
		t.Fatalf("structSet is %d bytes (%q); more than eight sends MaskBitsAny "+
			"to the portable path", len(structSet), structSet)
	}
	for _, c := range []byte{'{', '}', '[', ']', ':', ','} {
		found := false
		for i := 0; i < len(structSet); i++ {
			if structSet[i] == c {
				found = true
			}
		}
		if !found {
			t.Errorf("structSet is missing %q", c)
		}
	}
}
