package simdjson

import (
	"strings"
	"testing"
)

func mustSet(t *testing.T, in, path string, v any) string {
	t.Helper()
	out, err := SetPath([]byte(in), path, v)
	if err != nil {
		t.Fatalf("SetPath(%q, %q): %v", in, path, err)
	}
	if !Valid(out) {
		t.Fatalf("SetPath(%q, %q) produced invalid JSON: %s", in, path, out)
	}
	return string(out)
}

func TestSetPathReplace(t *testing.T) {
	for _, c := range []struct{ in, path, want string }{
		{`{"a":1}`, "a", `{"a":2}`},
		{`{"a":1,"b":2}`, "b", `{"a":1,"b":2}`},
		{`{"a":{"b":1}}`, "a.b", `{"a":{"b":2}}`},
		{`[1,2,3]`, "1", `[1,2,3]`},
		{`{"a":"old"}`, "a", `{"a":2}`},
	} {
		got := mustSet(t, c.in, c.path, 2)
		// Reparse and check the value landed rather than comparing text, since
		// whitespace placement is not the contract.
		if v := GetPath([]byte(got), c.path); v.Int() != 2 {
			t.Errorf("SetPath(%q, %q) = %s; value is %v", c.in, c.path, got, v.Int())
		}
	}
}

func TestSetPathCreates(t *testing.T) {
	for _, c := range []struct{ in, path string }{
		{`{}`, "a"},
		{`{}`, "a.b.c"},
		{`{"x":1}`, "y"},
		{`{"x":1}`, "y.z"},
		{`{"a":{}}`, "a.b"},
		{`{"a":{"b":1}}`, "a.c"},
		{`[]`, "0"},
		{`[1]`, "1"},
		{`{"a":[]}`, "a.0"},
	} {
		got := mustSet(t, c.in, c.path, 42)
		if v := GetPath([]byte(got), c.path); v.Int() != 42 {
			t.Errorf("SetPath(%q, %q) = %s; reading it back gives %v (exists=%v)",
				c.in, c.path, got, v.Int(), v.Exists())
		}
	}
}

func TestSetPathAppend(t *testing.T) {
	// -1 appends. It is a write-only idiom -- there is no reading a value back
	// through it -- so it is checked by length and by the last element.
	for _, c := range []struct {
		in, path, read string
		want           int
	}{
		{`[1,2]`, "-1", "2", 42},
		{`{"a":[1]}`, "a.-1", "a.1", 42},
		{`[]`, "-1", "0", 42},
		{`[1,2]`, "2", "2", 42}, // exactly one past the end also appends
	} {
		got := mustSet(t, c.in, c.path, c.want)
		if v := GetPath([]byte(got), c.read); v.Int() != int64(c.want) {
			t.Errorf("SetPath(%q, %q) = %s; %q is %v", c.in, c.path, got, c.read, v.Int())
		}
	}
}

func TestSetPathPreservesSiblings(t *testing.T) {
	got := mustSet(t, `{"keep":"me","a":{"also":"here"}}`, "a.new", 1)
	d := MustParse([]byte(got))
	if d.Path("keep").String() != "me" {
		t.Errorf("sibling lost: %s", got)
	}
	if d.Path("a.also").String() != "here" {
		t.Errorf("nested sibling lost: %s", got)
	}
	if d.Path("a.new").Int() != 1 {
		t.Errorf("new value missing: %s", got)
	}
}

func TestSetRawPath(t *testing.T) {
	out, err := SetRawPath([]byte(`{"a":1}`), "b", []byte(`{"raw":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if GetPath(out, "b.raw").Bool() != true {
		t.Errorf("raw set = %s", out)
	}
	// Invalid replacement must be refused, not spliced in.
	if _, err := SetRawPath([]byte(`{"a":1}`), "b", []byte(`{oops`)); err == nil {
		t.Error("invalid replacement accepted")
	}
}

func TestSetPathErrors(t *testing.T) {
	for _, c := range []struct{ in, path string }{
		{`{"a":1}`, ""},
		{`{"a":1}`, "a.*"},
		{`{"a":1}`, "*"},
		{`{`, "a"},
		{`{"a":1}`, "a.b"}, // cannot add a field to a number
		{`[]`, "1"},        // index past the end: -1 appends, this cannot mean anything
		{`[1,2]`, "9"},
		{`{}`, "a.5"},       // creating an array at index 5 would need four nulls
		{`{"a":1}`, "\xdc"}, // a key the same path could never find again
	} {
		if _, err := SetPath([]byte(c.in), c.path, 1); err == nil {
			t.Errorf("SetPath(%q, %q) = nil error", c.in, c.path)
		}
	}
}

func TestDeletePath(t *testing.T) {
	for _, c := range []struct{ in, path, gone string }{
		{`{"a":1,"b":2}`, "a", "a"},
		{`{"a":1,"b":2}`, "b", "b"},
		{`{"a":1}`, "a", "a"},
		{`{"a":{"b":1,"c":2}}`, "a.b", "a.b"},
		{`[1,2,3]`, "1", ""},
		{`{"a" : 1 , "b" : 2}`, "a", "a"},
		{`{"esc\"key":1,"b":2}`, `esc\"key`, ""},
	} {
		out, err := DeletePath([]byte(c.in), c.path)
		if err != nil {
			t.Fatalf("DeletePath(%q, %q): %v", c.in, c.path, err)
		}
		if !Valid(out) {
			t.Fatalf("DeletePath(%q, %q) produced invalid JSON: %s", c.in, c.path, out)
		}
		if c.gone != "" && GetPath(out, c.gone).Exists() {
			t.Errorf("DeletePath(%q, %q) = %s; %q still there", c.in, c.path, out, c.gone)
		}
	}
	// Deleting what is not there leaves the document alone.
	out, err := DeletePath([]byte(`{"a":1}`), "zz")
	if err != nil || string(out) != `{"a":1}` {
		t.Errorf("delete of a missing path = %s, %v", out, err)
	}
}

func TestDeletePathKeepsSiblings(t *testing.T) {
	out, err := DeletePath([]byte(`{"a":1,"b":2,"c":3}`), "b")
	if err != nil {
		t.Fatal(err)
	}
	d := MustParse(out)
	if d.Path("a").Int() != 1 || d.Path("c").Int() != 3 {
		t.Errorf("siblings damaged: %s", out)
	}
	if d.Path("b").Exists() {
		t.Errorf("b survived: %s", out)
	}
}

// There is no stdlib to compare against here, so the differential is with the
// package's own reader: whatever Set produces must parse, and the path must
// read back as what was written. Delete's is that the path must be gone and the
// document must still parse.
func FuzzSetPath(f *testing.F) {
	seeds := []struct{ doc, path string }{
		{`{}`, "a"},
		{`{"a":1}`, "a"},
		{`{"a":1}`, "b.c"},
		{`{"a":{"b":[1,2]}}`, "a.b.0"},
		{`[]`, "0"},
		{`[1,2,3]`, "1"},
		{`{"a":1,"b":2,"c":3}`, "b"},
		{`{"k":"v"}`, `k`},
		{`{"a\"b":1}`, `a\"b`},
	}
	for _, s := range seeds {
		f.Add([]byte(s.doc), s.path)
	}
	f.Fuzz(func(t *testing.T, doc []byte, path string) {
		if !Valid(doc) || path == "" || len(path) > 64 {
			return
		}
		out, err := SetPath(doc, path, 12345)
		if err != nil {
			return // refusing is always allowed; producing rubbish is not
		}
		if !Valid(out) {
			t.Fatalf("SetPath(%q, %q) produced invalid JSON: %q", doc, path, out)
		}
		// -1 means append and there is no reading a value back through it, so
		// the round trip only applies to paths that name a position — and an
		// append ANYWHERE in the path makes everything under it land at an
		// index the path does not say (".-1.0" creates [[v]], readable as
		// ".0.0"). The fuzzer found the mid-path case a long time after the
		// trailing one.
		appendCursor := false
		for _, c := range strings.Split(path, ".") {
			if c == "-1" {
				appendCursor = true
				break
			}
		}
		_, lastComp := cutLast(path)
		if !appendCursor {
			if v := GetPath(out, path); v.Int() != 12345 {
				t.Fatalf("SetPath(%q, %q) = %q; reading the path back gives %v (exists=%v)",
					doc, path, out, v.Int(), v.Exists())
			}
		}
		if lastComp == "-1" {
			return // and nothing to delete through it either
		}
		del, err := DeletePath(out, path)
		if err != nil {
			return
		}
		if !Valid(del) {
			t.Fatalf("DeletePath(%q, %q) produced invalid JSON: %q", out, path, del)
		}
		// "the path is gone" is the wrong invariant, for two reasons the fuzzer
		// found in order. Deleting element 0 of an array shifts the rest down,
		// so index 0 exists afterwards and should -- it is a different element.
		// And duplicate keys are legal JSON, so deleting `b` from
		// {"b":1,"b":2} leaves a `b`. What is always true is that something
		// went away -- unless the path holds an append cursor, which addresses
		// nothing, so deleting through it removes nothing and should not.
		if !appendCursor && len(del) >= len(out) {
			t.Fatalf("DeletePath(%q, %q) = %q; nothing was removed", out, path, del)
		}
	})
}
