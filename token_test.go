package simdjson

import (
	stdjson "encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
)

// Every token, in order, against encoding/json's. Same values and same types,
// not merely the same shape — a Delim that came back as a rune, or a number
// that came back as a Number when it should be a float64, would be a different
// API wearing the same name.
func tokensOf(t *testing.T, in string, useNumber bool) ([]string, error) {
	t.Helper()
	d := NewDecoder(strings.NewReader(in))
	if useNumber {
		d.UseNumber()
	}
	var out []string
	for {
		tok, err := d.Token()
		if err != nil {
			return out, err
		}
		out = append(out, fmt.Sprintf("%T:%v", tok, tok))
	}
}

func stdTokensOf(in string, useNumber bool) ([]string, error) {
	d := stdjson.NewDecoder(strings.NewReader(in))
	if useNumber {
		d.UseNumber()
	}
	var out []string
	for {
		tok, err := d.Token()
		if err != nil {
			return out, err
		}
		out = append(out, fmt.Sprintf("%T:%v", tok, tok))
	}
}

var tokenInputs = []string{
	`null`, `true`, `false`, `1`, `-2.5`, `1e3`, `"s"`, `""`,
	`{}`, `[]`, `[1]`, `[1,2,3]`, `{"a":1}`, `{"a":1,"b":2}`,
	`{"a":[1,{"b":null}],"c":{}}`,
	`[[[[1]]]]`, `[{},{},[]]`,
	`{"k":"v"} {"k2":"v2"}`, `1 2 3`,
	`{"unicode":"日本語","esc":"a\"b\\c\nd"}`,
	`  { "a" : [ 1 , 2 ] }  `,
	// Malformed: the point is that both stop in the same place.
	`{`, `[`, `}`, `]`, `[1,]`, `{"a"}`, `{"a":}`, `{,}`, `[1 2]`,
	`{"a":1,}`, `tru`, `01`, `"unterminated`, `[1,,2]`,
}

func TestTokenMatchesStdlib(t *testing.T) {
	for _, useNumber := range []bool{false, true} {
		for _, in := range tokenInputs {
			got, gErr := tokensOf(t, in, useNumber)
			want, wErr := stdTokensOf(in, useNumber)
			if len(got) != len(want) {
				t.Errorf("Token(%q, useNumber=%v)\n got %v (%v)\nwant %v (%v)",
					in, useNumber, got, gErr, want, wErr)
				continue
			}
			for i := range got {
				if got[i] != want[i] {
					t.Errorf("Token(%q) token %d = %s, want %s", in, i, got[i], want[i])
				}
			}
			if (gErr == io.EOF) != (wErr == io.EOF) {
				t.Errorf("Token(%q) stopped with %v, stdlib with %v", in, gErr, wErr)
			}
		}
	}
}

// The reason Token exists: read the opening bracket, then decode the elements
// one at a time, so an array larger than memory can be walked.
func TestTokenThenDecodeElements(t *testing.T) {
	type row struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	const in = `[{"id":1,"name":"a"},{"id":2,"name":"b"},{"id":3,"name":"c"}]`

	check := func(t *testing.T, got []row, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		want := []row{{1, "a"}, {2, "b"}, {3, "c"}}
		if len(got) != len(want) {
			t.Fatalf("got %d rows %+v, want %d", len(got), got, len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("row %d = %+v, want %+v", i, got[i], want[i])
			}
		}
	}

	d := NewDecoder(strings.NewReader(in))
	tok, err := d.Token()
	if err != nil {
		t.Fatalf("opening token: %v", err)
	}
	if tok != Delim('[') {
		t.Fatalf("opening token = %v, want [", tok)
	}
	var got []row
	for d.More() {
		var r row
		if err := d.Decode(&r); err != nil {
			t.Fatalf("Decode: %v", err)
		}
		got = append(got, r)
	}
	if tok, err = d.Token(); err != nil || tok != Delim(']') {
		t.Fatalf("closing token = %v, %v", tok, err)
	}
	check(t, got, nil)

	// And the same through encoding/json, so the shape of the loop is known to
	// be the shape that works there too.
	sd := stdjson.NewDecoder(strings.NewReader(in))
	sd.Token()
	var sgot []row
	for sd.More() {
		var r row
		if err := sd.Decode(&r); err != nil {
			t.Fatalf("stdlib Decode: %v", err)
		}
		sgot = append(sgot, r)
	}
	check(t, sgot, nil)
}

// Nested containers, where More has to answer about the innermost one.
func TestTokenNestedMore(t *testing.T) {
	const in = `{"a":[1,2],"b":[3]}`
	d := NewDecoder(strings.NewReader(in))
	sd := stdjson.NewDecoder(strings.NewReader(in))
	for i := 0; i < 12; i++ {
		gm, wm := d.More(), sd.More()
		if gm != wm {
			t.Fatalf("step %d: More() = %v, stdlib %v", i, gm, wm)
		}
		gt, ge := d.Token()
		wt, we := sd.Token()
		if (ge != nil) != (we != nil) {
			t.Fatalf("step %d: err %v, stdlib %v", i, ge, we)
		}
		if ge != nil {
			break
		}
		if fmt.Sprintf("%T:%v", gt, gt) != fmt.Sprintf("%T:%v", wt, wt) {
			t.Fatalf("step %d: %v, stdlib %v", i, gt, wt)
		}
	}
}

func FuzzTokenAgainstStdlib(f *testing.F) {
	for _, s := range tokenInputs {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		got, gErr := tokensOf(t, string(data), false)
		want, wErr := stdTokensOf(string(data), false)
		if len(got) != len(want) {
			t.Fatalf("got %d tokens %v (%v), want %d %v (%v)",
				len(got), got, gErr, len(want), want, wErr)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("token %d = %s, want %s", i, got[i], want[i])
			}
		}
		if (gErr == io.EOF) != (wErr == io.EOF) {
			t.Fatalf("stopped with %v, stdlib with %v", gErr, wErr)
		}
	})
}
