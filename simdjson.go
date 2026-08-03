// Package simdjson parses JSON by finding the whole document's structure in a
// few vector passes, then walking that instead of the bytes.
//
// It is built on [simd.go](https://github.com/sebishogun/simd), so it needs no
// cgo and runs the same way on amd64, arm64, riscv64, s390x, ppc64le and
// loong64 — unlike the existing Go ports of simdjson, which are amd64 with
// hand-written assembly.
//
//	doc, err := simdjson.Parse(data)
//	name := doc.Get("user", "name").String()
//	age  := doc.Get("user", "age").Int()
//
// # How it works
//
// Two stages, which is the design simdjson introduced.
//
// Stage one finds every structural character — the braces, brackets, colons and
// commas — in one vector pass each, and works out which quotes really open and
// close strings rather than being escaped. A conventional parser reads a byte
// and branches on what it is, which is a dependent and unpredictable branch per
// byte; this makes eight branch-free passes over the document instead, and
// eight passes with no branches beat one pass with a branch per byte.
//
// Stage two walks those positions. A document of a megabyte might have fifty
// thousand structural characters, so the second stage sees fifty thousand
// items rather than a million bytes.
//
// # What it is for
//
// Pulling a few values out of a document, which is most of what JSON is used
// for and the case encoding/json is worst at — it decodes everything to reach
// anything. [Doc.Get] navigates the index without decoding what it passes.
//
// It is not a replacement for encoding/json. There is no struct unmarshalling,
// no tags, no interfaces, no streaming. If you want a Go value, use the
// standard library; if you want three fields out of a large payload, this is
// several times faster.
package simdjson

import (
	"fmt"
	"math/bits"
	"unicode/utf8"
	"unsafe"
)

type syntaxError struct{ msg string }

func (e *syntaxError) Error() string { return "simdjson: " + e.msg }

func errSyntax(msg string) error { return &syntaxError{msg} }

func errAt(msg string, pos int) error {
	return &syntaxError{fmt.Sprintf("%s at byte %d", msg, pos)}
}

// Kind is the type of a JSON value.
type Kind uint8

const (
	Invalid Kind = iota
	Null
	Bool
	Number
	String
	Array
	Object
)

func (k Kind) String() string {
	switch k {
	case Null:
		return "null"
	case Bool:
		return "bool"
	case Number:
		return "number"
	case String:
		return "string"
	case Array:
		return "array"
	case Object:
		return "object"
	}
	return "invalid"
}

// Doc is a parsed document. It holds the input and its structural index; no
// values are decoded until they are asked for.
type Doc struct {
	data []byte
	ix   *index
	root Value

	// inStr is ix.inStr, copied here so the hot path is one slice index
	// rather than a walk through the index struct. It is read once per string.
	inStr []uint64

	// brAt is where matchBracket last looked. Containers are met in ascending
	// order by every walk that matters — validation, ForEach, Decode — so the
	// bracket wanted is almost always the next one in the array, and the
	// binary search behind it is only for navigation that jumps.
	brAt int

	// strbuf carries the bytes of every string this document has decoded.
	//
	// A decoded string has to own its bytes — the caller may outlive the
	// document — so each one was its own allocation, and twitter.json into a
	// struct made 1,309 of them against goccy's 105 for the same 707 KB. The
	// bytes are the same either way; what differs is how many times the
	// allocator is asked.
	//
	// So they are carved out of one growing buffer. When it fills, a new one is
	// started and the old is left behind: the strings already pointing into it
	// keep it alive, which is exactly the guarantee needed and costs nothing to
	// arrange. The buffer is only ever appended to, so a string handed out
	// earlier can never be overwritten.
	//
	// It lives on the Doc, which is fresh for every parse. Reusing it across
	// parses would hand the next document's bytes to the previous document's
	// strings.
	strbuf []byte

	// scratch holds one string's bytes while its escapes are undone, before it
	// is copied into strbuf. Reused, because only one string is in flight.
	scratch []byte

	// strictSkip makes the decoder validate the values it steps over rather
	// than jumping past them.
	//
	// Unmarshal needs the whole document proven well-formed, including the
	// fields the destination type does not name — that is encoding/json's
	// contract. Doing it as a separate descent before decoding means walking
	// the document twice, and the second walk is the decode. So the decoder
	// does both in one pass: it validates what it decodes anyway, and this
	// tells it to validate what it skips too.
	//
	// Off after Parse, which has already proven everything, and off after Scan,
	// which is documented not to.
	strictSkip bool

	// useNumber and disallowUnknown are the two things encoding/json lets a
	// Decoder ask for that change what a decode means rather than how fast it
	// is. They live on the Doc because that is what the decode walks.
	useNumber       bool
	disallowUnknown bool

	// noWS is ix.noWS: the document has no whitespace between its tokens, so
	// every skip is the identity. wsw is ix.wsw, the whitespace mask, which
	// turns skipping a run of it into a bit scan; it is nil after Scan, which
	// does not build it, and the byte loop stands in. See index.ws.
	noWS bool
	wsw  []uint64

	// navigating is false during the initial pass, where containers are
	// descended into and checked, and true afterwards, where their extent is
	// taken from the index instead. Without it every Get and ForEach pays for
	// the validation again on everything it steps over.
	navigating bool
}

// Parser parses documents, reusing its index buffers between them.
//
// A server handling many payloads should keep one per goroutine: [Parse]
// allocates a fresh index each time, and for a document of a few hundred
// kilobytes that index is several times the size of the document itself. A
// Parser reuses it, so the second and later documents allocate almost nothing.
//
// A Parser is not safe for concurrent use.
type Parser struct{ ix *index }

// Parse indexes and validates data, reusing p's buffers.
//
// The returned Doc borrows those buffers, so it is only valid until the next
// call to Parse on the same Parser. Use [Parse] if a Doc has to outlive that.
func (p *Parser) Parse(data []byte) (*Doc, error) {
	ix, err := buildIndex(data, p.ix, true)
	if err != nil {
		return nil, err
	}
	p.ix = ix
	return finish(data, ix)
}

// Parse indexes data and validates its structure.
//
// The returned Doc keeps data — it is not copied, and every string a Value
// yields points into it unless the string contains an escape.
func Parse(data []byte) (*Doc, error) {
	ix, err := buildIndex(data, nil, true)
	if err != nil {
		return nil, err
	}
	return finish(data, ix)
}

// finish validates the document and records its root.
func finish(data []byte, ix *index) (*Doc, error) {
	d := &Doc{data: data, ix: ix, inStr: ix.inStr, noWS: ix.noWS, wsw: ix.wsw}
	v, end, err := d.value(d.skip(0))
	if err != nil {
		return nil, err
	}
	if p := d.skip(end); p < len(data) {
		return nil, errAt("trailing data", p)
	}
	d.root = v
	d.navigating = true
	return d, nil
}

// Root returns the document's top-level value.
func (d *Doc) Root() Value { return d.root }

// Get walks a path of object keys and returns the value at the end.
//
// A missing key, or a path that runs into a non-object, yields an Invalid
// Value rather than an error — chaining is the common case and an error at
// every step would be unusable. Check [Value.Exists].
func (d *Doc) Get(path ...string) Value {
	v := d.root
	for _, key := range path {
		v = v.Key(key)
		if v.kind == Invalid {
			return v
		}
	}
	return v
}

// Value is one JSON value inside a document.
type Value struct {
	d     *Doc
	kind  Kind
	start int // first byte
	end   int // one past the last byte
}

// Kind returns the value's type.
func (v Value) Kind() Kind { return v.kind }

// Exists reports whether the value was found.
func (v Value) Exists() bool { return v.kind != Invalid }

// Raw returns the value's bytes, undecoded, pointing into the document.
func (v Value) Raw() []byte {
	if v.kind == Invalid {
		return nil
	}
	return v.d.data[v.start:v.end]
}

// skipSpace returns the offset of the first byte at or after i that is not
// JSON whitespace.
//
// Deriving this from the index instead — indexing every token start so the next
// one is an array read — was tried and is slower; see docs/wrong.md.
// skip is skipSpace, except that on a document with no whitespace outside its
// strings — which is most JSON that travels over a wire — it is the identity
// and stage one already proved it.
//
// The branch is on a field that never changes during a parse, so it predicts
// perfectly, where the skip it replaces is a load of the byte and a dependent
// load into a table.
func (d *Doc) skip(i int) int {
	// One comparison, not a table lookup. Every byte JSON allows as whitespace
	// is at or below 0x20, so anything above it ends the run — and the table
	// version was a load that depended on the byte just loaded. A control
	// character below 0x20 that is not whitespace falls through to skipRun,
	// whose mask marks only the real four, so it returns immediately and the
	// grammar rejects the byte where it stands.
	if d.noWS || i >= len(d.data) || d.data[i] > ' ' {
		return i
	}
	// Nothing may be added here. This function inlines at cost 79 against a
	// budget of 80, it is called once or twice per token, and losing the inline
	// costs 6% — measured, by adding a fast path for the single-byte runs that
	// are 46% of the whitespace in twitter.json. See docs/wrong.md.
	return d.skipRun(i)
}

// skipRun steps over a run of whitespace that has already been found to start
// at i.
//
// The byte test in skip that gets here is not redundant work, though it looks
// it: the mask alone answers both cases, since a non-whitespace byte's bit is
// clear and the scan returns i. Doing that instead pushes skip past the inlining
// budget, and losing the inline costs far more than the two loads it saves --
// twitter 293 us against 243, citm 756 against 650, interleaved.
//
// A bit scan over the whitespace mask, so a run of any length costs the same as
// a run of one — which matters more than it sounds: citm_catalog.json is 71%
// whitespace, four spaces of indentation at a time, and the byte loop this
// replaces was 37% of its parse.
//
// The mask is only built by Parse. After Scan it is nil and the byte loop
// stands in, which is the same trade Scan makes everywhere else.
func (d *Doc) skipRun(i int) int {
	w := i >> 6
	if w >= len(d.wsw) {
		// Covers a Doc from Scan, which has no mask at all.
		return skipSpaceSlow(d.data, i)
	}
	x := ^d.wsw[w] &^ (1<<uint(i&63) - 1)
	if x == 0 {
		return d.skipRunAcross(w + 1)
	}
	// No clamp against len(data): the bits past the end of the document are
	// cleared in the mask, so the first clear bit at or after i is at most
	// len(data). A run that reaches the end of its word goes to skipRunAcross,
	// which does clamp.
	return w<<6 + bits.TrailingZeros64(x)
}

// skipRunAcross continues a whitespace run that filled the rest of its word.
// Split out so skipRun stays loop-free and inlines into skip.
func (d *Doc) skipRunAcross(w int) int {
	for ; w < len(d.wsw); w++ {
		if x := ^d.wsw[w]; x != 0 {
			if e := w<<6 + bits.TrailingZeros64(x); e < len(d.data) {
				return e
			}
			break
		}
	}
	return len(d.data)
}

func skipSpace(b []byte, i int) int {
	// Loop-free so it inlines. Most JSON in flight has no whitespace between
	// tokens at all, and even indented JSON has none most of the time a token
	// ends, so the first byte answers it and the call disappears.
	if i < len(b) && !spaceByte[b[i]] {
		return i
	}
	return skipSpaceSlow(b, i)
}

func skipSpaceSlow(b []byte, i int) int {
	for i < len(b) {
		switch b[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
}

// value parses the value starting at i and returns it with the offset just past
// it.
//
// Containers are not descended into here — their extent is found by matching
// brackets over the structural index, which is a walk over a handful of entries
// rather than over their contents. That is what makes skipping a large nested
// object cheap.
func (d *Doc) value(i int) (Value, int, error) {
	if i >= len(d.data) {
		return Value{}, i, errAt("unexpected end of input", i)
	}
	switch c := d.data[i]; {
	case c == '{' || c == '[':
		// Once the document has been through its initial pass, the extent of a
		// container comes from matching brackets over the structural index —
		// a walk over a handful of positions. Descending into it again would
		// re-validate a subtree that navigation is only stepping past, which
		// made ForEach over ten thousand items quadratic: 20 ms against the
		// 0.3 ms the index itself costs.
		var end int
		var err error
		if d.navigating {
			end, err = d.matchBracket(i)
		} else if c == '{' {
			end, err = d.validateObject(i)
		} else {
			end, err = d.validateArray(i)
		}
		if err != nil {
			return Value{}, i, err
		}
		k := Object
		if c == '[' {
			k = Array
		}
		return Value{d: d, kind: k, start: i, end: end}, end, nil
	case c == '"':
		end, ok := d.stringEnd(i)
		if !ok {
			var err error
			if end, err = d.stringEndSlow(i); err != nil {
				return Value{}, i, err
			}
		}
		// The body is not checked here. Parse checked every string in the
		// document before the descent started; Scan checks none, by design.
		return Value{d: d, kind: String, start: i, end: end}, end, nil
	case c == 't':
		return d.lit(i, "true", Bool)
	case c == 'f':
		return d.lit(i, "false", Bool)
	case c == 'n':
		return d.lit(i, "null", Null)
	case c == '-' || (c >= '0' && c <= '9'):
		end, ok := d.number(i)
		if !ok {
			return Value{}, i, errAt("invalid number", i)
		}
		return Value{d: d, kind: Number, start: i, end: end}, end, nil
	}
	return Value{}, i, errAt("unexpected character", i)
}

// number scans and validates the number starting at i in one pass, returning
// the offset just past it.
//
// It used to be two passes: a scan with isNumberByte to find where the number
// ended, then validNumber walking the same bytes again to check the grammar.
// On canada.json — 2.25 MB that is almost entirely coordinate pairs — those
// three functions were 37% of the parse between them.
//
// The grammar, which is the whole of it:
//
//	-? ( 0 | [1-9][0-9]* ) ( . [0-9]+ )? ( [eE] [+-]? [0-9]+ )?
//
// Trailing junk is not rejected here. `1x` returns a number ending at the x,
// and the caller — which is looking for a comma or a closing bracket next —
// rejects it there. That is the same division of labour as before.
func (d *Doc) number(i int) (int, bool) {
	// The cursor is a uint, and that is the whole of why this function is
	// fifteen percent of Valid on canada.json.
	//
	// With an int, the compiler cannot prove j is not negative, so `b[j]` under
	// `j < len(b)` compiles to two branches: the signed test the source asked
	// for, and an unsigned bounds check it did not. That makes each of the
	// three digit loops 24 bytes, and Go's alignment pass then spends three of
	// them on a `nopl` to put the loop's *condition* on a 32-byte boundary —
	// which leaves the branch target, the increment, three bytes earlier, in
	// whatever came before. A uint cursor cannot be negative, the second branch
	// goes, and the loop is 21 bytes needing no padding.
	//
	// It has to be a uint rather than a slice taken at i. Re-slicing gets the
	// same loop shape, and building a fresh slice header at each of the three
	// loops cost 3.07 billion instructions across canada.json — the shape was
	// bought and then paid for again in setup. This form has neither: 59.92
	// billion instructions against 60.41 for the int version it replaces.
	//
	// This is not cosmetic. The loop runs once per digit byte, about 1.6
	// million times per pass over canada.json, and when its 24 bytes straddle
	// two 64-byte lines the frontend stalls two and a half times as often for
	// no other reason at all. Entry 13 in docs/wrong.md has the disassembly and
	// the counters.
	b := d.data
	n := uint(len(b))
	j := uint(i)
	if j < n && b[j] == '-' {
		j++
	}
	if j >= n {
		return 0, false
	}
	switch {
	case b[j] == '0':
		// A leading zero admits no more digits, which is what rejects `01`.
		j++
	case b[j] >= '1' && b[j] <= '9':
		j++
		for j < n && isDigit(b[j]) {
			j++
		}
	default:
		return 0, false
	}
	if j < n && b[j] == '.' {
		j++
		if j >= n || !isDigit(b[j]) {
			return 0, false
		}
		for j < n && isDigit(b[j]) {
			j++
		}
	}
	if j < n && (b[j] == 'e' || b[j] == 'E') {
		j++
		if j < n && (b[j] == '+' || b[j] == '-') {
			j++
		}
		if j >= n || !isDigit(b[j]) {
			return 0, false
		}
		for j < n && isDigit(b[j]) {
			j++
		}
	}
	return int(j), true
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

// spaceByte is a lookup table rather than the four-wide comparison chain it
// replaced. It is the first test in every whitespace skip.
//
// The whitespace one looked wrong afterwards: a table lookup is a load that
// *depends* on the byte just loaded, where four compares are register work with
// no such chain, and skipSpace's flat time in the profile doubled. Reverting it
// and measuring interleaved — table, chain, table, chain — says otherwise:

//	table  952467 ns    chain  998914 ns
//	table  947234 ns    chain  997093 ns
//
// The flat-time move was an artefact of what inlined where. Profiles locate
// cost; they do not settle an A/B.
var spaceByte = func() (t [256]bool) {
	t[' '], t['\t'], t['\n'], t['\r'] = true, true, true, true
	return
}()

func (d *Doc) lit(i int, want string, k Kind) (Value, int, error) {
	if i+len(want) > len(d.data) || string(d.data[i:i+len(want)]) != want {
		return Value{}, i, errAt("invalid literal", i)
	}
	return Value{d: d, kind: k, start: i, end: i + len(want)}, i + len(want), nil
}

// stringEnd returns the offset just past the string whose opening quote is at
// i, and whether it is terminated inside its own word of the mask.
//
// Small and loop-free on purpose: it has to inline, because it is called once
// per string and the surrounding work is a handful of instructions. The version
// before this one returned an error and carried its own fallback, which cost
// 317 against an inlining budget of 80 — so every string paid a call, and the
// call was most of what the function cost.
//
// The in-string mask marks the opening quote and the body and clears the
// closing quote, so the answer is the first clear bit at or after i+1.
func (d *Doc) stringEnd(i int) (int, bool) {
	j := i + 1
	if j >= len(d.data) {
		return 0, false
	}
	w := j >> 6
	x := ^d.inStr[w] &^ (1<<uint(j&63) - 1)
	if x == 0 {
		return 0, false
	}
	end := w<<6 + bits.TrailingZeros64(x)
	if end >= len(d.data) {
		return 0, false
	}
	return end + 1, true
}

// stringEndSlow handles a string that runs past the end of its mask word, and
// turns a genuine failure into the error.
//
// See index.stringEndAt for the versions this whole thing replaced and why
// each of them was worse.
func (d *Doc) stringEndSlow(i int) (int, error) {
	j := i + 1
	if j < len(d.data) {
		// Walked here rather than delegating to index.stringEndAt, which does
		// the same thing: a string that crosses a word boundary was paying two
		// non-inlined calls, and on twitter.json — where strings average
		// thirty-four bytes, so about half of them cross one — that was the
		// largest single item in the profile.
		w := j >> 6
		x := ^d.inStr[w] &^ (1<<uint(j&63) - 1)
		for x == 0 {
			w++
			if w >= len(d.inStr) {
				return 0, errAt("unterminated string", i)
			}
			x = ^d.inStr[w]
		}
		if e := w<<6 + bits.TrailingZeros64(x); e < len(d.data) {
			return e + 1, nil
		}
	}
	return 0, errAt("unterminated string", i)
}

// validateValue is value() without the Value.
//
// The descent throws away every Value it builds — validateObject and
// validateArray want only the offset the value ends at — and building one is
// five words stored and returned per element. On canada.json, which is a
// couple of million numbers, that is the single most repeated thing the parse
// does.
//
// It is a copy of value()'s dispatch rather than a flag on it, because a flag
// would be a branch on the hottest path in the package to save a duplicate that
// fits on a screen. The two must agree, and TestValidateValueMatchesValue holds
// them to it.
func (d *Doc) validateValue(i int) (int, error) {
	data := d.data
	if i >= len(data) {
		return 0, errAt("unexpected end of input", i)
	}
	switch c := data[i]; {
	case c == '{':
		return d.validateObject(i)
	case c == '[':
		return d.validateArray(i)
	case c == '"':
		end, ok := d.stringEnd(i)
		if !ok {
			return d.stringEndSlow(i)
		}
		return end, nil
	case c == 't':
		return d.litEnd(i, "true")
	case c == 'f':
		return d.litEnd(i, "false")
	case c == 'n':
		return d.litEnd(i, "null")
	case c == '-' || (c >= '0' && c <= '9'):
		end, ok := d.number(i)
		if !ok {
			return 0, errAt("invalid number", i)
		}
		return end, nil
	}
	return 0, errAt("unexpected character", i)
}

// intern copies b into the document's string buffer and returns it as a string
// without a second copy.
//
// unsafe.String over bytes this package owns and never mutates. The buffer is
// append-only and a fresh backing array is started rather than growing in
// place, so the bytes behind a returned string are immutable for its lifetime.
func (d *Doc) intern(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if len(d.strbuf)+len(b) > cap(d.strbuf) {
		// A new buffer rather than append's growth, because append would copy
		// the old contents and the strings pointing at them do not need moving.
		n := 2 * cap(d.strbuf)
		if n < len(b)+4096 {
			n = len(b) + 4096
		}
		d.strbuf = make([]byte, 0, n)
	}
	start := len(d.strbuf)
	d.strbuf = append(d.strbuf, b...)
	return unsafe.String(&d.strbuf[start], len(b))
}

// decodeStr returns the contents of the string whose quotes are at [start,end),
// carved out of the document's string buffer.
//
// Three cases, in the order they occur. Plain ASCII is copied straight in.
// Valid UTF-8 with no escape is the same — being non-ASCII is not a reason to
// allocate, and on a document of tweets it is the common case, which is why the
// first version of this went through unquote and left 92% of every decode's
// allocations there. Only a string that needs bytes changed builds them first.
func (d *Doc) decodeStr(start, end int) string {
	in := d.data[start+1 : end-1]
	if plainASCII(in) {
		return d.intern(in)
	}
	if indexEscape(in) < 0 {
		// Per string, not once for the document. Validating the whole input in
		// one vector pass is correct — a string's boundaries are quotes, and
		// cutting valid UTF-8 at an ASCII byte cannot make it invalid — and it
		// is 17% slower: the strings actually decoded are a small fraction of
		// the document, so it checks about ten times more bytes than it saves.
		// Interleaved: 587/582 us against 496/502.
		if utf8.Valid(in) {
			return d.intern(in)
		}
		return d.intern([]byte(sanitize(string(in))))
	}
	d.scratch = unescapeInto(d.scratch[:0], in)
	return d.intern(d.scratch)
}

// skipValue returns the offset just past the value at i without looking inside
// it.
//
// The document has already been validated by the time anything calls this, so a
// container's extent is the bracket paired with its opening one — a lookup, not
// a walk. validateValue would descend and check the whole subtree again, which
// is right during the initial pass and wrong afterwards: on twitter.json, where
// a struct names twelve of the thirty keys each status carries, skipping the
// other eighteen by re-validating them was 35% of the decode.
func (d *Doc) skipValue(i int) (int, error) {
	if i >= len(d.data) {
		return 0, errAt("unexpected end of input", i)
	}
	switch c := d.data[i]; {
	case c == '{' || c == '[':
		return d.matchBracket(i)
	case c == '"':
		if end, ok := d.stringEnd(i); ok {
			return end, nil
		}
		return d.stringEndSlow(i)
	case c == 't':
		return d.litEnd(i, "true")
	case c == 'f':
		return d.litEnd(i, "false")
	case c == 'n':
		return d.litEnd(i, "null")
	case c == '-' || (c >= '0' && c <= '9'):
		end, ok := d.number(i)
		if !ok {
			return 0, errAt("invalid number", i)
		}
		return end, nil
	}
	return 0, errAt("unexpected character", i)
}

// litEnd is lit() without the Value, for the same reason.
func (d *Doc) litEnd(i int, want string) (int, error) {
	if i+len(want) > len(d.data) || string(d.data[i:i+len(want)]) != want {
		return 0, errAt("invalid literal", i)
	}
	return i + len(want), nil
}

// validateObject checks the grammar of the object at i and returns the offset
// just past it.
//
// Matching the brackets alone is not enough: `{"a"}` has balanced braces and is
// not an object. The contents have to be walked, and walking them is also what
// rejects a trailing comma, a missing colon and a non-string key — all of which
// encoding/json rejects and all of which an index-only parse accepts.
func (d *Doc) validateObject(i int) (int, error) {
	data := d.data
	j := d.skip(i + 1)
	if j < len(data) && data[j] == '}' {
		return j + 1, nil
	}
	for {
		if j >= len(data) || data[j] != '"' {
			return 0, errAt("expected a string key", j)
		}
		// Straight to the string, not through value(). The byte at j has just
		// been checked to be a quote, so the dispatch would re-derive what is
		// already known, and the Value it builds is five words that nothing
		// reads — only the key's end is wanted here. Keys are about half of
		// every value() call on an object-heavy document.
		//
		// The string's body is not checked here either: Parse validated every
		// string in the document before the descent started.
		kend, ok := d.stringEnd(j)
		if !ok {
			var err error
			if kend, err = d.stringEndSlow(j); err != nil {
				return 0, err
			}
		}
		j = d.skip(kend)
		if j >= len(data) || data[j] != ':' {
			return 0, errAt("expected ':' after object key", j)
		}
		next, err := d.validateValue(d.skip(j + 1))
		if err != nil {
			return 0, err
		}
		j = d.skip(next)
		if j >= len(data) {
			return 0, errAt("unterminated object", i)
		}
		switch data[j] {
		case ',':
			j = d.skip(j + 1)
		case '}':
			return j + 1, nil
		default:
			return 0, errAt("expected ',' or '}'", j)
		}
	}
}

// validateArray is validateObject for arrays.
func (d *Doc) validateArray(i int) (int, error) {
	data := d.data
	j := d.skip(i + 1)
	if j < len(data) && data[j] == ']' {
		return j + 1, nil
	}
	for {
		next, err := d.validateValue(j)
		if err != nil {
			return 0, err
		}
		j = d.skip(next)
		if j >= len(data) {
			return 0, errAt("unterminated array", i)
		}
		switch data[j] {
		case ',':
			j = d.skip(j + 1)
		case ']':
			return j + 1, nil
		default:
			return 0, errAt("expected ',' or ']'", j)
		}
	}
}

// matchBracket returns the offset just past the bracket closing the one at i.
//
// A lookup, because stage one paired every bracket as it extracted them
// over the index. It used to walk the positions from i counting depth, which
// made stepping over a nested value cost the size of that value — and Index(j)
// the sum of everything before j.
func (d *Doc) matchBracket(i int) (int, error) {
	pos := d.ix.pos
	// A cursor first. Decoding canada.json, which is a couple of hundred
	// thousand nested arrays, spent 9.6% of its time in the binary search this
	// skips.
	if k := d.brAt; k < len(pos) && int(pos[k]) == i {
		d.brAt = k + 1
		return int(pos[d.ix.match[k]]) + 1, nil
	}
	// Galloping from the cursor, not a search over the whole array.
	//
	// The cursor misses whenever a subtree was validated rather than decoded:
	// those brackets are stepped over without the cursor moving, so the next
	// container wanted is some distance ahead. Doubling out from the cursor and
	// then bisecting costs the logarithm of that distance instead of the
	// logarithm of the index, and the distance is a subtree rather than a
	// document.
	lo := d.brAt
	if lo >= len(pos) || int(pos[lo]) > i {
		lo = 0
	}
	hi := len(pos)
	for step := 1; lo+step < len(pos); step *= 2 {
		if int(pos[lo+step]) > i {
			hi = lo + step
			break
		}
		lo += step
	}
	lo += lowerBound(pos[lo:hi], int32(i))
	if lo >= len(pos) || int(pos[lo]) != i {
		return 0, errAt("internal: opening bracket is not in the index", i)
	}
	d.brAt = lo + 1
	return int(pos[d.ix.match[lo]]) + 1, nil
}

func lowerBound(a []int32, target int32) int {
	lo, hi := 0, len(a)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if a[mid] < target {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}
