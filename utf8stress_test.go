package simdjson

// Kuhn's UTF-8 stress classes, embedded in JSON strings: overlong encodings,
// truncated sequences, stray continuations, encoded surrogates, out-of-range
// code points, and the valid boundary characters. encoding/json replaces the
// malformed with U+FFFD during decode and accepts them in Valid; the bar is
// byte-for-byte agreement on the decoded string and agreement on Valid.

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestUTF8StressAgreesWithStdlib(t *testing.T) {
	cases := [][]byte{
		// Valid boundaries.
		{0x7F}, {0xC2, 0x80}, {0xDF, 0xBF}, {0xE0, 0xA0, 0x80},
		{0xEF, 0xBF, 0xBF}, {0xF0, 0x90, 0x80, 0x80}, {0xF4, 0x8F, 0xBF, 0xBF},
		// Stray continuations.
		{0x80}, {0xBF}, {0x80, 0xBF}, {0x80, 0x80, 0x80, 0x80},
		// Lonely leads and truncated sequences.
		{0xC2}, {0xE1}, {0xE1, 0x80}, {0xF1}, {0xF1, 0x80}, {0xF1, 0x80, 0x80},
		{0xC2, 0x41}, {0xE1, 0x80, 0x41}, {0xF1, 0x80, 0x80, 0x41},
		// Overlongs.
		{0xC0, 0x80}, {0xC1, 0xBF}, {0xE0, 0x80, 0x80}, {0xE0, 0x9F, 0xBF},
		{0xF0, 0x80, 0x80, 0x80}, {0xF0, 0x8F, 0xBF, 0xBF},
		// Encoded surrogates.
		{0xED, 0xA0, 0x80}, {0xED, 0xAF, 0xBF}, {0xED, 0xB0, 0x80}, {0xED, 0xBF, 0xBF},
		// Out of range and impossible bytes.
		{0xF4, 0x90, 0x80, 0x80}, {0xF5, 0x80, 0x80, 0x80}, {0xFE}, {0xFF},
		{0xFE, 0xFE, 0xFF, 0xFF},
	}
	for i, raw := range cases {
		for _, tmpl := range []string{`{"k":"A%sB"}`, `{"k":"%s"}`, `{"%s":1}`, `["%s","x"]`} {
			doc := []byte(fmt.Sprintf(tmpl, raw))
			if sv, ov := json.Valid(doc), Valid(doc); sv != ov {
				t.Errorf("case %d tmpl %q: Valid ours=%v stdlib=%v (bytes % x)", i, tmpl, ov, sv, raw)
				continue
			}
			var oa, sa any
			oerr := Unmarshal(doc, &oa)
			serr := json.Unmarshal(doc, &sa)
			if (oerr == nil) != (serr == nil) {
				t.Errorf("case %d tmpl %q: err ours=%v stdlib=%v", i, tmpl, oerr, serr)
				continue
			}
			if oerr == nil {
				of, _ := json.Marshal(oa)
				sf, _ := json.Marshal(sa)
				if string(of) != string(sf) {
					t.Errorf("case %d tmpl %q (bytes % x): decode differs:\n ours %s\n std  %s",
						i, tmpl, raw, of, sf)
				}
			}
		}
	}
}
