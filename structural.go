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

// structural characters, in the order they are scanned.
var structChars = [...]byte{'{', '}', '[', ']', ':', ','}

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

	need := want*(len(structChars)+2) + len(structChars) + 2
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

	var lists [len(structChars)][]int32
	total := 0
	for i, c := range structChars {
		b := take()
		n := simd.IndexAll(b, data, c)
		if n == len(b) {
			return nil, false, nil
		}
		lists[i] = b[:n]
		total += n
	}
	if cap(ix.pos) < total {
		ix.pos = make([]int32, 0, total)
	}
	ix.pos = mergeOutsideStrings(lists[:], ix.strs, ix.pos)
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

// mergeOutsideStrings merges the per-character position lists into one
// ascending list, dropping anything that falls inside a string literal.
func mergeOutsideStrings(lists [][]int32, strs []strRange, out []int32) []int32 {
	heads := make([]int, len(lists))
	si := 0
	for {
		best, bi := int32(-1), -1
		for i := range lists {
			if heads[i] < len(lists[i]) {
				if bi < 0 || lists[i][heads[i]] < best {
					best, bi = lists[i][heads[i]], i
				}
			}
		}
		if bi < 0 {
			return out
		}
		heads[bi]++

		// Advance the string cursor; ranges and positions are both ascending,
		// so this is a merge rather than a search.
		for si < len(strs) && strs[si].close < best {
			si++
		}
		if si < len(strs) && best > strs[si].open && best < strs[si].close {
			continue // inside a string: text, not structure
		}
		out = append(out, best)
	}
}
