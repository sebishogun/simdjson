// Package stdlibtest runs encoding/json's own black-box decode tests against
// this library's drop-in surface. The test file is Go's decode_test.go
// (BSD-3, The Go Authors), package-renamed; the shim below binds the names
// it exercises to this package's implementations. Divergences fail here
// before a user finds them.
package stdlibtest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"runtime"
	"strings"
	"testing"

	simdjson "github.com/sebishogun/simdjson"
)

func Unmarshal(data []byte, v any) error { return simdjson.Unmarshal(data, v) }
func Marshal(v any) ([]byte, error)      { return simdjson.Marshal(v) }
func Valid(data []byte) bool             { return simdjson.Valid(data) }

func NewDecoder(r io.Reader) *simdjson.Decoder { return simdjson.NewDecoder(r) }

type (
	Number                = json.Number
	RawMessage            = json.RawMessage
	UnmarshalTypeError    = json.UnmarshalTypeError
	InvalidUnmarshalError = json.InvalidUnmarshalError
	Unmarshaler           = json.Unmarshaler
	Marshaler             = json.Marshaler
	Delim                 = json.Delim
)

func Compact(dst *bytes.Buffer, src []byte) error { return simdjson.Compact(dst, src) }

// CaseName and Name are encoding/json's internal test-case annotations
// (internal/jsontest), reproduced so the vendored test file compiles
// unchanged.
type CaseName struct {
	Name  string
	Where CasePos
}

func Name(s string) (c CaseName) {
	c.Name = s
	runtime.Callers(2, c.Where.pc[:])
	return c
}

type CasePos struct{ pc [1]uintptr }

func (pos CasePos) String() string {
	frames := runtime.CallersFrames(pos.pc[:])
	frame, _ := frames.Next()
	return fmt.Sprintf("%s:%d", path.Base(frame.File), frame.Line)
}

// SyntaxError mirrors the shape the vendored tables construct. The library's
// own syntax errors differ in wording by documented design ("simdjson: "
// prefix, its own phrasing), so equalError below compares by class and
// offset rather than text -- the same bar the conformance suites hold.
type SyntaxError struct {
	msg    string
	Offset int64
}

func (e *SyntaxError) Error() string { return e.msg }

// stripWhitespace is encoding/json's test helper (v2_encode_test.go).
func stripWhitespace(s string) string {
	var out []byte
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ' ', '\t', '\n', '\r':
		default:
			out = append(out, s[i])
		}
	}
	return string(out)
}

// diff and trim are encoding/json's scanner_test helpers.
func diff(t *testing.T, a, b []byte) {
	t.Helper()
	for i := 0; ; i++ {
		if i >= len(a) || i >= len(b) || a[i] != b[i] {
			j := i - 10
			if j < 0 {
				j = 0
			}
			t.Errorf("diverge at %d: «%s» vs «%s»", i, trim(a[j:]), trim(b[j:]))
			return
		}
	}
}

func trim(b []byte) []byte {
	return b[:min(len(b), 20)]
}

// MarshalIndent binds the indented encoder; the vendored encode tests use it
// directly.
func MarshalIndent(v any, prefix, indent string) ([]byte, error) {
	return simdjson.MarshalIndent(v, prefix, indent)
}

// isZeroer is encoding/json's omitzero hook, vendored from encode.go.
type isZeroer interface {
	IsZero() bool
}

// indentNewlines is vendored from encoding/json's scanner_test.go, which the
// encode tests share helpers with.
func indentNewlines(s string) string {
	return strings.Join(strings.Split(s, "\n"), "\n\t")
}

// The error types the encode tables name, aliased straight to encoding/json's
// like the decoder-side errors are.
type (
	UnsupportedValueError = json.UnsupportedValueError
	MarshalerError        = json.MarshalerError
)

// startDetectingCyclesAfter is vendored from encode.go: the depth at which
// stdlib starts checking for cycles, which its cycle tests multiply against.
const startDetectingCyclesAfter = 1000

// HTMLEscape binds encoding/json's; this library does not reimplement it and
// the semantics under test are the escaper's.
func HTMLEscape(dst *bytes.Buffer, src []byte) {
	json.HTMLEscape(dst, src)
}
