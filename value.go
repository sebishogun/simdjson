package simdjson

import (
	"strconv"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/sebishogun/simd"
)

// Key returns the value of a field in an object.
//
// The scan walks the object's structural entries rather than its bytes, so
// passing over a large nested value costs one bracket match instead of a parse.
// A missing key gives an Invalid Value; see [Value.Exists].
func (v Value) Key(name string) Value {
	if v.kind != Object {
		return Value{}
	}
	d := v.d
	i := d.skip(v.start + 1)
	for i < v.end-1 {
		if d.data[i] != '"' {
			return Value{}
		}
		kend, ok := d.stringEnd(i)
		if !ok {
			var err error
			if kend, err = d.stringEndSlow(i); err != nil {
				return Value{}
			}
		}
		// Compare against the raw bytes when the key has no escape, which is
		// almost always, and only unescape when it does.
		key := d.data[i+1 : kend-1]
		match := false
		if simd.IndexByte(key, '\\') < 0 {
			match = string(key) == name
		} else {
			s, ok := unquote(d.data[i:kend])
			match = ok && s == name
		}

		i = d.skip(kend)
		if i >= v.end || d.data[i] != ':' {
			return Value{}
		}
		i = d.skip(i + 1)

		val, next, err := d.value(i)
		if err != nil {
			return Value{}
		}
		if match {
			return val
		}
		i = d.skip(next)
		if i < v.end-1 && d.data[i] == ',' {
			i = d.skip(i + 1)
		}
	}
	return Value{}
}

// Index returns the nth element of an array.
func (v Value) Index(n int) Value {
	if v.kind != Array || n < 0 {
		return Value{}
	}
	d := v.d
	i := d.skip(v.start + 1)
	for k := 0; i < v.end-1; k++ {
		val, next, err := d.value(i)
		if err != nil {
			return Value{}
		}
		if k == n {
			return val
		}
		i = d.skip(next)
		if i < v.end-1 && d.data[i] == ',' {
			i = d.skip(i + 1)
		}
	}
	return Value{}
}

// Len returns the number of elements in an array or fields in an object.
func (v Value) Len() int {
	switch v.kind {
	case Array:
		n := 0
		v.ForEach(func(Value) bool { n++; return true })
		return n
	case Object:
		n := 0
		v.ForEachKey(func(string, Value) bool { n++; return true })
		return n
	}
	return 0
}

// ForEach calls fn for each element of an array until it returns false.
func (v Value) ForEach(fn func(Value) bool) {
	if v.kind != Array {
		return
	}
	d := v.d
	i := d.skip(v.start + 1)
	for i < v.end-1 {
		val, next, err := d.value(i)
		if err != nil || !fn(val) {
			return
		}
		i = d.skip(next)
		if i < v.end-1 && d.data[i] == ',' {
			i = d.skip(i + 1)
		}
	}
}

// ForEachKey calls fn for each field of an object until it returns false.
func (v Value) ForEachKey(fn func(string, Value) bool) {
	if v.kind != Object {
		return
	}
	d := v.d
	i := d.skip(v.start + 1)
	for i < v.end-1 {
		if d.data[i] != '"' {
			return
		}
		kend, ok := d.stringEnd(i)
		if !ok {
			var err error
			if kend, err = d.stringEndSlow(i); err != nil {
				return
			}
		}
		key, _ := unquote(d.data[i:kend])
		i = d.skip(kend)
		if i >= v.end || d.data[i] != ':' {
			return
		}
		i = d.skip(i + 1)
		val, next, err := d.value(i)
		if err != nil || !fn(key, val) {
			return
		}
		i = d.skip(next)
		if i < v.end-1 && d.data[i] == ',' {
			i = d.skip(i + 1)
		}
	}
}

// String returns a string value's contents, or "" for anything else.
//
// A string with no escape is returned without copying the bytes out of the
// document; one with an escape is decoded into a new string.
func (v Value) String() string {
	if v.kind != String {
		return ""
	}
	s, _ := unquote(v.d.data[v.start:v.end])
	return s
}

// Int returns a number value as an int64.
func (v Value) Int() int64 {
	if v.kind != Number {
		return 0
	}
	n, err := strconv.ParseInt(string(v.Raw()), 10, 64)
	if err != nil {
		f, ferr := strconv.ParseFloat(string(v.Raw()), 64)
		if ferr != nil {
			return 0
		}
		return int64(f)
	}
	return n
}

// Float returns a number value as a float64.
func (v Value) Float() float64 {
	if v.kind != Number {
		return 0
	}
	f, _ := strconv.ParseFloat(string(v.Raw()), 64)
	return f
}

// Bool returns a boolean value.
func (v Value) Bool() bool { return v.kind == Bool && v.d.data[v.start] == 't' }

// IsNull reports whether the value is JSON null.
func (v Value) IsNull() bool { return v.kind == Null }

// unquote decodes a quoted JSON string, including its surrounding quotes.
//
// The fast path is the common one: a string with no backslash is the bytes
// between the quotes, and converting those to a string is the only copy. Only
// an escape sends it through the slow path.
func unquote(b []byte) (string, bool) {
	if len(b) < 2 || b[0] != '"' || b[len(b)-1] != '"' {
		return "", false
	}
	in := b[1 : len(b)-1]
	if simd.IndexByte(in, '\\') < 0 {
		return sanitize(string(in)), true
	}
	out := make([]byte, 0, len(in))
	for i := 0; i < len(in); {
		c := in[i]
		if c != '\\' {
			out = append(out, c)
			i++
			continue
		}
		i++
		if i >= len(in) {
			return "", false
		}
		switch in[i] {
		case '"', '\\', '/':
			out = append(out, in[i])
			i++
		case 'b':
			out = append(out, '\b')
			i++
		case 'f':
			out = append(out, '\f')
			i++
		case 'n':
			out = append(out, '\n')
			i++
		case 'r':
			out = append(out, '\r')
			i++
		case 't':
			out = append(out, '\t')
			i++
		case 'u':
			if i+5 > len(in) {
				return "", false
			}
			r, err := strconv.ParseUint(string(in[i+1:i+5]), 16, 32)
			if err != nil {
				return "", false
			}
			i += 5
			cp := rune(r)
			// A surrogate pair is two escapes and has to be joined, or the
			// result is two replacement characters instead of one rune.
			if utf16.IsSurrogate(cp) && i+6 <= len(in) && in[i] == '\\' && in[i+1] == 'u' {
				r2, err2 := strconv.ParseUint(string(in[i+2:i+6]), 16, 32)
				if err2 == nil {
					if dec := utf16.DecodeRune(cp, rune(r2)); dec != utf8.RuneError {
						cp = dec
						i += 6
					}
				}
			}
			out = utf8.AppendRune(out, cp)
		default:
			return "", false
		}
	}
	return sanitize(string(out)), true
}

// sanitize replaces invalid UTF-8 with the replacement character, which is what
// encoding/json does and what a caller comparing against it will expect.
//
// Found by fuzzing within seconds: a string holding a lone 0x82 came back as
// that byte rather than as U+FFFD, so the two packages disagreed on a document
// both accepted. JSON is defined over Unicode text and a Go string is not
// required to hold any, so the coercion has to be explicit.
//
// The check is the common path and costs one pass; the rebuild only happens for
// input that was already malformed.
func sanitize(s string) string {
	// simd.ValidUTF8 rather than utf8.ValidString: this runs on every string a
	// document contains, and it was 7.3% of decoding twitter.json into a
	// struct. Same answer, a vector pass instead of a byte loop.
	if simd.ValidUTF8(s) {
		return s
	}
	out := make([]byte, 0, len(s)+8)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			out = utf8.AppendRune(out, utf8.RuneError)
			i++
			continue
		}
		out = append(out, s[i:i+size]...)
		i += size
	}
	return string(out)
}
