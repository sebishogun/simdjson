package simdjson

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func ndStream(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, `{"id":%d,"name":"record %d","ok":%v}`+"\n", i, i, i%2 == 0)
	}
	return b.String()
}

// Order is the whole contract: records must arrive as they appeared.
func TestParallelOrder(t *testing.T) {
	for _, n := range []int{0, 1, 2, 100, 5000, 60000} {
		in := ndStream(n)
		var got []int64
		if err := ForEachLineReaderParallel(strings.NewReader(in), func(v Value) bool {
			got = append(got, v.Key("id").Int())
			return true
		}); err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}
		if len(got) != n {
			t.Fatalf("n=%d: got %d records", n, len(got))
		}
		for i, id := range got {
			if id != int64(i) {
				t.Fatalf("n=%d: record %d has id %d -- out of order", n, i, id)
				break
			}
		}
	}
}

// The parallel and sequential forms must agree, record for record.
func TestParallelMatchesSequential(t *testing.T) {
	in := ndStream(20000)
	var par, seq []int64
	if err := ForEachLineReaderParallel(strings.NewReader(in), func(v Value) bool {
		par = append(par, v.Key("id").Int())
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if err := ForEachLineReader(strings.NewReader(in), func(v Value) bool {
		seq = append(seq, v.Key("id").Int())
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if len(par) != len(seq) {
		t.Fatalf("parallel %d records, sequential %d", len(par), len(seq))
	}
	for i := range par {
		if par[i] != seq[i] {
			t.Fatalf("record %d: parallel %d, sequential %d", i, par[i], seq[i])
		}
	}
}

// A bad record stops the walk, is reported, and the error's offset points into
// the whole stream rather than into the chunk it was found in.
func TestParallelReportsBadLineWithStreamOffset(t *testing.T) {
	good := ndStream(30000)
	bad := good + "{oops}\n" + ndStream(10)
	seen := 0
	err := ForEachLineReaderParallel(strings.NewReader(bad), func(v Value) bool {
		seen++
		return true
	})
	if err == nil {
		t.Fatal("malformed record accepted")
	}
	se, ok := err.(*SyntaxError)
	if !ok {
		t.Fatalf("error is %T, want *SyntaxError", err)
	}
	// The bad record starts at len(good). The offset must be near it, not near
	// the start of whatever chunk happened to contain it.
	want := int64(len(good))
	if se.Offset < want || se.Offset > want+16 {
		t.Errorf("offset %d, want within 16 bytes of %d -- it is chunk-relative",
			se.Offset, want)
	}
	if seen != 30000 {
		t.Errorf("saw %d good records before the error, want 30000", seen)
	}
}

// Returning false stops promptly and does not deadlock, which is the failure
// mode minio has.
func TestParallelStops(t *testing.T) {
	in := ndStream(200000)
	seen := 0
	done := make(chan error, 1)
	go func() {
		done <- ForEachLineReaderParallel(strings.NewReader(in), func(v Value) bool {
			seen++
			return seen < 5
		})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-timeoutAfter():
		t.Fatal("ForEachLineReaderParallel did not return after the callback said stop")
	}
	if seen != 5 {
		t.Errorf("saw %d records after stopping at 5", seen)
	}
}

func TestParallelBlankAndCRLF(t *testing.T) {
	in := "{\"id\":1}\r\n\r\n  {\"id\":2}  \r\n\n{\"id\":3}\n"
	var got []int64
	if err := ForEachLineReaderParallel(strings.NewReader(in), func(v Value) bool {
		got = append(got, v.Key("id").Int())
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Errorf("got %v", got)
	}
}

func BenchmarkLineSequential(b *testing.B) {
	data := []byte(ndStream(200000))
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		n := int64(0)
		if err := ForEachLine(data, func(v Value) bool { n += v.Key("id").Int(); return true }); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkLineParallel(b *testing.B) {
	data := []byte(ndStream(200000))
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		n := int64(0)
		if err := ForEachLineReaderParallel(bytes.NewReader(data), func(v Value) bool {
			n += v.Key("id").Int()
			return true
		}); err != nil {
			b.Fatal(err)
		}
	}
}
