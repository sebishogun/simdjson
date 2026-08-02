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
	ix, err := buildIndex(data, p.ix)
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
	ix, err := buildIndex(data, nil)
	if err != nil {
		return nil, err
	}
	return finish(data, ix)
}

// finish validates the document and records its root.
func finish(data []byte, ix *index) (*Doc, error) {
	// Every string in the document, checked in one pass over the masks rather
	// than a byte at a time as each one is met. Scan does not call this, which
	// is part of what Scan gives up.
	if err := ix.validateStrings(data); err != nil {
		return nil, err
	}
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
	if d.noWS || i >= len(d.data) || !spaceByte[d.data[i]] {
		return i
	}
	return d.skipRun(i)
}

// skipRun steps over a run of whitespace that has already been found to start
// at i.
//
// A bit scan over the whitespace mask, so a run of any length costs the same as
// a run of one — which matters more than it sounds: citm_catalog.json is 71%
// whitespace, four spaces of indentation at a time, and the byte loop this
// replaces was 37% of its parse.
//
// The mask is only built by Parse. After Scan it is nil and the byte loop
// stands in, which is the same trade Scan makes everywhere else.
func (d *Doc) skipRun(i int) int {
	if d.wsw == nil {
		return skipSpaceSlow(d.data, i)
	}
	w := i >> 6
	x := ^d.wsw[w] &^ (1<<uint(i&63) - 1)
	if x != 0 {
		if e := w<<6 + bits.TrailingZeros64(x); e < len(d.data) {
			return e
		}
		return len(d.data)
	}
	return d.skipRunAcross(w + 1)
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
	b := d.data
	j := i
	if j < len(b) && b[j] == '-' {
		j++
	}
	if j >= len(b) {
		return 0, false
	}
	switch {
	case b[j] == '0':
		// A leading zero admits no more digits, which is what rejects `01`.
		j++
	case b[j] >= '1' && b[j] <= '9':
		j++
		for j < len(b) && isDigit(b[j]) {
			j++
		}
	default:
		return 0, false
	}
	if j < len(b) && b[j] == '.' {
		j++
		if j >= len(b) || !isDigit(b[j]) {
			return 0, false
		}
		for j < len(b) && isDigit(b[j]) {
			j++
		}
	}
	if j < len(b) && (b[j] == 'e' || b[j] == 'E') {
		j++
		if j < len(b) && (b[j] == '+' || b[j] == '-') {
			j++
		}
		if j >= len(b) || !isDigit(b[j]) {
			return 0, false
		}
		for j < len(b) && isDigit(b[j]) {
			j++
		}
	}
	return j, true
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
	if i >= len(d.data) {
		return 0, errAt("unexpected end of input", i)
	}
	switch c := d.data[i]; {
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
	j := d.skip(i + 1)
	if j < len(d.data) && d.data[j] == '}' {
		return j + 1, nil
	}
	for {
		if j >= len(d.data) || d.data[j] != '"' {
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
		if j >= len(d.data) || d.data[j] != ':' {
			return 0, errAt("expected ':' after object key", j)
		}
		next, err := d.validateValue(d.skip(j + 1))
		if err != nil {
			return 0, err
		}
		j = d.skip(next)
		if j >= len(d.data) {
			return 0, errAt("unterminated object", i)
		}
		switch d.data[j] {
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
	j := d.skip(i + 1)
	if j < len(d.data) && d.data[j] == ']' {
		return j + 1, nil
	}
	for {
		next, err := d.validateValue(j)
		if err != nil {
			return 0, err
		}
		j = d.skip(next)
		if j >= len(d.data) {
			return 0, errAt("unterminated array", i)
		}
		switch d.data[j] {
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
	lo := lowerBound(pos, int32(i))
	if lo >= len(pos) || int(pos[lo]) != i {
		return 0, errAt("internal: opening bracket is not in the index", i)
	}
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
