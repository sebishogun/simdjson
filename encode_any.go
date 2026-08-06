package simdjson

// Encoding the shapes that come out of a JSON decoder, without reflect.
//
// `map[string]any`, `[]any`, `string`, `float64`, `bool` and nil are the entire
// output of encoding/json's Unmarshal into an interface. Anything that decoded
// a document and is now encoding it back — a proxy, a filter, a test — is
// holding exactly these and nothing else.
//
// Going through reflect for them is expensive in a way that does not show up as
// one obvious line. reflect.Value.MapIndex, reflect.MapIter.Key and the
// reflect.New inside ptrOf each box their result, so every key and every value
// is an allocation: 44,960 of them to marshal a decoded twitter.json, against
// goccy's 1,870. An allocation profile put reflect.unsafe_New at 96.9% of the
// count.
//
// A type switch on the concrete types costs nothing and allocates nothing.
// Anything not in the list falls back to the compiled encoders, so this is a
// fast path and not a second implementation: the reflect path still has to be
// right, and the differential fuzzer still compares both against
// encoding/json.

import (
	"encoding/json"
	"math"
	"reflect"
	"strconv"
	"unsafe"
)

// encodeAny writes a value of one of the types a JSON decode produces, and
// reports whether it recognised it.
func (e *encodeState) encodeAny(v any) (bool, error) {
	switch x := v.(type) {
	case nil:
		e.buf = append(e.buf, "null"...)
	case string:
		e.buf = appendQuotedOpts(e.buf, x, e.opts)
	case bool:
		if x {
			e.buf = append(e.buf, "true"...)
		} else {
			e.buf = append(e.buf, "false"...)
		}
	case float64:
		if math.IsInf(x, 0) || math.IsNaN(x) {
			return true, &json.UnsupportedValueError{Str: strconv.FormatFloat(x, 'g', -1, 64)}
		}
		e.buf = appendFloat(e.buf, x, 64)
	case int:
		e.buf = appendInt(e.buf, int64(x))
	case int64:
		e.buf = appendInt(e.buf, x)
	case json.Number:
		// A json.Number is its own digits, already valid, and writing it any
		// other way would change them.
		if x == "" {
			e.buf = append(e.buf, '0')
		} else {
			e.buf = append(e.buf, x...)
		}
	case []any:
		if x == nil {
			e.buf = append(e.buf, "null"...)
			return true, nil
		}
		// Cycle tracking with explicit decrements, not defer: this is the
		// canada hot loop, one call per coordinate ring, and a deferred
		// closure here is exactly the per-slice overhead entry 23 was about.
		if e.ptrLevel++; e.ptrLevel > startDetectingCyclesAfter {
			sp := struct {
				p unsafe.Pointer
				n int
			}{unsafe.Pointer(unsafe.SliceData(x)), len(x)}
			if err := e.enterCycle(sp, anySliceType); err != nil {
				e.ptrLevel--
				return true, err
			}
			err := e.encodeAnySliceBody(x)
			delete(e.ptrSeen, sp)
			e.ptrLevel--
			return true, err
		}
		err := e.encodeAnySliceBody(x)
		e.ptrLevel--
		return true, err
	case map[string]any:
		return true, e.encodeStringMap(x)
	default:
		return false, nil
	}
	return true, nil
}

// encodeAnySliceBody is the []any body of encodeAny, split out so the cycle
// bookkeeping above can wrap it without a defer on the hot path.
//
// A big one shards across workers wherever it appears -- a decoded document
// is a two-key map whose bytes are all in one nested slice. The decision
// happens INSIDE the loop, after four elements are already encoded, from
// their size times the count: on a slice that declines it costs one
// predicted compare per element and one estimate ever. The first version
// wrapped the whole thing in reflect machinery that ran per slice even to
// decline, and canada -- thousands of thousand-element coordinate rings,
// every one entering and declining -- paid 23% on the floats gate row.
func (e *encodeState) encodeAnySliceBody(x []any) error {
	mark := len(e.buf)
	e.buf = append(e.buf, '[')
	for i, el := range x {
		if i > 0 {
			e.buf = append(e.buf, ',')
		}
		if i == 4 && !e.noParallel && e.hint >= parallelMarshalMin && len(x) >= 24 {
			if done, err := e.shardAnyRest(x, i, mark); done {
				return err
			}
		}
		if err := e.encodeAnyOrFall(el); err != nil {
			return err
		}
	}
	e.buf = append(e.buf, ']')
	return nil
}

// encodeAnyOrFall is encodeAny with the compiled encoders behind it.
func (e *encodeState) encodeAnyOrFall(v any) error {
	if ok, err := e.encodeAny(v); ok {
		return err
	}
	rv := reflect.ValueOf(v)
	return e.encoderForCached(rv.Type())(e, ptrOf(rv), rv)
}

// encodeStringMap writes a map[string]any, sorting the keys when asked.
//
// The keys are collected into the encoder's shared buffer with stack
// discipline, so a nested map takes the space after this one's and gives it
// back. No reflect, so no boxing: the keys are strings and the values are
// interfaces already.
func (e *encodeState) encodeStringMap(m map[string]any) error {
	if e.ptrLevel++; e.ptrLevel > startDetectingCyclesAfter {
		mp := reflect.ValueOf(m).UnsafePointer()
		if err := e.enterCycle(mp, stringMapType); err != nil {
			e.ptrLevel--
			return err
		}
		err := e.encodeStringMapBody(m)
		delete(e.ptrSeen, mp)
		e.ptrLevel--
		return err
	}
	err := e.encodeStringMapBody(m)
	e.ptrLevel--
	return err
}

// encodeStringMapBody is encodeStringMap behind the cycle bookkeeping,
// which stays defer-free on the hot path.
func (e *encodeState) encodeStringMapBody(m map[string]any) error {
	// Key and value together, not the keys alone. Collecting only the keys
	// means looking each value up again after the sort, and that second hash
	// was 10% of marshalling a decoded document -- mapaccess1_faststr, once per
	// key, for something the range already had in hand.
	mark := len(e.anybuf)
	for k, v := range m {
		e.anybuf = append(e.anybuf, kvAny{k: k, v: v})
	}
	pairs := e.anybuf[mark:]
	// Given back on the way out so a nested map gets the space after this one.
	defer func() { e.anybuf = e.anybuf[:mark] }()

	if e.opts.SortMapKeys {
		sortPairs(pairs)
	}
	e.buf = append(e.buf, '{')
	for i := range pairs {
		if i > 0 {
			e.buf = append(e.buf, ',')
		}
		e.buf = appendQuotedOpts(e.buf, pairs[i].k, e.opts)
		e.buf = append(e.buf, ':')
		if err := e.encodeAnyOrFall(pairs[i].v); err != nil {
			return err
		}
	}
	e.buf = append(e.buf, '}')
	return nil
}

// kvAny is one entry of a map[string]any, held while the keys are sorted. The
// sort is shared with the reflect path; see sortmap.go.
type kvAny = pair[any]
