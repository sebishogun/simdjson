package simdjson

// The two limits the index has, tested at the size where they actually bite.
//
// Both of these were latent for the whole life of the library and neither was
// reachable from any test in it, because reaching them takes more than a
// gigabyte of input. TestBracketStackOverflow is the one that mattered: it is a
// panic, on valid JSON, inside the size this is documented to accept.

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

// The bracket stack packs the entry index and the opening kind into one word,
// so the index only gets word-width-minus-one bits. At int32 that overflowed the
// sign bit at 2^30 entries -- and 2^30 entries fit in 1.5 GiB of "[[],[],...]",
// which is inside the documented 2 GiB limit. It panicked with
// "index out of range [-1073741823]" rather than parsing or refusing.
//
// The document has to be this big. There is no scaled-down version: the bug is
// the entry count crossing 2^30 and nothing else triggers it.
func TestBracketStackOverflow(t *testing.T) {
	if testing.Short() {
		t.Skip("needs 1.5 GiB of input and about 16 GiB of heap")
	}
	if os.Getenv("SIMDJSON_HUGE") == "" {
		t.Skip("set SIMDJSON_HUGE=1; needs about 16 GiB of heap")
	}

	// Entries are 2N+2 and the last opening bracket is at 2N-1, so N must clear
	// 2^29 for an opening index above 2^30 -- an opening, because only openings
	// go on the stack.
	const N = 1<<29 + 100
	b := make([]byte, 0, 3*N+1)
	b = append(b, '[')
	for i := 0; i < N; i++ {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, '[', ']')
	}
	b = append(b, ']')

	var p Parser
	d, err := p.Parse(b)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	n := 0
	for range d.Root().Values() {
		n++
	}
	if n != N {
		t.Fatalf("root array has %d elements, want %d", n, N)
	}
	b = nil
	runtime.GC()
}

// A bracket at the very top of the range, where the int32 holding its position
// is one value from overflowing. Cheap despite the size: two entries, so the
// index is nothing and only the document itself is allocated.
func TestPositionAtMaxDocument(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates 2 GiB")
	}
	if os.Getenv("SIMDJSON_HUGE") == "" {
		t.Skip("set SIMDJSON_HUGE=1; allocates 2 GiB")
	}
	b := make([]byte, maxDocument)
	b[0] = '['
	for i := 1; i < len(b)-1; i++ {
		b[i] = ' '
	}
	b[len(b)-1] = ']'

	var p Parser
	d, err := p.Parse(b)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	n := 0
	for range d.Root().Values() {
		n++
	}
	if n != 0 {
		t.Fatalf("root array has %d elements, want 0", n)
	}
	if !Valid(b) {
		t.Fatal("Valid said no")
	}
	b = nil
	runtime.GC()
}

// Past the limit the answer is an error naming the way out, not a panic and not
// a wrong parse.
func TestOverMaxDocument(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates just over 2 GiB")
	}
	if os.Getenv("SIMDJSON_HUGE") == "" {
		t.Skip("set SIMDJSON_HUGE=1; allocates just over 2 GiB")
	}
	b := make([]byte, maxDocument+1)
	b[0] = '['
	for i := 1; i < len(b)-1; i++ {
		b[i] = ' '
	}
	b[len(b)-1] = ']'

	var p Parser
	if _, err := p.Parse(b); err == nil {
		t.Fatal("Parse accepted a document over maxDocument")
	} else if !strings.Contains(err.Error(), "Decoder") {
		t.Fatalf("error does not name the way out: %v", err)
	}
	b = nil
	runtime.GC()
}

// The validator carries its level word into the shared stack every 64 levels of
// nesting, and that carry is the only thing the stack is used for there. It
// used to be two int32 halves and is now one int64; a document nested past 64
// is what tells the difference.
//
// Small enough for the ordinary suite, unlike the two above, which is why the
// packing change is covered even when SIMDJSON_HUGE is unset.
func TestValidateDeepNesting(t *testing.T) {
	for _, depth := range []int{63, 64, 65, 127, 128, 129, 200, 1000} {
		for _, pair := range []struct{ open, close string }{{"[", "]"}, {"{\"a\":", "}"}} {
			doc := strings.Repeat(pair.open, depth) + "1" + strings.Repeat(pair.close, depth)
			if !Valid([]byte(doc)) {
				t.Errorf("depth %d %q: Valid said no", depth, pair.open)
			}
			// One bracket short on the right: the carry has to unwind correctly
			// for this to be rejected rather than accepted.
			bad := strings.Repeat(pair.open, depth) + "1" + strings.Repeat(pair.close, depth-1)
			if Valid([]byte(bad)) {
				t.Errorf("depth %d %q: Valid accepted an unclosed document", depth, pair.open)
			}
		}
	}
}

// Mixed nesting across the carry boundary, where a wrong level word shows up as
// a brace closing a bracket.
func TestValidateDeepMixedNesting(t *testing.T) {
	for _, depth := range []int{64, 65, 128, 129, 300} {
		var open, close strings.Builder
		for i := 0; i < depth; i++ {
			if i%2 == 0 {
				open.WriteString("[")
				close.WriteString("]")
			} else {
				open.WriteString("{\"a\":")
				close.WriteString("}")
			}
		}
		cs := []byte(close.String())
		for i, j := 0, len(cs)-1; i < j; i, j = i+1, j-1 {
			cs[i], cs[j] = cs[j], cs[i]
		}
		doc := open.String() + "1" + string(cs)
		if !Valid([]byte(doc)) {
			t.Errorf("depth %d: Valid said no", depth)
		}
		// Swap the two innermost closers so a brace closes a bracket.
		if depth >= 2 {
			b := []byte(doc)
			i := strings.Index(doc, "1") + 1
			b[i], b[i+1] = b[i+1], b[i]
			if Valid(b) {
				t.Errorf("depth %d: Valid accepted mismatched brackets", depth)
			}
		}
	}
}
