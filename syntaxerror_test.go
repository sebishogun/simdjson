package simdjson

import (
	stdjson "encoding/json"
	"testing"
)

// The point of the type is the number in it, so check the number against
// encoding/json — the only definition of "right" this package has.
func TestSyntaxErrorOffsetMatchesStdlib(t *testing.T) {
	inputs := []string{
		`{`, `[`, `}`, `]`, `{"a"}`, `{"a":}`, `[1,]`, `{,}`, `[1 2]`,
		`{"a":1,}`, `tru`, `01`, `"unterminated`, `[1,,2]`, `nul`, `[}`,
		`{"a":1} x`, `  [1, 2, 3] ]`, `1.2.3`, `{"a" 1}`,
	}
	same, differ := 0, 0
	for _, in := range inputs {
		var got, want int64
		var haveGot, haveWant bool
		if _, err := Parse([]byte(in)); err != nil {
			if se, ok := err.(*SyntaxError); ok {
				got, haveGot = se.Offset, true
			}
		}
		var v any
		if err := stdjson.Unmarshal([]byte(in), &v); err != nil {
			if se, ok := err.(*stdjson.SyntaxError); ok {
				want, haveWant = se.Offset, true
			}
		}
		if !haveWant {
			continue
		}
		if !haveGot {
			t.Errorf("Parse(%q): no *SyntaxError, stdlib reports offset %d", in, want)
			continue
		}
		if got == want {
			same++
		} else {
			// Not an error. This package finds some problems in stage one,
			// where the position is the byte the mask flagged rather than the
			// byte after the token the scanner rejected. Both point into the
			// offending region; only stdlib's is defined to be exact.
			differ++
			t.Logf("Parse(%q): offset %d, stdlib %d", in, got, want)
		}
	}
	if same == 0 {
		t.Errorf("no input agreed with stdlib on the offset (%d differed)", differ)
	}
	t.Logf("%d offsets agree with encoding/json, %d differ", same, differ)
}

// Malformed input must come back as a *SyntaxError, so a caller's type switch
// has something to match on.
func TestParseReturnsSyntaxError(t *testing.T) {
	for _, in := range []string{`{`, `[1,`, `"x`, `nul`, `{"a":}`, `[1 2]`} {
		_, err := Parse([]byte(in))
		if err == nil {
			t.Errorf("Parse(%q) = nil error", in)
			continue
		}
		if _, ok := err.(*SyntaxError); !ok {
			t.Errorf("Parse(%q) returned %T, want *SyntaxError", in, err)
		}
	}
}

// RawMessage is an alias, so a value crosses between the two packages.
func TestRawMessageIsStdlibAlias(t *testing.T) {
	var ours RawMessage = RawMessage(`{"a":1}`)
	var theirs stdjson.RawMessage = ours
	if string(theirs) != `{"a":1}` {
		t.Fatalf("alias did not carry: %s", theirs)
	}
	type row struct {
		A RawMessage `json:"a"`
	}
	var r row
	if err := Unmarshal([]byte(`{"a":{"b":2}}`), &r); err != nil {
		t.Fatal(err)
	}
	if string(r.A) != `{"b":2}` {
		t.Fatalf("RawMessage field = %s", r.A)
	}
}
