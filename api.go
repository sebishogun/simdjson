package simdjson

// The parts of the surface that are convenience rather than machinery: reaching
// several paths at once, navigating from a Value rather than from the document,
// locating a value without decoding it, and ranging over a container.
//
// Each of these exists in at least one of the libraries this one is measured
// against, and each is cheaper here than there for the same reason: the index is
// already built, so a second lookup walks structure rather than bytes.

import (
	"iter"
	"time"
)

// Get returns the value at a path relative to v.
//
// The same walk as [Doc.Get] but starting here, which is what makes a Value
// worth holding on to: find the interesting subtree once, then ask it questions.
// gjson's Result.Get is the same idea.
//
// A path that does not exist gives an Invalid Value; see [Value.Exists].
func (v Value) Get(path ...string) Value {
	for _, key := range path {
		v = v.Key(key)
		if v.kind == Invalid {
			return v
		}
	}
	return v
}

// GetMany returns the values at each of paths, in order.
//
// One index, many lookups. gjson's GetMany is documented as one pass over the
// document for N paths, which is what it has to do because it has no index; here
// the document is scanned once whatever N is, and each path after that is a walk
// over structural positions. So the second path is nearly free and the
// hundredth is too.
//
// A path that does not exist gives an Invalid Value in that position rather than
// an error, matching gjson. A document that does not parse gives all-Invalid.
//
// Each path is a sequence of object keys. For anything more than that — array
// indices, wildcards, queries — walk with [Value.Index] and [Value.ForEach].
func GetMany(data []byte, paths ...[]string) []Value {
	out := make([]Value, len(paths))
	d, err := Parse(data)
	if err != nil {
		return out
	}
	for i, p := range paths {
		out[i] = d.Get(p...)
	}
	return out
}

// Skip returns the extent of the first JSON value in data: the offset of its
// first byte and the offset one past its last.
//
// Validate and locate in one call, which sonic exposes as decoder.Skip. It is
// the operation behind "give me this value's bytes without decoding it" — a
// router picking one field out of a body, a proxy forwarding a subtree, a test
// comparing raw JSON.
//
// ok is false if data holds no complete valid value. Trailing bytes after the
// first value are not an error and not included: `{} garbage` gives 0, 2, true.
func Skip(data []byte) (start, end int, ok bool) {
	d, err := Scan(data)
	if err != nil {
		return 0, 0, false
	}
	r := d.Root()
	if r.kind == Invalid {
		return 0, 0, false
	}
	return r.start, r.end, true
}

// MustParse is [Parse] for input already known to be valid. It panics if it is
// not.
//
// For tests, for constants compiled into the program, and for the top of a
// function that has already validated its input. fastjson exposes the same
// thing and for the same reason: an error return that can never fire is noise
// at the call site.
func MustParse(data []byte) *Doc {
	d, err := Parse(data)
	if err != nil {
		panic(err)
	}
	return d
}

// All ranges over the elements of an array, or over nothing for any other kind.
//
// The range form of [Value.ForEach], for `for i, e := range v.All()`.
func (v Value) All() iter.Seq2[int, Value] {
	return func(yield func(int, Value) bool) {
		if v.kind != Array {
			return
		}
		i := 0
		v.ForEach(func(e Value) bool {
			ok := yield(i, e)
			i++
			return ok
		})
	}
}

// Members ranges over the fields of an object, or over nothing for any other
// kind.
//
// The range form of [Value.ForEachKey]. It is not called All because an object
// and an array are different shapes and returning the same type for both would
// mean an index nobody wants or a key that does not exist.
func (v Value) Members() iter.Seq2[string, Value] {
	return func(yield func(string, Value) bool) {
		if v.kind != Object {
			return
		}
		v.ForEachKey(func(k string, e Value) bool { return yield(k, e) })
	}
}

// Keys ranges over the field names of an object.
func (v Value) Keys() iter.Seq[string] {
	return func(yield func(string) bool) {
		if v.kind != Object {
			return
		}
		v.ForEachKey(func(k string, _ Value) bool { return yield(k) })
	}
}

// Values ranges over the elements of an array or the field values of an object.
func (v Value) Values() iter.Seq[Value] {
	return func(yield func(Value) bool) {
		switch v.kind {
		case Array:
			v.ForEach(func(e Value) bool { return yield(e) })
		case Object:
			v.ForEachKey(func(_ string, e Value) bool { return yield(e) })
		}
	}
}

// Time parses a string value as an RFC 3339 timestamp.
//
// The zero Time if the value is not a string or does not parse. gjson's
// Result.Time is the same, and it is here because timestamps in JSON are
// strings often enough that everybody writes this function.
func (v Value) Time() time.Time {
	if v.kind != String {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, v.String())
	if err != nil {
		return time.Time{}
	}
	return t
}
