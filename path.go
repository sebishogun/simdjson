package simdjson

// Paths: reaching a value by writing where it is.
//
// gjson's whole argument is that `gjson.Get(json, "user.name")` is what people
// want and a decode into a struct is not. It is right, and the reason it is
// fast — scan to the field and stop, keep nothing — is also the reason it is
// slow the second time: the second query costs exactly what the first did,
// because nothing was remembered.
//
// Here the index is built once and a path walks structural positions, so the
// hundredth query is nearly free. That used to come at the cost of the first
// one; it does not any more, because [GetPath] indexes without validating —
// which is exactly the contract gjson.Get has — and indexing twitter.json that
// way is 57 us against the 244 us a validating parse costs.
//
// The result is that the trade is gone: this is ahead of gjson on one field and
// two and a half times ahead on ten. docs/lazy-paths.md has the numbers.
//
// # The grammar
//
// A path is components separated by dots.
//
//	user.name          a field of a field
//	items.0.id         the first element's id -- a number component indexes
//	                   an array and names a field of an object
//	users.*.name       every element or field, one level
//	a\.b               a literal dot in a name, backslash-escaped
//
// `*` and `?` match a component: `*` any run of characters, `?` exactly one.
// A wildcard that matches several values yields the first; use [Value.ForEach]
// when several is the point.
//
// What is deliberately not here, because it is a query language rather than a
// path and each piece needs its own semantics settled: gjson's `#` length and
// `#(...)` filters, `|` multipath, `@` modifiers, `!` literals and the `[...]`
// and `{...}` subselectors. [Value.Len] answers the first of those already.

import "strings"

// Path returns the value at a dot-separated path, relative to v.
//
// A component that does not exist gives an Invalid Value; see [Value.Exists].
func (v Value) Path(path string) Value {
	if path == "" {
		return v
	}
	for len(path) > 0 && v.kind != Invalid {
		var comp string
		comp, path = cutPath(path)
		v = v.step(comp)
	}
	return v
}

// Path returns the value at a dot-separated path from the document's root.
func (d *Doc) Path(path string) Value { return d.root.Path(path) }

// GetPath indexes data and returns the value at path.
//
// It does not validate the whole document, only the part it walks through —
// the same contract gjson.Get has, and for the same reason: a caller pulling
// one field out of a payload is not asking whether the other fields are
// well-formed, and proving it costs four times what finding the field does.
// Use [Parse] when the answer matters.
//
// For more than one query on the same document, index once with [Parser.Scan]
// or [Parse] and use [Doc.Path]. That is the whole point of having an index and
// it is where this stops being a straight loss against gjson: gjson keeps
// nothing, so its second query costs exactly what its first did.
//
//	one field, near the front   gjson 105 us   this  83 us
//	one field, near the back    gjson 105 us   this  85 us
//	ten fields                  gjson 634 us   this 239 us
//
// It returns no error: a document that does not parse gives an Invalid Value,
// the same as a path that does not exist. [Value.Exists] tells them apart from
// a value that is there.
func GetPath(data []byte, path string) Value {
	d, err := Scan(data)
	if err != nil {
		return Value{}
	}
	return d.Path(path)
}

// step applies one path component to v.
func (v Value) step(comp string) Value {
	if strings.ContainsAny(comp, "*?") {
		return v.wildcard(comp)
	}
	comp = unescapePath(comp)
	if v.kind == Array {
		// A number indexes an array. A name cannot, so an array asked for one
		// has nothing to give.
		n, ok := atoiPath(comp)
		if !ok {
			return Value{}
		}
		return v.Index(n)
	}
	return v.Key(comp)
}

// wildcard returns the first child whose name matches the pattern, or for an
// array the first element -- an array's elements have no names, so every
// pattern matches all of them.
func (v Value) wildcard(pat string) Value {
	var out Value
	switch v.kind {
	case Object:
		v.ForEachKey(func(k string, e Value) bool {
			if matchPath(pat, k) {
				out = e
				return false
			}
			return true
		})
	case Array:
		v.ForEach(func(e Value) bool {
			out = e
			return false
		})
	}
	return out
}

// cutPath splits off the first component, honouring backslash escapes so that
// `a\.b` is one component and not two.
func cutPath(p string) (comp, rest string) {
	for i := 0; i < len(p); i++ {
		switch p[i] {
		case '\\':
			i++ // whatever follows is literal, including a dot
		case '.':
			return p[:i], p[i+1:]
		}
	}
	return p, ""
}

// unescapePath removes the backslashes cutPath stepped over.
func unescapePath(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// atoiPath reads a non-negative decimal index, and reports whether the whole
// component was one. Written out rather than calling strconv because a path
// component is a handful of bytes and this is called once per component.
func atoiPath(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
		if n < 0 {
			return 0, false // overflowed; no array has this many elements
		}
	}
	return n, true
}

// matchPath reports whether name matches a pattern of literals, `*` and `?`.
//
// Iterative with one backtrack point rather than recursive: a pattern like
// `*a*b*c` against a long name is quadratic under the naive recursion and
// linear-ish here, and a path component is attacker-supplied often enough to
// care.
func matchPath(pat, name string) bool {
	var star, mark int
	star = -1
	i, j := 0, 0
	for i < len(name) {
		switch {
		case j < len(pat) && (pat[j] == '?' || pat[j] == name[i]):
			i++
			j++
		case j < len(pat) && pat[j] == '*':
			star, mark = j, i
			j++
		case star >= 0:
			j = star + 1
			mark++
			i = mark
		default:
			return false
		}
	}
	for j < len(pat) && pat[j] == '*' {
		j++
	}
	return j == len(pat)
}
