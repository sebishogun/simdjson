package simdjson

// Line-delimited JSON, as an API rather than as a convention.
//
// NDJSON is what JSON at scale actually looks like — logs, exports, event
// streams, anything that has to be appendable and resumable — and almost every
// library leaves you to write `for { dec.Decode(&v) }` around it. Two do not:
// minio/simdjson-go has ParseND and ParseNDStream, and gjson has ForEachLine.
//
// It is also the shape that parallelises, because a newline outside a string is
// a place the document can be cut. Nothing here runs on more than one goroutine
// yet; the signatures are chosen so that it can without changing.
//
// gjson's ForEachLine has no error return and stops silently at the first chunk
// that does not parse, which turns a malformed line in the middle of a ten
// gigabyte file into a short read nobody notices. These report it.

import (
	"bytes"
	"io"
)

// ForEachLine calls fn for each JSON value in data.
//
// Whitespace between values, including the newlines, is skipped, so this reads
// NDJSON and equally a file of values with no separators at all. Input that is
// not valid JSON stops the walk and is returned as an error carrying the offset.
//
// fn returning false stops the walk without an error, the same way
// [Value.ForEach] does.
//
// The Value passed to fn is only valid for the duration of the call: it points
// into a batch that is reused for the values after it, which is what keeps this
// to a fixed amount of memory however long the input is.
func ForEachLine(data []byte, fn func(Value) bool) error {
	return ForEachLineReader(bytes.NewReader(data), fn)
}

// ForEachLineReader is [ForEachLine] over a stream.
//
// Memory is one batch plus the index over it, so a file larger than memory is
// fine: ten gigabytes of NDJSON goes through this in under twenty megabytes.
func ForEachLineReader(r io.Reader, fn func(Value) bool) error {
	d := NewDecoder(r)
	for {
		v, err := d.Value()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if !fn(v) {
			return nil
		}
	}
}
