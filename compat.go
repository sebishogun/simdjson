package simdjson

// The names encoding/json exports that a caller has to be able to write.
//
// A drop-in replacement is not drop-in if `json.RawMessage` has to become
// `[]byte` and `json.Number` has to be imported from somewhere else. Code
// moving over from encoding/json says `json.RawMessage` in struct fields and
// `json.Number` in switches, and it should keep compiling after changing one
// import line.
//
// These are aliases, not defined types, which matters: a defined type would not
// satisfy the interfaces encoding/json checks for, and a value could not pass
// between the two packages. `Token` and `Delim` are already aliased in token.go
// for the same reason.

import "encoding/json"

// RawMessage is a raw encoded JSON value. It implements [json.Marshaler] and
// [json.Unmarshaler] and can be used to delay JSON decoding or precompute a
// JSON encoding.
//
// It is an alias for [json.RawMessage], so the two are the same type.
type RawMessage = json.RawMessage

// There is deliberately no Number alias. This package already exports Number as
// a [Kind] constant, naming the kind of a JSON number, and that name was
// published first. Use [json.Number] directly — [Decoder.UseNumber] and
// [Value.Decode] both produce exactly that type, so it works, it just has to be
// spelled with its own import.

// Marshaler is the interface implemented by types that can marshal themselves
// into valid JSON.
//
// It is an alias for [json.Marshaler].
type Marshaler = json.Marshaler

// Unmarshaler is the interface implemented by types that can unmarshal a JSON
// description of themselves.
//
// It is an alias for [json.Unmarshaler].
type Unmarshaler = json.Unmarshaler

// The error types encoding/json returns, aliased so that a caller's type
// switch keeps working.
//
// [SyntaxError] is not among them: it needs constructing here, so it is a
// defined type in errors.go with the same shape and the same Offset.
type (
	// UnmarshalTypeError describes a JSON value that was not appropriate for a
	// value of a specific Go type. Its Offset field is the byte offset in the
	// input after reading the value.
	UnmarshalTypeError = json.UnmarshalTypeError

	// InvalidUnmarshalError describes an invalid argument passed to
	// [Unmarshal] — the argument must be a non-nil pointer.
	InvalidUnmarshalError = json.InvalidUnmarshalError

	// UnsupportedTypeError is returned by [Marshal] for a Go type that cannot
	// be represented as JSON.
	UnsupportedTypeError = json.UnsupportedTypeError

	// UnsupportedValueError is returned by [Marshal] for a value that cannot be
	// represented as JSON — an infinity or a NaN.
	UnsupportedValueError = json.UnsupportedValueError

	// MarshalerError is returned when a type's own MarshalJSON or MarshalText
	// method returns an error.
	MarshalerError = json.MarshalerError
)
