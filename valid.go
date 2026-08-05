package simdjson

// The parts of encoding/json's surface that are about JSON text rather than Go
// values: checking it, squeezing the whitespace out of it, putting whitespace
// back in, and escaping the characters a browser would misread.
//
// All four are the same shape — walk the document, treat the bytes inside
// strings as opaque, act on the structural ones — and all four are places where
// having a structural index already means the walk is not a byte loop asking
// "am I in a string" per byte. The index answers that a word at a time.

import (
	"bytes"
	"math/bits"
)

// wsDenom sets where Valid switches from walking the document to walking the
// mask: the mask when whitespace outside strings is more than one part in
// wsDenom, the document otherwise.
//
// The mask walk exists to replace whitespace skipping, so a document with no
// whitespace has nothing for it to replace and pays its bookkeeping for
// nothing. Measured both ways on the same binary, MB/s:
//
//	           mask   descent
//	twitter    3857     2897     26% whitespace
//	citm       3969     3180     71%
//	canada     1457     1672     0.001%
//
// The threshold is not a knife edge. canada.json has 24 bytes of whitespace in
// 2.25 MB and twitter.json has 26%, four orders of magnitude apart, so anything
// between routes the three the same way. A sweep over synthetic documents at
// 0, 16, 28, 44 and 61% found the mask ahead at every one of them, so the only
// shape this has to catch is the one with essentially none.
//
// It is not free of error: a synthetic document of nothing but numbers at 13%
// whitespace wants the descent and gets the mask, and loses 4% for it. That is
// the price of dispatching on something the scan already counts rather than on
// something it would have to go and measure.
var wsDenom = 64

// Valid reports whether data is a well-formed JSON document.
//
// The same grammar encoding/json.Valid applies, and the same answer for every
// input: it is checked against it by a fuzzer. Trailing whitespace is allowed,
// trailing anything else is not, and an empty input is not a document.
func Valid(data []byte) bool {
	ix, _ := indexPool.Get().(*index)
	defer func() {
		if ix != nil {
			indexPool.Put(ix)
		}
	}()
	// Large documents take the full bracket index -- the parallel path Scan
	// uses -- and, when the root is an array of containers, walk its elements
	// across workers. Valid returns a bool, so the bar is bool agreement with
	// the serial walk, which the differential holds. Anything the shape check
	// declines walks serially over the same already-built index.
	if len(data) >= parallelMinBytes {
		if v, ok := validParallel(data, ix); ok {
			return v
		}
	}
	ix, err := buildIndexMode(data, ix, true, masksOnly, false)
	if err != nil {
		return false
	}
	d := &Doc{data: data, ix: ix, inStr: ix.inStr, noWS: ix.noWS, wsw: ix.wsw}
	if ix.wsCount*wsDenom > len(data) {
		// The mask walk exists to replace whitespace skipping, so it is worth
		// having exactly when there is whitespace to skip. twitter.json is 26%
		// whitespace and citm_catalog.json is 71%, and it is 28% and 20% faster
		// on them. canada.json has none at all — skip is then three
		// instructions that always take the same branch, there is nothing to
		// replace, and walking the mask to find bytes a pointer was already on
		// cost it 16%.
		//
		// The flag is not a heuristic and it is not measured here: the scan
		// sets it because Parse wants it for the same reason.
		return d.validTokens()
	}
	start, err := docFront2(data, d)
	if err != nil {
		return false
	}
	end, err := d.validateValue(start)
	if err != nil {
		return false
	}
	return d.skip(end) >= len(data)
}

// Compact appends the JSON in src to dst with insignificant whitespace removed.
//
// Whitespace inside a string is significant and is kept; everything between
// tokens goes. Invalid input is reported and dst is left as it was, which is
// what encoding/json.Compact does — it validates as it copies.
func Compact(dst *bytes.Buffer, src []byte) error {
	ix, _ := indexPool.Get().(*index)
	defer func() {
		if ix != nil {
			indexPool.Put(ix)
		}
	}()
	ix, d, _, err := indexAndValidate(src, ix)
	if err != nil {
		return err
	}
	// A document with no whitespace outside its strings is already compact, and
	// most machine-generated JSON is. Proving it costs one flag that the scan
	// has already set.
	if d.noWS {
		dst.Write(src)
		return nil
	}
	// Large documents strip across workers; see compact_parallel.go. The
	// serial copier below is the same word-run loop, one segment, and stays
	// the authority the differential compares against.
	if compactParallel(dst, src, ix) {
		return nil
	}
	dst.Grow(len(src))
	writeCompact(dst, src, ix)
	return nil
}

// writeCompact copies src minus the whitespace that is outside a string.
//
// Word at a time over the masks. The whitespace mask covers the whole document,
// including the spaces inside strings, so the bytes to keep are the ones it
// does not claim or the in-string mask does — and a word where that is all of
// them, which is the usual case inside a long string or a run of digits, is one
// write of 64 bytes.
//
// The kept bits are taken a run at a time rather than a bit at a time. A
// pretty-printed document is mostly whitespace — citm_catalog.json is 71% —
// so iterating the dropped bits meant one call per space, nearly all of them
// writing nothing.
func writeCompact(dst *bytes.Buffer, src []byte, ix *index) {
	for w, ws := range ix.wsw {
		base := w * 64
		n := len(src) - base
		if n <= 0 {
			break
		}
		if n > 64 {
			n = 64
		}
		keep := ^(ws &^ ix.inStr[w])
		if n < 64 {
			keep &= 1<<uint(n) - 1
		}
		if keep == ^uint64(0) {
			dst.Write(src[base : base+n])
			continue
		}
		for keep != 0 {
			t := bits.TrailingZeros64(keep)
			l := bits.TrailingZeros64(^(keep >> uint(t)))
			if t+l >= n {
				dst.Write(src[base+t : base+n])
				break
			}
			dst.Write(src[base+t : base+t+l])
			keep &^= (1<<uint(l) - 1) << uint(t)
		}
	}
}

// Indent appends the JSON in src to dst, one element per line, each nested
// level prefixed by one more copy of indent and every line by prefix.
//
// Byte-for-byte what encoding/json.Indent produces, including the space after
// a colon and the empty object written as {} rather than opened and closed on
// two lines.
func Indent(dst *bytes.Buffer, src []byte, prefix, indent string) error {
	ix, _ := indexPool.Get().(*index)
	defer func() {
		if ix != nil {
			indexPool.Put(ix)
		}
	}()
	ix, _, end, err := indexAndValidate(src, ix)
	if err != nil {
		return err
	}
	// Large documents lay out across workers; see indent_parallel.go. The
	// serial writer below stays the authority the differential compares
	// against.
	if indentParallel(dst, src, ix, prefix, indent, end) {
		return nil
	}
	dst.Grow(len(src) * 2)
	writeIndent(dst, src, ix, prefix, indent, end)
	return nil
}

// writeIndent re-lays out src[:end], which is the top-level value, and then
// copies src[end:] — the whitespace after it — through unchanged. Leading
// whitespace is replaced and trailing whitespace is kept, which is asymmetric
// and is what encoding/json does.
//
// Two things keep it off a byte-at-a-time loop. A string is copied whole, in
// one write, because the in-string mask says where it ends and nothing inside
// it can be structural — and strings are most of the bytes in most documents.
// And the newline plus its indentation is one write from a buffer built once,
// rather than one call per level per line; Indent emits a newline for every
// element, so that inner loop ran more often than any other.
func writeIndent(dst *bytes.Buffer, src []byte, ix *index, prefix, indent string, end int) {
	// pad is "\n" + prefix + indent repeated, so newline(d) is one write of a
	// prefix of it. It grows to whatever depth the document turns out to have.
	pad := make([]byte, 0, 1+len(prefix)+16*len(indent))
	pad = append(append(pad, '\n'), prefix...)
	head := len(pad)
	grow := func(depth int) {
		for (len(pad)-head)/max(len(indent), 1) < depth && len(indent) > 0 {
			pad = append(pad, indent...)
		}
	}
	newline := func(depth int) {
		if len(indent) == 0 {
			dst.Write(pad[:head])
			return
		}
		grow(depth)
		dst.Write(pad[:head+depth*len(indent)])
	}

	depth := 0
	// An opened container does not get its newline until something turns up
	// inside it, so that {} and [] stay on one line the way encoding/json
	// writes them.
	pending := false
	for i := 0; i < end; {
		// A byte the in-string mask claims begins a string literal: its opening
		// quote. Everything to the closing quote is opaque, so the whole thing
		// goes out in one write and the scan resumes after it.
		if ix.inStr[i>>6]&(1<<(uint(i)&63)) != 0 {
			if pending {
				newline(depth)
				pending = false
			}
			j := stringRunEnd(ix, i, end)
			dst.Write(src[i:j])
			i = j
			continue
		}
		c := src[i]
		i++
		switch c {
		case ' ', '\t', '\n', '\r':
			// Whatever the input's layout was, it is replaced. Skipping the
			// whole run rather than one byte per iteration matters on
			// pretty-printed input, which is most of what anyone re-indents.
			for i < end && isJSONSpace[src[i]] {
				i++
			}
		case '{', '[':
			if pending {
				newline(depth)
			}
			depth++
			pending = true
			dst.WriteByte(c)
		case '}', ']':
			depth--
			if pending {
				pending = false // an empty container: nothing came between
			} else {
				newline(depth)
			}
			dst.WriteByte(c)
		case ',':
			dst.WriteByte(c)
			newline(depth)
		case ':':
			dst.WriteByte(c)
			dst.WriteByte(' ')
		default:
			if pending {
				newline(depth)
				pending = false
			}
			// A number or a literal. Both are runs of bytes that pass through
			// untouched, and a document of numbers is nothing but these — one
			// write per run rather than per byte.
			j := i
			for j < end && !indentBreak[src[j]] {
				j++
			}
			dst.Write(src[i-1 : j])
			i = j
		}
	}
	dst.Write(src[end:])
}

// indentBreak marks the bytes that end a run of untouched text: the structural
// characters, the quote that opens the next string, and whitespace. isJSONSpace
// is the four bytes JSON counts as whitespace and no others — a control byte is
// not whitespace, it is a syntax error, and the index has already said so.
var indentBreak = func() (t [256]bool) {
	for _, c := range []byte(`{}[],:"` + " \t\n\r") {
		t[c] = true
	}
	return
}()

var isJSONSpace = func() (t [256]bool) {
	for _, c := range []byte(" \t\n\r") {
		t[c] = true
	}
	return
}()

// stringRunEnd returns one past the closing quote of the string whose opening
// quote is at i. The in-string mask covers the opening quote and the contents
// and stops short of the closing quote, so this is the next clear bit plus one.
func stringRunEnd(ix *index, i, end int) int {
	w := i >> 6
	// Bits at or after i in this word that are still inside the string.
	m := ^ix.inStr[w] &^ ((1 << (uint(i) & 63)) - 1)
	for m == 0 {
		w++
		if w >= len(ix.inStr) {
			return end
		}
		m = ^ix.inStr[w]
	}
	j := w<<6 + bits.TrailingZeros64(m) + 1
	if j > end {
		return end
	}
	return j
}

// MarshalIndent is [Marshal] followed by [Indent], minus the proof: the
// bytes between them are this package's own output, compact and valid by
// construction, so the grammar walk Indent runs over input from outside
// proves nothing here. The masks are still built -- the writer lays out
// strings and depth from them -- and the walk was a fifth of the total.
func MarshalIndent(v any, prefix, indent string) ([]byte, error) {
	b, err := Marshal(v)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := indentTrusted(&buf, b, prefix, indent); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// indentTrusted is Indent for bytes already proven valid and known compact:
// the index is built for its masks and nothing is validated. end is len(src)
// because Marshal emits no trailing whitespace.
func indentTrusted(dst *bytes.Buffer, src []byte, prefix, indent string) error {
	ix, _ := indexPool.Get().(*index)
	defer func() {
		if ix != nil {
			indexPool.Put(ix)
		}
	}()
	var err error
	ix, err = buildIndexMode(src, ix, true, masksOnly, false)
	if err != nil {
		return err
	}
	if indentParallel(dst, src, ix, prefix, indent, len(src)) {
		return nil
	}
	dst.Grow(len(src) * 2)
	writeIndent(dst, src, ix, prefix, indent, len(src))
	return nil
}

// HTMLEscape appends src to dst with <, >, & and the two Unicode line
// terminators replaced by their \u escapes, so the result can go inside a
// <script> tag without ending it.
//
// It does not parse. In well-formed JSON none of those five can appear outside
// a string literal, so replacing them wherever they occur is the same thing as
// replacing them inside strings, and encoding/json.HTMLEscape makes the same
// bet. Nothing here validates, which is also what encoding/json does.
func HTMLEscape(dst *bytes.Buffer, src []byte) {
	start := 0
	for i, c := range src {
		if c == '<' || c == '>' || c == '&' {
			dst.Write(src[start:i])
			dst.WriteString(`\u00`)
			dst.WriteByte(hexDigits[c>>4])
			dst.WriteByte(hexDigits[c&0xF])
			start = i + 1
			continue
		}
		// U+2028 and U+2029 end a line in JavaScript and not in JSON.
		if c == 0xE2 && i+2 < len(src) && src[i+1] == 0x80 && src[i+2]&^1 == 0xA8 {
			dst.Write(src[start:i])
			dst.WriteString(`\u202`)
			dst.WriteByte(hexDigits[src[i+2]&0xF])
			start = i + 3
		}
	}
	dst.Write(src[start:])
}

// docFront2 finds where the top-level value starts in a Doc already built.
func docFront2(data []byte, d *Doc) (int, error) {
	i := d.skip(0)
	if i >= len(data) {
		return 0, errSyntax("empty input")
	}
	return i, nil
}

// docFront wraps an index in a Doc and returns where the top-level value
// starts, without working out where it ends.
//
// scanRoot does that too, and does it by matching the root's brackets over the
// position array — which is the array these three callers asked not to have
// built. They do not need it: the grammar descent finds the end of the value on
// its way through, which is the same walk it was going to make anyway.
func docFront(data []byte, ix *index) (*Doc, int, error) {
	d := &Doc{data: data, ix: ix, inStr: ix.inStr, noWS: ix.noWS, wsw: ix.wsw}
	i := d.skip(0)
	if i >= len(data) {
		return nil, 0, errSyntax("empty input")
	}
	return d, i, nil
}

// indexAndValidate builds the index and proves the document well-formed, which
// is what Compact and Indent both need before they are allowed to copy
// anything: encoding/json reports a syntax error rather than emitting a
// prefix.
func indexAndValidate(src []byte, ix *index) (*index, *Doc, int, error) {
	// A large document takes the bracket index -- the parallel path -- and,
	// for a root array of containers, validates its elements across workers
	// exactly as Parse and Valid do. Any anomaly falls through to the serial
	// walk below over the same index; masks-only versus brackets changes
	// nothing the callers here read beyond what both modes fill.
	if len(src) >= parallelMinBytes {
		ix2, err2, ran := buildIndexParallel(src, ix, true, false)
		if ran {
			if err2 != nil {
				return ix, nil, 0, err2
			}
			ix = ix2
			if elems, rootClose, shaped := enumerateTopContainers(ix, src, 2); shaped {
				if end, ok := walkTopParallel(src, ix, elems, rootClose); ok {
					d := &Doc{data: src, ix: ix, inStr: ix.inStr, noWS: ix.noWS, wsw: ix.wsw}
					return ix, d, end, nil
				}
			}
			d, start, err := docFront(src, ix)
			if err != nil {
				return ix, nil, 0, err
			}
			end, err := d.validateValue(start)
			if err != nil {
				return ix, nil, 0, err
			}
			if d.skip(end) < len(src) {
				return ix, nil, 0, errSyntax("trailing data after top-level value")
			}
			return ix, d, end, nil
		}
	}
	ix, err := buildIndexMode(src, ix, true, masksOnly, false)
	if err != nil {
		return ix, nil, 0, err
	}
	d, start, err := docFront(src, ix)
	if err != nil {
		return ix, nil, 0, err
	}
	end, err := d.validateValue(start)
	if err != nil {
		return ix, nil, 0, err
	}
	if d.skip(end) < len(src) {
		return ix, nil, 0, errSyntax("trailing data after top-level value")
	}
	return ix, d, end, nil
}
