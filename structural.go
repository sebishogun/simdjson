package simdjson

import (
	"encoding/binary"
	"math/bits"

	"github.com/sebishogun/simd"
)

// Stage one: find every structural character in the document.
//
// This is the idea simdjson is built on. A conventional parser reads a byte,
// branches on what it is, reads the next — a dependent branch per byte, and the
// branch is unpredictable because JSON is not predictable. Stage one instead
// classifies the document with vector compares and answers the questions that
// follow with bitwise arithmetic, so that stage two walks a few thousand
// positions instead of a few million bytes.
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

// The structural characters, as a set for one pass.
const structSet = "{}[]:,"

// Byte classes, recorded while stage one walks the token positions so that
// nothing downstream has to read the document again. The first four are laid
// out so the low bit is set on a closing bracket and the second bit
// distinguishes a square bracket from a brace, which is what lets buildMatches
// check that a bracket closes its own kind with a compare rather than a
// lookback into the input.
const (
	clsOpenBrace  = 0 // {
	clsCloseBrace = 1 // }
	clsOpenBrack  = 2 // [
	clsCloseBrack = 3 // ]
	clsOther      = 4 // : and ,
)

// classOf maps a byte to its class. Only the six structural characters are ever
// looked up, but the table covers 256 so the lookup needs no guard.
var classOf = func() (t [256]uint8) {
	for i := range t {
		t[i] = clsOther
	}
	t['{'], t['}'] = clsOpenBrace, clsCloseBrace
	t['['], t[']'] = clsOpenBrack, clsCloseBrack
	return
}()

// index holds the positions of everything stage two needs.
type index struct {
	pos []int32 // structural positions, ascending, outside strings
	cls []uint8 // class of pos[i], parallel

	// inStr is one bit per byte of the document, set where that byte is inside
	// a string literal. The opening quote of a string is set and the closing
	// quote is not, which is what makes finding the end of a string the search
	// for the next clear bit rather than a lookup in a list of ranges.
	inStr []uint64

	// Raw masks from the scan, consumed word by word to produce inStr and pos.
	// They are fields rather than locals so the buffers survive between parses.
	quote, esc, structural []byte

	// Masks used only by validateStrings, kept here for the same reason.
	ctl, escOK, uEsc []byte

	match []int32 // for each entry of pos, the index of its partner bracket
	stack []int32 // reused by buildMatches
}

// buildIndex scans data and returns the structural positions outside strings.
//
// Three vector passes and one word-at-a-time pass. The vector passes are
// simd.MaskBits, which is a compare and a predicate store; the word pass is the
// escape and string arithmetic plus the extraction of the surviving positions.
//
// Only the six structural characters are indexed. Indexing every *token* start
// as well — the opening quote of each string and the first byte of each number,
// which is what C++ simdjson's pseudo-structural characters are — was tried and
// is slower here; see docs/wrong.md.
func buildIndex(data []byte, ix *index) (*index, error) {
	if ix == nil {
		ix = &index{}
	}
	ix.pos, ix.cls = ix.pos[:0], ix.cls[:0]
	if len(data) == 0 {
		ix.inStr = ix.inStr[:0]
		return ix, ix.buildMatches()
	}

	// Whole words, so the arithmetic below never has to special-case the last
	// one. The bytes between the end of the document and the end of its last
	// word are zeroed rather than left over, so they match nothing.
	nw := (len(data) + 63) / 64
	ix.quote = maskBuf(ix.quote, nw, len(data))
	ix.esc = maskBuf(ix.esc, nw, len(data))
	ix.structural = maskBuf(ix.structural, nw, len(data))
	simd.MaskBits(ix.quote, data, '"')
	simd.MaskBits(ix.esc, data, '\\')
	simd.MaskBitsAny(ix.structural, data, structSet)

	if cap(ix.inStr) < nw {
		ix.inStr = make([]uint64, nw)
	}
	ix.inStr = ix.inStr[:nw]

	// Reserve for the usual density rather than growing from nothing. A
	// structural character every eight bytes is a low guess for real JSON, so
	// this is one allocation in the common case and append handles the rest.
	if cap(ix.pos) < len(data)/8 {
		ix.pos = make([]int32, 0, len(data)/8)
		ix.cls = make([]uint8, 0, len(data)/8)
	}
	pos, cls := ix.pos, ix.cls

	var prevEsc, strCarry uint64
	for w := 0; w < nw; w++ {
		off := w * 8
		escaped := escapedMask(binary.LittleEndian.Uint64(ix.esc[off:]), &prevEsc)

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

		st := binary.LittleEndian.Uint64(ix.structural[off:]) &^ in
		base := int32(w * 64)
		for st != 0 {
			p := base + int32(bits.TrailingZeros64(st))
			pos = append(pos, p)
			// Ascending and roughly one every few bytes, so this streams
			// through the document rather than jumping around it.
			cls = append(cls, classOf[data[p]])
			st &= st - 1
		}
	}
	if strCarry != 0 {
		return nil, errSyntax("unterminated string")
	}
	ix.pos, ix.cls = pos, cls

	if err := ix.buildMatches(); err != nil {
		return nil, err
	}
	return ix, nil
}

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

// buildMatches records, for every opening bracket, the index in pos of the
// bracket that closes it — and the reverse.
//
// This is the part of a simdjson tape that matters most here. Without it,
// finding where a container ends means walking the structural positions from it
// and counting depth, so stepping over a nested value costs the size of that
// value and Index(j) costs the sum of everything before j. With it both are a
// lookup.
//
// It reads the class array rather than the document, so the whole pass is two
// sequential streams and a stack. The stack entry packs the opening bracket's
// kind into its low bit, which is what makes the matching check a compare
// against the closing class instead of a random read back into the input.
func (ix *index) buildMatches() error {
	if cap(ix.match) < len(ix.pos) {
		ix.match = make([]int32, len(ix.pos))
	}
	ix.match = ix.match[:len(ix.pos)]
	stack := ix.stack[:0]
	for i, c := range ix.cls {
		if c >= clsOther {
			continue
		}
		kind := int32(c) >> 1
		if c&1 == 0 {
			stack = append(stack, int32(i)<<1|kind)
			continue
		}
		if len(stack) == 0 {
			return errSyntax("unbalanced brackets")
		}
		o := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if o&1 != kind {
			return errSyntax("mismatched brackets")
		}
		oi := o >> 1
		ix.match[oi] = int32(i)
		ix.match[i] = oi
	}
	if len(stack) != 0 {
		return errSyntax("unterminated container")
	}
	ix.stack = stack
	return nil
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

// The escape characters JSON allows after a backslash, less 'u'. Exactly eight,
// which is what MaskBitsAny takes; 'u' is checked separately because it is the
// one that needs the four hex digits after it looked at.
const escSet = "\"\\/bfnrt"

// validateStrings checks every string in the document at once.
//
// Two rules, both found by fuzzing against encoding/json rather than by reading
// the grammar: a raw control character is not allowed and has to be escaped,
// and only a fixed set of escapes exists, so \0 is invalid however reasonable
// it looks.
//
// Checking them per string, walking its bytes, was 14% of Parse — a branch per
// byte of every string in the document, which is the thing stage one exists to
// avoid. Both rules are properties of the whole document and both are maskable:
// "is there a control character inside a string" is an and of two masks over
// the document, and "is every escape one of the nine allowed" is a shift and an
// and-not. What is left scalar is the four hex digits after a \u, and those are
// rare enough to extract one at a time.
func (ix *index) validateStrings(data []byte) error {
	nw := len(ix.inStr)
	if nw == 0 {
		return nil
	}
	ix.ctl = maskBuf(ix.ctl, nw, len(data))
	ix.escOK = maskBuf(ix.escOK, nw, len(data))
	ix.uEsc = maskBuf(ix.uEsc, nw, len(data))
	simd.MaskBitsLess(ix.ctl, data, 0x20)
	simd.MaskBitsAny(ix.escOK, data, escSet)
	simd.MaskBits(ix.uEsc, data, 'u')

	// prevEsc carries the backslash-run parity between words exactly as it does
	// in buildIndex; prevLead carries the top bit of the leader mask, because
	// the character a backslash escapes is one position later and that position
	// can be in the next word.
	var prevEsc, prevLead uint64
	for w := 0; w < nw; w++ {
		off := w * 8
		in := ix.inStr[w]
		bs := binary.LittleEndian.Uint64(ix.esc[off:])
		escaped := escapedMask(bs, &prevEsc)

		if binary.LittleEndian.Uint64(ix.ctl[off:])&in != 0 {
			return errSyntax("control character in string")
		}

		// A backslash inside a string that is not itself escaped opens an
		// escape. One outside a string is an ordinary byte and not our concern.
		leaders := bs & in &^ escaped
		target := leaders<<1 | prevLead
		prevLead = leaders >> 63

		u := binary.LittleEndian.Uint64(ix.uEsc[off:])
		if target&^(binary.LittleEndian.Uint64(ix.escOK[off:])|u) != 0 {
			return errSyntax("invalid escape")
		}
		for t := target & u; t != 0; t &= t - 1 {
			p := w*64 + bits.TrailingZeros64(t)
			if p+4 >= len(data) {
				return errSyntax("short \\u escape")
			}
			for k := 1; k <= 4; k++ {
				if !isHex(data[p+k]) {
					return errSyntax("invalid \\u escape")
				}
			}
		}
	}
	if prevLead != 0 {
		return errSyntax("string ends in a backslash")
	}
	return nil
}
