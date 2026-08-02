package simdjson

// Scan indexes data without validating it.
//
// [Parse] walks the whole document and checks every value against JSON's
// grammar, which is what makes it safe for input you did not produce — and it
// is most of the cost. If the goal is three fields out of a payload your own
// service just serialised, validating the other nine thousand is work nobody
// asked for.
//
// Scan skips it. The structural index is still built, so navigation works
// exactly as it does after Parse; what is gone is the recursive descent that
// proves the parts you never look at are well-formed.
//
// # What that costs
//
// Malformed input gives wrong answers rather than errors. A missing colon, a
// trailing comma, a number like 10., an invalid escape — all are accepted, and
// the values around them may come back wrong or absent instead of failing. The
// index itself is still consistent, so nothing reads out of bounds and nothing
// panics; the result is simply not to be trusted.
//
// Two things are still checked, because the index cannot be built without
// them: every string is terminated, and quotes balance. A document that fails
// either is rejected here too.
//
// Use Parse for anything from outside. Use Scan when you produced the bytes.
func Scan(data []byte) (*Doc, error) {
	ix, err := buildIndex(data, nil, false)
	if err != nil {
		return nil, err
	}
	return scanRoot(data, ix)
}

// Scan indexes data without validating it, reusing p's buffers.
//
// See [Scan] for what is given up, and [Parser.Parse] for the lifetime of the
// returned Doc.
func (p *Parser) Scan(data []byte) (*Doc, error) {
	ix, err := buildIndex(data, p.ix, false)
	if err != nil {
		return nil, err
	}
	p.ix = ix
	return scanRoot(data, ix)
}

// scanRoot identifies the root value without descending into it.
func scanRoot(data []byte, ix *index) (*Doc, error) {
	d := &Doc{data: data, ix: ix, inStr: ix.inStr, noWS: ix.noWS}
	i := d.skip(0)
	if i >= len(data) {
		return nil, errSyntax("empty input")
	}
	switch c := data[i]; {
	case c == '{' || c == '[':
		// The extent comes from matching brackets over the index, which is a
		// walk over the structural positions rather than over the contents.
		end, err := d.matchBracket(i)
		if err != nil {
			return nil, err
		}
		k := Object
		if c == '[' {
			k = Array
		}
		d.root = Value{d: d, kind: k, start: i, end: end}
	default:
		// A scalar at the root is cheap to identify properly.
		v, _, err := d.value(i)
		if err != nil {
			return nil, err
		}
		d.root = v
	}
	d.navigating = true
	return d, nil
}
