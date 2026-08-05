package simdjson

// Registration for encoders written out per type rather than compiled from
// reflection at run time.
//
// The compiled struct encoder walks a table of fields, and the disassembly of
// that loop shows why generated code can beat it: the loop keeps six to eight
// values live across the call that writes each field -- the index, the buffer,
// the base pointer, the option flags -- and the register allocator spills them,
// while straight-line code with the offsets and key bytes as constants keeps
// two. Measured on twitter.json decoded into a struct, straight-line code using
// this package's own primitives is 43,354 ns against the compiled loop's
// 52,409.
//
// This is the seam that lets that code be used. It is deliberately small: a
// function per type, and one adapter. Whether the function is written by hand
// or emitted by a generator is not this file's concern.
//
// It is NOT a JIT. Nothing is assembled or compiled at run time; a registered
// encoder is ordinary Go, compiled by the ordinary toolchain and readable in
// the repository that owns it.

import (
	"reflect"
	"unsafe"
)

// AppendFunc writes v as JSON to the end of dst and returns the extended
// buffer. p points at a value of the registered type.
//
// The contract is exact and unforgiving, because nothing checks it at run time:
// the bytes written must be the bytes [Marshal] would have written for the same
// value under the same [Options], including the escaping and the field order.
// A registered encoder that disagrees produces wrong output with no error, so
// whatever writes one owes a differential test against [Marshal].
type AppendFunc func(dst []byte, p unsafe.Pointer, opts Options) []byte

// RegisterEncoder installs fn as the encoder for T, for [Marshal] and
// everything built on it -- including T appearing as a struct field, a slice
// element or a map value.
//
// It must be called before the first encode of a value containing T, which in
// practice means from an init function in the package that owns the generated
// code. Registering after a type has been encoded once has no effect: the
// compiled encoder for it is already cached, and so is every encoder that has
// already inlined a reference to it.
//
// Registering the same type twice replaces the earlier registration.
func RegisterEncoder[T any](fn AppendFunc) {
	if fn == nil {
		panic("simdjson: RegisterEncoder called with a nil function")
	}
	var zero T
	t := reflect.TypeOf(&zero).Elem()
	encoderCache.Store(t, encodeFn(func(e *encodeState, p unsafe.Pointer, rv reflect.Value) error {
		// The buffer goes out and comes back rather than being appended to
		// through e, so a registered encoder needs no knowledge of the encoder
		// state and cannot get it wrong.
		e.buf = fn(e.buf, p, e.opts)
		return nil
	}))
}

// AppendString writes s as a quoted JSON string under opts, which is what a
// generated encoder needs for a string field. It is the same code [Marshal]
// uses, exported so that generated code cannot drift from it.
func AppendString(dst []byte, s string, opts Options) []byte {
	return appendQuotedOpts(dst, s, opts)
}

// AppendInt, AppendUint, AppendFloat and AppendBool are the rest of what a
// generated encoder needs for scalar fields, and are the same code [Marshal]
// uses for them.
func AppendInt(dst []byte, v int64) []byte { return appendInt(dst, v) }

// AppendUint appends v in decimal.
func AppendUint(dst []byte, v uint64) []byte { return appendUint(dst, v) }

// AppendFloat appends v in the shortest form that round-trips, as JSON
// requires. bits is 32 or 64.
func AppendFloat(dst []byte, v float64, bits int) []byte {
	return appendFloat(dst, v, bits)
}

// AppendBool appends "true" or "false".
func AppendBool(dst []byte, v bool) []byte {
	if v {
		return append(dst, "true"...)
	}
	return append(dst, "false"...)
}
