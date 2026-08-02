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
	d := &Doc{data: data, ix: ix}
	v, end, err := d.value(skipSpace(data, 0))
	if err != nil {
		return nil, err
	}
	if p := skipSpace(data, end); p < len(data) {
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

func skipSpace(b []byte, i int) int {
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
		end, err := d.stringEnd(i)
		if err != nil {
			return Value{}, i, err
		}
		if err := validateString(d.data[i+1 : end-1]); err != nil {
			return Value{}, i, err
		}
		return Value{d: d, kind: String, start: i, end: end}, end, nil
	case c == 't':
		return d.lit(i, "true", Bool)
	case c == 'f':
		return d.lit(i, "false", Bool)
	case c == 'n':
		return d.lit(i, "null", Null)
	case c == '-' || (c >= '0' && c <= '9'):
		end := i
		for end < len(d.data) && isNumberByte(d.data[end]) {
			end++
		}
		if !validNumber(d.data[i:end]) {
			return Value{}, i, errAt("invalid number", i)
		}
		return Value{d: d, kind: Number, start: i, end: end}, end, nil
	}
	return Value{}, i, errAt("unexpected character", i)
}

// validateString checks a string body — the bytes between the quotes — without
// decoding it.
//
// Two rules, both found by fuzzing against encoding/json rather than by reading
// the grammar. A raw control character is not allowed and has to be escaped;
// and only a fixed set of escapes exists, so \0 is invalid however reasonable
// it looks. Both were accepted here and rejected by the standard library, which
// means the two disagreed on documents only one of them would take.
//
// Nothing is built: this walks the escapes and checks them, where decoding
// would allocate a string for every field of every document at parse time.
func validateString(b []byte) error {
	for i := 0; i < len(b); i++ {
		c := b[i]
		if c < 0x20 {
			return errSyntax("control character in string")
		}
		if c != '\\' {
			continue
		}
		i++
		if i >= len(b) {
			return errSyntax("string ends in a backslash")
		}
		switch b[i] {
		case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
		case 'u':
			if i+4 >= len(b) {
				return errSyntax("short \\u escape")
			}
			for k := 1; k <= 4; k++ {
				if !isHex(b[i+k]) {
					return errSyntax("invalid \\u escape")
				}
			}
			i += 4
		default:
			return errSyntax("invalid escape")
		}
	}
	return nil
}

// validNumber checks JSON's number grammar, which is narrower than what
// strconv.ParseFloat accepts.
//
//	-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?
//
// ParseFloat takes "10." and JSON does not, which is how this arrived: fuzzing
// against encoding/json rejected a document this accepted. Checking the grammar
// directly is also cheaper than parsing, and skips the string allocation that
// ParseFloat needed per number.
func validNumber(b []byte) bool {
	i := 0
	if i < len(b) && b[i] == '-' {
		i++
	}
	// An integer part is required, and a leading zero may not be followed by
	// more digits: 0 is a number and 01 is not.
	switch {
	case i < len(b) && b[i] == '0':
		i++
	case i < len(b) && b[i] >= '1' && b[i] <= '9':
		for i < len(b) && isDigit(b[i]) {
			i++
		}
	default:
		return false
	}
	if i < len(b) && b[i] == '.' {
		i++
		if i >= len(b) || !isDigit(b[i]) {
			return false
		}
		for i < len(b) && isDigit(b[i]) {
			i++
		}
	}
	if i < len(b) && (b[i] == 'e' || b[i] == 'E') {
		i++
		if i < len(b) && (b[i] == '+' || b[i] == '-') {
			i++
		}
		if i >= len(b) || !isDigit(b[i]) {
			return false
		}
		for i < len(b) && isDigit(b[i]) {
			i++
		}
	}
	return i == len(b)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func isNumberByte(c byte) bool {
	return c >= '0' && c <= '9' || c == '-' || c == '+' || c == '.' || c == 'e' || c == 'E'
}

func (d *Doc) lit(i int, want string, k Kind) (Value, int, error) {
	if i+len(want) > len(d.data) || string(d.data[i:i+len(want)]) != want {
		return Value{}, i, errAt("invalid literal", i)
	}
	return Value{d: d, kind: k, start: i, end: i + len(want)}, i + len(want), nil
}

// stringEnd returns the offset just past the string starting at i.
//
// Binary search, not a scan. The first version walked every string in the
// document to find the one starting at i, which is O(strings) per string and so
// quadratic overall: a document with 10,000 items took 2.5 seconds against
// encoding/json's 10 milliseconds. The ranges are built in ascending order, so
// the search is available for free.
func (d *Doc) stringEnd(i int) (int, error) {
	strs := d.ix.strs
	lo, hi := 0, len(strs)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if int(strs[mid].open) < i {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo < len(strs) && int(strs[lo].open) == i {
		return int(strs[lo].close) + 1, nil
	}
	return 0, errAt("unterminated string", i)
}

// validateObject checks the grammar of the object at i and returns the offset
// just past it.
//
// Matching the brackets alone is not enough: `{"a"}` has balanced braces and is
// not an object. The contents have to be walked, and walking them is also what
// rejects a trailing comma, a missing colon and a non-string key — all of which
// encoding/json rejects and all of which an index-only parse accepts.
func (d *Doc) validateObject(i int) (int, error) {
	j := skipSpace(d.data, i+1)
	if j < len(d.data) && d.data[j] == '}' {
		return j + 1, nil
	}
	for {
		if j >= len(d.data) || d.data[j] != '"' {
			return 0, errAt("expected a string key", j)
		}
		if _, _, err := d.value(j); err != nil {
			return 0, err
		}
		kend, err := d.stringEnd(j)
		if err != nil {
			return 0, err
		}
		j = skipSpace(d.data, kend)
		if j >= len(d.data) || d.data[j] != ':' {
			return 0, errAt("expected ':' after object key", j)
		}
		_, next, err := d.value(skipSpace(d.data, j+1))
		if err != nil {
			return 0, err
		}
		j = skipSpace(d.data, next)
		if j >= len(d.data) {
			return 0, errAt("unterminated object", i)
		}
		switch d.data[j] {
		case ',':
			j = skipSpace(d.data, j+1)
		case '}':
			return j + 1, nil
		default:
			return 0, errAt("expected ',' or '}'", j)
		}
	}
}

// validateArray is validateObject for arrays.
func (d *Doc) validateArray(i int) (int, error) {
	j := skipSpace(d.data, i+1)
	if j < len(d.data) && d.data[j] == ']' {
		return j + 1, nil
	}
	for {
		_, next, err := d.value(j)
		if err != nil {
			return 0, err
		}
		j = skipSpace(d.data, next)
		if j >= len(d.data) {
			return 0, errAt("unterminated array", i)
		}
		switch d.data[j] {
		case ',':
			j = skipSpace(d.data, j+1)
		case ']':
			return j + 1, nil
		default:
			return 0, errAt("expected ',' or ']'", j)
		}
	}
}

// matchBracket finds the closing bracket for the one at i by walking the
// structural index, not the bytes.
func (d *Doc) matchBracket(i int) (int, error) {
	pos := d.ix.pos
	lo := lowerBound(pos, int32(i))
	if lo >= len(pos) || int(pos[lo]) != i {
		return 0, errAt("internal: opening bracket is not in the index", i)
	}
	depth := 0
	for j := lo; j < len(pos); j++ {
		switch d.data[pos[j]] {
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 {
				return int(pos[j]) + 1, nil
			}
		}
	}
	return 0, errAt("unterminated container", i)
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
