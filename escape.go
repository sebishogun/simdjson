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

// kernelScanMin is where the vector scan starts winning, and it is not a number
// chosen here: it is simd.go's own dispatch threshold, below which the call
// lands on a portable path that rebuilds a set table per call. Measured on a
// string with nothing to escape, so the whole length is scanned:
//
//	bytes      word    kernel
//	   32      13.6      22.3
//	   48      20.0      28.3
//	   64      26.5       6.7
//	  256     104.8      14.9
//	 4096      1652     190.8
const kernelScanMin = 64

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
		// It is worth an operation here and not in cleanRun, and the difference
		// is the rest of the set. This path looks for two bytes, both ASCII, so
		// an exact control test means the word loop runs through non-ASCII text
		// to the end. cleanRun also looks for 0xE2, which stops it at that text
		// anyway, so there the extra operation buys nothing and costs 2.5%.
		// Measured both ways on both paths: Fast 46.5-47.1 us to 44.8-45.5,
		// MarshalTo 67.9-68.5 to 68.2-68.6.
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

// appendQuotedInvalid handles a string carrying bytes that are not UTF-8,
// replacing each undecodable byte the way encoding/json does.
//
// Rare enough to be worth no cleverness. It exists because a raw byte written
// through looks identical to the replacement character when printed and is not
// the same bytes, which is exactly the kind of difference that ships unnoticed.
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
