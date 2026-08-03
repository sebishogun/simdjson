package simdjson

// Changing a value by writing where it is.
//
// sjson is the only library in the field with a string-path set; fastjson and
// sonic can only mutate a DOM they already built, which means parsing the whole
// document into objects first. This works on the bytes: find the extent of the
// value at the path, splice, done. The index makes finding it a walk over
// structural positions rather than a scan.
//
// Everything here returns a new slice and never writes through the input. sjson
// has a ReplaceInPlace option that rewrites the caller's []byte when the new
// value fits, which is a real speed-up and a real way to corrupt a buffer
// somebody else is holding. If it turns up in a profile it can be added
// explicitly; it should not be the default.

import (
	"strings"
	"unicode/utf8"
)

// SetPath returns data with the value at path replaced by v, encoding v with
// [Marshal].
//
// Missing structure is created: setting `a.b.c` on `{}` gives
// `{"a":{"b":{"c":...}}}`. A numeric component creates an array only if it is 0
// or the path already leads to an array; sjson pads with nulls for a larger
// index and that is a footgun rather than a feature, so an index past the end
// of an existing array appends instead.
//
// The path grammar is [Value.Path]'s, minus the wildcards: `*` and `?` have no
// single answer to write to, so a path containing either is an error.
func SetPath(data []byte, path string, v any) ([]byte, error) {
	raw, err := Marshal(v)
	if err != nil {
		return nil, err
	}
	return SetRawPath(data, path, raw)
}

// SetRawPath is [SetPath] with the replacement given as JSON text rather than a
// Go value.
//
// raw is validated before it is spliced in, because a Set that produces a
// document which no longer parses is worse than an error.
func SetRawPath(data []byte, path string, raw []byte) ([]byte, error) {
	if path == "" {
		return nil, errSyntax("empty path")
	}
	if strings.ContainsAny(path, "*?") {
		return nil, errSyntax("cannot set through a wildcard path")
	}
	// A key this path could never find again. JSON strings are text, so a key
	// is written through the same escaping every other string gets, and that
	// replaces an invalid byte with U+FFFD -- after which the path that created
	// the key does not match it. Found by fuzzing for exactly that round trip.
	if !utf8.ValidString(path) {
		return nil, errSyntax("path is not valid UTF-8")
	}
	if !Valid(raw) {
		return nil, errSyntax("replacement is not valid JSON")
	}
	d, err := Parse(data)
	if err != nil {
		return nil, err
	}
	// The whole path exists: replace the value where it sits.
	if v := d.Path(path); v.Exists() {
		return splice(data, v.start, v.end, raw), nil
	}
	return insertPath(d, data, path, raw)
}

// DeletePath returns data with the value at path removed, along with its key if
// it had one and the separating comma if it needed one.
//
// A path that does not exist is not an error: the document comes back
// unchanged, which is what "make sure this is not there" should do.
func DeletePath(data []byte, path string) ([]byte, error) {
	if path == "" {
		return nil, errSyntax("empty path")
	}
	if strings.ContainsAny(path, "*?") {
		return nil, errSyntax("cannot delete through a wildcard path")
	}
	if !utf8.ValidString(path) {
		return nil, errSyntax("path is not valid UTF-8")
	}
	d, err := Parse(data)
	if err != nil {
		return nil, err
	}
	v := d.Path(path)
	if !v.Exists() {
		return append([]byte(nil), data...), nil
	}
	parent, last := cutLast(path)
	p := d.Path(parent)
	if !p.Exists() {
		return append([]byte(nil), data...), nil
	}
	start, end := deleteSpan(data, p, v, last)
	return splice(data, start, end, nil), nil
}

// deleteSpan widens the value's extent to take in whatever else has to go: the
// key in front of it for an object member, and one of the commas around it.
func deleteSpan(data []byte, parent, v Value, last string) (int, int) {
	start, end := v.start, v.end
	if parent.kind == Object {
		// Back up over the colon, the whitespace and the key.
		i := start - 1
		for i > parent.start && isLineSpace(data[i]) {
			i--
		}
		if i > parent.start && data[i] == ':' {
			i--
			for i > parent.start && isLineSpace(data[i]) {
				i--
			}
			if i > parent.start && data[i] == '"' {
				// Walk back to the opening quote of the key. Escapes cannot
				// contain an unescaped quote, so the first unescaped one going
				// backwards is it.
				i--
				for i > parent.start {
					if data[i] == '"' && !escapedAt(data, i) {
						break
					}
					i--
				}
				start = i
			}
		}
	}
	// One comma, whichever side has one. Taking the one in front keeps the
	// document valid when the deleted member was the last.
	j := start - 1
	for j > parent.start && isLineSpace(data[j]) {
		j--
	}
	if j > parent.start && data[j] == ',' {
		return j, end
	}
	k := end
	for k < len(data) && isLineSpace(data[k]) {
		k++
	}
	if k < len(data) && data[k] == ',' {
		return start, k + 1
	}
	return start, end
}

// escapedAt reports whether the byte at i is preceded by an odd number of
// backslashes, and so is escaped rather than structural.
func escapedAt(data []byte, i int) bool {
	n := 0
	for j := i - 1; j >= 0 && data[j] == '\\'; j-- {
		n++
	}
	return n%2 == 1
}

// insertPath handles the case where the path does not exist yet: walk as far as
// the document goes, then build the rest.
func insertPath(d *Doc, data []byte, path string, raw []byte) ([]byte, error) {
	// Find the longest prefix of the path that does exist.
	comps := splitPath(path)
	at := d.Root()
	n := 0
	for n < len(comps) {
		next := at.step(comps[n])
		if !next.Exists() {
			break
		}
		at, n = next, n+1
	}
	if n == len(comps) {
		// Cannot happen -- the caller checked -- but a wrong answer here would
		// be a corrupted document, so it is worth the branch.
		return nil, errSyntax("internal: path exists after all")
	}
	// Everything past the first missing component is structure to create.
	value := wrapRemaining(comps[n+1:], raw)
	if value == nil {
		return nil, errSyntax("cannot create an array at that index; use 0 or -1")
	}
	switch at.kind {
	case Object:
		key := appendQuoted(nil, unescapePath(comps[n]))
		member := make([]byte, 0, len(key)+1+len(value))
		member = append(member, key...)
		member = append(member, ':')
		member = append(member, value...)
		return insertIntoContainer(data, at, member), nil
	case Array:
		// Append, and only append. sjson pads with nulls for an index past the
		// end, so setting item 900 of an empty array writes 899 nulls nobody
		// asked for; and appending on any out-of-range index is worse, because
		// then setting index 1 puts the value at index 0 and reading the path
		// back does not find it. Fuzzing found that immediately.
		//
		// So: -1 appends, which is sjson's own idiom for it, and so does an
		// index exactly one past the end. Anything else is a path that cannot
		// mean what it says.
		if comps[n] == "-1" {
			return insertIntoContainer(data, at, value), nil
		}
		i, ok := atoiPath(comps[n])
		if !ok {
			return nil, errSyntax("cannot set a named field on an array")
		}
		if i != at.Len() {
			return nil, errSyntax("array index out of range; use -1 to append")
		}
		return insertIntoContainer(data, at, value), nil
	default:
		return nil, errSyntax("cannot set a field on a " + at.kind.String())
	}
}

// insertIntoContainer splices member in just before the container's closing
// bracket, with a comma if the container already held something.
func insertIntoContainer(data []byte, c Value, member []byte) []byte {
	close := c.end - 1 // the ] or }
	// Empty if there is nothing but whitespace between the brackets.
	empty := true
	for i := c.start + 1; i < close; i++ {
		if !isLineSpace(data[i]) {
			empty = false
			break
		}
	}
	ins := member
	if !empty {
		ins = make([]byte, 0, len(member)+1)
		ins = append(ins, ',')
		ins = append(ins, member...)
	}
	return splice(data, close, close, ins)
}

// wrapRemaining builds the nested containers for path components that do not
// exist yet, innermost first.
func wrapRemaining(comps []string, raw []byte) []byte {
	out := append([]byte(nil), raw...)
	for i := len(comps) - 1; i >= 0; i-- {
		if n, isIndex := atoiPath(comps[i]); isIndex || comps[i] == "-1" {
			// A numeric component with nothing there yet makes a one-element
			// array. Only index 0 and -1 can mean that; anything else names a
			// position the array being created does not have.
			if isIndex && n != 0 {
				return nil
			}
			next := make([]byte, 0, len(out)+2)
			next = append(next, '[')
			next = append(next, out...)
			next = append(next, ']')
			out = next
			continue
		}
		key := appendQuoted(nil, unescapePath(comps[i]))
		next := make([]byte, 0, len(key)+len(out)+3)
		next = append(next, '{')
		next = append(next, key...)
		next = append(next, ':')
		next = append(next, out...)
		next = append(next, '}')
		out = next
	}
	return out
}

// splice returns data with [start,end) replaced by ins.
func splice(data []byte, start, end int, ins []byte) []byte {
	out := make([]byte, 0, len(data)-(end-start)+len(ins))
	out = append(out, data[:start]...)
	out = append(out, ins...)
	return append(out, data[end:]...)
}

// splitPath breaks a path into its components, honouring escapes.
func splitPath(p string) []string {
	var out []string
	for len(p) > 0 {
		var c string
		c, p = cutPath(p)
		out = append(out, c)
	}
	return out
}

// cutLast splits a path into everything but its last component, and that
// component.
func cutLast(p string) (parent, last string) {
	comps := splitPath(p)
	if len(comps) == 0 {
		return "", ""
	}
	if len(comps) == 1 {
		return "", comps[0]
	}
	return strings.Join(comps[:len(comps)-1], "."), comps[len(comps)-1]
}

// isLineSpace reports whether c is one of the four bytes JSON allows between
// tokens. Splicing has to step over them to find the punctuation it is aiming
// at, and an indented document has plenty.
func isLineSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}
