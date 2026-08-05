package simdjson

// Tests for loadWindow, the path that takes a batch boundary from the
// structural index instead of finding it by scanning bytes first.
//
// It only runs when the buffer holds a whole streamChunk ahead of the read
// point, which at 64 KB no other test in this package comes close to. Every one
// of them passed with loadWindow present and passed before it existed, which
// means none of them was testing it. So these shrink streamChunk to a few dozen
// bytes, where ordinary inputs cross many windows and the boundaries land in
// awkward places -- inside strings, between an element and its comma, on the
// closing bracket -- and then check the whole thing against encoding/json.

import (
	"bytes"
	stdjson "encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
)

// withStreamChunk runs fn with the batch size set to n.
func withStreamChunk(t *testing.T, n int, fn func()) {
	t.Helper()
	old := streamChunk
	streamChunk = n
	defer func() { streamChunk = old }()
	fn()
}

// windowChunks are sizes to run everything at. The small ones put a window
// boundary every few bytes; 0 is a marker for "leave it alone", so the same
// table also covers the shipped configuration.
var windowChunks = []int{4, 7, 8, 16, 31, 64, 128, 1024, 0}

// TestWindowMatchesStdlib is TestDecoderMatchesStdlib again at every window
// size, which is what makes it a test of loadWindow rather than a test that
// happens to have loadWindow compiled in.
func TestWindowMatchesStdlib(t *testing.T) {
	for _, n := range windowChunks {
		run := func() {
			for _, in := range streamInputs {
				got, gErr := decodeAll(NewDecoder(strings.NewReader(in)))
				want, wErr := decodeAll(stdjson.NewDecoder(strings.NewReader(in)))
				if len(got) != len(want) {
					t.Errorf("chunk=%d Decode(%q) got %d values %q, want %d %q",
						n, in, len(got), got, len(want), want)
					continue
				}
				for i := range got {
					var g, w bytes.Buffer
					if err := stdjson.Compact(&g, []byte(got[i])); err != nil {
						t.Fatalf("chunk=%d our value %q is not JSON: %v", n, got[i], err)
					}
					_ = stdjson.Compact(&w, []byte(want[i]))
					if g.String() != w.String() {
						t.Errorf("chunk=%d Decode(%q) value %d = %s, want %s",
							n, in, i, g.String(), w.String())
					}
				}
				if (gErr == io.EOF) != (wErr == io.EOF) {
					t.Errorf("chunk=%d Decode(%q) stopped with %v, stdlib with %v",
						n, in, gErr, wErr)
				}
			}
		}
		if n == 0 {
			run()
			continue
		}
		withStreamChunk(t, n, run)
	}
}

// windowArrays are arrays streamed element by element. loadWindow's whole
// reason to exist is this shape, and the cases are the ones that can make an
// index-derived boundary disagree with a scanned one: brackets and commas
// inside strings, escapes at a window edge, elements that are bare scalars and
// so close no container at all, an element far larger than the window, and
// nesting deep enough that a window ends several levels down.
var windowArrays = []string{
	`[]`,
	`[{}]`,
	`[{},{},{}]`,
	`[{"a":1},{"b":2},{"c":3}]`,
	`[1,2,3,4,5]`,
	`["a","b","c"]`,
	`[true,false,null]`,
	`[{"a":1},2,"three",[4],null]`,
	`[{"s":"] , } [ \" ]"},{"s":"another ] one"}]`,
	`[{"s":"\\"},{"s":"\\\\"},{"s":"\""}]`,
	`[[[[[1]]]]]`,
	`[{"a":{"b":{"c":[1,2,{"d":"e"}]}}},{"f":1}]`,
	`[  {"a":1}  ,  {"b":2}  ,  {"c":3}  ]`,
	"[\n{\"a\":1},\n{\"b\":2}\n]",
	`[{"n":1.5e10},{"n":-0.0},{"n":123456789012345}]`,
	`[{"u":"é 😀"},{"u":"café"}]`,
	`[""]`,
	`["","",""]`,
	`[{"k":""},{"k":""}]`,
}

// TestWindowArrayElements drains each array with Token then Decode -- the way
// the README documents for a document too large to hold -- and requires the
// same elements encoding/json's decoder gives, at every window size.
func TestWindowArrayElements(t *testing.T) {
	for _, n := range windowChunks {
		for _, in := range windowArrays {
			run := func() {
				got, gErr := tokenDecodeAll(t, NewDecoder(strings.NewReader(in)))
				want, wErr := tokenDecodeAllStd(t, stdjson.NewDecoder(strings.NewReader(in)))
				if gErr != nil || wErr != nil {
					t.Errorf("chunk=%d %s: ours %v, stdlib %v", n, in, gErr, wErr)
					return
				}
				if len(got) != len(want) {
					t.Errorf("chunk=%d %s: got %d elements %q, want %d %q",
						n, in, len(got), got, len(want), want)
					return
				}
				for i := range got {
					if got[i] != want[i] {
						t.Errorf("chunk=%d %s: element %d = %s, want %s",
							n, in, i, got[i], want[i])
					}
				}
			}
			if n == 0 {
				run()
				continue
			}
			withStreamChunk(t, n, run)
		}
	}
}

// TestWindowLargeArray is the shape the benchmark uses and the unit cases do
// not: enough elements that many windows are crossed, with an element that on
// its own is larger than several windows. The count is checked, not just the
// contents, because a boundary bug that drops or repeats one element is exactly
// what an index-derived boundary can get wrong.
func TestWindowLargeArray(t *testing.T) {
	var b strings.Builder
	b.WriteByte('[')
	const n = 500
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		switch i % 5 {
		case 0:
			fmt.Fprintf(&b, `{"i":%d,"s":"plain"}`, i)
		case 1:
			fmt.Fprintf(&b, `{"i":%d,"s":"%s"}`, i, strings.Repeat("wide ", 40))
		case 2:
			fmt.Fprintf(&b, `{"i":%d,"s":"] } , [ \" \\"}`, i)
		case 3:
			fmt.Fprintf(&b, `{"i":%d,"nested":{"a":[1,2,{"b":"c"}]}}`, i)
		default:
			fmt.Fprintf(&b, `{"i":%d}`, i)
		}
	}
	b.WriteByte(']')
	in := b.String()

	for _, chunk := range []int{8, 32, 64, 256, 4096, 0} {
		run := func() {
			got, err := tokenDecodeAll(t, NewDecoder(strings.NewReader(in)))
			if err != nil {
				t.Errorf("chunk=%d: %v", chunk, err)
				return
			}
			want, err := tokenDecodeAllStd(t, stdjson.NewDecoder(strings.NewReader(in)))
			if err != nil {
				t.Fatalf("stdlib: %v", err)
			}
			if len(got) != n || len(want) != n {
				t.Errorf("chunk=%d: got %d elements, stdlib %d, want %d",
					chunk, len(got), len(want), n)
				return
			}
			for i := range got {
				if got[i] != want[i] {
					t.Errorf("chunk=%d: element %d = %s, want %s", chunk, i, got[i], want[i])
					return
				}
			}
		}
		if chunk == 0 {
			run()
			continue
		}
		withStreamChunk(t, chunk, run)
	}
}

// TestWindowRejectsBadElement checks that shortcutting the boundary scan did
// not also shortcut the errors. An array with a broken element must fail, and
// must still hand back the elements before the broken one -- which is the
// property safeEnd exists to preserve and the easiest one to lose.
func TestWindowRejectsBadElement(t *testing.T) {
	cases := []struct {
		in   string
		good int // elements decodable before the failure
	}{
		{`[{"a":1},{"b":2},{bad},{"c":3}]`, 2},
		{`[{"a":1},{"b":}]`, 1},
		{`[{"a":1},"unterminated]`, 1},
		{`[{"a":1},{"b":2},01]`, 2},
		{`[{"a":1},{"b":"` + "\x10" + `"}]`, 1},
	}
	for _, chunk := range []int{4, 16, 64, 0} {
		for _, c := range cases {
			run := func() {
				got, gErr := tokenDecodeAll(t, NewDecoder(strings.NewReader(c.in)))
				if gErr == nil {
					t.Errorf("chunk=%d %s: accepted, want an error", chunk, c.in)
					return
				}
				want, wErr := tokenDecodeAllStd(t, stdjson.NewDecoder(strings.NewReader(c.in)))
				if wErr == nil {
					t.Fatalf("%s: stdlib accepted it, so the case is wrong", c.in)
				}
				// The stdlib is the authority on how many come back first.
				if len(got) != len(want) {
					t.Errorf("chunk=%d %s: %d elements before the error, stdlib %d",
						chunk, c.in, len(got), len(want))
				}
				for i := range got {
					if i < len(want) && got[i] != want[i] {
						t.Errorf("chunk=%d %s: element %d = %s, want %s",
							chunk, c.in, i, got[i], want[i])
					}
				}
			}
			if chunk == 0 {
				run()
				continue
			}
			withStreamChunk(t, chunk, run)
		}
	}
}

// tokenDecodeAll reads the opening bracket and then every element, as compacted
// JSON so two decoders that kept different whitespace still compare equal.
func tokenDecodeAll(t *testing.T, dec *Decoder) ([]string, error) {
	t.Helper()
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	var out []string
	for dec.More() {
		var raw stdjson.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return out, err
		}
		var c bytes.Buffer
		if err := stdjson.Compact(&c, raw); err != nil {
			return out, fmt.Errorf("element %d is not JSON: %v", len(out), err)
		}
		out = append(out, c.String())
	}
	if _, err := dec.Token(); err != nil {
		return out, err
	}
	return out, nil
}

func tokenDecodeAllStd(t *testing.T, dec *stdjson.Decoder) ([]string, error) {
	t.Helper()
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	var out []string
	for dec.More() {
		var raw stdjson.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return out, err
		}
		var c bytes.Buffer
		if err := stdjson.Compact(&c, raw); err != nil {
			return out, fmt.Errorf("element %d is not JSON: %v", len(out), err)
		}
		out = append(out, c.String())
	}
	if _, err := dec.Token(); err != nil {
		return out, err
	}
	return out, nil
}

// FuzzWindowElements is the differential for the path loadWindow is on.
//
// Neither existing stream fuzzer reaches it. loadBatch runs only when a
// container has been opened with Token, and FuzzDecoderAgainstStdlib decodes at
// the top level, where a different loader handles the framing. FuzzToken reads
// tokens and never decodes an element. So the code that this change rewrote had
// no fuzzer over it at all.
//
// Every input is run at three batch sizes for the reason given on
// windowChunks: at 64 KB a fuzzer's input is far too small to reach the window
// path, so a target that did not vary it would be green whatever loadWindow
// did.
func FuzzWindowElements(f *testing.F) {
	for _, s := range windowArrays {
		f.Add([]byte(s))
	}
	for _, s := range streamInputs {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		want, wErr := tokenDecodeAllStd(t, stdjson.NewDecoder(bytes.NewReader(data)))
		for _, chunk := range []int{0, 8, 64} {
			old := streamChunk
			if chunk != 0 {
				streamChunk = chunk
			}
			got, gErr := tokenDecodeAll(t, NewDecoder(bytes.NewReader(data)))
			streamChunk = old

			if len(got) != len(want) {
				t.Fatalf("chunk=%d: got %d elements %q (%v), want %d %q (%v)",
					chunk, len(got), got, gErr, len(want), want, wErr)
			}
			for i := range got {
				if got[i] != want[i] {
					t.Fatalf("chunk=%d: element %d = %s, want %s", chunk, i, got[i], want[i])
				}
			}
			if (gErr == nil) != (wErr == nil) {
				t.Fatalf("chunk=%d: stopped with %v, stdlib with %v", chunk, gErr, wErr)
			}
		}
	})
}

// FuzzWindowObject walks an object the documented way -- Token for the key,
// Decode for the value -- against encoding/json doing the same.
//
// Nothing covered this. Token on its own had a fuzzer and Decode inside an
// array had one, and the combination was broken for every object there is:
// Token left the colon for whoever read the value and Decode did not consume
// it, so `{"a":1}` failed on the first value. Two green fuzzers either side of
// a broken interaction.
func FuzzWindowObject(f *testing.F) {
	for _, s := range []string{
		`{"a":1}`, `{"a":1,"b":"x"}`, `{}`, `{"a":{"b":[1,2]},"c":null}`,
		`{"a":1,}`, `{"a"1}`, `{"a":}`, `{"a":1 "b":2}`, `{"":""}`,
		`{"a":"\u00e9"}`, `{"a":[{"b":1}]}`, `{"a":1:2}`,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		want, wErr := objectWalk(stdjson.NewDecoder(bytes.NewReader(data)))
		for _, chunk := range []int{0, 8, 64} {
			old := streamChunk
			if chunk != 0 {
				streamChunk = chunk
			}
			got, gErr := objectWalk(NewDecoder(bytes.NewReader(data)))
			streamChunk = old

			if got != want {
				t.Fatalf("chunk=%d: got %q (%v), want %q (%v)", chunk, got, gErr, want, wErr)
			}
			if (gErr == nil) != (wErr == nil) {
				t.Fatalf("chunk=%d: stopped with %v, stdlib with %v", chunk, gErr, wErr)
			}
		}
	})
}

// objectWalk reads an object as Token-for-the-key, Decode-for-the-value, and
// renders what it saw so two decoders can be compared on the sequence and not
// only on the final error.
func objectWalk(dec interface {
	Token() (stdjson.Token, error)
	More() bool
	Decode(any) error
}) (string, error) {
	var sb strings.Builder
	tok, err := dec.Token()
	if err != nil {
		return "", err
	}
	fmt.Fprintf(&sb, "open=%v;", tok)
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			return sb.String(), err
		}
		var v stdjson.RawMessage
		if err := dec.Decode(&v); err != nil {
			return sb.String(), err
		}
		var c bytes.Buffer
		if err := stdjson.Compact(&c, v); err != nil {
			return sb.String(), err
		}
		fmt.Fprintf(&sb, "%v=%s;", k, c.String())
	}
	if tok, err = dec.Token(); err != nil {
		return sb.String(), err
	}
	fmt.Fprintf(&sb, "close=%v", tok)
	return sb.String(), nil
}
