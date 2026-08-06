package simdjson

// Validation driven by the masks rather than by a walk over the document.
//
// The recursive descent in simdjson.go asks "where does the next token start"
// once per token, and answers it by skipping whitespace: a byte test, and then
// a bit scan over the whitespace mask when the test says there is some. On
// twitter.json, which is 27% whitespace, that was 40% of Valid.
//
// It is the wrong question to ask per token. The masks already know where every
// significant byte is — a byte outside a string that is not whitespace — and
// the set of them is one expression:
//
//	^ws &^ inStr
//
// Walking that, a word at a time with the word in a register, visits exactly
// the bytes the grammar cares about and never looks at the whitespace at all.
// Measured on its own, walking every significant byte of twitter.json costs
// 22 us on top of the 58 us index, against 155 us for the descent it replaces.
//
// A string appears as exactly one significant byte: its closing quote. The
// in-string mask covers a string from its opening quote to the byte before its
// closing one, so the opening quote and the contents are excluded and the
// closing quote is not. That is convenient rather than a coincidence — it is
// what makes a string one token here instead of a range to step over.
//
// This is used by Valid alone. Parse and Unmarshal need the descent for what it
// produces along the way; Valid needs only the answer.

import (
	"math/bits"

	"github.com/sebishogun/simd"
)

// The grammar as six states. Splitting "first" from "subsequent" is what makes
// an empty container legal and a trailing comma not: `[]` is stFirstValue
// meeting `]`, and `[1,]` is stValue meeting it.
const (
	stValue      = iota // a value is required here
	stFirstValue        // a value, or the ']' that closes an empty array
	stKey               // a string key is required here
	stFirstKey          // a key, or the '}' that closes an empty object
	stColon             // the ':' between a key and its value
	stAfterValue        // ',' or the close of the container
)

// sigWord returns the significant bytes of word w: outside a string, and not
// whitespace. The bits past the end of the document are cleared, since the
// whitespace mask has them clear and this expression would otherwise set them.
func sigWord(ix *index, n, w int) uint64 {
	s := ^ix.wsw[w] &^ ix.inStr[w]
	if rem := n - w<<6; rem < 64 {
		s &= 1<<uint(rem) - 1
	}
	return s
}

// validTokens reports whether the significant bytes form one well-formed JSON
// value, with nothing after it.
func (d *Doc) validTokens() bool {
	ix, data := d.ix, d.data
	if ix.s1ok {
		nw := (len(data) + 63) / 64
		if 3*nw <= len(ix.stage1) {
			// The whole grammar walk in one kernel call, over the stage-one
			// buffer it already owns: inStr and wsw are its first two
			// regions, and the dead targets region is the container spill --
			// depth cannot exceed len/128, so it always fits and the
			// kernel's too-deep answer is unreachable here.
			switch simd.JSONValidTokens(data, ix.stage1[:2*nw], ix.stage1[2*nw:3*nw]) {
			case 1:
				return true
			case 0:
				return false
			}
		}
	}
	// From the document, not from the mask: an empty input leaves the
	// whitespace mask at whatever length the previous parse gave it, while the
	// in-string mask is truncated to nothing. Taking the length from the data
	// is the only one of the three that is always right.
	nw := (len(data) + 63) / 64
	if nw == 0 || nw > len(ix.wsw) || nw > len(ix.inStr) {
		return false
	}

	// One bit per open container, 1 for an object. A slice rather than a fixed
	// array because JSON nests as deep as it likes and a fixed one would be
	// either a limit or a waste.
	stk := ix.stack[:0]
	var lvl uint64 // the current word of the stack
	depth := 0

	st := stValue
	w := 0
	word := sigWord(ix, len(data), 0)
	for {
		for word == 0 {
			w++
			if w >= nw {
				// Nothing significant left. Valid only if a complete value was
				// seen and every container closed.
				ix.stack = stk
				return st == stAfterValue && depth == 0
			}
			word = sigWord(ix, len(data), w)
		}
		i := w<<6 + bits.TrailingZeros64(word)
		word &= word - 1
		c := data[i]

		switch st {
		case stColon:
			if c != ':' {
				return false
			}
			st = stValue
			continue

		case stKey, stFirstKey:
			if c == '"' {
				st = stColon
				continue
			}
			if c == '}' && st == stFirstKey {
				goto closeObject
			}
			return false

		case stAfterValue:
			switch c {
			case ',':
				if depth == 0 {
					return false
				}
				if lvl&1 != 0 {
					st = stKey
				} else {
					st = stValue
				}
				continue
			case '}':
				goto closeObject
			case ']':
				goto closeArray
			}
			return false
		}

		// stValue or stFirstValue.
		switch c {
		case '{':
			// Push, and carry the old level word into the stack every 64
			// levels. Documents this deep do not exist, which is why the slice
			// is only touched then.
			if depth&63 == 0 && depth > 0 {
				stk = append(stk, int64(lvl))
			}
			lvl = lvl<<1 | 1
			depth++
			st = stFirstKey
			continue
		case '[':
			if depth&63 == 0 && depth > 0 {
				stk = append(stk, int64(lvl))
			}
			lvl <<= 1
			depth++
			st = stFirstValue
			continue
		case '"':
			st = stAfterValue
			continue
		case ']':
			if st != stFirstValue {
				return false
			}
			goto closeArray
		}

		// A number or a literal. Both are runs of significant bytes, so the
		// scanner is asked where the run ends and the walk resumes from there
		// rather than stepping through the digits one bit at a time.
		{
			var end int
			switch {
			case c == 't':
				e, err := d.litEnd(i, "true")
				if err != nil {
					return false
				}
				end = e
			case c == 'f':
				e, err := d.litEnd(i, "false")
				if err != nil {
					return false
				}
				end = e
			case c == 'n':
				e, err := d.litEnd(i, "null")
				if err != nil {
					return false
				}
				end = e
			case c == '-' || (c >= '0' && c <= '9'):
				e, ok := d.number(i)
				if !ok {
					return false
				}
				end = e
			default:
				return false
			}
			st = stAfterValue
			if end >= len(data) {
				ix.stack = stk
				return depth == 0
			}
			// Usually the token ended in the word already in hand, and then
			// resuming is clearing the bits it covered — no reload, no
			// recompute. canada.json is a million numbers and nothing else, so
			// it does this once per value and rebuilding the word each time
			// cost it 16%.
			if end>>6 == w {
				word &^= 1<<uint(end&63) - 1
			} else {
				w = end >> 6
				word = sigWord(ix, len(data), w) &^ (1<<uint(end&63) - 1)
			}
			continue
		}

	closeObject:
		if depth == 0 || lvl&1 == 0 {
			return false
		}
		goto pop
	closeArray:
		if depth == 0 || lvl&1 != 0 {
			return false
		}
	pop:
		lvl >>= 1
		depth--
		if depth&63 == 0 && depth > 0 {
			lvl = uint64(stk[len(stk)-1])
			stk = stk[:len(stk)-1]
		}
		st = stAfterValue
	}
}
