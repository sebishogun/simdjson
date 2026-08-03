package simdjson

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
}

// Std matches encoding/json byte for byte. It is what the package-level
// Marshal uses.
var Std = Options{EscapeHTML: true, ValidateStrings: true}

// Fast gives up HTML escaping and UTF-8 validation.
//
// Use it when the output is not going into a page and the strings are known to
// be valid UTF-8 — decoded from JSON, read from a UTF-8 database column, built
// from Go string literals. The output is identical to Std's for any input that
// meets those conditions, and differs for any that does not.
var Fast = Options{}

// Marshal returns the JSON encoding of v under these options.
func (o Options) Marshal(v any) ([]byte, error) {
	e := encoderPool.Get().(*encodeState)
	e.buf, e.opts = e.buf[:0], o
	err := e.marshal(v)
	if err != nil {
		encoderPool.Put(e)
		return nil, err
	}
	out := make([]byte, len(e.buf))
	copy(out, e.buf)
	encoderPool.Put(e)
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
