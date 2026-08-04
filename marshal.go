package simdjson

import (
	"encoding"
	"encoding/base64"
	"encoding/json"
	"math"
	"reflect"
	"strconv"
	"sync"
	"unsafe"
)

// Marshal returns the JSON encoding of v.
//
// It is [encoding/json.Marshal]'s contract — the same escaping, the same tag
// handling, the same sorted map keys, the same treatment of nil — produced by an
// encoder compiled once per type rather than by walking reflect for every
// field. Where the two could differ they are held together by a differential
// fuzz test rather than by inspection.
func Marshal(v any) ([]byte, error) {
	e := encoderPool.Get().(*encodeState)
	// Encoded straight into the buffer that gets handed back, rather than into
	// the pooled one and then copied out of it. The copy was the whole output,
	// once per call: 600 KB on a document of tweets, and a quarter of what
	// Marshal cost.
	//
	// The pooled buffer cannot be handed out — it goes back to the pool and the
	// next caller would overwrite the last one's result — so what the pool
	// carries instead is the size the last encode needed. A fresh buffer of
	// that size grows no more than the first one did.
	saved := e.buf
	e.buf, e.opts = make([]byte, 0, e.hint), Std
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

// MarshalTo appends the JSON encoding of v to dst and returns the extended
// slice.
//
// The shape a server wants: one buffer, reused across responses, so encoding a
// stream of payloads does not allocate a new one for each.
func MarshalTo(dst []byte, v any) ([]byte, error) {
	e := encoderPool.Get().(*encodeState)
	e.buf, e.opts = e.buf[:0], Std
	err := e.marshal(v)
	if err != nil {
		encoderPool.Put(e)
		return dst, err
	}
	dst = append(dst, e.buf...)
	encoderPool.Put(e)
	return dst, nil
}

type encodeState struct {
	buf     []byte
	opts    Options
	scratch [64]byte

	// addrTyp and addrVal give the top-level value somewhere to live that has
	// an address. A value taken out of an interface has none, and the compiled
	// encoders work from pointers, so one copy is unavoidable — but allocating
	// somewhere to put it is not. Held on the pooled encoder, it is one
	// allocation per type rather than one per call, which on a stream is one
	// per type rather than one per record.
	addrTyp reflect.Type
	addrVal reflect.Value
	addrFn  encodeFn

	// hint is how many bytes the last encode through this state produced, so
	// the next Marshal can size its buffer without growing into it.
	hint int

	// lastTyp and lastFn remember the encoder used for the previous value
	// reached through an interface.
	//
	// A map[string]any or a []any looks up an encoder per element, and that
	// lookup hashes an interface -- HashTrieMap.Load plus nilinterhash were 14%
	// of marshalling a decoded document. Real documents are homogeneous: every
	// element of an array is the same shape, and the values of a map mostly
	// are, so the previous answer is usually this answer.
	//
	// One entry, not a map. A second map lookup to avoid a map lookup would be
	// no better, and a one-entry cache costs a comparison.
	lastTyp reflect.Type
	lastFn  encodeFn

	// kvbuf is where every map encoder in this encode collects its keys.
	//
	// One growing buffer with stack discipline -- a nested map takes a slice
	// from the end and gives it back on the way out -- instead of two fresh
	// slices per map, which is what MapKeys plus a pairs slice cost. A decoded
	// twitter.json is thousands of nested maps, and this package allocated
	// 44,960 times to marshal it against goccy's 1,870.
	kvbuf []kv

	// anybuf is where encodeStringMap collects its entries, with the same
	// stack discipline as kvbuf: a nested map takes the space after this one's
	// and gives it back. Plain strings and interfaces, not reflect.Values,
	// which is the whole point -- see encode_any.go.
	anybuf []kvAny
}

// encoderForCached is encoderFor with the previous answer checked first.
//
// Only for values reached through an interface, where the type is not known
// until it is in hand. The compiled paths resolve their encoders once at
// compile time and must not pay this comparison.
func (e *encodeState) encoderForCached(t reflect.Type) encodeFn {
	if e.lastTyp == t {
		return e.lastFn
	}
	f := encoderFor(t)
	e.lastTyp, e.lastFn = t, f
	return f
}

// addressable returns a pointer to rv's data, reusing this encoder's scratch
// when rv has no address of its own. marshal is the only caller and is never
// re-entered, so the scratch cannot be in use by an outer frame.
func (e *encodeState) addressable(rv reflect.Value) unsafe.Pointer {
	if rv.CanAddr() {
		return unsafe.Pointer(rv.UnsafeAddr())
	}
	if t := rv.Type(); e.addrTyp != t {
		e.addrVal, e.addrTyp, e.addrFn = reflect.New(t).Elem(), t, encoderFor(t)
	}
	e.addrVal.Set(rv)
	return unsafe.Pointer(e.addrVal.UnsafeAddr())
}

var encoderPool = sync.Pool{New: func() any { return &encodeState{buf: make([]byte, 0, 512)} }}

func (e *encodeState) marshal(v any) error {
	if v == nil {
		e.buf = append(e.buf, "null"...)
		return nil
	}
	rv := reflect.ValueOf(v)
	// The value out of an interface is not addressable, and the compiled
	// encoders work from a pointer. One copy, at the top level only.
	//
	// The encoder for the type is looked up alongside the scratch and cached
	// with it. encoderFor is a sync.Map keyed by reflect.Type, so it hashes an
	// interface and walks a trie -- fine once, and 15% of a stream when it is
	// once per value.
	// The shapes a JSON decode produces, written without reflect. See
	// encode_any.go: going through the compiled encoders for these means an
	// allocation per key and per value, because every reflect operation on a
	// map boxes its result.
	if ok, err := e.encodeAny(v); ok {
		return err
	}
	p := e.addressable(rv)
	if e.addrFn != nil && e.addrTyp == rv.Type() {
		return e.addrFn(e, p, rv)
	}
	return encoderFor(rv.Type())(e, p, rv)
}

// encodeFn writes one value.
//
// It takes both a pointer and a reflect.Value because the compiled path uses
// the pointer — a field's address is its parent's plus a fixed offset — while
// the cases that still need reflection (interfaces, maps, anything with its own
// MarshalJSON) need the Value. Whichever a given encoder uses, the other is
// cheap to carry.
type encodeFn func(e *encodeState, p unsafe.Pointer, rv reflect.Value) error

var encoderCache sync.Map // reflect.Type -> encodeFn

func encoderFor(t reflect.Type) encodeFn {
	if f, ok := encoderCache.Load(t); ok {
		return f.(encodeFn)
	}
	var self encodeFn
	var once sync.Once
	stub := encodeFn(func(e *encodeState, p unsafe.Pointer, rv reflect.Value) error {
		once.Do(func() {
			if f, ok := encoderCache.Load(t); ok {
				if g, ok := f.(encodeFn); ok {
					self = g
				}
			}
		})
		return self(e, p, rv)
	})
	encoderCache.Store(t, stub)
	f := compileEncoder(t)
	encoderCache.Store(t, f)
	return f
}

var (
	marshalerType     = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	textMarshalerType = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
)

func compileEncoder(t reflect.Type) encodeFn {
	if t.Implements(marshalerType) || reflect.PointerTo(t).Implements(marshalerType) ||
		t.Implements(textMarshalerType) || reflect.PointerTo(t).Implements(textMarshalerType) {
		return encodeViaStdlib(t)
	}
	switch t.Kind() {
	case reflect.String:
		return func(e *encodeState, p unsafe.Pointer, rv reflect.Value) error {
			e.writeString(*(*string)(p))
			return nil
		}
	case reflect.Bool:
		return func(e *encodeState, p unsafe.Pointer, rv reflect.Value) error {
			if *(*bool)(p) {
				e.buf = append(e.buf, "true"...)
			} else {
				e.buf = append(e.buf, "false"...)
			}
			return nil
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return encodeIntFn(t)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return encodeUintFn(t)
	case reflect.Float32, reflect.Float64:
		return encodeFloatFn(t)
	case reflect.Struct:
		return compileStructEncoder(t)
	case reflect.Pointer:
		return compilePointerEncoder(t)
	case reflect.Slice:
		return compileSliceEncoder(t)
	case reflect.Array:
		return compileArrayEncoder(t)
	case reflect.Map:
		return compileMapEncoder(t)
	case reflect.Interface:
		return func(e *encodeState, p unsafe.Pointer, rv reflect.Value) error {
			// Rebuilt from the pointer when the caller had no Value to give.
			// The compiled struct path passes only a pointer, so without this
			// every interface-typed field encoded as null.
			if !rv.IsValid() {
				rv = reflect.NewAt(t, p).Elem()
			}
			if rv.IsNil() {
				e.buf = append(e.buf, "null"...)
				return nil
			}
			el := rv.Elem()
			// The scalars are written straight from the reflect.Value.
			//
			// The compiled encoders work from a pointer, and a value pulled out
			// of an interface has no address, so reaching one means ptrOf --
			// which is a reflect.New and an allocation per value. On a decoded
			// document that is an allocation for every string, number and bool
			// in it, and malloc was 17% of marshalling one.
			//
			// reflect.Value can read a scalar without an address. Only the
			// composites, which need a pointer to walk, still pay.
			switch el.Kind() {
			case reflect.String:
				e.buf = appendQuotedOpts(e.buf, el.String(), e.opts)
				return nil
			case reflect.Bool:
				if el.Bool() {
					e.buf = append(e.buf, "true"...)
				} else {
					e.buf = append(e.buf, "false"...)
				}
				return nil
			case reflect.Float64, reflect.Float32:
				f := el.Float()
				if math.IsInf(f, 0) || math.IsNaN(f) {
					return &json.UnsupportedValueError{Str: strconv.FormatFloat(f, 'g', -1, 64)}
				}
				bits := 64
				if el.Kind() == reflect.Float32 {
					bits = 32
				}
				e.buf = appendFloat(e.buf, f, bits)
				return nil
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
				e.buf = appendInt(e.buf, el.Int())
				return nil
			case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
				e.buf = appendUint(e.buf, el.Uint())
				return nil
			}
			return e.encoderForCached(el.Type())(e, ptrOf(el), el)
		}
	}
	return func(e *encodeState, p unsafe.Pointer, rv reflect.Value) error {
		return &json.UnsupportedTypeError{Type: t}
	}
}

// ptrOf returns an addressable pointer to rv's data, copying to the heap when
// rv is not addressable — which is the case for a value pulled out of an
// interface.
func ptrOf(rv reflect.Value) unsafe.Pointer {
	if rv.CanAddr() {
		return unsafe.Pointer(rv.UnsafeAddr())
	}
	c := reflect.New(rv.Type()).Elem()
	c.Set(rv)
	return unsafe.Pointer(c.UnsafeAddr())
}

// encodeViaStdlib defers to encoding/json for the types that decide their own
// representation.
//
// A type with MarshalJSON can return anything, including something that needs
// compacting or HTML-escaping, and reproducing that faithfully is work with no
// speed to gain: these types are rare and their own method dominates whatever
// wraps it.
func encodeViaStdlib(t reflect.Type) encodeFn {
	return func(e *encodeState, p unsafe.Pointer, rv reflect.Value) error {
		if !rv.IsValid() {
			rv = reflect.NewAt(t, p).Elem()
		}
		b, err := json.Marshal(rv.Interface())
		if err != nil {
			return err
		}
		e.buf = append(e.buf, b...)
		return nil
	}
}

func encodeIntFn(t reflect.Type) encodeFn {
	switch t.Kind() {
	case reflect.Int:
		return func(e *encodeState, p unsafe.Pointer, rv reflect.Value) error {
			e.buf = appendInt(e.buf, int64(*(*int)(p)))
			return nil
		}
	case reflect.Int8:
		return func(e *encodeState, p unsafe.Pointer, rv reflect.Value) error {
			e.buf = appendInt(e.buf, int64(*(*int8)(p)))
			return nil
		}
	case reflect.Int16:
		return func(e *encodeState, p unsafe.Pointer, rv reflect.Value) error {
			e.buf = appendInt(e.buf, int64(*(*int16)(p)))
			return nil
		}
	case reflect.Int32:
		return func(e *encodeState, p unsafe.Pointer, rv reflect.Value) error {
			e.buf = appendInt(e.buf, int64(*(*int32)(p)))
			return nil
		}
	}
	return func(e *encodeState, p unsafe.Pointer, rv reflect.Value) error {
		e.buf = appendInt(e.buf, *(*int64)(p))
		return nil
	}
}

func encodeUintFn(t reflect.Type) encodeFn {
	switch t.Kind() {
	case reflect.Uint:
		return func(e *encodeState, p unsafe.Pointer, rv reflect.Value) error {
			e.buf = appendUint(e.buf, uint64(*(*uint)(p)))
			return nil
		}
	case reflect.Uint8:
		return func(e *encodeState, p unsafe.Pointer, rv reflect.Value) error {
			e.buf = appendUint(e.buf, uint64(*(*uint8)(p)))
			return nil
		}
	case reflect.Uint16:
		return func(e *encodeState, p unsafe.Pointer, rv reflect.Value) error {
			e.buf = appendUint(e.buf, uint64(*(*uint16)(p)))
			return nil
		}
	case reflect.Uint32:
		return func(e *encodeState, p unsafe.Pointer, rv reflect.Value) error {
			e.buf = appendUint(e.buf, uint64(*(*uint32)(p)))
			return nil
		}
	}
	return func(e *encodeState, p unsafe.Pointer, rv reflect.Value) error {
		e.buf = appendUint(e.buf, *(*uint64)(p))
		return nil
	}
}

func encodeFloatFn(t reflect.Type) encodeFn {
	bits := t.Bits()
	return func(e *encodeState, p unsafe.Pointer, rv reflect.Value) error {
		var f float64
		if bits == 32 {
			f = float64(*(*float32)(p))
		} else {
			f = *(*float64)(p)
		}
		if math.IsInf(f, 0) || math.IsNaN(f) {
			return &json.UnsupportedValueError{Str: strconv.FormatFloat(f, 'g', -1, bits)}
		}
		e.buf = appendFloat(e.buf, f, bits)
		return nil
	}
}

// appendFloat writes a float the way encoding/json does.
//
// Not simply AppendFloat with 'g': the standard library uses 'f' unless the
// exponent is extreme, and then rewrites e+07 as e+7. Byte-identical output is
// the whole point of a drop-in encoder, so the shape is copied exactly.
func appendFloat(b []byte, f float64, bits int) []byte {
	// A float that is a whole number prints as that whole number, and printing
	// an integer is far cheaper than the shortest-representation search a float
	// needs. `f` is the format this always uses below 1e21, and `f` with a
	// precision of -1 gives "3" for 3.0, which is what appendInt gives too.
	//
	// Worth having because whole numbers are most of the floats in real JSON:
	// counts, ids, prices in whole units, anything that was an integer before
	// it was put in a float64 field.
	//
	// The bound is what makes the exact integer also the *shortest* decimal that
	// round-trips, which is what a precision of -1 asks for. Below 2^53 every
	// integer is exactly representable as a float64 and no two share a value,
	// so no shorter decimal can round-trip to the same float — 1e15 leaves room
	// under that and is well inside the 1e21 where the format changes to 'e'.
	//
	// A float32 is checked for round-tripping as a float32, where only integers
	// below 2^24 are distinct. Above that the shortest decimal is not the exact
	// value: float32(123456792) prints as 123456790, and using the exact
	// integer would disagree with the standard library.
	//
	// Negative zero has to be excluded by hand: it is a whole number, and
	// int64(-0.0) is 0, which would print "0" where the standard library
	// prints "-0".
	abs := math.Abs(f)
	lim := 1e15
	if bits == 32 {
		lim = 1 << 24
	}
	if abs < lim && f == math.Trunc(f) && !(f == 0 && math.Signbit(f)) {
		return appendInt(b, int64(f))
	}
	// Not `if i := int64(f); float64(i) == f`, which looks cheaper and is 2-3%
	// slower: math.Trunc is one ROUNDSD, and a conversion out to an integer and
	// back is two conversions with worse latency plus a range test to make the
	// first one defined. Measured, twice, on both Marshal and MarshalTo.

	// A float32 goes to strconv, which finds the shortest decimal that
	// round-trips as a float32. Schubfach here is the float64 algorithm and its
	// answer is the shortest for a float64, which is a different and longer
	// string for the same value.
	if bits == 32 {
		return appendFloatStrconv(b, f, 32)
	}
	if f == 0 {
		if math.Signbit(f) {
			return append(b, '-', '0')
		}
		return append(b, '0')
	}
	// encoding/json's rule, not strconv's 'g': decimal while the magnitude is
	// in [1e-6, 1e21), scientific outside it.
	expFormat := abs < 1e-6 || abs >= 1e21
	return appendShortest(b, math.Signbit(f), schubfach(math.Float64bits(abs)), expFormat)
}

// appendFloatStrconv is the path this used to take everywhere, kept for
// float32 and as the thing the tests compare against.
func appendFloatStrconv(b []byte, f float64, bits int) []byte {
	abs := math.Abs(f)
	fmtc := byte('f')
	if abs != 0 {
		if bits == 64 && (abs < 1e-6 || abs >= 1e21) ||
			bits == 32 && (float32(abs) < 1e-6 || float32(abs) >= 1e21) {
			fmtc = 'e'
		}
	}
	b = strconv.AppendFloat(b, f, fmtc, -1, bits)
	if fmtc == 'e' {
		n := len(b)
		if n >= 4 && b[n-4] == 'e' && b[n-3] == '-' && b[n-2] == '0' {
			b[n-2] = b[n-1]
			b = b[:n-1]
		}
	}
	return b
}

func compilePointerEncoder(t reflect.Type) encodeFn {
	elem := t.Elem()
	inner := encoderFor(elem)
	return func(e *encodeState, p unsafe.Pointer, rv reflect.Value) error {
		ep := *(*unsafe.Pointer)(p)
		if ep == nil {
			e.buf = append(e.buf, "null"...)
			return nil
		}
		var erv reflect.Value
		if rv.IsValid() {
			erv = rv.Elem()
		}
		return inner(e, ep, erv)
	}
}

func compileSliceEncoder(t reflect.Type) encodeFn {
	elem := t.Elem()
	if elem.Kind() == reflect.Uint8 && !elem.Implements(marshalerType) {
		return func(e *encodeState, p unsafe.Pointer, rv reflect.Value) error {
			b := *(*[]byte)(p)
			if b == nil {
				e.buf = append(e.buf, "null"...)
				return nil
			}
			e.buf = append(e.buf, '"')
			n := base64.StdEncoding.EncodedLen(len(b))
			for cap(e.buf)-len(e.buf) < n {
				e.buf = append(e.buf[:cap(e.buf)], 0)[:len(e.buf)]
			}
			out := e.buf[len(e.buf) : len(e.buf)+n]
			base64.StdEncoding.Encode(out, b)
			e.buf = e.buf[:len(e.buf)+n]
			e.buf = append(e.buf, '"')
			return nil
		}
	}
	// A slice of a leaf kind is written element by element without a call, for
	// the same reason a struct field of one is: the call is more than the work.
	// A slice pays it per element, so the shape that gains most is the one that
	// turns up most -- a []string of tags, a []int of ids.
	if leaf := encLeafOf(elem); leaf != leafNone {
		return compileLeafSliceEncoder(leaf, elem.Size())
	}
	inner := encoderFor(elem)
	esize := elem.Size()
	return func(e *encodeState, p unsafe.Pointer, rv reflect.Value) error {
		h := (*sliceHeader)(p)
		if h.data == nil {
			e.buf = append(e.buf, "null"...)
			return nil
		}
		e.buf = append(e.buf, '[')
		for i := 0; i < h.len; i++ {
			if i > 0 {
				e.buf = append(e.buf, ',')
			}
			var erv reflect.Value
			if rv.IsValid() {
				erv = rv.Index(i)
			}
			if err := inner(e, unsafe.Add(h.data, uintptr(i)*esize), erv); err != nil {
				return err
			}
		}
		e.buf = append(e.buf, ']')
		return nil
	}
}

// compileLeafSliceEncoder writes a slice whose elements the loop can write
// itself. The kind is decided once, outside the loop, so the switch costs one
// well-predicted branch per element rather than an indirect call.
func compileLeafSliceEncoder(leaf encLeaf, esize uintptr) encodeFn {
	return func(e *encodeState, p unsafe.Pointer, _ reflect.Value) error {
		h := (*sliceHeader)(p)
		if h.data == nil {
			e.buf = append(e.buf, "null"...)
			return nil
		}
		e.buf = append(e.buf, '[')
		for i := 0; i < h.len; i++ {
			if i > 0 {
				e.buf = append(e.buf, ',')
			}
			ep := unsafe.Add(h.data, uintptr(i)*esize)
			switch leaf {
			case leafString:
				e.writeString(*(*string)(ep))
			case leafInt:
				e.buf = appendInt(e.buf, *(*int64)(ep))
			case leafUint:
				e.buf = appendUint(e.buf, *(*uint64)(ep))
			case leafBool:
				if *(*bool)(ep) {
					e.buf = append(e.buf, "true"...)
				} else {
					e.buf = append(e.buf, "false"...)
				}
			case leafFloat64:
				e.buf = appendFloat(e.buf, *(*float64)(ep), 64)
			}
		}
		e.buf = append(e.buf, ']')
		return nil
	}
}

type sliceHeader struct {
	data unsafe.Pointer
	len  int
	cap  int
}

func compileArrayEncoder(t reflect.Type) encodeFn {
	elem := t.Elem()
	inner := encoderFor(elem)
	esize := elem.Size()
	n := t.Len()
	return func(e *encodeState, p unsafe.Pointer, rv reflect.Value) error {
		e.buf = append(e.buf, '[')
		for i := 0; i < n; i++ {
			if i > 0 {
				e.buf = append(e.buf, ',')
			}
			var erv reflect.Value
			if rv.IsValid() {
				erv = rv.Index(i)
			}
			if err := inner(e, unsafe.Add(p, uintptr(i)*esize), erv); err != nil {
				return err
			}
		}
		e.buf = append(e.buf, ']')
		return nil
	}
}

// compileMapEncoder writes a map with its keys sorted, which is what
// encoding/json does and what makes its output reproducible.
// kv is a map key as it will be written and as it must be looked up.
// kv is one entry of a map being encoded through reflect. The sort is shared
// with the map[string]any path; see sortmap.go for why it is generic.
// kv is a key and the index of its value in the slice built for this map.
//
// An index, not the reflect.Value itself, for two reasons. The values live in
// one slice rather than one box each -- see compileMapEncoder -- so there is
// something to index. And it makes the sort's element eight bytes instead of
// twenty-four, so every swap in the radix partition moves a third as much.
type kv = pair[int]

func compileMapEncoder(t reflect.Type) encodeFn {
	// Three things decided once for the type, which were being decided once per
	// entry. All of them are properties of the map's key or value type, and a
	// map's key and value types do not vary between its entries.
	//
	//   - whether the key needs MarshalText. mapKeyString asked by calling
	//     k.Interface() and asserting, and k.Interface() boxes -- an allocation
	//     per key to learn something the type already answered.
	//   - whether the key is a plain string, so its text is k.String() and not a
	//     conversion.
	//   - which encoder writes the value. encoderForCached was called per entry
	//     on el.Type(), and el.Type() is t.Elem() every time unless the value
	//     type is an interface, where the dynamic type is the point.
	kt, et := t.Key(), t.Elem()
	keyText := kt.Implements(textMarshalerType)
	keyIsString := !keyText && kt.Kind() == reflect.String
	elemIsIface := et.Kind() == reflect.Interface

	return func(e *encodeState, p unsafe.Pointer, rv reflect.Value) error {
		if !rv.IsValid() {
			rv = reflect.NewAt(t, p).Elem()
		}
		if rv.IsNil() {
			e.buf = append(e.buf, "null"...)
			return nil
		}
		// MapRange rather than MapKeys: MapKeys builds a slice of every key
		// before anything is written, and the keys are wanted one at a time.
		// Every value in one slice, not one box each.
		//
		// MapIter.Value allocates a fresh Value per entry, and then ptrOf
		// allocates a SECOND time and copies: a Value from MapIter.Value is not
		// addressable, because reflect's copyVal does not set flagAddr, so
		// ptrOf falls to its reflect.New-and-Set path. Two allocations and a
		// copy for every value in the map.
		//
		// A slice of the element type costs one allocation for all of them, and
		// its elements ARE addressable, so ptrOf takes the UnsafeAddr fast path
		// and copies nothing. rv.Len() gives the count before the walk starts,
		// so it is sized once and never grown.
		n := rv.Len()
		vals := reflect.MakeSlice(reflect.SliceOf(et), n, n)

		mark := len(e.kvbuf)
		it := rv.MapRange()
		// One Value for the key, filled in place. MapIter.Key allocates a fresh
		// one per entry; SetIterKey writes into this one and does not, which is
		// what reflect documents it for. The string is taken out immediately, so
		// nothing needs the Value to survive the next iteration.
		keyV := reflect.New(kt).Elem()
		for it.Next() {
			keyV.SetIterKey(it)
			var s string
			if keyIsString {
				s = keyV.String()
			} else {
				var err error
				if s, err = mapKeyString(keyV); err != nil {
					e.kvbuf = e.kvbuf[:mark]
					return err
				}
			}
			// The value, not the key. Keeping the key meant fetching the value
			// again with MapIndex after the sort -- a second hash of every key,
			// for something the range already had in hand. It is not a trade
			// against an allocation either: MapIndex boxes its result exactly
			// as MapIter.Value does, so this is the same allocation count with
			// one less hash. reflect's own SetIterValue documents the boxing.
			//
			// Retaining it across Next is safe: MapIter.Value goes through
			// copyVal, which copies an indirect value into fresh memory and
			// takes the pointer for a pointer-shaped one.
			i := len(e.kvbuf) - mark
			if i >= n {
				// A map that grew during the walk. Go does not define what
				// iteration returns then, but it must not write past the slice.
				break
			}
			vals.Index(i).SetIterValue(it)
			e.kvbuf = append(e.kvbuf, kv{k: s, v: i})
		}
		pairs := e.kvbuf[mark:]
		// Given back on the way out, whatever happens, so a nested map inside
		// this one gets the space after it.
		defer func() { e.kvbuf = e.kvbuf[:mark] }()
		if e.opts.SortMapKeys {
			// A three-way radix quicksort, not slices.SortFunc: no comparator
			// call and no re-walking the prefix that map keys in one object
			// share. See sortmap.go. It replaced sort.Slice before that, which
			// swapped through reflect and was 18% of marshalling a decoded
			// document on its own.
			sortPairs(pairs)
		}

		// The value encoder, once. Looked up here rather than before the range
		// so a map whose value type contains itself still compiles: this runs
		// after compileMapEncoder has returned and been cached.
		var valFn encodeFn
		if !elemIsIface {
			valFn = e.encoderForCached(et)
		}

		e.buf = append(e.buf, '{')
		for i, pr := range pairs {
			if i > 0 {
				e.buf = append(e.buf, ',')
			}
			e.writeString(pr.k)
			e.buf = append(e.buf, ':')
			el := vals.Index(pr.v)
			fn := valFn
			if fn == nil {
				fn = e.encoderForCached(el.Type())
			}
			if err := fn(e, ptrOf(el), el); err != nil {
				return err
			}
		}
		e.buf = append(e.buf, '}')
		return nil
	}
}

func mapKeyString(k reflect.Value) (string, error) {
	if tm, ok := k.Interface().(encoding.TextMarshaler); ok {
		b, err := tm.MarshalText()
		return string(b), err
	}
	switch {
	case k.Kind() == reflect.String:
		return k.String(), nil
	case isIntKind(k.Kind()):
		return strconv.FormatInt(k.Int(), 10), nil
	case isUintKind(k.Kind()):
		return strconv.FormatUint(k.Uint(), 10), nil
	}
	return "", &json.UnsupportedTypeError{Type: k.Type()}
}

// encField is one struct field, prepared once.
// encLeaf names the field types the struct loop writes without a call. Only
// the ones whose in-memory shape is exactly the one the writer wants: int64 and
// uint64 but not the narrower widths, which would need a conversion and would
// then not be a load.
type encLeaf uint8

const (
	leafNone encLeaf = iota
	leafString
	leafInt
	leafUint
	leafBool
	leafFloat64
)

// encLeafOf reports how the struct loop can write a field of type t, or leafNone
// if it has to call the compiled encoder. A named type with its own MarshalJSON
// is never a leaf however it is shaped, which is why this asks encoderFor's
// question first.
func encLeafOf(t reflect.Type) encLeaf {
	if t.Implements(marshalerType) || reflect.PointerTo(t).Implements(marshalerType) ||
		t.Implements(textMarshalerType) || reflect.PointerTo(t).Implements(textMarshalerType) {
		return leafNone
	}
	switch t.Kind() {
	case reflect.String:
		return leafString
	case reflect.Int64, reflect.Int:
		if t.Size() == 8 {
			return leafInt
		}
	case reflect.Uint64, reflect.Uint, reflect.Uintptr:
		if t.Size() == 8 {
			return leafUint
		}
	case reflect.Bool:
		return leafBool
	case reflect.Float64:
		return leafFloat64
	}
	return leafNone
}

type encField struct {
	// key is the whole `"name":` prefix, escaped and quoted at compile time so
	// that writing a field is one append rather than a quote, an escape pass
	// and a colon.
	key []byte
	// keyComma is key with the separating comma already on the front, because
	// every field but the first needs both and appending them separately is two
	// bounds checks and two length updates to write six bytes.
	keyComma  []byte
	offset    uintptr
	fn        encodeFn
	typ       reflect.Type
	index     []int
	omitEmpty bool
	omitZero  bool
	// isZero is the type's own IsZero method, when it has one. omitzero asks
	// the type first and falls back to comparing against the zero value.
	isZero func(unsafe.Pointer) bool
	// leaf names the kinds the field loop writes itself. The compiled encoder
	// for a string or an int is three instructions behind an indirect call, and
	// the call is the larger half; a struct of scalars pays one per field.
	leaf    encLeaf
	quoted  bool
	ptrPath bool
	// simple marks a field the loop can write with no questions asked: a fixed
	// offset, a leaf kind, and none of omitempty, omitzero or ,string. Almost
	// every field of almost every struct is one, and testing the four flags
	// separately meant four branches per field in a loop that is 24% of an
	// encode.
	simple bool
}

func compileStructEncoder(t reflect.Type) encodeFn {
	var fields []encField
	var walk func(reflect.Type, []int, uintptr, bool, int)
	walk = func(st reflect.Type, index []int, off uintptr, viaPtr bool, depth int) {
		if depth > 10 {
			return
		}
		for i := 0; i < st.NumField(); i++ {
			sf := st.Field(i)
			tag := sf.Tag.Get("json")
			if tag == "-" {
				continue
			}
			name, opts, _ := cutComma(tag)
			ft := sf.Type
			ptrHere := viaPtr || ft.Kind() == reflect.Pointer
			if sf.Anonymous && name == "" {
				base := ft
				if base.Kind() == reflect.Pointer {
					base = base.Elem()
				}
				if base.Kind() == reflect.Struct {
					walk(base, append(append([]int{}, index...), i), off+sf.Offset,
						ptrHere && ft.Kind() == reflect.Pointer, depth+1)
					continue
				}
			}
			if !sf.IsExported() {
				continue
			}
			if name == "" {
				name = sf.Name
			}
			var key []byte
			key = append(key, '{')
			key = appendQuoted(key[:0], name)
			key = append(key, ':')
			fields = append(fields, encField{
				key:       key,
				offset:    off + sf.Offset,
				fn:        encoderFor(sf.Type),
				typ:       sf.Type,
				index:     append(append([]int{}, index...), i),
				omitEmpty: containsOpt(opts, "omitempty"),
				omitZero:  containsOpt(opts, "omitzero"),
				leaf:      encLeafOf(sf.Type),
				isZero:    isZeroMethod(sf.Type),
				quoted:    containsOpt(opts, "string"),
				ptrPath:   viaPtr,
			})
		}
	}
	walk(t, nil, 0, false, 0)
	for i := range fields {
		f := &fields[i]
		f.simple = !f.ptrPath && !f.omitEmpty && !f.omitZero && !f.quoted && f.leaf != leafNone
		f.keyComma = append(append(make([]byte, 0, len(f.key)+1), ','), f.key...)
	}

	return func(e *encodeState, p unsafe.Pointer, rv reflect.Value) error {
		// The buffer lives in a local for the length of the field loop rather
		// than in e. Every `e.buf = append(e.buf, ...)` is a read of a slice
		// header from the heap, an append, and a write of it back; a local is a
		// register. On a struct of scalars that is three memory operations per
		// field spent on bookkeeping.
		//
		// This is the portable half of what sonic's JIT gets by keeping the
		// buffer's pointer, length and capacity in registers for the whole
		// encode (assembler_regabi_amd64.go:320). It goes back into e before
		// any call that takes e, because those append to e.buf themselves, and
		// at the end.
		b := append(e.buf, '{')
		first := true
		// Hoisted: with OmitZeroStructFields set, no field is simple any more,
		// because every one of them has to be asked whether it is zero first.
		// Read once rather than per field.
		omitAll := e.opts.OmitZeroStructFields
		for i := range fields {
			f := &fields[i]
			if f.simple && !omitAll {
				fp := unsafe.Add(p, f.offset)
				// One append for the separator and the key together. They were
				// two, and the comma is one byte.
				if first {
					b = append(b, f.key...)
					first = false
				} else {
					b = append(b, f.keyComma...)
				}
				switch f.leaf {
				case leafString:
					b = appendQuotedOpts(b, *(*string)(fp), e.opts)
				case leafInt:
					b = appendInt(b, *(*int64)(fp))
				case leafUint:
					b = appendUint(b, *(*uint64)(fp))
				case leafBool:
					if *(*bool)(fp) {
						b = append(b, "true"...)
					} else {
						b = append(b, "false"...)
					}
				default: // leafFloat64
					b = appendFloat(b, *(*float64)(fp), 64)
				}
				continue
			}
			// Everything below can call back into e, so the buffer goes home
			// first and is picked up again after.
			e.buf = b
			var fp unsafe.Pointer
			var frv reflect.Value
			if f.ptrPath {
				// A field behind an embedded pointer has no fixed offset, so it
				// is reached through reflect.
				if !rv.IsValid() {
					rv = reflect.NewAt(t, p).Elem()
				}
				frv = rv
				ok := true
				for _, x := range f.index {
					if frv.Kind() == reflect.Pointer {
						if frv.IsNil() {
							ok = false
							break
						}
						frv = frv.Elem()
					}
					frv = frv.Field(x)
				}
				if !ok {
					continue
				}
				fp = ptrOf(frv)
			} else {
				fp = unsafe.Add(p, f.offset)
			}
			if f.omitEmpty && isEmptyPtr(f.typ, fp) {
				continue
			}
			if (f.omitZero || omitAll) && isZeroPtr(f, fp) {
				continue
			}
			if first {
				e.buf = append(e.buf, f.key...)
				first = false
			} else {
				e.buf = append(e.buf, f.keyComma...)
			}
			if f.quoted {
				if err := e.writeQuotedValue(f, fp, frv); err != nil {
					return err
				}
				b = e.buf
				continue
			}
			// The leaf kinds are written here rather than through f.fn. What
			// the closure does for a string or an int is a load and an append;
			// what the call costs is more than that, and a struct of scalars
			// pays it once per field. Anything else still goes through f.fn,
			// which is every kind that actually needs a compiled encoder.
			switch f.leaf {
			case leafString:
				e.writeString(*(*string)(fp))
			case leafInt:
				e.buf = appendInt(e.buf, *(*int64)(fp))
			case leafUint:
				e.buf = appendUint(e.buf, *(*uint64)(fp))
			case leafBool:
				if *(*bool)(fp) {
					e.buf = append(e.buf, "true"...)
				} else {
					e.buf = append(e.buf, "false"...)
				}
			case leafFloat64:
				e.buf = appendFloat(e.buf, *(*float64)(fp), 64)
			default:
				if err := f.fn(e, fp, frv); err != nil {
					return err
				}
			}
			b = e.buf
		}
		b = append(b, '}')
		e.buf = b
		return nil
	}
}

// writeQuotedValue implements the `,string` tag option: the value's ordinary
// encoding, wrapped in a JSON string.
func (e *encodeState) writeQuotedValue(f *encField, p unsafe.Pointer, rv reflect.Value) error {
	mark := len(e.buf)
	if err := f.fn(e, p, rv); err != nil {
		return err
	}
	inner := append([]byte(nil), e.buf[mark:]...)
	e.buf = e.buf[:mark]
	e.writeString(string(inner))
	return nil
}

func isEmptyPtr(t reflect.Type, p unsafe.Pointer) bool {
	switch t.Kind() {
	case reflect.Bool:
		return !*(*bool)(p)
	case reflect.String:
		return len(*(*string)(p)) == 0
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return reflect.NewAt(t, p).Elem().Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return reflect.NewAt(t, p).Elem().Uint() == 0
	case reflect.Float32, reflect.Float64:
		return reflect.NewAt(t, p).Elem().Float() == 0
	case reflect.Pointer, reflect.Interface:
		return *(*unsafe.Pointer)(p) == nil
	case reflect.Slice:
		return (*sliceHeader)(p).len == 0
	case reflect.Map:
		return reflect.NewAt(t, p).Elem().Len() == 0
	case reflect.Array:
		return t.Len() == 0
	}
	return false
}

func cutComma(s string) (before, after string, found bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}

// isZeroPtr reports whether the field at fp is the zero value for its type.
//
// omitzero is not omitempty with a different name. omitempty drops what looks
// empty -- a length of nothing, a false, a zero -- and omitzero drops only what
// *is* the zero value, so an empty but non-nil slice survives omitzero and does
// not survive omitempty. A type may also decide for itself by having IsZero,
// which is how time.Time gets to say that the zero instant is zero when its
// struct fields say nothing of the kind.
func isZeroPtr(f *encField, fp unsafe.Pointer) bool {
	if f.isZero != nil {
		return f.isZero(fp)
	}
	return reflect.NewAt(f.typ, fp).Elem().IsZero()
}

// isZeroMethod returns a call into t's IsZero, if it has one with the right
// shape. Resolved once at compile time rather than per value.
func isZeroMethod(t reflect.Type) func(unsafe.Pointer) bool {
	m, ok := t.MethodByName("IsZero")
	if !ok {
		if pt := reflect.PointerTo(t); pt.Implements(isZeroerType) {
			return func(p unsafe.Pointer) bool {
				return reflect.NewAt(t, p).Interface().(interface{ IsZero() bool }).IsZero()
			}
		}
		return nil
	}
	if m.Type.NumIn() != 1 || m.Type.NumOut() != 1 || m.Type.Out(0).Kind() != reflect.Bool {
		return nil
	}
	return func(p unsafe.Pointer) bool {
		return reflect.NewAt(t, p).Elem().Interface().(interface{ IsZero() bool }).IsZero()
	}
}

var isZeroerType = reflect.TypeOf((*interface{ IsZero() bool })(nil)).Elem()

func containsOpt(opts, want string) bool {
	for len(opts) > 0 {
		var one string
		one, opts, _ = cutComma(opts)
		if one == want {
			return true
		}
	}
	return false
}
