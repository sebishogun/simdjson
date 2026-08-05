package simdjson

import (
	"unicode/utf8"

	"github.com/sebishogun/simd"
)

// String escaping, which is most of what encoding a document costs.
//
// encoding/json escapes the two characters JSON requires — the quote and the
// backslash — everything below 0x20, and, by default, `<`, `>` and `&` so that
// output can be embedded in HTML without becoming script. It also rewrites the
// two Unicode line terminators U+2028 and U+2029 for the same reason.
//
// One thing measured and rejected: doing the scan with the vector kernels for
// strings past some length. On the scan alone the kernels look decisive —
//
//	bytes      word    kernel
//	   64      37.5       7.9
//	  128      73.6      10.9
//	 1024     585.5      54.9
//
// — and in the encoder it is 46% SLOWER. Two reasons, both invisible in that
// table. The strings a document actually contains are mostly short, so the
// threshold rarely fires and the branch is paid every time. And a real scan
// usually stops early at the first byte needing an escape, where the
// microbenchmark scanned to the end of a string with none: the word loop is
// rarely running to completion, which is the only case the kernel wins.
//
// Restructuring the scan into methods so it could reach the mask buffers cost a
// further 21% on its own, by putting the hot functions past the inliner.
//
// Nearly every string needs none of that. So the question asked first is not
// "what does this byte need" but "does this run need anything at all", and that
// is answered eight bytes at a time. A string needing nothing is one memmove.
//
// One thing tried and rejected: tracking in that same loop whether any byte was
// above ASCII, so a pure-ASCII string could skip the UTF-8 validity check
// entirely. It costs an OR per word and pays only when the string is ASCII —
// and on a document of tweets most strings are not, so it was 102 us against
// 86. Sound reasoning, wrong document.

// needsEscape[c] is true for the bytes that end a clean run.
var needsEscape = func() (t [256]bool) {
	for i := 0; i < 0x20; i++ {
		t[i] = true
	}
	t['"'] = true
	t['\\'] = true
	t['<'] = true
	t['>'] = true
	t['&'] = true
	// 0xE2 leads U+2028 and U+2029; the run stops so those three bytes can be
	// checked. Every other byte above ASCII is part of a rune that passes
	// through untouched, provided the string is valid UTF-8 — which is settled
	// once per string rather than once per rune. See appendQuoted.
	t[0xE2] = true
	return
}()

// cleanRun returns the length of the prefix of s that can be copied verbatim.
//
// Eight bytes at a time: the standard has-a-zero-byte trick once per byte value
// that matters, plus one masked comparison for the control range.
// escSet is the set cleanRun stops on that is not a control byte. 0xE2 leads
// U+2028 and U+2029; see needsEscape.
const escSet = "\"\\<>&\xE2"

// scan is cleanRun with the vector kernel taking over once the string is long
// enough to pay for the call. It is here rather than inside cleanRun because a
// function holding a call cannot be inlined, and cleanRun is small enough to be
// — putting the branch here keeps the short-string path exactly as it was.
func scan(s string) int { return cleanRun(s) }

// scanOpts is scan for a caller that may have HTML escaping turned off, which
// takes four bytes out of the set and the U+2028 check with them.
func scanOpts(s string, html bool) int {
	if html {
		return scan(s)
	}
	if len(s) >= kernelScanMin {
		if n := simd.IndexAnyOrLess(s, `"\`, 0x20); n >= 0 {
			return n
		}
		return len(s)
	}
	return cleanRunOpts(s, false)
}

// kernelScanMin is where the vector scan starts winning: one vector block.
//
// It was 64, from a table that timed the wrong thing. Below simd's own
// threshold the call lands on a Go byte loop, so the "kernel" column below 64
// was never the kernel:
//
//	bytes      word    kernel(*)
//	   32      13.6      22.3     (*) the fallback, not the kernel
//	   48      20.0      28.3     (*)
//	   64      26.5       6.7
//	  256     104.8      14.9
//	 4096      1652     190.8
//
// Timed against the kernel itself, with its threshold lowered to one block and
// its tail finished by an overlapping block rather than a byte loop:
//
//	bytes     32     40     48     64     96
//	kernel  4.26   4.89   4.96   4.64   5.48
const kernelScanMin = 32

func cleanRun(s string) int {
	const (
		lo = 0x0101010101010101
		hi = 0x8080808080808080
	)
	i := 0
	for ; i+8 <= len(s); i += 8 {
		w := le64str(s, i)
		// Anything below 0x20, exactly.
		//
		// Clearing the high bits first and testing that -- which is what this
		// did -- answers the wrong question: it maps 0x8D to 0x0D and calls it a
		// control character. It is wrong for 0x80 through 0x9F, thirty-two byte
		// values out of two hundred and fifty-six, and those are the commonest
		// UTF-8 continuation bytes. So on text with any non-ASCII in it the loop
		// broke almost immediately and the byte-at-a-time tail did all the work.
		// The output stayed correct, because the tail consults needsEscape; only
		// the speed was gone, and only on the strings long enough to care.
		//
		// The exact form: force each byte's high bit, subtract, and take the
		// bytes that borrowed -- then `&^ w` drops the ones that were already
		// above ASCII, which are not controls whatever their low seven bits say.
		if d := (w&^hi | hi) - lo*0x20; ^d&^w&hi != 0 {
			break
		}
		if hasByte(w, '"') || hasByte(w, '\\') || hasByte(w, '<') ||
			hasByte(w, '>') || hasByte(w, '&') || hasByte(w, 0xE2) {
			break
		}
	}
	for ; i < len(s); i++ {
		if needsEscape[s[i]] {
			break
		}
	}
	return i
}

// le64str reads eight bytes of a string as a little-endian word.
func le64str(s string, i int) uint64 {
	_ = s[i+7]
	return uint64(s[i]) | uint64(s[i+1])<<8 | uint64(s[i+2])<<16 | uint64(s[i+3])<<24 |
		uint64(s[i+4])<<32 | uint64(s[i+5])<<40 | uint64(s[i+6])<<48 | uint64(s[i+7])<<56
}

// hasByte reports whether the word contains the given byte.
func hasByte(w uint64, b byte) bool {
	const (
		lo = 0x0101010101010101
		hi = 0x8080808080808080
	)
	x := w ^ (lo * uint64(b))
	return (x-lo)&^x&hi != 0
}

const hexDigits = "0123456789abcdef"

// writeString appends s as a quoted JSON string, under the encoder's options.
func (e *encodeState) writeString(s string) {
	e.buf = appendQuotedOpts(e.buf, s, e.opts)
}

// appendQuotedOpts is appendQuoted with the checks the caller asked for.
//
// The two dimensions are independent: HTML escaping changes which bytes end a
// clean run, and validation decides whether an undecodable byte becomes U+FFFD
// or is written through. Both default on.
func appendQuotedOpts(dst []byte, s string, o Options) []byte {
	if o.EscapeHTML && o.ValidateStrings {
		return appendQuoted(dst, s)
	}
	// Validation, when it was asked for and the fused path above did not run.
	//
	// This was missing, and the comment above -- "the two dimensions are
	// independent" -- described the intent rather than the code. Only the
	// both-flags path validated, so Options{ValidateStrings: true} with
	// EscapeHTML off wrote an undecodable byte straight through: marshalling
	// "a\xffb" gave "a\xffb" where encoding/json and this package's own Std
	// give "a\ufffdb". That is not merely a missing replacement, it is invalid
	// JSON out of a call that asked for the opposite.
	//
	// The check costs a pass per string, which is what the option is for. The
	// replacement itself only runs for input that was already malformed.
	if o.ValidateStrings && !validUTF8String(s) {
		return appendQuotedInvalidOpts(dst, s, o)
	}
	dst = append(dst, '"')
	// The same fused copy the stdlib-compatible path uses, once before the
	// loop. The loop below is what handles a string that has something to
	// escape; this is what handles the ones that do not, which is most of them.
	if len(s) >= kernelScanMin {
		dst = growTo(dst, len(s))
		k := simd.JSONCopyRun(dst[len(dst):len(dst)+len(s)], s, o.EscapeHTML)
		dst = dst[:len(dst)+k]
		if k == len(s) {
			return append(dst, '"')
		}
		s = s[k:]
	}
	for {
		n := scanOpts(s, o.EscapeHTML)
		if n == len(s) {
			dst = append(dst, s...)
			break
		}
		dst = append(dst, s[:n]...)
		s = s[n:]
		c := s[0]
		switch {
		case c == '"':
			dst = append(dst, '\\', '"')
		case c == '\\':
			dst = append(dst, '\\', '\\')
		case c == '\b':
			dst = append(dst, '\\', 'b')
		case c == '\f':
			dst = append(dst, '\\', 'f')
		case c == '\n':
			dst = append(dst, '\\', 'n')
		case c == '\r':
			dst = append(dst, '\\', 'r')
		case c == '\t':
			dst = append(dst, '\\', 't')
		case c < 0x20 || (o.EscapeHTML && (c == '<' || c == '>' || c == '&')):
			dst = append(dst, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xF])
		default:
			if o.EscapeHTML && c == 0xE2 && len(s) >= 3 && s[1] == 0x80 &&
				(s[2] == 0xA8 || s[2] == 0xA9) {
				dst = append(dst, '\\', 'u', '2', '0', '2', hexDigits[s[2]&0xF])
				s = s[3:]
				continue
			}
			dst = append(dst, c)
		}
		s = s[1:]
	}
	return append(dst, '"')
}

// cleanRunOpts is cleanRun with the HTML bytes optionally left alone.
//
// Both paths go eight bytes at a time. The first version of the non-HTML one
// was a byte loop — the checks are fewer, so it looked like it did not need the
// word treatment — and it was 50% of the encode in Fast mode. Fewer conditions
// per byte is not the same as fewer bytes.
func cleanRunOpts(s string, html bool) int {
	if html {
		return cleanRun(s)
	}
	const (
		lo = 0x0101010101010101
		hi = 0x8080808080808080
	)
	i := 0
	for ; i+8 <= len(s); i += 8 {
		w := le64str(s, i)
		// Anything below 0x20, exactly.
		//
		// Masking the high bit off and comparing, which is what cleanRun does,
		// stops a byte above ASCII borrowing into its neighbour by turning it
		// into a control character itself: 0x81 masked is 0x01, and 0x81 is a
		// UTF-8 continuation byte. Setting the high bit instead stops the borrow
		// without changing the value — 0x80 + low7 - 0x20 never goes negative —
		// and anding away the bytes that started with their high bit set leaves
		// bytes genuinely below 0x20 and nothing else.
		//
		// cleanRun uses the same exact test now, and the note that used to be
		// here said it should not: that cleanRun's 0xE2 probe "stops it at that
		// text anyway", so the extra operation bought nothing and cost 2.5%.
		//
		// Both halves of that were wrong. The 0xE2 probe matches one byte, and
		// CJK is E3 81 82 and friends -- it never fires on Japanese text, so
		// what stopped the loop there was the masked control test turning a
		// continuation byte into a control character. And re-measured over three
		// passes of twelve samples each, with the Fast path as a control because
		// it comes through here and not through cleanRun:
		//
		//	                masked   exact
		//	Marshal         60,204  58,327   -3.1%
		//	MarshalTo       53,216  51,452   -3.3%
		//	Fast            34,902  35,000   +0.3%  (control, untouched)
		if d := (w&^hi | hi) - lo*0x20; ^d&^w&hi != 0 {
			break
		}
		if hasByte(w, '"') || hasByte(w, '\\') {
			break
		}
	}
	for ; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c == '"' || c == '\\' {
			break
		}
	}
	return i
}

// appendQuoted is writeString for a caller with its own buffer.
//
// UTF-8 validity is settled once for the whole string, not once per rune. When
// the string is valid — which it is unless something upstream is wrong — a
// multi-byte rune needs no inspection and rides through the fast run with
// everything else. Deciding it per rune meant calling the decoder for every
// non-ASCII character, and on a document of tweets that was 20% of the encode.
//
// The check itself is simd.ValidUTF8, which is a block classifier rather than
// a rune walk: each byte checked against the three before it, so no sequence
// has to be located before the checking starts. unicode/utf8.ValidString is
// eight times slower on text that is not ASCII than on text that is, and a
// document of tweets is full of the former — it was 42% of the encode.
// plainASCIIRun returns the length of the prefix of s that is ASCII and needs
// no escaping. Both questions from one table lookup per byte.
func plainASCIIRun(s string) int {
	i := 0
	for i < len(s) && plainByte[s[i]] {
		i++
	}
	return i
}

// plainByte marks the bytes that need neither escaping nor a second thought
// about UTF-8: printable ASCII, minus the five characters JSON or HTML escaping
// rewrites.
var plainByte = func() (t [256]bool) {
	for c := 0x20; c < 0x80; c++ {
		t[c] = true
	}
	for _, c := range []byte(`"\<>&`) {
		t[c] = false
	}
	return
}()

func appendQuoted(dst []byte, s string) []byte {
	// One pass, answering both questions at once. A string of printable ASCII
	// with nothing to escape is valid UTF-8 by construction, so it needs no
	// validity check and no escape scan -- it is a quote, a memmove and a
	// quote.
	//
	// That is not a special case, it is the case: 95% of the strings in
	// twitter.json and 99% of those in citm_catalog.json are pure ASCII, and
	// the median string in either is under a dozen bytes, so what this path
	// costs per string matters far more than what it costs per byte. The
	// separate ValidUTF8 call alone was 14% of an encode.
	n := plainASCIIRun(s)
	if n == len(s) {
		dst = append(dst, '"')
		dst = append(dst, s...)
		return append(dst, '"')
	}
	// An ASCII byte that needs escaping does not make the string non-ASCII, and
	// a string that is all ASCII is valid UTF-8 whatever escapes it holds. So
	// that case never needs the validation pass below.
	//
	// It is behind a call, and the arrangement is the point: the path this
	// guards is 25% of the strings and they are the short ones, while the code
	// below carries 84.8% of the bytes. Putting THAT behind a call instead --
	// which is the obvious refactor and was tried -- costs 5.3% on the encode.
	if s[n] < 0x80 {
		return appendQuotedASCII(dst, s, n)
	}
	// The prefix is already known: printable ASCII, nothing to escape, and
	// therefore valid UTF-8 by construction. Neither of the two scans that
	// follow needs to look at it again, and both used to start from the front.
	//
	// Cutting at n is a rune boundary whatever stopped the run. A byte above
	// ASCII stops it at that byte, which is a lead byte or a stray continuation
	// and is where validation has to begin either way; a byte needing an escape
	// stops it at an ASCII byte, which is a boundary too.
	if !validUTF8String(s[n:]) {
		return appendQuotedInvalid(dst, s)
	}
	dst = append(dst, '"')
	dst = append(dst, s[:n]...)
	tail := s[n:]
	// Copy and scan in one pass, for the tail that is left once the ASCII run
	// has been taken. What reaches here has something above ASCII in it, which
	// makes it one of the long strings, and most of those hold nothing that
	// needs escaping at all — so the usual answer is "all of it", arrived at
	// without reading the bytes twice.
	if len(tail) >= kernelScanMin {
		dst = growTo(dst, len(tail))
		k := simd.JSONCopyRun(dst[len(dst):len(dst)+len(tail)], tail, true)
		dst = dst[:len(dst)+k]
		if k == len(tail) {
			return append(dst, '"')
		}
		tail = tail[k:]
	}
	dst = appendBody(dst, tail)
	return append(dst, '"')
}

// growTo makes room for n more bytes past dst's length without changing it.
func growTo(dst []byte, n int) []byte {
	if cap(dst)-len(dst) < n {
		nd := make([]byte, len(dst), 2*(len(dst)+n))
		copy(nd, dst)
		return nd
	}
	return dst
}

// appendBody writes the contents of a valid-UTF-8 string, without quotes.
func appendBody(dst []byte, s string) []byte {
	for {
		n := scan(s)
		if n == len(s) {
			return append(dst, s...)
		}
		dst = append(dst, s[:n]...)
		s = s[n:]

		c := s[0]
		switch {
		case c == '"':
			dst = append(dst, '\\', '"')
		case c == '\\':
			dst = append(dst, '\\', '\\')
		case c == '\b':
			dst = append(dst, '\\', 'b')
		case c == '\f':
			dst = append(dst, '\\', 'f')
		case c == '\n':
			dst = append(dst, '\\', 'n')
		case c == '\r':
			dst = append(dst, '\\', 'r')
		case c == '\t':
			dst = append(dst, '\\', 't')
		case c == '<' || c == '>' || c == '&' || c < 0x20:
			// The HTML three are escaped by default exactly as encoding/json
			// does, so output is safe to embed in a page without a second pass.
			dst = append(dst, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xF])
		default:
			// A 0xE2. Either it begins U+2028 or U+2029, which terminate a line
			// in JavaScript but not in JSON, or it is an ordinary rune.
			if len(s) >= 3 && s[1] == 0x80 && (s[2] == 0xA8 || s[2] == 0xA9) {
				dst = append(dst, '\\', 'u', '2', '0', '2', hexDigits[s[2]&0xF])
				s = s[3:]
				continue
			}
			_, size := utf8.DecodeRuneInString(s)
			dst = append(dst, s[:size]...)
			s = s[size:]
			continue
		}
		s = s[1:]
	}
}

// appendQuotedASCII writes a string whose first stopping byte is ASCII, staying
// on the ASCII path across escapes for as long as it can.
//
// n is where the caller's run stopped. Everything before it is printable ASCII
// with nothing to escape, and therefore valid UTF-8 by construction; the same
// holds for every run this loop takes afterwards, so no validation happens
// until a byte above ASCII actually turns up.
//
// Measured on a 512-byte string, by escape count:
//
//	escapes   0      1      2      4      8     16     32
//	before  131.8  195.4  202.1  218.7  240.1  262.7  301.7
//	after   128.9  137.8  140.0  124.1  133.8  158.6  197.1
//
// The step at the first escape was the validation pass, not the escape.
func appendQuotedASCII(dst []byte, s string, n int) []byte {
	out := append(dst, '"')
	for {
		out = append(out, s[:n]...)
		s = s[n:]
		if len(s) == 0 {
			return append(out, '"')
		}
		c := s[0]
		if c >= 0x80 {
			break
		}
		var ok bool
		if out, ok = appendEscape(out, c); !ok {
			break
		}
		s = s[1:]
		if len(s) == 0 {
			return append(out, '"')
		}
		n = plainASCIIRun(s)
	}
	// Something above ASCII, or an escape this does not handle. The rest has to
	// be proved valid; the part already written does not, and is not re-read.
	if !validUTF8String(s) {
		return appendQuotedInvalidTail(out, s)
	}
	if len(s) >= kernelScanMin {
		out = growTo(out, len(s))
		k := simd.JSONCopyRun(out[len(out):len(out)+len(s)], s, true)
		out = out[:len(out)+k]
		if k == len(s) {
			return append(out, '"')
		}
		s = s[k:]
	}
	return append(appendBody(out, s), '"')
}

// appendQuotedInvalidTail is appendQuotedInvalid for a tail whose prefix has
// already been written, quote included.
func appendQuotedInvalidTail(dst []byte, s string) []byte {
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		if r == utf8.RuneError && size == 1 {
			dst = append(dst, '\\', 'u', 'f', 'f', 'f', 'd')
			s = s[1:]
			continue
		}
		dst = appendBody(dst, s[:size])
		s = s[size:]
	}
	return append(dst, '"')
}

// appendEscape writes the escape for one byte that ended a clean run, and
// reports whether it recognised it. appendBody's switch is the authority; this
// is the same set, extracted so the ASCII loop can share it. 0xE2 is not in it
// -- U+2028 and U+2029 need the rune decoded, which is the caller's slow path.
func appendEscape(dst []byte, c byte) ([]byte, bool) {
	switch {
	case c == '"':
		return append(dst, '\\', '"'), true
	case c == '\\':
		return append(dst, '\\', '\\'), true
	case c == '\b':
		return append(dst, '\\', 'b'), true
	case c == '\f':
		return append(dst, '\\', 'f'), true
	case c == '\n':
		return append(dst, '\\', 'n'), true
	case c == '\r':
		return append(dst, '\\', 'r'), true
	case c == '\t':
		return append(dst, '\\', 't'), true
	case c == '<' || c == '>' || c == '&' || c < 0x20:
		return append(dst, '\\', 'u', '0', '0',
			hexDigits[c>>4], hexDigits[c&0xF]), true
	}
	return dst, false
}

// appendQuotedInvalid handles a string carrying bytes that are not UTF-8,
// replacing each undecodable byte the way encoding/json does.
//
// Rare enough to be worth no cleverness. It exists because a raw byte written
// through looks identical to the replacement character when printed and is not
// the same bytes, which is exactly the kind of difference that ships unnoticed.
// appendQuotedInvalidOpts is appendQuotedInvalid for the paths that are not
// escaping HTML.
//
// It writes the escaped \ufffd rather than the replacement rune's own bytes,
// which is what encoding/json does and what appendQuotedInvalid already did --
// the two must not disagree, or the same malformed input would come out one way
// under Std and another under Options{ValidateStrings: true}.
// appendPlainRune appends one already-decoded rune, escaping only what JSON
// requires and not what HTML would.
func appendPlainRune(dst []byte, s string) []byte {
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"':
			dst = append(dst, '\\', '"')
		case c == '\\':
			dst = append(dst, '\\', '\\')
		case c == '\b':
			dst = append(dst, '\\', 'b')
		case c == '\f':
			dst = append(dst, '\\', 'f')
		case c == '\n':
			dst = append(dst, '\\', 'n')
		case c == '\r':
			dst = append(dst, '\\', 'r')
		case c == '\t':
			dst = append(dst, '\\', 't')
		case c < 0x20:
			dst = append(dst, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xF])
		default:
			dst = append(dst, c)
		}
	}
	return dst
}

func appendQuotedInvalidOpts(dst []byte, s string, o Options) []byte {
	if o.EscapeHTML {
		return appendQuotedInvalid(dst, s)
	}
	dst = append(dst, '"')
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		if r == utf8.RuneError && size == 1 {
			dst = append(dst, '\\', 'u', 'f', 'f', 'f', 'd')
			s = s[1:]
			continue
		}
		// A decoded rune needs only the escapes JSON requires. The
		// HTML-specific ones are exactly what this branch is not doing.
		dst = appendPlainRune(dst, s[:size])
		s = s[size:]
	}
	return append(dst, '"')
}

func appendQuotedInvalid(dst []byte, s string) []byte {
	dst = append(dst, '"')
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		if r == utf8.RuneError && size == 1 {
			dst = append(dst, '\\', 'u', 'f', 'f', 'f', 'd')
			s = s[1:]
			continue
		}
		dst = appendBody(dst, s[:size])
		s = s[size:]
	}
	return append(dst, '"')
}
