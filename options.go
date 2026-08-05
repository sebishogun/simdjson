package simdjson

import "io"

// Options selects what an encoder checks and escapes.
//
// The defaults match [encoding/json] exactly, because a drop-in replacement
// that quietly produces different bytes is worse than a slow one. Everything
// here is a way to buy speed by giving something up, and each says what.
type Options struct {
	// EscapeHTML writes `<`, `>` and `&` as <, > and &, and
	// rewrites U+2028 and U+2029, so the output can be embedded in an HTML
	// document without becoming script. encoding/json does this by default and
	// so does this package.
	//
	// Turning it off is worth a few percent and is safe only if the output
	// never reaches a page. Note that some other libraries have it off by
	// default, which is worth knowing when comparing their numbers.
	EscapeHTML bool

	// ValidateStrings replaces bytes that are not valid UTF-8 with U+FFFD,
	// which is what encoding/json does. Off, they are written through as-is,
	// producing output that is not valid JSON if the input was not valid UTF-8.
	//
	// This is the expensive one — on a document of non-ASCII text, validation
	// is about a third of the encode — and the right choice when the strings
	// come from somewhere that already guarantees UTF-8.
	ValidateStrings bool

	// SortMapKeys writes a map's keys in order. encoding/json always does, so
	// this is on in [Std] and every byte-for-byte comparison depends on it.
	//
	// Off, keys come out in whatever order the map iterates, which Go
	// deliberately randomises — so the same map encodes differently on
	// successive calls. That is fine for a payload nobody diffs and fatal for
	// a cache key, an ETag or a signature. encoding/json/v2 makes it opt-in;
	// this keeps v1's default and lets you turn it off, which is the safer way
	// round.
	SortMapKeys bool

	// OmitZeroStructFields drops every struct field holding its type's zero
	// value, as though each carried `omitzero`.
	//
	// New in encoding/json/v2 as an option, and useful for the case the tag
	// cannot serve: a type from another package, or a struct being encoded for
	// a wire format that treats absent and zero the same.
	//
	// It follows `omitzero` and not `omitempty`: an empty slice and an empty
	// map are their zero value only when nil, and a type with its own IsZero
	// method is asked. A field with an explicit tag keeps whatever the tag
	// said.
	OmitZeroStructFields bool
}

// Std matches encoding/json byte for byte. It is what the package-level
// Marshal uses.
var Std = Options{EscapeHTML: true, ValidateStrings: true, SortMapKeys: true}

// Fast gives up HTML escaping and UTF-8 validation.
//
// Use it when the output is not going into a page and the strings are known to
// be valid UTF-8 — decoded from JSON, read from a UTF-8 database column, built
// from Go string literals. The output is identical to Std's for any input that
// meets those conditions, and differs for any that does not.
//
// Map keys are still sorted. Not sorting them is a bigger change than a few
// percent: it makes the same value encode differently on successive calls,
// which is a different promise rather than a faster one. Set SortMapKeys to
// false deliberately if that is wanted.
var Fast = Options{SortMapKeys: true}

// Marshal returns the JSON encoding of v under these options.
func (o Options) Marshal(v any) ([]byte, error) {
	e := encoderPool.Get().(*encodeState)
	// Straight into the buffer that gets handed back, the same way the
	// package-level Marshal does it. This encoded into the pooled buffer and
	// then copied the whole output out of it -- the copy that one was written
	// to avoid, and its comment measures at a quarter of what Marshal cost.
	// So Std.Marshal was slower than Marshal for the identical operation.
	saved := e.buf
	e.buf, e.opts = make([]byte, 0, e.hint), o
	err := e.marshal(v)
	out := e.buf
	if len(out) > e.hint {
		e.hint = len(out)
	}
	e.buf = saved
	encoderPool.Put(e)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// MarshalTo appends the JSON encoding of v to dst under these options.
func (o Options) MarshalTo(dst []byte, v any) ([]byte, error) {
	e := encoderPool.Get().(*encodeState)
	e.buf, e.opts = e.buf[:0], o
	err := e.marshal(v)
	if err != nil {
		encoderPool.Put(e)
		return dst, err
	}
	dst = append(dst, e.buf...)
	encoderPool.Put(e)
	return dst, nil
}

// MarshalWrite writes the JSON encoding of v to w.
//
// The shape encoding/json/v2 added as MarshalWrite: encode straight into the
// destination rather than building a []byte and handing it over. For a large
// value going to a socket or a file this is the difference between one buffer
// and two.
//
// It is not [Encoder.Encode]: that appends a newline, because it is for writing
// a stream of values. This writes exactly the value.
func (o Options) MarshalWrite(w io.Writer, v any) error {
	e := encoderPool.Get().(*encodeState)
	e.buf, e.opts = e.buf[:0], o
	err := e.marshal(v)
	if err != nil {
		encoderPool.Put(e)
		return err
	}
	_, err = w.Write(e.buf)
	encoderPool.Put(e)
	return err
}

// MarshalWrite writes the JSON encoding of v to w, matching encoding/json.
func MarshalWrite(w io.Writer, v any) error { return Std.MarshalWrite(w, v) }
