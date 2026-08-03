package simdjson

import (
	"bytes"
	"strings"
	"testing"
)

const nd = `{"id":1,"name":"a"}
{"id":2,"name":"b"}

{"id":3,"name":"c"}
`

func collect(t *testing.T, in string) []int64 {
	t.Helper()
	var ids []int64
	if err := ForEachLine([]byte(in), func(v Value) bool {
		ids = append(ids, v.Key("id").Int())
		return true
	}); err != nil {
		t.Fatalf("ForEachLine: %v", err)
	}
	return ids
}

func TestForEachLine(t *testing.T) {
	got := collect(t, nd)
	if len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Errorf("ids = %v", got)
	}
	// No trailing newline.
	if got := collect(t, `{"id":7}`); len(got) != 1 || got[0] != 7 {
		t.Errorf("single line without newline: %v", got)
	}
	// CRLF, and indentation around the value.
	if got := collect(t, "  {\"id\":1}  \r\n\t{\"id\":2}\r\n"); len(got) != 2 {
		t.Errorf("crlf/indent: %v", got)
	}
	// Empty input is not an error.
	if got := collect(t, ""); len(got) != 0 {
		t.Errorf("empty: %v", got)
	}
	if got := collect(t, "\n\n\n"); len(got) != 0 {
		t.Errorf("blank lines: %v", got)
	}
}

// gjson's ForEachLine stops silently at the first bad chunk. This must not.
func TestForEachLineReportsBadLine(t *testing.T) {
	in := "{\"id\":1}\n{bad}\n{\"id\":3}\n"
	seen := 0
	err := ForEachLine([]byte(in), func(v Value) bool { seen++; return true })
	if err == nil {
		t.Fatal("a malformed line was accepted silently")
	}
	if seen != 1 {
		t.Errorf("saw %d good lines before the error, want 1", seen)
	}
	if _, ok := err.(*SyntaxError); !ok {
		t.Errorf("error is %T, want *SyntaxError", err)
	}
}

func TestForEachLineStops(t *testing.T) {
	seen := 0
	if err := ForEachLine([]byte(nd), func(v Value) bool { seen++; return false }); err != nil {
		t.Fatal(err)
	}
	if seen != 1 {
		t.Errorf("returning false saw %d lines, want 1", seen)
	}
}

func TestForEachLineReader(t *testing.T) {
	var ids []int64
	err := ForEachLineReader(strings.NewReader(nd), func(v Value) bool {
		ids = append(ids, v.Key("id").Int())
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 || ids[1] != 2 {
		t.Errorf("ids = %v", ids)
	}

	// A record longer than the read buffer has to survive being reassembled.
	long := `{"id":9,"pad":"` + strings.Repeat("x", 300<<10) + `"}`
	ids = ids[:0]
	err = ForEachLineReader(strings.NewReader(long+"\n"+`{"id":10}`+"\n"), func(v Value) bool {
		ids = append(ids, v.Key("id").Int())
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != 9 || ids[1] != 10 {
		t.Errorf("long line: ids = %v", ids)
	}

	// And the stream form reports a bad line too.
	if err := ForEachLineReader(bytes.NewReader([]byte("{\"a\":1}\n{oops}\n")), func(Value) bool {
		return true
	}); err == nil {
		t.Error("reader form accepted a malformed line")
	}
}

// A newline inside a string is not a line break: JSON forbids a literal
// newline in a string, so any \n in the input really is a separator. The
// escaped form must survive.
func TestForEachLineEscapedNewline(t *testing.T) {
	got := collect(t, `{"id":1,"s":"a\nb"}`+"\n")
	if len(got) != 1 {
		t.Fatalf("ids = %v", got)
	}
	var s string
	if err := ForEachLine([]byte(`{"s":"a\nb"}`+"\n"), func(v Value) bool {
		s = v.Key("s").String()
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if s != "a\nb" {
		t.Errorf("escaped newline = %q", s)
	}
}

// The reason for the API is that it should beat the loop it replaces.
func ndCorpus(n int) []byte {
	var b []byte
	for i := 0; i < n; i++ {
		b = append(b, `{"id":`...)
		b = append(b, byte('0'+i%10))
		b = append(b, `,"name":"a tweet-ish string with some length","ok":true,"n":1.5}`...)
		b = append(b, '\n')
	}
	return b
}

func BenchmarkForEachLine(b *testing.B) {
	data := ndCorpus(20000)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		n := int64(0)
		if err := ForEachLine(data, func(v Value) bool { n += v.Key("id").Int(); return true }); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecodeLoop(b *testing.B) {
	data := ndCorpus(20000)
	type row struct {
		ID   int64   `json:"id"`
		Name string  `json:"name"`
		OK   bool    `json:"ok"`
		N    float64 `json:"n"`
	}
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		dec := NewDecoder(bytes.NewReader(data))
		n := int64(0)
		for {
			var r row
			if err := dec.Decode(&r); err != nil {
				break
			}
			n += r.ID
		}
	}
}
