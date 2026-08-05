package simdjson

// Token: reading a document as a sequence of syntax rather than a sequence of
// values.
//
// This exists for one thing that nothing else here can do. A stream of values
// decodes in constant memory — ten gigabytes of line-delimited JSON goes
// through a Decoder in nineteen megabytes — but a single enormous *array* does
// not, because Decode buffers until it has one whole value and one ten-gigabyte
// array is one ten-gigabyte value. Reading the opening bracket as a token and
// then decoding the elements one at a time is how that document gets handled at
// all.
//
// It is the slower way to read anything that fits in memory, and deliberately
// so: a token is a value boxed into an interface, which is an allocation the
// index-and-decode path does not make. Reach for it when the document is too
// big to hold, not when it is merely large.

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
)

// A Token is a delimiter, a string, a number, a bool, or nil — the same set
// encoding/json.Token holds, and the same types, so code written against one
// works against the other.
type Token = json.Token

// A Delim is one of the four bracket characters.
type Delim = json.Delim

// Token returns the next syntactic element: a [Delim] for a bracket, or the
// value of a string, number, bool or null.
//
// Object keys come back as strings, in the position they appear. Commas and
// colons are consumed and never returned, which is what makes the token stream
// the same shape as encoding/json's.
//
// It returns [io.EOF] when the input is exhausted. Token and [Decoder.Decode]
// interleave: after Token has returned the opening bracket of an array, Decode
// reads the next element of it, which is the whole point.
func (d *Decoder) Token() (Token, error) {
	// Any index built for Decode describes bytes this is about to step past,
	// and a caller walking tokens is not the pure Value loop the batch
	// staging is armed for.
	d.doc, d.data = nil, nil
	d.valStreak, d.stElems = 0, d.stElems[:0]

	c, err := d.peek()
	if err != nil {
		return nil, err
	}

	// A colon belongs to the key before it and is consumed here rather than
	// with the key, so that a key followed by something other than a colon
	// comes back as a key and then an error — which is where encoding/json
	// reports it, and callers match on the position.
	mustValue := false
	if d.afterKey {
		if c != ':' {
			return nil, errAt("expected ':' after object key", d.off)
		}
		d.off++
		if c, err = d.peek(); err != nil {
			// Whatever the reader said, unchanged. Input that simply stops after
			// a separator is EOF to encoding/json, not an unexpected one, and
			// callers distinguish the two.
			return nil, err
		}
		d.afterKey = false
		// A colon promises a value. A closing bracket here is not one.
		mustValue = true
	} else if d.inItem && c != '}' && c != ']' {
		// A separator is expected between items and never between an opener and
		// its first item, which is what inItem tracks.
		if c != ',' {
			return nil, errAt("expected ',' after element", d.off)
		}
		d.off++
		if c, err = d.peek(); err != nil {
			// Whatever the reader said, unchanged. Input that simply stops after
			// a separator is EOF to encoding/json, not an unexpected one, and
			// callers distinguish the two.
			return nil, err
		}
		if c == '}' || c == ']' {
			return nil, errAt("unexpected ',' before closing bracket", d.off)
		}
	}

	// Inside an object waiting for a key, the only things that can appear are a
	// key and the bracket that closes it. Not a value, not another object.
	if d.wantKey && !mustValue && c != '"' && c != '}' {
		return nil, errAt("expected a string key", d.off)
	}

	switch c {
	case '{', '[':
		d.off++
		d.tstack = append(d.tstack, byte(c))
		d.inItem = false
		d.wantKey = c == '{'
		return Delim(c), nil

	case '}', ']':
		if mustValue {
			return nil, errAt("expected a value after ':'", d.off)
		}
		if len(d.tstack) == 0 {
			return nil, errAt("unexpected closing bracket", d.off)
		}
		open := d.tstack[len(d.tstack)-1]
		if (open == '{') != (c == '}') {
			return nil, errAt("mismatched closing bracket", d.off)
		}
		d.tstack = d.tstack[:len(d.tstack)-1]
		d.off++
		d.afterItem()
		return Delim(c), nil
	}

	// A value, or an object key, which is a string in a place where the object
	// wants one.
	if !canStartValue[c] {
		return nil, errAt("unexpected character", d.off)
	}
	key := d.wantKey && !mustValue && len(d.tstack) > 0 && d.tstack[len(d.tstack)-1] == '{'
	start, end, err := d.scalarSpan()
	if err != nil {
		return nil, err
	}
	tok, err := tokenOf(d.buf[start:end], d.useNumber)
	if err != nil {
		return nil, err
	}
	if key {
		s, ok := tok.(string)
		if !ok {
			return nil, errAt("expected a string key", start)
		}
		d.afterKey = true
		return s, nil
	}
	d.afterItem()
	return tok, nil
}

// afterItem records that the enclosing container now holds something, so the
// next item needs a separator and an object is back to wanting a key.
func (d *Decoder) afterItem() {
	if len(d.tstack) == 0 {
		d.inItem = false
		d.wantKey = false
		return
	}
	d.inItem = true
	d.wantKey = d.tstack[len(d.tstack)-1] == '{'
}

// scalarSpan finds the extent of the string, number or literal at the cursor,
// reading more input until it has all of it, and consumes it.
func (d *Decoder) scalarSpan() (int, int, error) {
	for {
		start := d.off
		end, ok := d.valueEnd(d.buf, start)
		if ok {
			d.off = end
			return start, end, nil
		}
		if d.err != nil {
			// At end of stream a bare number or literal that reaches the end of
			// the buffer is complete; a string or container is truncated.
			if d.err == io.EOF && end > start && d.buf[start] != '"' {
				d.off = end
				return start, end, nil
			}
			if d.err == io.EOF {
				return 0, 0, io.ErrUnexpectedEOF
			}
			return 0, 0, d.err
		}
		if err := d.fill(); err != nil && err != io.EOF {
			return 0, 0, err
		}
	}
}

// tokenOf turns one scalar's bytes into the value encoding/json would produce.
func tokenOf(raw []byte, useNumber bool) (Token, error) {
	switch raw[0] {
	case '"':
		var s string
		if err := Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		return s, nil
	case 't':
		if string(raw) != "true" {
			return nil, errSyntax("invalid literal")
		}
		return true, nil
	case 'f':
		if string(raw) != "false" {
			return nil, errSyntax("invalid literal")
		}
		return false, nil
	case 'n':
		if string(raw) != "null" {
			return nil, errSyntax("invalid literal")
		}
		return nil, nil
	}
	if !validJSONNumber(raw) {
		// strconv is more permissive than JSON: it reads "0." as zero, and JSON
		// has no such number. The grammar has to be checked before the parser
		// is asked, or an incomplete number at the end of a stream comes back
		// as a value.
		return nil, fmt.Errorf("json: invalid number literal %q", raw)
	}
	if useNumber {
		return json.Number(raw), nil
	}
	f, err := strconv.ParseFloat(string(raw), 64)
	if err != nil {
		return nil, fmt.Errorf("json: invalid number literal %q", raw)
	}
	return f, nil
}

// validJSONNumber reports whether b is exactly a JSON number: an optional
// minus, an integer part with no leading zero, an optional fraction with at
// least one digit, and an optional exponent with at least one digit.
func validJSONNumber(b []byte) bool {
	i := 0
	if i < len(b) && b[i] == '-' {
		i++
	}
	switch {
	case i < len(b) && b[i] == '0':
		i++
	case i < len(b) && b[i] >= '1' && b[i] <= '9':
		for i++; i < len(b) && isDigit(b[i]); i++ {
		}
	default:
		return false
	}
	if i < len(b) && b[i] == '.' {
		i++
		if i >= len(b) || !isDigit(b[i]) {
			return false
		}
		for ; i < len(b) && isDigit(b[i]); i++ {
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
		for ; i < len(b) && isDigit(b[i]); i++ {
		}
	}
	return i == len(b)
}
