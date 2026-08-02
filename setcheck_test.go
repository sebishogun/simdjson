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
		ix, err := buildIndex(data, nil, true)
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

// noWS is set by validateStrings, which only Parse calls. A Parser reused for
// Scan after a Parse must not inherit the previous document's answer.
func TestReusedParserDoesNotCarryNoWhitespace(t *testing.T) {
	var p Parser
	// A document with no whitespace at all: this sets noWS.
	if _, err := p.Parse([]byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if !p.ix.noWS {
		t.Fatal("expected noWS on a document with no whitespace")
	}
	// Now Scan a document that does have whitespace, on the same Parser.
	d, err := p.Scan([]byte(`{"a" : [1, 2] , "b" : 3}`))
	if err != nil {
		t.Fatal(err)
	}
	if p.ix.noWS {
		t.Fatal("Scan inherited noWS from the previous Parse")
	}
	if got := d.Get("b").Float(); got != 3 {
		t.Fatalf(`Get("b") = %v, want 3 — whitespace was skipped wrongly`, got)
	}
	if got := d.Get("a").Index(1).Float(); got != 2 {
		t.Fatalf(`Get("a").Index(1) = %v, want 2`, got)
	}
}

// And the whitespace path itself has to work when whitespace is present.
func TestWhitespaceHeavyDocument(t *testing.T) {
	doc := "  {\n\t\"a\" :\r\n [ 1 , 2 ,\t3 ] ,\n \"b\" : { \"c\" : \"d\" }\n}  "
	d, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("Parse(%q): %v", doc, err)
	}
	if d.ix.noWS {
		t.Fatal("noWS set on a document full of whitespace")
	}
	if got := d.Get("a").Index(2).Float(); got != 3 {
		t.Fatalf("Index(2) = %v, want 3", got)
	}
	if got := d.Get("b", "c").String(); got != "d" {
		t.Fatalf(`Get("b","c") = %q, want "d"`, got)
	}
}

// validateValue is a copy of value()'s dispatch without the Value, so the two
// have to agree about where every value ends and about what is an error.
//
// A duplicate that drifts is worse than the branch it avoided: Parse would
// accept what navigation rejects, or step to a different offset.
func TestValidateValueMatchesValue(t *testing.T) {
	cases := []string{
		`1`, `-0`, `0`, `123`, `1.5`, `1e10`, `1E+10`, `-1.5e-10`, `0.0`,
		`01`, `1.`, `.1`, `1e`, `1e+`, `-`, `+1`, `1x`, `00`, `1.2.3`,
		`"a"`, `""`, `"\n"`, `"A"`, `"\q"`, `"a`, `"\"`,
		`true`, `false`, `null`, `tru`, `truex`, `nul`, `falsey`,
		`{}`, `[]`, `{"a":1}`, `[1,2]`, `{"a":}`, `[1,]`, `{`, `[`,
		`{"a":[1,{"b":"c"}]}`, ` 1`, `x`, ``,
	}
	for _, in := range cases {
		// Wrap in an array so the index is built the same way for both paths.
		data := []byte(in)
		ix, err := buildIndex(data, nil, true)
		if err != nil {
			continue // stage one rejected it; neither path is reached
		}
		d := &Doc{data: data, ix: ix, inStr: ix.inStr, noWS: ix.noWS, wsw: ix.wsw}
		vEnd, vErr := d.validateValue(0)
		_, wEnd, wErr := d.value(0)
		if (vErr == nil) != (wErr == nil) {
			t.Errorf("%q: validateValue err=%v, value err=%v", in, vErr, wErr)
			continue
		}
		if vErr == nil && vEnd != wEnd {
			t.Errorf("%q: validateValue ends at %d, value ends at %d", in, vEnd, wEnd)
		}
	}
}
