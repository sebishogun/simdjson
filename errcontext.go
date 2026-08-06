package simdjson

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// numberRType is json.Number's reflect.Type: string-kinded, but it accepts
// the number token and marshals as raw digits, so both planners and both
// decode paths must tell it apart from string before switching on Kind.
var numberRType = reflect.TypeOf(json.Number(""))

// wrapFieldErr adds encoding/json's struct context to a type error unwinding
// out of a struct field. The field path grows outward ("X", then "T.X");
// Struct is set only by the innermost level, because stdlib stamps it once,
// at save time, with the struct whose field walk was current. An unmarshaler
// boundary clears Struct on the way out (see clearStructCtx), which is what
// lets the enclosing struct restamp it -- stdlib runs the unmarshaler in a
// fresh decodeState whose context dies with it.
func wrapFieldErr(err error, name string, t reflect.Type) error {
	te, ok := err.(*json.UnmarshalTypeError)
	if !ok {
		return err
	}
	switch {
	case te.Struct == restampMark:
		// Fresh out of an unmarshaler: this struct owns the error now.
		te.Struct = t.Name()
		if te.Field == "" {
			te.Field = name
		} else {
			te.Field = name + "." + te.Field
		}
	case te.Field == "":
		// Innermost level: the only one that stamps Struct, and "" is a
		// real stamp -- an unnamed struct type reports an empty name.
		te.Field = name
		te.Struct = t.Name()
	default:
		te.Field = name + "." + te.Field
	}
	return te
}

// restampMark is an impossible struct name marking a type error that just
// crossed an unmarshaler boundary; the next enclosing struct claims it.
// takeSaved scrubs it from errors that never meet a struct.
const restampMark = "\x00restamp"

// clearStructCtx strips the struct stamp from a type error returned by a
// caller-supplied unmarshaler, so the struct enclosing the unmarshaler
// restamps it as its own -- encoding/json's addErrorContext overwrites
// Struct wholesale at the boundary while keeping Field and Offset.
func clearStructCtx(err error) error {
	if te, ok := err.(*json.UnmarshalTypeError); ok {
		te.Struct = restampMark
	}
	return err
}

// isValidNumber reports whether s is a valid JSON number literal, per
// encoding/json's grammar check for marshalling json.Number.
func isValidNumber(s string) bool {
	if s == "" {
		return false
	}
	if s[0] == '-' {
		s = s[1:]
		if s == "" {
			return false
		}
	}
	switch {
	case s[0] == '0':
		s = s[1:]
	case '1' <= s[0] && s[0] <= '9':
		s = s[1:]
		for len(s) > 0 && '0' <= s[0] && s[0] <= '9' {
			s = s[1:]
		}
	default:
		return false
	}
	if len(s) >= 2 && s[0] == '.' && '0' <= s[1] && s[1] <= '9' {
		s = s[2:]
		for len(s) > 0 && '0' <= s[0] && s[0] <= '9' {
			s = s[1:]
		}
	}
	if len(s) >= 2 && (s[0] == 'e' || s[0] == 'E') {
		s = s[1:]
		if s[0] == '+' || s[0] == '-' {
			s = s[1:]
			if s == "" {
				return false
			}
		}
		for len(s) > 0 && '0' <= s[0] && s[0] <= '9' {
			s = s[1:]
		}
	}
	return s == ""
}

// writeNumberLiteral emits a json.Number the way encoding/json does: raw
// digits after a grammar check, an empty Number as 0.
func (e *encodeState) writeNumberLiteral(n string) error {
	if n == "" {
		n = "0"
	}
	if !isValidNumber(n) {
		return fmt.Errorf("json: invalid number literal, trying to marshal %q", n)
	}
	e.buf = append(e.buf, n...)
	return nil
}

// saveable reports whether encoding/json would save this error and keep
// decoding -- type mismatches, unknown fields, ,string misuse -- rather
// than stop where it stood. Syntax and I/O errors stay fatal.
func saveable(err error) bool {
	if _, ok := err.(*json.UnmarshalTypeError); ok {
		return true
	}
	s := err.Error()
	return strings.HasPrefix(s, "json: unknown field ") ||
		strings.HasPrefix(s, "json: invalid use of ,string struct tag")
}

// saveErr records the first saveable error; later ones are dropped, which is
// stdlib's rule -- the caller sees the error closest to the document's start
// of walk order.
func (d *Doc) saveErr(err error) {
	if d.savedErr == nil {
		d.savedErr = err
	}
}

// takeSaved resolves a finished top-level decode: a fatal error wins, else
// whatever was saved along the way. Clears the slot either way.
func (d *Doc) takeSaved(err error) error {
	saved := d.savedErr
	d.savedErr = nil
	if err == nil {
		err = saved
	}
	if te, ok := err.(*json.UnmarshalTypeError); ok && te.Struct == restampMark {
		te.Struct = ""
	}
	return err
}
