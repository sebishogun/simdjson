package simdjson

import "unicode/utf8"

// String escaping, which is most of what encoding a document costs.
//
// encoding/json escapes the two characters JSON requires — the quote and the
// backslash — everything below 0x20, and, by default, `<`, `>` and `&` so that
// output can be embedded in HTML without becoming script. It also rewrites the
// two Unicode line terminators U+2028 and U+2029 for the same reason.
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
func cleanRun(s string) int {
	const (
		lo = 0x0101010101010101
		hi = 0x8080808080808080
	)
	i := 0
	for ; i+8 <= len(s); i += 8 {
		w := le64str(s, i)
		// Anything below 0x20. The high bits are masked off first so a byte
		// above ASCII cannot borrow into its neighbour and read as a control.
		if a := w &^ hi; (a-lo*0x20)&^a&hi != 0 {
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

// writeString appends s as a quoted JSON string.
func (e *encodeState) writeString(s string) {
	e.buf = appendQuoted(e.buf, s)
}

// appendQuoted is writeString for a caller with its own buffer.
//
// UTF-8 validity is settled once for the whole string, not once per rune. When
// the string is valid — which it is unless something upstream is wrong — a
// multi-byte rune needs no inspection and rides through the fast run with
// everything else. Deciding it per rune meant calling the decoder for every
// non-ASCII character, and on a document of tweets that was 20% of the encode.
func appendQuoted(dst []byte, s string) []byte {
	if !utf8.ValidString(s) {
		return appendQuotedInvalid(dst, s)
	}
	dst = append(dst, '"')
	dst = appendBody(dst, s)
	return append(dst, '"')
}

// appendBody writes the contents of a valid-UTF-8 string, without quotes.
func appendBody(dst []byte, s string) []byte {
	for {
		n := cleanRun(s)
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
