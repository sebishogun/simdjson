package simdjson

import (
	"encoding/json"
	"math/rand/v2"
	"strings"
	"testing"
)

// validateString is the definition index.validateStrings has to meet: the
// byte-at-a-time check it replaced, kept as the oracle for it.
//
// It checks a string body — the bytes between the quotes — without
// decoding it.
//
// Two rules, both found by fuzzing against encoding/json rather than by reading
// the grammar. A raw control character is not allowed and has to be escaped;
// and only a fixed set of escapes exists, so \0 is invalid however reasonable
// it looks. Both were accepted here and rejected by the standard library, which
// means the two disagreed on documents only one of them would take.
//
// Nothing is built: this walks the escapes and checks them, where decoding
// would allocate a string for every field of every document at parse time.
func validateString(b []byte) error {
	for i := 0; i < len(b); i++ {
		c := b[i]
		if c < 0x20 {
			return errSyntax("control character in string")
		}
		if c != '\\' {
			continue
		}
		i++
		if i >= len(b) {
			return errSyntax("string ends in a backslash")
		}
		switch b[i] {
		case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
		case 'u':
			if i+4 >= len(b) {
				return errSyntax("short \\u escape")
			}
			for k := 1; k <= 4; k++ {
				if !isHex(b[i+k]) {
					return errSyntax("invalid \\u escape")
				}
			}
			i += 4
		default:
			return errSyntax("invalid escape")
		}
	}
	return nil
}

// refValidateStrings applies validateString to every string in the document,
// which is what Parse used to do one string at a time as it met them.
func refValidateStrings(t *testing.T, data []byte) error {
	t.Helper()
	ix, err := buildIndex(data, nil)
	if err != nil {
		return err
	}
	// Walk the in-string mask for string bodies.
	for i := 0; i < len(data); i++ {
		if ix.inStr[i/64]&(1<<uint(i%64)) == 0 || data[i] != '"' {
			continue
		}
		end, ok := ix.stringEndAt(i, len(data))
		if !ok {
			return errSyntax("unterminated string")
		}
		if err := validateString(data[i+1 : end-1]); err != nil {
			return err
		}
		i = end - 1
	}
	return nil
}

// The mask validator and the byte validator must agree on every document.
//
// The mask version checks the whole document at once with bit arithmetic — the
// control characters as an and of two masks, the escapes as a shift and an
// and-not — so a disagreement here is a document one of them accepts and the
// other does not, which is exactly the class of bug that ships silently.
func TestValidateStringsMatchesTheByteWalk(t *testing.T) {
	atoms := []string{
		`"a"`, `"\""`, `"\\\\"`, `"\/"`, `"\b"`, `"\f"`, `"\n"`, `"\r"`, `"\t"`,
		`"\u0041"`, `"\u00zz"`, `"\u004"`, `"\q"`, `"\"`, `"\u"`,
		`"` + "\t" + `"`, `"` + "\n" + `"`, `"` + "\x00" + `"`,
		`"ok"`, `"{}[]:,"`, `1`, `true`, `null`, `"` + strings.Repeat("z", 70) + `"`,
		`"` + strings.Repeat(`\\`, 35) + `"`,
	}
	r := rand.New(rand.NewPCG(41, 43))
	for trial := 0; trial < 4000; trial++ {
		var sb strings.Builder
		sb.WriteByte('[')
		for i := 0; i < 1+r.IntN(6); i++ {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(atoms[r.IntN(len(atoms))])
		}
		sb.WriteByte(']')
		doc := []byte(sb.String())

		ix, err := buildIndex(doc, nil)
		if err != nil {
			continue // not indexable at all; both sides agree by not being reached
		}
		got := ix.validateStrings(doc)
		want := refValidateStrings(t, doc)
		if (got == nil) != (want == nil) {
			t.Fatalf("%s: mask validator says %v, byte validator says %v", doc, got, want)
		}

		// And against the standard library, which is the actual contract.
		stdOK := json.Valid(doc)
		if got == nil && !stdOK {
			// The mask validator only checks strings; the grammar is checked
			// separately. Confirm the full parse agrees.
			if _, perr := Parse(doc); perr == nil {
				t.Fatalf("%s: accepted, encoding/json rejects it", doc)
			}
		}
		if got != nil && stdOK {
			t.Fatalf("%s: string validation rejected it (%v), encoding/json accepts it", doc, got)
		}
	}
}
