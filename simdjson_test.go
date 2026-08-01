package simdjson

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
)

// toGo turns a Value back into the shape encoding/json would produce, so the
// two can be compared directly. encoding/json is the definition of correct
// here; this package exists to be faster, not different.
func toGo(v Value) any {
	switch v.Kind() {
	case Null:
		return nil
	case Bool:
		return v.Bool()
	case Number:
		return v.Float()
	case String:
		return v.String()
	case Array:
		out := []any{}
		v.ForEach(func(e Value) bool { out = append(out, toGo(e)); return true })
		return out
	case Object:
		out := map[string]any{}
		v.ForEachKey(func(k string, e Value) bool { out[k] = toGo(e); return true })
		return out
	}
	return nil
}

func checkAgainstStdlib(t *testing.T, in string) {
	t.Helper()
	// json.Valid decides accept-or-reject; Unmarshal also converts, and
	// rejects valid JSON that will not fit a Go float64.
	valid := json.Valid([]byte(in))
	doc, gotErr := Parse([]byte(in))
	if valid != (gotErr == nil) {
		t.Fatalf("input %q: json.Valid=%v, simdjson err=%v", in, valid, gotErr)
	}
	if !valid {
		return
	}
	var want any
	if json.Unmarshal([]byte(in), &want) != nil {
		return
	}
	got := toGo(doc.Root())
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("input %q:\n got %#v\nwant %#v", in, got, want)
	}
}

func TestMatchesStdlib(t *testing.T) {
	for _, in := range []string{
		`null`, `true`, `false`, `0`, `-1`, `1.5`, `1e3`, `-2.5e-3`,
		`""`, `"a"`, `"hello world"`,
		`[]`, `{}`, `[1,2,3]`, `["a","b"]`,
		`{"a":1}`, `{"a":1,"b":2}`,
		`{"a":{"b":{"c":42}}}`,
		`[[1,2],[3,4]]`,
		`{"a":[1,{"b":2}],"c":null}`,
		` { "a" : 1 , "b" : [ 2 , 3 ] } `,
		`{"esc":"a\"b"}`,
		`{"esc":"a\\b"}`,
		`{"esc":"a\\"}`,
		`{"esc":"tab\there"}`,
		`{"esc":"nl\nhere"}`,
		`{"uni":"\u00e9"}`,
		`{"uni":"\u4e2d\u6587"}`,
		`{"surrogate":"\ud83d\ude00"}`,
		`{"brace":"{not structure}"}`,
		`{"comma":"a,b","colon":"c:d"}`,
		`{"bracket":"[]"}`,
		`{"":"empty key"}`,
		`{"a":"","b":""}`,
		`[{"x":1},{"x":2},{"x":3}]`,
		"{\n  \"pretty\": true,\n  \"n\": [1, 2]\n}",
		// invalid
		`{`, `[`, `{"a"}`, `{"a":}`, `[1,]`, `"unterminated`, `tru`, `{"a":1}x`,
	} {
		checkAgainstStdlib(t, in)
	}
}

// Randomised documents, because the failures in a parser are the combinations
// nobody thinks to write down — especially strings containing structure.
func TestMatchesStdlibRandom(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	var gen func(depth int) string
	atoms := []string{`1`, `-2.5`, `true`, `false`, `null`, `""`, `"a"`,
		`"{"`, `"}"`, `"["`, `"]"`, `","`, `":"`, `"a\"b"`, `"a\\"`, `"\u00e9"`, `"\n"`}
	gen = func(depth int) string {
		if depth <= 0 || r.IntN(3) == 0 {
			return atoms[r.IntN(len(atoms))]
		}
		n := r.IntN(4)
		parts := make([]string, n)
		if r.IntN(2) == 0 {
			for i := range parts {
				parts[i] = gen(depth - 1)
			}
			return "[" + strings.Join(parts, ",") + "]"
		}
		for i := range parts {
			parts[i] = fmt.Sprintf(`%s:%s`, atoms[r.IntN(7)+5], gen(depth-1))
		}
		return "{" + strings.Join(parts, ",") + "}"
	}
	for i := 0; i < 2000; i++ {
		checkAgainstStdlib(t, gen(4))
	}
}

func TestGet(t *testing.T) {
	doc, err := Parse([]byte(`{"user":{"name":"ada","age":36,"tags":["x","y"]},"ok":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := doc.Get("user", "name").String(); got != "ada" {
		t.Errorf(`Get("user","name") = %q, want "ada"`, got)
	}
	if got := doc.Get("user", "age").Int(); got != 36 {
		t.Errorf(`Get("user","age") = %d, want 36`, got)
	}
	if got := doc.Get("user", "tags").Index(1).String(); got != "y" {
		t.Errorf("tags[1] = %q, want \"y\"", got)
	}
	if doc.Get("user", "missing").Exists() {
		t.Error("a missing key should not Exist")
	}
	if doc.Get("nope", "deeper").Exists() {
		t.Error("a path through a missing key should not Exist")
	}
	if !doc.Get("ok").Bool() {
		t.Error(`Get("ok") should be true`)
	}
	if n := doc.Get("user", "tags").Len(); n != 2 {
		t.Errorf("tags Len = %d, want 2", n)
	}
}

// A structural character inside a string is text. If stage one gets that wrong
// the index is wrong and everything downstream is nonsense, so it is asserted
// directly rather than only through the differential test.
func TestStringsHideStructure(t *testing.T) {
	in := `{"a":"},{\"b\":2},[","c":1}`
	doc, err := Parse([]byte(in))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := doc.Get("a").String(); got != `},{"b":2},[` {
		t.Errorf("a = %q, want %q", got, `},{"b":2},[`)
	}
	if got := doc.Get("c").Int(); got != 1 {
		t.Errorf("c = %d, want 1", got)
	}
}

// An escaped quote does not close a string, and an escaped backslash before a
// quote does. Both are the backslash-parity rule and both are easy to get
// backwards.
func TestBackslashParity(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`{"k":"a\"b"}`, `a"b`},
		{`{"k":"a\\"}`, `a\`},
		{`{"k":"a\\\"b"}`, `a\"b`},
		{`{"k":"a\\\\"}`, `a\\`},
	} {
		doc, err := Parse([]byte(c.in))
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if got := doc.Get("k").String(); got != c.want {
			t.Errorf("%s: got %q, want %q", c.in, got, c.want)
		}
	}
}

// Scan skips validation, so the only claim is that it agrees with Parse on
// documents that are valid. Anything else is explicitly not promised.
func TestScanAgreesWithParseOnValidInput(t *testing.T) {
	for _, in := range []string{
		`{"a":1}`, `[1,2,3]`, `{"a":{"b":[1,{"c":"x"}]}}`,
		`{"esc":"a\"b","uni":"é"}`, `{"s":"},{"}`, `"top"`, `42`, `null`,
	} {
		p, perr := Parse([]byte(in))
		s, serr := Scan([]byte(in))
		if perr != nil || serr != nil {
			t.Fatalf("%s: parse=%v scan=%v", in, perr, serr)
		}
		if !reflect.DeepEqual(toGo(p.Root()), toGo(s.Root())) {
			t.Errorf("%s: Scan and Parse disagree", in)
		}
	}
}
