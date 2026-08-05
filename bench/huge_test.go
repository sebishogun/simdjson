package bench

// The very large end: documents far past the 2 GiB Parse limit, up to 10 GB.
//
// The README publishes throughput and peak-heap figures for 10 GB documents.
// They were measured once, by hand, by code that is not in the repository --
// which makes them unreproducible and, on the record of this project, at least
// as likely to be wrong as right. This is the benchmark that produces them.
//
// TEN GIGABYTES IS NOT SYNTHESISED ON DISK. A repeating reader feeds the same
// buffer over and over, so the test needs no disk and no large heap; that is
// also the point being demonstrated, since a Decoder that needed either would
// not be a streaming decoder. The reader's own cost is measured separately and
// reported alongside, because a throughput number that silently includes a
// memcpy of the whole input is a throughput number for the memcpy.
//
// Off by default: -huge runs it. The default size is 1 GB so the shape can be
// checked cheaply; -huge-bytes sets it, and the README figures are at 10e9.

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	simdjson "github.com/sebishogun/simdjson"
)

var (
	hugeRun   = flag.Bool("huge", false, "run the multi-gigabyte document tests")
	hugeBytes = flag.Int64("huge-bytes", 1e9, "how many bytes of JSON to stream")
	hugeOnly  = flag.String("huge-target", "", "run only this decode target")
)

// targets is hugeTargets filtered by -huge-target, so a profile can be taken of
// one of them without the others in it.
func targets() []hugeTarget {
	if *hugeOnly == "" {
		return hugeTargets
	}
	var out []hugeTarget
	for _, t := range hugeTargets {
		if t.name == *hugeOnly {
			out = append(out, t)
		}
	}
	return out
}

// repeatReader serves src end to end, over and over, until total bytes have
// been read. It never allocates and never holds more than src, so whatever the
// heap does during a run is the decoder's doing and not the input's.
type repeatReader struct {
	src  []byte
	off  int
	left int64
}

// repeat rounds want down to a whole number of copies of src, so the stream
// always ends where src ends. src is a whole number of records, so that is a
// record boundary; stopping anywhere else truncates one and the decoder is
// right to reject it.
func repeat(src []byte, want int64) *repeatReader {
	n := want / int64(len(src)) * int64(len(src))
	if n == 0 {
		n = int64(len(src))
	}
	return &repeatReader{src: src, left: n}
}

func (r *repeatReader) total() int64 { return r.left }

func (r *repeatReader) Read(p []byte) (int, error) {
	if r.left <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > r.left {
		p = p[:r.left]
	}
	n := copy(p, r.src[r.off:])
	r.off += n
	if r.off == len(r.src) {
		r.off = 0
	}
	r.left -= int64(n)
	return n, nil
}

// heapWatch samples the heap while something runs. Peak heap is the figure the
// README quotes and it cannot be read after the fact: the whole claim is that
// the high-water mark stays small, and a reading taken at the end would show
// that only by accident.
type heapWatch struct {
	peak atomic.Uint64
	stop chan struct{}
	done chan struct{}
}

func watchHeap() *heapWatch {
	w := &heapWatch{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(w.done)
		var ms runtime.MemStats
		t := time.NewTicker(2 * time.Millisecond)
		defer t.Stop()
		for {
			runtime.ReadMemStats(&ms)
			if ms.HeapInuse > w.peak.Load() {
				w.peak.Store(ms.HeapInuse)
			}
			select {
			case <-w.stop:
				return
			case <-t.C:
			}
		}
	}()
	return w
}

func (w *heapWatch) result() uint64 {
	close(w.stop)
	<-w.done
	return w.peak.Load()
}

func report(tb testing.TB, what string, n int64, d time.Duration, peak uint64, items int64) {
	tb.Helper()
	tb.Logf("%-28s %6.2f GB  %6.2f s  %4.0f MB/s  peak heap %5.1f MB  %d items",
		what, float64(n)/1e9, d.Seconds(), float64(n)/1e6/d.Seconds(),
		float64(peak)/1e6, items)
}

// oneRecord is a line of the line-delimited document: a real object from the
// vendored corpus, so the shape is not a strawman of the author's choosing.
func oneRecord(tb testing.TB) []byte {
	tb.Helper()
	var v struct {
		Statuses []json.RawMessage `json:"statuses"`
	}
	if err := json.Unmarshal(loadCorpus(tb, "twitter"), &v); err != nil {
		tb.Fatal(err)
	}
	if len(v.Statuses) == 0 {
		tb.Fatal("twitter.json has no statuses")
	}
	return bytes.Join([][]byte{v.Statuses[0]}, nil)
}

// TestHugeReaderFloor is what the input costs with no parsing at all. Every
// throughput below is bounded by this, and reporting them without it would
// credit the decoder for the machine's memory bandwidth.
func TestHugeReaderFloor(t *testing.T) {
	if !*hugeRun {
		t.Skip("-huge not set")
	}
	rec := oneRecord(t)
	r := repeat(rec, *hugeBytes)
	start := time.Now()
	n, err := io.Copy(io.Discard, r)
	d := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	report(t, "reader floor, no parse", n, d, 0, 0)
}

// hugeTargets are the things a record can be decoded into. Throughput at this
// scale is decided by this choice far more than by the parser, which is exactly
// what the README's figures omitted: a number with no target named is not a
// number anyone can reproduce.
//
// Value builds no Go value at all, so it is the parser's own rate. The struct
// takes four fields and ignores the rest. map[string]any builds a map and an
// interface per field and is the most expensive thing anyone actually asks for.
type hugeTarget struct {
	name   string
	decode func(*simdjson.Decoder) error
}

var hugeTargets = []hugeTarget{
	{"Value", func(d *simdjson.Decoder) error { _, err := d.Value(); return err }},
	{"struct", func(d *simdjson.Decoder) error {
		var v struct {
			ID    int64  `json:"id"`
			Text  string `json:"text"`
			Lang  string `json:"lang"`
			Trunc bool   `json:"truncated"`
		}
		return d.Decode(&v)
	}},
	{"map[string]any", func(d *simdjson.Decoder) error {
		var v map[string]any
		return d.Decode(&v)
	}},
}

// TestHugeLineDelimited is the README's "10 GB, line-delimited" row: a Decoder
// reading records one after another with nothing between them.
func TestHugeLineDelimited(t *testing.T) {
	if !*hugeRun {
		t.Skip("-huge not set")
	}
	rec := append(oneRecord(t), '\n')
	src := bytes.Repeat(rec, 1+(1<<20)/len(rec)) // ~1 MB of whole records

	for _, tgt := range targets() {
		r := repeat(src, *hugeBytes)
		total := r.total()

		w := watchHeap()
		start := time.Now()
		dec := simdjson.NewDecoder(r)
		var count int64
		for {
			err := tgt.decode(dec)
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("%s: after %d records at %d bytes: %v",
					tgt.name, count, dec.InputOffset(), err)
			}
			count++
		}
		d := time.Since(start)
		peak := w.result()

		if want := total / int64(len(rec)); count != want {
			t.Errorf("%s: decoded %d records, want exactly %d", tgt.name, count, want)
		}
		report(t, "line-delimited, "+tgt.name, total, d, peak, count)
	}
}

// TestHugeSingleArray is the README's "10 GB, one array" row: one array far
// past what Parse can index, opened with Token and drained with Decode.
func TestHugeSingleArray(t *testing.T) {
	if !*hugeRun {
		t.Skip("-huge not set")
	}
	rec := oneRecord(t)
	// A body of whole records separated by commas, so any rotation of it that
	// the repeating reader produces is still a valid element sequence.
	body := bytes.Repeat(append(append([]byte{}, rec...), ','), 1+(1<<20)/(len(rec)+1))

	for _, tgt := range targets() {
		rr := repeat(body, *hugeBytes)
		bodyBytes := rr.total()
		total := bodyBytes + int64(len(rec)) + 2
		r := io.MultiReader(
			bytes.NewReader([]byte{'['}),
			rr,
			bytes.NewReader(append(append([]byte{}, rec...), ']')),
		)

		w := watchHeap()
		start := time.Now()
		dec := simdjson.NewDecoder(r)
		tok, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		if delim, ok := tok.(simdjson.Delim); !ok || delim != '[' {
			t.Fatalf("first token %v, want [", tok)
		}
		var count int64
		for dec.More() {
			if err := tgt.decode(dec); err != nil {
				t.Fatalf("%s: after %d elements at %d bytes: %v",
					tgt.name, count, dec.InputOffset(), err)
			}
			count++
		}
		if tok, err = dec.Token(); err != nil {
			t.Fatalf("%s: closing token after %d elements: %v", tgt.name, count, err)
		}
		if delim, ok := tok.(simdjson.Delim); !ok || delim != ']' {
			t.Fatalf("%s: last token %v, want ]", tgt.name, tok)
		}
		d := time.Since(start)
		peak := w.result()

		if want := bodyBytes/int64(len(rec)+1) + 1; count != want {
			t.Errorf("%s: decoded %d elements, want exactly %d", tgt.name, count, want)
		}
		report(t, "one array, "+tgt.name, total, d, peak, count)
	}
}

// TestHugeSmallElements is the README's "13.4 M small elements" row. Same
// bytes, elements three orders of magnitude smaller, which is the case where
// per-element cost rather than bandwidth decides the throughput.
func TestHugeSmallElements(t *testing.T) {
	if !*hugeRun {
		t.Skip("-huge not set")
	}
	var b []byte
	for i := 0; len(b) < 1<<20; i++ {
		b = append(b, fmt.Sprintf(`{"i":%d,"s":"r%d","b":true},`, i, i)...)
	}
	tail := []byte(`{"i":0,"s":"r0","b":true}]`)
	rr := repeat(b, *hugeBytes)
	total := rr.total() + int64(len(tail)) + 1
	r := io.MultiReader(
		bytes.NewReader([]byte{'['}),
		rr,
		bytes.NewReader(tail),
	)

	w := watchHeap()
	start := time.Now()
	dec := simdjson.NewDecoder(r)
	if _, err := dec.Token(); err != nil {
		t.Fatal(err)
	}
	var count int64
	for dec.More() {
		var v struct {
			I int    `json:"i"`
			S string `json:"s"`
			B bool   `json:"b"`
		}
		if err := dec.Decode(&v); err != nil {
			t.Fatalf("after %d elements at %d bytes: %v", count, dec.InputOffset(), err)
		}
		count++
	}
	d := time.Since(start)
	peak := w.result()

	if count == 0 {
		t.Fatal("no elements")
	}
	report(t, "small elements", total, d, peak, count)
}
