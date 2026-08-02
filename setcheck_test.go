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
	// The four brackets are what matchBracket needs, and matchBracket is the
	// only consumer of the index. Colons and commas are deliberately absent —
	// see the comment on structSet.
	for _, c := range []byte{'{', '}', '[', ']'} {
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

// Every bracket outside a string, and nothing else, ends up in the index.
//
// This is the property the whole of stage two rests on: matchBracket does a
// binary search for an offset and expects to find it. A bracket inside a string
// appearing here would pair with the wrong partner and corrupt navigation.
func TestIndexHoldsExactlyTheBracketsOutsideStrings(t *testing.T) {
	docs := []string{
		`{"a":[1,2],"b":{"c":[]}}`,
		`{"a":"}{[]","b":[1]}`,
		`{"a":"\"}{","b":{}}`,
		`[[[[]]]]`,
		`{"k":"` + string(make([]byte, 0)) + `"}`,
	}
	for _, doc := range docs {
		data := []byte(doc)
		ix, err := buildIndex(data, nil)
		if err != nil {
			t.Fatalf("%s: %v", doc, err)
		}
		// Reference: walk the bytes tracking string state by hand.
		var want []int32
		inStr, esc := false, false
		for i := 0; i < len(data); i++ {
			c := data[i]
			switch {
			case esc:
				esc = false
			case c == '\\' && inStr:
				esc = true
			case c == '"':
				inStr = !inStr
			case !inStr && (c == '{' || c == '}' || c == '[' || c == ']'):
				want = append(want, int32(i))
			}
		}
		if len(ix.pos) != len(want) {
			t.Fatalf("%s: index has %d brackets %v, want %d %v",
				doc, len(ix.pos), ix.pos, len(want), want)
		}
		for k := range want {
			if ix.pos[k] != want[k] {
				t.Fatalf("%s: index[%d] = %d, want %d", doc, k, ix.pos[k], want[k])
			}
		}
		// And every pair points at its partner, both ways.
		for k := range ix.pos {
			m := ix.match[k]
			if int(m) < 0 || int(m) >= len(ix.pos) || ix.match[m] != int32(k) {
				t.Fatalf("%s: bracket %d at %d pairs with %d, which does not pair back",
					doc, k, ix.pos[k], m)
			}
		}
	}
}
