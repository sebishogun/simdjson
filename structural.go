package simdjson

import (
	"encoding/binary"
	"math/bits"

	"github.com/sebishogun/simd"
)

// Stage one: classify the document with vector compares, and answer everything
// that follows with bit arithmetic.
//
// This is the idea simdjson is built on. A conventional parser reads a byte,
// branches on what it is, reads the next — a dependent branch per byte, and the
// branch is unpredictable because JSON is not predictable. Stage one has no
// per-byte branch at all: three vector passes produce a bitmask each, and the
// questions after them are shifts, adds and and-nots over sixty-four bytes at a
// time.
//
// What it emits is deliberately small: the position of every bracket outside a
// string, and each bracket paired with its partner. Not every structural
// character, and not every token — both were tried and both are slower here.
// See structSet and docs/wrong.md.
//
// The whole difficulty is strings. A '{' inside a string is text, not
// structure, and a '"' preceded by an odd number of backslashes is text rather
// than a string boundary. Both are resolved here, and both are resolved as bit
// arithmetic over sixty-four bytes at a time.
//
// # Why bitmasks and not offset lists
//
// The first three versions of this produced *lists of offsets* — one pass per
// character class writing the position of every match. That is the natural
// thing to build on simd.IndexAll, and it is the wrong representation. A JSON
// document is around 40% structural characters, so the offset list came out
// four times the size of the document it described, and every question asked
// afterwards cost a scalar step per entry: dropping escaped quotes walked the
// backslash list, dropping structural characters inside strings merged two
// ascending lists, and splitting one combined list by class read the document
// back at every position found.
//
// None of that was a cache problem, which is what made it worth measuring
// rather than guessing about. perf put branch misses at 0.006% and cache misses
// at 1.1% with an IPC of 5.83: nothing was stalling, the code was simply
// issuing 72 instructions per byte of input. Windowing the input to keep it in
// L1 changed nothing at all — a sweep from 4 KiB to 64 MiB came out flat.
//
// The bitmask form asks the same questions with arithmetic instead. "Which
// quotes are escaped" is an add and a couple of shifts. "Which bytes are inside
// a string" is a prefix XOR. "Which structural characters survive" is an
// and-not. All of them run sixty-four bytes at a time with no per-match work,
// and simd.MaskBits produces the input to them at 139 GB/s against IndexAll's
// 4.2 GB/s, because a compare into a predicate register and a store of that
// predicate is two instructions per sixty-four bytes and a compress-store is
// not.

// The whitespace JSON allows between tokens. Exactly these four: anything else
// below 0x20 is a syntax error there, not something to skip past.
const wsSet = " \t\n\r"

// The characters stage one indexes.
//
// Brackets only, not the full `{}[]:,`. The index has exactly one consumer —
// matchBracket, which is only ever asked about the offset of an opening
// bracket — so the colons and commas were 190,000 of the 230,000 positions
// extracted from a 10,000-item document and none of them was ever read. The
// mask pass costs the same either way, since MaskBitsAny compares eight bytes
// whether or not the caller supplies eight; what changes is how many bits the
// extraction loop has to walk.
//
// If stage two ever needs to step token by token, this is where that starts —
// and see docs/wrong.md for why indexing every token start was worse.
const structSet = "{}[]"

// index holds the positions of everything stage two needs.
type index struct {
	pos []int32 // bracket positions, ascending, outside strings

	// inStr is one bit per byte of the document, set where that byte is inside
	// a string literal. The opening quote of a string is set and the closing
	// quote is not, which is what makes finding the end of a string the search
	// for the next clear bit rather than a lookup in a list of ranges.
	inStr []uint64

	// Raw masks from the scan, consumed word by word to produce inStr and pos.
	// They are fields rather than locals so the buffers survive between parses.
	quote, esc, structural []byte

	// Masks used only by validateStrings, kept here for the same reason.
	ctl []byte

	// masks is the one buffer the five above are slices of. simd.JSONMasks
	// writes them end to end in a fixed order, so they are carved out of it
	// rather than allocated apart.
	masks []byte

	// ws is the whitespace mask as words, and noWS records that the document
	// has none at all outside its strings.
	//
	// Most JSON in flight is machine-generated and has none, and proving that
	// once turns every whitespace skip in stage two — several per field, and
	// the largest single item in the profile — into a branch that always goes
	// the same way. Worth about 52 us of the descent on a 1.17 MB document.
	//
	// It is computed in validateStrings rather than in buildIndex, because a
	// MaskBitsAny pass there cost Scan 36 us to save Parse 16 — Scan would be
	// paying for something only Parse uses.
	//
	// The mask is kept, not just its emptiness, because skipping whitespace is
	// then a bit scan rather than a byte loop. citm_catalog.json is 71%
	// whitespace and spent 37% of its parse in that loop.
	//
	// It has to be the exact four bytes JSON allows. An earlier version built
	// it from the control-character mask, which is cheaper and wrong: every
	// control byte below 0x20 would have been skipped as whitespace, so a NUL
	// between two tokens would have been accepted.
	ws   []byte
	wsw  []uint64
	noWS bool
	// wsCount is how many bytes of whitespace lie outside the strings. Valid
	// uses it to choose between walking the mask and walking the document; see
	// validate.go. It is a popcount per word in a loop that already runs.
	wsCount int

	// wsW and wsX are the last word of ws skipRun looked at, already inverted,
	// and which word it was.
	//
	// A word covers 64 bytes of document and tokens are far closer together
	// than that -- citm_catalog.json averages about nine bytes between them --
	// so six or seven consecutive skips land in the same word, and without this
	// each of them re-loads it. For a 1.7 MB document the mask is 216 KB, too
	// big for L1: an L2 access per token for a value already in a register.
	//
	// They live here rather than on Doc, where they belong logically, because
	// Doc is read by everything and sixteen more bytes of it cost about 1% on
	// Valid/citm -- 412,943 ns against 417,487, three runs each, the control
	// checked against the recorded baseline first.
	//
	// It is close. Doc placement is about 1% BETTER on Parse/twitter (221,943
	// against 220,647), so this is a wash chosen on the benchmark with more
	// whitespace in it rather than a clear win. An earlier note here said 4.8%;
	// that number came from an A/B script whose columns were inverted, and the
	// direction survived re-measurement while the magnitude did not.
	//
	// wsW is reset to -1 for every document, because an index is reused between
	// them and word zero of the last one is not word zero of this one.
	wsW int
	wsX uint64

	match []int32 // for each entry of pos, the index of its partner bracket
	// stack holds one entry per open bracket: the entry index shifted left one,
	// with the opening kind in the low bit. int64 rather than int32 because the
	// shift is what sets the real limit -- an int32 entry overflows its sign bit
	// at 2^30 entries, which "[[],[],...]" reaches in a 1.5 GiB document, well
	// inside the 2 GiB this is documented to accept. That panicked with
	// "index out of range [-1073741823]".
	//
	// Widening costs nothing: this is one entry per level of nesting, not one
	// per bracket, so it is the smallest array here by several orders.
	stack []int64

	// The three fields below are only written in partial mode, where the input
	// is a prefix of a document rather than a document.
	//
	// A prefix ends wherever the buffer ended: inside a string, inside a
	// container, in the middle of a number. The masks before that point are
	// still correct — they are computed left to right and nothing later can
	// change them — so the answer to "index this prefix" is not an error, it is
	// an index plus a mark saying how far it can be trusted.
	//
	// safeEnd is one past the last top-level container that closed. Everything
	// before it is a whole number of complete values.
	//
	// partErr is the first syntax error found, and partErrAt where. It is not
	// returned unless it lies before safeEnd: a bad escape in the truncated
	// tail belongs to a value that has not finished arriving, and reporting it
	// now would reject input that is about to become valid.
	safeEnd   int
	partErr   error
	partErrAt int
}

// buildIndex scans data and returns the bracket positions outside strings,
//
// Three vector passes and one word-at-a-time pass. The vector passes are
// simd.MaskBits, which is a compare and a predicate store; the word pass is the
// escape and string arithmetic plus the extraction of the surviving positions.
//
// Only the brackets are indexed — see structSet for why the colons and commas
// are not, and docs/wrong.md for why indexing every *token* start, which is
// what C++ simdjson's pseudo-structural characters are, is slower here.
// buildIndex indexes data, choosing its strategy by size.
//
// The two paths differ only in whether the masks are built for the whole
// document or a window at a time, and the choice is made on one number: whether
// the masks will stay in cache. Below the limit the whole-document path is
// faster because it has no window bookkeeping; above it, it falls off a cliff
// and the windowed path stays flat. See wholeDocMax.
// masksOnly is the third thing buildIndex can be asked for, alongside indexing
// and validating.
//
// Valid, Compact and Indent all walk the document from the front and none of
// them navigates it, so none of them ever reads a bracket position or its
// partner — the grammar descent proves the brackets nest by expecting them, and
// the two copying functions only ever consult the whitespace and in-string
// masks. Extracting the positions and pairing them is then two int32 writes per
// bracket and a stack, for arrays nobody reads.
const masksOnly = true

func buildIndex(data []byte, ix *index, validate bool) (*index, error) {
	return buildIndexMode(data, ix, validate, false, false)
}

func buildIndexMode(data []byte, ix *index, validate, noBrackets, partial bool) (*index, error) {
	if ix == nil {
		ix = &index{}
	}
	ix.pos = ix.pos[:0]
	ix.wsW, ix.wsX = -1, 0
	ix.safeEnd, ix.partErr, ix.partErrAt = 0, nil, 0
	// Cleared here rather than in the validating half, because Scan never runs
	// that — a Parser reused for Scan after a Parse would otherwise carry the
	// previous document's answer and skip whitespace that is really there.
	ix.noWS = false
	if len(data) == 0 {
		ix.inStr, ix.match = ix.inStr[:0], ix.match[:0]
		return ix, nil
	}
	// A bracket position is an int32, so the index cannot describe a document
	// larger than one will hold. Found by running a 10 GB file through it,
	// which panicked in the windowed path with a negative index rather than
	// saying anything useful.
	//
	// int32 is not a mistake to be corrected. The index is already 0.93x the
	// document; int64 positions would take it past 1.4x and cost every ordinary
	// parse to serve a size that has a better answer. That answer is Decoder,
	// which reads a stream in 64 KiB buffers and has no limit at all.
	if len(data) > maxDocument {
		return ix, errSyntax("document larger than 2 GiB; use a Decoder, which streams and has no limit")
	}
	// Large whole documents go parallel when the machine allows it; anything
	// the parallel path declines -- too small, one core, or input that defeats
	// the boundary snap -- runs the serial paths unchanged. Partial mode never
	// goes parallel: safeEnd and the note mechanism are left-to-right by
	// definition.
	if !partial && !noBrackets && len(data) >= parallelMinBytes {
		if px, err, ok := buildIndexParallel(data, ix, validate); ok {
			return px, err
		}
	}
	if len(data) <= wholeDocMax {
		return buildIndexWhole(data, ix, validate, noBrackets, partial)
	}
	return buildIndexWindowed(data, ix, validate, noBrackets, partial)
}

// indexWordsPlain resolves the in-string mask for a document that is being
// indexed and not validated — Scan's path, and the only one that reaches here.
//
// It returns where it stopped and the three running values the tail loop needs
// to carry on: the escape carry, the string carry, and how many bracket bits
// survived outside strings.
//
// It is a function of its own rather than a branch inside buildIndexWhole
// because the validating body is forty lines longer and the two do not belong
// in one instruction stream. Both other arrangements were measured: one loop
// with `if !validate { continue }` in it cost Scan 3.7% to 6.1%, and two loops
// in the same function still cost it 0.6% to 1.8% with the loop itself
// unchanged.
func indexWordsPlain(ix *index, nw int, noBrackets bool) (int, uint64, uint64, int) {
	var prevEsc, strCarry uint64
	total := 0
	w0 := 0
	for ; w0+2 <= nw; w0 += 2 {
		off := w0 * 8
		e0 := escapedMask(binary.LittleEndian.Uint64(ix.esc[off:]), &prevEsc)
		e1 := escapedMask(binary.LittleEndian.Uint64(ix.esc[off+8:]), &prevEsc)
		x0 := binary.LittleEndian.Uint64(ix.quote[off:]) &^ e0
		x1 := binary.LittleEndian.Uint64(ix.quote[off+8:]) &^ e1
		x0 ^= x0 << 1
		x1 ^= x1 << 1
		x0 ^= x0 << 2
		x1 ^= x1 << 2
		x0 ^= x0 << 4
		x1 ^= x1 << 4
		x0 ^= x0 << 8
		x1 ^= x1 << 8
		x0 ^= x0 << 16
		x1 ^= x1 << 16
		x0 ^= x0 << 32
		x1 ^= x1 << 32
		in0 := x0 ^ strCarry
		in1 := x1 ^ uint64(int64(in0)>>63)
		strCarry = uint64(int64(in1) >> 63)
		ix.inStr[w0], ix.inStr[w0+1] = in0, in1
		if !noBrackets {
			total += bits.OnesCount64(binary.LittleEndian.Uint64(ix.structural[off:]) &^ in0)
			total += bits.OnesCount64(binary.LittleEndian.Uint64(ix.structural[off+8:]) &^ in1)
		}
	}
	return w0, prevEsc, strCarry, total
}

// checkEscapes validates the escape sequences whose backslashes are marked in
// target, which are the ones inside strings in the word starting at base.
//
// It is a function rather than the loop it used to be inline because the word
// loop is unrolled two at a time and this is the one part too long to duplicate.
// The call costs nothing worth measuring: target is zero for every word of a
// document with no escapes in its strings, which is most words of most
// documents, and the caller does not call at all in that case.
func (ix *index) checkEscapes(data []byte, base int, target uint64, partial bool) error {
	for t := target; t != 0; t &= t - 1 {
		p := base + bits.TrailingZeros64(t)
		if p >= len(data) {
			if !partial {
				return errSyntax("string ends in a backslash")
			}
			ix.note(p, "string ends in a backslash")
			continue
		}
		switch data[p] {
		case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
		case 'u':
			if p+4 >= len(data) {
				if !partial {
					return errSyntax("short \\u escape")
				}
				ix.note(p, "short \\u escape")
				continue
			}
			for k := 1; k <= 4; k++ {
				if !isHex(data[p+k]) {
					if !partial {
						return errSyntax("invalid \\u escape")
					}
					ix.note(p, "invalid \\u escape")
					break
				}
			}
		default:
			if !partial {
				return errSyntax("invalid escape")
			}
			ix.note(p, "invalid escape")
		}
	}
	return nil
}

// note records a syntax error found while indexing a prefix, keeping the first.
//
// It is not returned unless it turns out to lie before safeEnd. A bad escape in
// the truncated tail belongs to a value that has not finished arriving, and
// rejecting it now would reject input that is about to become valid.
func (ix *index) note(at int, msg string) {
	if ix.partErr == nil {
		ix.partErr, ix.partErrAt = errSyntax(msg), at
	}
}

// buildIndexWhole indexes a document small enough to hold its masks in cache.
//
// Five vector passes over the whole input, then two passes over the masks. That
// is the shape to use when the masks are never going to leave cache, and it is
// measurably better than doing the same work through the windowed loop below —
// about 9% on a 1 MB document — because there is no window bookkeeping.
func buildIndexWhole(data []byte, ix *index, validate, noBrackets, partial bool) (*index, error) {
	// Whole words, so the arithmetic below never has to special-case the last
	// one. The bytes between the end of the document and the end of its last
	// word are zeroed rather than left over, so they match nothing.
	nw := (len(data) + 63) / 64
	// All five masks from one pass. Asking for them one at a time is five
	// passes over the document and five dispatches; simd.JSONMasks is one load
	// per block and five predicate stores, and it is about twice as fast --
	// 2 MB goes from 102,230 ns to 46,205, and the three an index-only pass
	// wants from 27,066 to 12,456.
	//
	// The five regions are laid out end to end whatever is asked for, so the
	// slices below are the same offsets either way, and a mask nobody wants is
	// simply not written. That is what the bracket mask needed: Valid, Compact
	// and Indent were paying a whole pass, and a popcount per word of it, for
	// something nothing then looked at.
	stride := simd.MaskWords(len(data))
	need := 5 * stride
	if cap(ix.masks) < need {
		ix.masks = make([]byte, need)
	}
	ix.masks = ix.masks[:need]
	want := uint32(simd.JSONMaskQuote | simd.JSONMaskEscape)
	if !noBrackets {
		want |= simd.JSONMaskStructural
	}
	if validate {
		want |= simd.JSONMaskControl | simd.JSONMaskSpace
	}
	simd.JSONMasks(ix.masks, data, want)
	ix.quote = ix.masks[0:stride]
	ix.esc = ix.masks[stride : 2*stride]
	ix.structural = ix.masks[2*stride : 3*stride]
	ix.ctl = ix.masks[3*stride : 4*stride]
	ix.ws = ix.masks[4*stride : 5*stride]
	// No tail to clear: the regions are whole words and the kernel zeroes the
	// bytes past the document, which is why its stride is word-aligned rather
	// than (n+7)/8.
	if validate {
		if cap(ix.wsw) < nw {
			ix.wsw = make([]uint64, nw)
		}
		ix.wsw = ix.wsw[:nw]
	} else {
		ix.wsw = nil
	}

	if cap(ix.inStr) < nw {
		ix.inStr = make([]uint64, nw)
	}
	ix.inStr = ix.inStr[:nw]

	// Two passes over the words: the first resolves strings and counts what
	// survives, the second writes it. Counting first is what lets the second
	// pass write by index into an exactly-sized array instead of appending —
	// no capacity check per position, and no growth.
	var prevEsc, strCarry, anyWS, prevLead uint64
	wsCount := 0
	total := 0
	// Two words at a time. The six shift-XOR steps below are a twelve-operation
	// dependency chain, and the chains for two different words do not depend on
	// each other — only the one-bit carry between them does, and that is one
	// XOR. Interleaving two of them keeps the shifters busy where one leaves
	// them waiting.
	//
	// Two, not four or eight. Measured on 27,000 words in isolation:
	// one 26,928 ns, two 21,933, four 22,087, eight 22,404. All of the gain is
	// there at two, and it unwinds slowly after.
	//
	// The per-word work after the chain is written out twice rather than looped,
	// which is the price of the unroll. Only the escape check is factored out,
	// because it is the one part long enough to be worth a call and it is
	// skipped entirely for any word whose strings hold no backslash.
	//
	// There are two of these loops and not one with a branch in it, which was
	// tried and cost Scan between 3.7% and 6.1%. A single loop carrying the
	// validating body behind `if !validate { continue }` still has to be
	// fetched, and Scan pays for forty lines it never runs. The duplication
	// here is the prefix XOR, which is a dozen lines and has not changed since
	// it was written; the alternative was making the path that does the least
	// work carry the code for the path that does the most.
	// And they are two *functions*, not two loops in one, for the same reason
	// one step further out. Sharing a function cost Scan another 0.6% to 1.8%
	// with its loop byte-for-byte unchanged — a function that holds both bodies
	// is a bigger function, and where its code lands is not something either
	// path chose. Given a body of its own, the plain path is small again.
	w0 := 0
	if !validate {
		w0, prevEsc, strCarry, total = indexWordsPlain(ix, nw, noBrackets)
		goto tail
	}
	for ; w0+2 <= nw; w0 += 2 {
		off := w0 * 8
		bs0 := binary.LittleEndian.Uint64(ix.esc[off:])
		bs1 := binary.LittleEndian.Uint64(ix.esc[off+8:])
		e0 := escapedMask(bs0, &prevEsc)
		e1 := escapedMask(bs1, &prevEsc)
		x0 := binary.LittleEndian.Uint64(ix.quote[off:]) &^ e0
		x1 := binary.LittleEndian.Uint64(ix.quote[off+8:]) &^ e1
		x0 ^= x0 << 1
		x1 ^= x1 << 1
		x0 ^= x0 << 2
		x1 ^= x1 << 2
		x0 ^= x0 << 4
		x1 ^= x1 << 4
		x0 ^= x0 << 8
		x1 ^= x1 << 8
		x0 ^= x0 << 16
		x1 ^= x1 << 16
		x0 ^= x0 << 32
		x1 ^= x1 << 32
		in0 := x0 ^ strCarry
		in1 := x1 ^ uint64(int64(in0)>>63)
		strCarry = uint64(int64(in1) >> 63)
		ix.inStr[w0], ix.inStr[w0+1] = in0, in1

		if !noBrackets {
			total += bits.OnesCount64(binary.LittleEndian.Uint64(ix.structural[off:]) &^ in0)
			total += bits.OnesCount64(binary.LittleEndian.Uint64(ix.structural[off+8:]) &^ in1)
		}
		// The string checks ride along here rather than in a pass of their own.
		// They need the escape mask and the in-string mask, and both are in
		// registers at this point — a separate pass recomputed escapedMask for
		// every word of the document to get back what this loop had already
		// worked out.
		if c := binary.LittleEndian.Uint64(ix.ctl[off:]) & in0; c != 0 {
			if !partial {
				return nil, errSyntax("control character in string")
			}
			ix.note(w0*64+bits.TrailingZeros64(c), "control character in string")
		}
		wsw0 := binary.LittleEndian.Uint64(ix.ws[off:])
		ix.wsw[w0] = wsw0
		outWS0 := wsw0 &^ in0
		anyWS |= outWS0
		wsCount += bits.OnesCount64(outWS0)
		leaders0 := bs0 & in0 &^ e0
		target0 := leaders0<<1 | prevLead
		prevLead = leaders0 >> 63
		if target0 != 0 {
			if err := ix.checkEscapes(data, w0*64, target0, partial); err != nil {
				return nil, err
			}
		}

		if c := binary.LittleEndian.Uint64(ix.ctl[off+8:]) & in1; c != 0 {
			if !partial {
				return nil, errSyntax("control character in string")
			}
			ix.note((w0+1)*64+bits.TrailingZeros64(c), "control character in string")
		}
		wsw1 := binary.LittleEndian.Uint64(ix.ws[off+8:])
		ix.wsw[w0+1] = wsw1
		outWS1 := wsw1 &^ in1
		anyWS |= outWS1
		wsCount += bits.OnesCount64(outWS1)
		leaders1 := bs1 & in1 &^ e1
		target1 := leaders1<<1 | prevLead
		prevLead = leaders1 >> 63
		if target1 != 0 {
			if err := ix.checkEscapes(data, (w0+1)*64, target1, partial); err != nil {
				return nil, err
			}
		}
	}
tail:
	// Whatever the unrolled loop above could not take in pairs: at most one
	// word, and every word when the document is smaller than two.
	for w := w0; w < nw; w++ {
		off := w * 8
		bs := binary.LittleEndian.Uint64(ix.esc[off:])
		escaped := escapedMask(bs, &prevEsc)

		// A quote that is escaped is text. Everything after this line treats
		// the survivors as the real string boundaries.
		q := binary.LittleEndian.Uint64(ix.quote[off:]) &^ escaped

		// Inclusive prefix XOR: bit i becomes the parity of all quote bits at
		// or below i, which is exactly "an odd number of quotes have opened, so
		// I am inside a string". Six shift-xor steps cover sixty-four bits.
		x := q
		x ^= x << 1
		x ^= x << 2
		x ^= x << 4
		x ^= x << 8
		x ^= x << 16
		x ^= x << 32
		in := x ^ strCarry
		// Sign-extending the top bit carries the state into the next word: all
		// ones if the document is inside a string at the word boundary.
		strCarry = uint64(int64(in) >> 63)
		ix.inStr[w] = in

		if !noBrackets {
			total += bits.OnesCount64(binary.LittleEndian.Uint64(ix.structural[off:]) &^ in)
		}

		if !validate {
			continue
		}
		// The string checks ride along here rather than in a pass of their own.
		// They need the escape mask and the in-string mask, and both are in
		// registers at this point — a separate pass recomputed escapedMask for
		// every word of the document to get back what this loop had already
		// worked out.
		if c := binary.LittleEndian.Uint64(ix.ctl[off:]) & in; c != 0 {
			if !partial {
				return nil, errSyntax("control character in string")
			}
			ix.note(w*64+bits.TrailingZeros64(c), "control character in string")
		}
		wsw := binary.LittleEndian.Uint64(ix.ws[off:])
		ix.wsw[w] = wsw
		outWS := wsw &^ in
		anyWS |= outWS
		wsCount += bits.OnesCount64(outWS)

		leaders := bs & in &^ escaped
		target := leaders<<1 | prevLead
		prevLead = leaders >> 63
		if target != 0 {
			if err := ix.checkEscapes(data, w*64, target, partial); err != nil {
				return nil, err
			}
		}
	}
	if validate {
		if prevLead != 0 && !partial {
			return nil, errSyntax("string ends in a backslash")
		}
		ix.noWS = anyWS == 0
		ix.wsCount = wsCount
	}
	if strCarry != 0 && !partial {
		// Blamed on the end of the input, which is where encoding/json puts it:
		// the string is unterminated because the document stopped, and the
		// opening quote was fine when it was read.
		return nil, errAt("unterminated string", len(data)-1)
	}

	if noBrackets {
		ix.pos, ix.match = ix.pos[:0], ix.match[:0]
		return ix, nil
	}

	// Checked separately: they are grown together here, but tying match's
	// capacity to pos's is the kind of coupling that survives one refactor and
	// then panics on the slice below.
	if cap(ix.pos) < total {
		ix.pos = make([]int32, total)
	}
	if cap(ix.match) < total {
		ix.match = make([]int32, total)
	}
	pos, match := ix.pos[:total], ix.match[:total]
	stack := ix.stack[:0]

	// Second pass: extract the bracket positions and pair them in the same
	// step.
	//
	// Pairing used to be its own pass over a parallel array of byte classes,
	// which meant writing a class per position and reading it back. Doing both
	// here reads data[p] once and uses it twice, and the class array is gone
	// entirely — that array's append was 120 ms of a 1.21 s profile, more than
	// half of stage one.
	k := 0
	for w := 0; w < nw; w++ {
		st := binary.LittleEndian.Uint64(ix.structural[w*8:]) &^ ix.inStr[w]
		base := int32(w * 64)
		for st != 0 {
			p := base + int32(bits.TrailingZeros64(st))
			pos[k] = p
			// Ascending and roughly one every few bytes, so this streams
			// through the document rather than jumping around it.
			switch data[p] {
			case '{':
				stack = append(stack, int64(k)<<1)
			case '[':
				stack = append(stack, int64(k)<<1|1)
			case '}', ']':
				if len(stack) == 0 {
					if !partial {
						return nil, errAt("unbalanced brackets", int(p))
					}
					ix.note(int(p), "unbalanced brackets")
					break
				}
				o := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				// A brace must close a brace and a bracket a bracket. The
				// opening kind rode along in the stack entry's low bit, so
				// this needs no second look at the input.
				if (o&1 == 1) != (data[p] == ']') {
					if !partial {
						return nil, errAt("mismatched brackets", int(p))
					}
					ix.note(int(p), "mismatched brackets")
					break
				}
				oi := o >> 1
				match[oi] = int32(k)
				match[k] = int32(oi)
				if len(stack) == 0 && ix.partErr == nil {
					// The top level just closed, and nothing wrong has been
					// seen yet. Everything up to here is a whole number of
					// complete values.
					//
					// It stops advancing at the first error rather than
					// advancing past it, because a caller decoding a stream
					// hands back the values before the bad one and only then
					// reports it. "0\"\x10\"[]" is a number, then a string
					// holding a control character: the number is a value and
					// the error comes after it. Advancing to the end of the []
					// would have thrown the number away with the error.
					ix.safeEnd = int(p) + 1
				}
			}
			k++
			st &= st - 1
		}
	}
	if len(stack) != 0 && !partial {
		return nil, errAt("unterminated container", len(data)-1)
	}
	ix.pos, ix.match, ix.stack = pos, match, stack
	return ix, nil
}

// buildIndexWindowed indexes a document too large for its masks to stay in
// cache, a window at a time.
//
// Whole-document masks for a 64 MB input are around 450 MB of memory traffic
// written and read back, and throughput falls off a cliff exactly where the
// document stops fitting: 9,146 MB/s at 8 MB against 2,740 at 64 MB. A window's
// masks are 40 KiB and never leave L2, so the document is read from memory once
// rather than five times, and throughput goes flat instead.
func buildIndexWindowed(data []byte, ix *index, validate, noBrackets, partial bool) (*index, error) {
	nw := (len(data) + 63) / 64

	// The masks are built a window at a time rather than for the whole
	// document, and each window is consumed before the next is built.
	//
	// Built whole, they are five arrays of n/8 bytes that stage one writes to
	// memory and then reads back — around 450 MB of traffic for a 64 MB
	// document, most of it missing cache. Throughput fell off a cliff exactly
	// where the document stopped fitting: Scan ran at 8,145 MB/s on 8 MB and
	// 2,802 MB/s on 64 MB. A window's masks are 160 KB and stay in L2, so the
	// document is read from memory once instead of five times.
	//
	// The window is a fixed size chosen for cache, not a fraction of the input
	// — the cache does not get bigger when the document does. It must be a
	// multiple of 64 so that window boundaries fall on word boundaries and the
	// carried parity below stays aligned.
	win := chunkBytes
	wnw := win / 64
	ix.quote = maskBuf(ix.quote, wnw, win)
	ix.esc = maskBuf(ix.esc, wnw, win)
	ix.structural = maskBuf(ix.structural, wnw, win)
	if validate {
		ix.ctl = maskBuf(ix.ctl, wnw, win)
		ix.ws = maskBuf(ix.ws, wnw, win)
		if cap(ix.wsw) < nw {
			ix.wsw = make([]uint64, nw)
		}
		ix.wsw = ix.wsw[:nw]
	} else {
		ix.wsw = nil
	}

	if cap(ix.inStr) < nw+1 {
		ix.inStr = make([]uint64, nw+1)
	}
	ix.inStr = ix.inStr[:nw+1]
	ix.inStr[nw] = 0

	// Reserve for the usual density rather than growing from nothing. A bracket
	// every sixteen bytes is a generous guess for real JSON, so this is one
	// allocation in the common case and append handles the rest.
	if cap(ix.pos) < len(data)/16 {
		ix.pos = make([]int32, 0, len(data)/16)
		ix.match = make([]int32, 0, len(data)/16)
	}
	pos, match := ix.pos[:0], ix.match[:0]
	stack := ix.stack[:0]

	var prevEsc, strCarry, anyWS, prevLead uint64
	wsCount := 0
	for base := 0; base < len(data); base += win {
		end := base + win
		if end > len(data) {
			end = len(data)
		}
		chunk := data[base:end]
		cnw := (len(chunk) + 63) / 64

		simd.MaskBits(ix.quote, chunk, '"')
		simd.MaskBits(ix.esc, chunk, '\\')
		simd.MaskBitsAny(ix.structural, chunk, structSet)
		if validate {
			simd.MaskBitsLess(ix.ctl, chunk, 0x20)
			simd.MaskBitsAny(ix.ws, chunk, wsSet)
		}
		// The kernels write ceil(len/8) bytes; the rest of the window's last
		// word is whatever the previous window left, so it is cleared.
		clear(ix.quote[simd.MaskLen(len(chunk)) : cnw*8])
		clear(ix.esc[simd.MaskLen(len(chunk)) : cnw*8])
		clear(ix.structural[simd.MaskLen(len(chunk)) : cnw*8])
		if validate {
			clear(ix.ctl[simd.MaskLen(len(chunk)) : cnw*8])
			clear(ix.ws[simd.MaskLen(len(chunk)) : cnw*8])
		}

		wbase := base / 64

		// Two passes over this window's words, both while its masks are hot in
		// L2. The first resolves strings and counts the brackets; the second
		// writes them by index into storage sized from that count.
		//
		// Appending instead, to avoid the counting pass, costs about 20% — more
		// than the windowing saves below 8 MB. That is worth stating plainly
		// because the first version of this did exactly that and looked like
		// the windowing was the regression.
		cnt := 0
		for w := 0; w < cnw; w++ {
			off := w * 8
			bs := binary.LittleEndian.Uint64(ix.esc[off:])
			escaped := escapedMask(bs, &prevEsc)

			// A quote that is escaped is text. Everything after this line
			// treats the survivors as the real string boundaries.
			q := binary.LittleEndian.Uint64(ix.quote[off:]) &^ escaped

			// Inclusive prefix XOR: bit i becomes the parity of all quote bits
			// at or below i, which is exactly "an odd number of quotes have
			// opened, so I am inside a string". Six shift-xor steps cover
			// sixty-four bits.
			x := q
			x ^= x << 1
			x ^= x << 2
			x ^= x << 4
			x ^= x << 8
			x ^= x << 16
			x ^= x << 32
			in := x ^ strCarry
			// Sign-extending the top bit carries the state into the next word:
			// all ones if the document is inside a string at the boundary. The
			// same carry crosses window boundaries, which is the whole reason
			// windowing is safe here.
			strCarry = uint64(int64(in) >> 63)
			ix.inStr[wbase+w] = in

			if validate {
				if binary.LittleEndian.Uint64(ix.ctl[off:])&in != 0 {
					return nil, errSyntax("control character in string")
				}
				wsw := binary.LittleEndian.Uint64(ix.ws[off:])
				ix.wsw[wbase+w] = wsw
				outWS := wsw &^ in
				anyWS |= outWS
				wsCount += bits.OnesCount64(outWS)

				// The escaped bytes are looked at directly rather than masked
				// for: escapes are rare, so a pass over the document to find
				// them costs the same on every input while the work it saves is
				// usually near zero.
				leaders := bs & in &^ escaped
				target := leaders<<1 | prevLead
				prevLead = leaders >> 63
				for t := target; t != 0; t &= t - 1 {
					pp := base + w*64 + bits.TrailingZeros64(t)
					if pp >= len(data) {
						return nil, errSyntax("string ends in a backslash")
					}
					switch data[pp] {
					case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
					case 'u':
						if pp+4 >= len(data) {
							return nil, errSyntax("short \\u escape")
						}
						for k := 1; k <= 4; k++ {
							if !isHex(data[pp+k]) {
								return nil, errSyntax("invalid \\u escape")
							}
						}
					default:
						return nil, errSyntax("invalid escape")
					}
				}
			}

			cnt += bits.OnesCount64(binary.LittleEndian.Uint64(ix.structural[off:]) &^ in)
		}

		if noBrackets {
			continue
		}

		// Grown to hold this window's brackets exactly, doubling when it has to
		// so the growth is amortised over the document rather than paid per
		// window.
		wrote := len(pos)
		if need := wrote + cnt; cap(pos) < need {
			grown := 2 * need
			np := make([]int32, need, grown)
			copy(np, pos)
			nm := make([]int32, need, grown)
			copy(nm, match)
			pos, match = np, nm
		} else {
			pos, match = pos[:need], match[:need]
		}

		k := wrote
		for w := 0; w < cnw; w++ {
			st := binary.LittleEndian.Uint64(ix.structural[w*8:]) &^ ix.inStr[wbase+w]
			bpos := int32(base + w*64)
			for st != 0 {
				p := bpos + int32(bits.TrailingZeros64(st))
				pos[k] = p
				switch data[p] {
				case '{':
					stack = append(stack, int64(k)<<1)
				case '[':
					stack = append(stack, int64(k)<<1|1)
				case '}', ']':
					if len(stack) == 0 {
						return nil, errAt("unbalanced brackets", int(p))
					}
					o := stack[len(stack)-1]
					stack = stack[:len(stack)-1]
					// A brace must close a brace and a bracket a bracket. The
					// opening kind rode along in the stack entry's low bit.
					if (o&1 == 1) != (data[p] == ']') {
						return nil, errAt("mismatched brackets", int(p))
					}
					oi := o >> 1
					match[oi] = int32(k)
					match[k] = int32(oi)
				}
				k++
				st &= st - 1
			}
		}
	}
	if strCarry != 0 {
		// Blamed on the end of the input, which is where encoding/json puts it:
		// the string is unterminated because the document stopped, and the
		// opening quote was fine when it was read.
		return nil, errAt("unterminated string", len(data)-1)
	}
	if validate {
		if prevLead != 0 {
			return nil, errSyntax("string ends in a backslash")
		}
		ix.noWS = anyWS == 0
		ix.wsCount = wsCount
	}
	if len(stack) != 0 {
		return nil, errAt("unterminated container", len(data)-1)
	}
	ix.pos, ix.match, ix.stack = pos, match, stack
	return ix, nil
}

// chunkBytes is the window stage one works in.
//
// Fixed, and small enough that the window and its five masks stay in L2 while
// both passes over them run: 64 KiB of document and 40 KiB of masks. A fraction
// of the input would be the wrong shape — the cache does not grow when the
// document does.
//
// Measured across document sizes, MB/s for Scan:
//
//	window     1 MB    8 MB   64 MB
//	 64 KiB    7337    7832    7145
//	256 KiB    7177    7249    7162
//	  1 MiB    5541    7159    6418
//	  4 MiB    6814    6652    6352
//
// 64 KiB is the best or equal at every size, and — the point — it is flat.
// maxDocument is the largest document Parse and Scan can index in one piece,
// set by the int32 the bracket positions are stored in. Streaming is unbounded.
//
// This used to be a limit the code did not actually honour. The bracket stack
// packed an entry index and the opening kind into one int32, so the index only
// got 31 bits and overflowed its sign at 2^30 entries -- which "[[],[],...]"
// reaches in 1.5 GiB, inside this limit, and panicked with
// "index out of range [-1073741823]". The stack is int64 now; see its
// declaration. With that, entries are bounded by the document length and an
// entry index fits an int32 for the same reason a position does.
//
// Raising this to 4 GiB by making pos and match unsigned was considered and
// not done: it is a 2x capability gain for a signedness change spread across
// the binary search and every consumer of match, in the hottest code here, and
// the answer above the limit does not change with the number. That answer is
// Decoder. C++ simdjson caps at 4 GB for the same reason and documents it.
const maxDocument = 1<<31 - 1

const chunkBytes = 64 << 10

// wholeDocMax is the largest document handled in a single window.
//
// Deliberately conservative. On the machine these numbers come from, L2 is
// 1 MiB per core and L3 is 64 MiB shared, and whole-document processing does
// not fall over until somewhere between 8 and 16 MB:
//
//	Scan MB/s     8 MB    16 MB    32 MB    64 MB
//	one window    9146     5028     2954     2740
//	64 KiB        7847     7078     6986     7112
//
// Tuning this to 8 MB would suit that machine and put a cliff in the middle of
// the range on a laptop with a 4 MiB L3. 4 MiB is inside any L3 worth the name
// and covers the overwhelming majority of real documents — canada.json, the
// largest file in the standard corpus, is 2.25 MB. Everything above takes the
// flat path. The cost is ~20% on an 8 MB document on hardware with a large L3,
// which is the right thing to give up for not falling off a cliff on hardware
// without one.
const wholeDocMax = 4 << 20

// maskBuf returns a buffer of nw whole words with the bytes past n's mask
// cleared, reusing buf when it is large enough.
//
// The clearing matters: simd.MaskBits writes (n+7)/8 bytes and the word loop
// reads nw*8, so without it the last word would carry whatever the previous
// parse left there and invent structural characters past the end of the
// document.
func maskBuf(buf []byte, nw, n int) []byte {
	if cap(buf) < nw*8 {
		buf = make([]byte, nw*8)
	}
	buf = buf[:nw*8]
	clear(buf[simd.MaskLen(n):])
	return buf
}

// escapedMask returns the bits of this word that are escaped by a preceding
// backslash, and updates the carry into the next word.
//
// The rule is that a backslash escapes the next character unless it is itself
// escaped, so what matters is the parity of each run of backslashes: in `"a\\"`
// the quote follows two and does close the string, in `"a\"` it follows one and
// does not. Counting each run is a loop; this is the arithmetic that replaces
// it.
//
// Adding the odd-length run starts back into the backslash mask propagates a
// carry through each run and lands it one past the run's end, which is what
// turns "parity of a run" into a single add. The even-bit pattern then selects
// alternate positions within every run, inverted for the runs that started on
// an odd bit. prev is the carry out of the last word: one if the word ended
// mid-escape.
func escapedMask(bs uint64, prev *uint64) uint64 {
	if bs == 0 {
		e := *prev
		*prev = 0
		return e
	}
	const even = 0x5555555555555555

	// A backslash that is itself escaped starts nothing.
	bs &^= *prev
	follows := bs<<1 | *prev
	oddStarts := bs &^ even &^ follows
	seq, carry := bits.Add64(oddStarts, bs, 0)
	*prev = carry
	return (even ^ (seq << 1)) & follows
}

// stringEndAt returns the offset just past the string whose opening quote is at
// i, and whether it is terminated.
//
// The in-string mask marks the opening quote and every byte of the body and
// clears the closing quote, so the answer is the first clear bit at or after
// i+1. Usually that is in the same word, which makes this a shift and a
// trailing-zero count — no list of string ranges, no cursor, and no search.
//
// The three versions this replaces are worth remembering: a linear scan of the
// string list, which was quadratic; a binary search, which profiled at 69% of
// Parse because sixteen probes into a fifty-thousand-entry array are sixteen
// chances to miss; and a cursor over that list, which was fast only while the
// walk stayed sequential.
func (ix *index) stringEndAt(i, n int) (int, bool) {
	j := i + 1
	if j >= n {
		return 0, false
	}
	w, b := j/64, uint(j%64)
	// Clear the bits below the starting position so they cannot be mistaken for
	// the end of this string.
	x := ^ix.inStr[w] &^ (1<<b - 1)
	for x == 0 {
		w++
		if w >= len(ix.inStr) {
			return 0, false
		}
		x = ^ix.inStr[w]
	}
	end := w*64 + bits.TrailingZeros64(x)
	if end >= n {
		return 0, false
	}
	return end + 1, true
}
