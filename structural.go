package simdjson

import "github.com/sebishogun/simd"

// Stage one: find every structural character in the document.
//
// This is the idea simdjson is built on. A conventional parser reads a byte,
// branches on what it is, reads the next — a dependent branch per byte, and the
// branch is unpredictable because JSON is not predictable. Stage one instead
// makes one vector pass per structural character over the whole document,
// writing the positions of every match, and stage two then walks a few thousand
// positions instead of a few million bytes.
//
// The whole difficulty is strings. A '{' inside a string is text, not
// structure, and a '"' preceded by an odd number of backslashes is text rather
// than a string boundary. Both have to be resolved before the structural
// positions mean anything, and both are resolved here.

// The structural characters, as a set for one pass.
const structSet = "{}[]:,"

// index holds the positions of everything stage two needs.
type index struct {
	pos     []int32 // structural positions, ascending, outside strings
	strs    []strRange
	scratch []int32 // backing storage for the per-character lists
}

// strRange is one string literal, from its opening quote to its closing one.
type strRange struct{ open, close int32 }

// buildIndex scans data and returns the structural positions outside strings.
//
// Six vector passes for the structural characters, two more for quotes and
// backslashes, then a merge. That is eight passes over the document where a
// byte-at-a-time parser makes one — and it is faster, because eight passes with
// no branches beat one pass with a branch per byte by a wide margin.
func buildIndex(data []byte, ix *index) (*index, error) {
	if ix == nil {
		ix = &index{}
	}
	// Guess how much room the position lists need and grow if the guess was
	// low. IndexAll stops early rather than failing when its destination fills,
	// so saturation is detectable and a retry is the whole recovery.
	//
	// The alternative is CountByte before each IndexAll to size exactly, which
	// is what this did first: eight extra passes over the document to avoid a
	// retry that almost never happens. Removing them is the difference between
	// sixteen passes and eight.
	want := len(data)/4 + 64
	for {
		out, ok, err := tryBuild(data, ix, want)
		if err != nil {
			return nil, err
		}
		if ok {
			return out, nil
		}
		want *= 2
	}
}

// tryBuild indexes data using a scratch of the given size, reporting whether it
// was large enough.
func tryBuild(data []byte, ix *index, want int) (*index, bool, error) {
	ix.pos = ix.pos[:0]
	ix.strs = ix.strs[:0]

	need := want*3 + 4
	if cap(ix.scratch) < need {
		ix.scratch = make([]int32, need)
	}
	buf := ix.scratch

	take := func() []int32 {
		out := buf[:want]
		buf = buf[want:]
		return out
	}

	// Quotes first: they decide which structural characters are real.
	qb := take()
	nq := simd.IndexAll(qb, data, '"')
	if nq == len(qb) {
		return nil, false, nil
	}
	quotes := qb[:nq]

	if nq > 0 {
		eb := take()
		ne := simd.IndexAll(eb, data, '\\')
		if ne == len(eb) {
			return nil, false, nil
		}
		if ne > 0 {
			quotes = dropEscaped(data, quotes, eb[:ne])
		}
	} else {
		take()
	}

	if len(quotes)%2 != 0 {
		return nil, false, errSyntax("unterminated string")
	}
	if cap(ix.strs) < len(quotes)/2 {
		ix.strs = make([]strRange, 0, len(quotes)/2)
	}
	for i := 0; i+1 < len(quotes); i += 2 {
		ix.strs = append(ix.strs, strRange{quotes[i], quotes[i+1]})
	}

	// Every structural character in one pass. IndexAllAny takes the whole set
	// and returns their positions already in ascending order, which also
	// removes the six-way merge that used to follow.
	//
	// This was six calls to IndexAll and a merge, which read the document six
	// times. Measuring against minio/simdjson-go — which computes the same
	// thing in one fused pass — is what made the cost obvious, and simd.go
	// v1.3.0 added the kernel to close it.
	sb := take()
	ns := simd.IndexAllAny(sb, data, structSet)
	if ns == len(sb) {
		return nil, false, nil
	}
	total := ns
	structs := sb[:ns]

	if cap(ix.pos) < total {
		ix.pos = make([]int32, 0, total)
	}
	ix.pos = dropInStrings(structs, ix.strs, ix.pos)
	return ix, true, nil
}

// dropEscaped removes quotes that are preceded by an odd number of backslashes.
//
// The parity has to be counted, not merely tested: in `"a\\"` the quote follows
// two backslashes and does close the string, while in `"a\"` it follows one and
// does not. Walking the backslash positions backwards from the quote is
// proportional to the run length rather than to the document.
func dropEscaped(data []byte, quotes, esc []int32) []int32 {
	if len(esc) == 0 {
		return quotes
	}
	out := quotes[:0]
	j := 0
	for _, q := range quotes {
		// Advance past every backslash before this quote. Afterwards esc[j-1]
		// is the last one, which is the candidate for position q-1.
		//
		// The first version advanced to the first backslash at or after q-1 and
		// then counted back from j-1, which skips the backslash *at* q-1 —
		// so an escaped quote was read as unescaped and every string after it
		// was misaligned.
		for j < len(esc) && esc[j] < q {
			j++
		}
		run := int32(0)
		for k := j - 1; k >= 0 && esc[k] == q-1-run; k-- {
			run++
		}
		if run%2 == 0 {
			out = append(out, q)
		}
	}
	return out
}

// dropInStrings keeps the structural positions that fall outside string
// literals.
//
// A '{' inside a string is text. The positions and the string ranges are both
// ascending, so this is a merge rather than a search: one cursor over each.
//
// It replaced a six-way merge, which existed only because the positions arrived
// as six separate ascending lists. One pass produces one list already in order.
func dropInStrings(pos []int32, strs []strRange, out []int32) []int32 {
	si := 0
	for _, p := range pos {
		for si < len(strs) && strs[si].close < p {
			si++
		}
		if si < len(strs) && p > strs[si].open && p < strs[si].close {
			continue
		}
		out = append(out, p)
	}
	return out
}
