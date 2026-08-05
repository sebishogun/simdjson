package simdjson

// ValidateStrings has to work on its own.
//
// appendQuotedOpts routes to the fused path only when EscapeHTML and
// ValidateStrings are BOTH set, and every other combination took a generic loop
// that never looked at UTF-8. So Options{ValidateStrings: true} wrote an
// undecodable byte straight through -- not a missing replacement but invalid
// JSON, out of the one option whose entire purpose is to prevent that. The
// option's own documentation and the comment on appendQuotedOpts both said the
// two dimensions were independent; only the code disagreed.

import (
	"bytes"
	stdjson "encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestValidateStringsIsIndependentOfEscapeHTML(t *testing.T) {
	inputs := []string{
		string([]byte{'a', 0xff, 'b'}),             // lone invalid byte
		string([]byte{0xc2}),                       // truncated two-byte lead
		string([]byte{0xe0, 0xa0}),                 // truncated three-byte
		string([]byte{'x', 0xed, 0xa0, 0x80, 'y'}), // surrogate
		string([]byte{0xf4, 0x90, 0x80, 0x80}),     // above U+10FFFF
		"clean ascii",
		"日本語",
		"quote\" back\\ tab\t",
		"<script>&</script>",
		strings.Repeat("a", 200) + string([]byte{0xff}),
	}
	for _, in := range inputs {
		withHTML, err := Options{EscapeHTML: true, ValidateStrings: true}.Marshal(in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		noHTML, err := Options{ValidateStrings: true}.Marshal(in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}

		// Both must be valid UTF-8 and therefore valid JSON, whatever went in.
		for name, got := range map[string][]byte{"with EscapeHTML": withHTML, "without": noHTML} {
			if !utf8.Valid(got) {
				t.Errorf("%q %s: output is not valid UTF-8: %q", in, name, got)
			}
			if bytes.ContainsRune(got, 0xfffd) {
				t.Errorf("%q %s: wrote the replacement rune raw; encoding/json escapes it: %q", in, name, got)
			}
		}

		// And they must agree except on the HTML characters, which is the only
		// dimension EscapeHTML is allowed to change.
		if !strings.ContainsAny(in, "<>&") {
			if !bytes.Equal(withHTML, noHTML) {
				t.Errorf("%q: EscapeHTML changed the output of a string with no HTML characters:\n with %q\n  w/o %q",
					in, withHTML, noHTML)
			}
		}

		// With both on, byte-identical to encoding/json.
		std, err := stdjson.Marshal(in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if !bytes.Equal(withHTML, std) {
			t.Errorf("%q: differs from encoding/json:\n ours %q\n  std %q", in, withHTML, std)
		}
	}
}

// Off, invalid bytes go through untouched, which is what the option documents.
func TestValidateStringsOffWritesThrough(t *testing.T) {
	in := string([]byte{'a', 0xff, 'b'})
	for _, o := range []Options{{}, {EscapeHTML: true}} {
		got, err := o.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(got, []byte{0xff}) {
			t.Errorf("EscapeHTML=%v: expected the invalid byte written through, got %q", o.EscapeHTML, got)
		}
	}
}

// TestValidateStringsSkipsASCII covers the branch reached when exactly one of
// EscapeHTML and ValidateStrings is set -- the other combinations go through
// appendQuoted, which the main fuzzer exercises and this one does not.
//
// That branch now proves the ASCII prefix before validating, so the strings
// that matter are the ones where the prefix ends: at a byte above ASCII, at a
// byte needing an escape, and at neither. Every case is checked against the
// documented behaviour, which is that invalid UTF-8 becomes U+FFFD.
func TestValidateStringsSkipsASCII(t *testing.T) {
	cases := []string{
		"", "plain ascii", "with \"quote\"", "with\nnewline", "<html>&</html>",
		"café", "日本語", "🙂",
		"ascii then \xff",
		"\xff then ascii",
		"quote \" then \xff",
		"\xff then quote \"",
		"valid \xc3\xa9 then invalid \xff",
		"\xed\xa0\x80",     // surrogate half
		"\xf4\x90\x80\x80", // above U+10FFFF
		"\xc3",             // truncated two-byte
		"\xe2\x80",         // truncated three-byte
		"a\xc3",            // truncated after ASCII
		strings.Repeat("x", 200) + "\xff",
		strings.Repeat("x", 200) + "é",
		strings.Repeat("é", 100) + "\xff",
	}
	for _, o := range []Options{
		{ValidateStrings: true},
		{EscapeHTML: true, ValidateStrings: true},
		{ValidateStrings: true, SortMapKeys: true},
	} {
		for _, in := range cases {
			got, err := o.Marshal(in)
			if err != nil {
				t.Fatalf("%+v %q: %v", o, in, err)
			}
			// Round-trip: whatever came out must be valid JSON holding valid
			// UTF-8, which is the whole point of the option.
			var back string
			if err := stdjson.Unmarshal(got, &back); err != nil {
				t.Fatalf("%+v %q: output %q is not JSON: %v", o, in, got, err)
			}
			if !utf8.ValidString(back) {
				t.Errorf("%+v %q: decoded output is not valid UTF-8: %q", o, in, back)
			}
			// And it must agree with encoding/json, which does the same
			// replacement, whenever HTML escaping matches its default.
			if o.EscapeHTML {
				want, err := stdjson.Marshal(in)
				if err != nil {
					t.Fatal(err)
				}
				if string(got) != string(want) {
					t.Errorf("%+v %q:\n got %s\nwant %s", o, in, got, want)
				}
			}
		}
	}
}
