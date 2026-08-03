package simdjson

import (
	"compress/gzip"
	"io"
	"os"
	"strings"
	"testing"
	"unsafe"
)

// StringNoCopy must agree with String on every input, and must actually alias
// the document when it says it does.
func TestStringNoCopy(t *testing.T) {
	cases := []string{
		`"plain"`, `""`, `"a"`, `"with space"`,
		`"esc\"aped"`, `"back\\slash"`, `"tab\there"`, `"é"`,
		`"日本語"`, `"mixed 日本 ascii"`, `"😀"`,
	}
	for _, in := range cases {
		doc := []byte(`{"k":` + in + `}`)
		d, err := Parse(doc)
		if err != nil {
			t.Fatalf("Parse(%s): %v", in, err)
		}
		v := d.Root().Key("k")
		got, want := v.StringNoCopy(), v.String()
		if got != want {
			t.Errorf("%s: StringNoCopy = %q, String = %q", in, got, want)
		}
	}
}

// The clean case has to alias, or the method is a lie.
func TestStringNoCopyAliases(t *testing.T) {
	doc := []byte(`{"k":"aliased"}`)
	d, err := Parse(doc)
	if err != nil {
		t.Fatal(err)
	}
	s := d.Root().Key("k").StringNoCopy()
	if s != "aliased" {
		t.Fatalf("got %q", s)
	}
	// The string's bytes must be the document's bytes, not a copy of them.
	sp := unsafe.StringData(s)
	dp := &doc[len(`{"k":"`)]
	if sp != dp {
		t.Errorf("StringNoCopy did not alias: string at %p, document at %p", sp, dp)
	}
}

// An escaped string cannot alias, and must still be correct.
func TestStringNoCopyEscapedIsCopied(t *testing.T) {
	doc := []byte(`{"k":"a\nb"}`)
	d, err := Parse(doc)
	if err != nil {
		t.Fatal(err)
	}
	s := d.Root().Key("k").StringNoCopy()
	if s != "a\nb" {
		t.Fatalf("got %q, want %q", s, "a\nb")
	}
	if strings.Contains(s, `\`) {
		t.Error("escape was not undone")
	}
}
func twitterBytes(b *testing.B) []byte {
	f, err := os.Open("testdata/bench/corpus/twitter.json.gz")
	if err != nil {
		b.Skip(err)
	}
	defer f.Close()
	zr, _ := gzip.NewReader(f)
	d, _ := io.ReadAll(zr)
	return d
}

func walk(v Value, fn func(Value)) {
	switch v.Kind() {
	case String:
		fn(v)
	case Array:
		v.ForEach(func(e Value) bool { walk(e, fn); return true })
	case Object:
		v.ForEachKey(func(_ string, e Value) bool { walk(e, fn); return true })
	}
}

func BenchmarkStringCopy(b *testing.B) {
	data := twitterBytes(b)
	d, err := Parse(data)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		n := 0
		walk(d.Root(), func(v Value) { n += len(v.String()) })
		if n == 0 {
			b.Fatal("no strings")
		}
	}
}

func BenchmarkStringNoCopy(b *testing.B) {
	data := twitterBytes(b)
	d, err := Parse(data)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		n := 0
		walk(d.Root(), func(v Value) { n += len(v.StringNoCopy()) })
		if n == 0 {
			b.Fatal("no strings")
		}
	}
}
