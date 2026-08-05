package simdjson

import (
	"encoding/json"
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
	"unsafe"
)

// A compiled decoder per type.
//
// The reflection-driven version of this dispatched on reflect.Kind for every
// field of every object and built a reflect.Value to hold the destination. That
// is the whole difference against goccy and sonic, which decide once per type
// what a field costs and then do only that.
//
// Here a type is compiled to a tree of decodeFn once, cached, and executed
// against a raw pointer. Writes go through a typed pointer — *(*string)(p) = s
// — which is not a way around the garbage collector: the compiler emits the
// write barrier because the type is known statically. The unsafe part is only
// the arithmetic that reaches the field, and unsafe.Add is the sanctioned form
// of it.
//
// Anything the compiler cannot express as a fixed offset — an interface, a map,
// a type with its own UnmarshalJSON, a field reached through an embedded
// pointer — falls back to the reflect path, which is still correct and is what
// the differential fuzz test covers.
// The signature is (destination, document, offset) rather than taking a Value.
//
// A Value is five words — document pointer, kind, start, end — and building one
// per field was the largest thing left in the decode. C++ simdjson's On-Demand
// API is the same observation: it never materialises a value object either, it
// walks the structural index and reads the bytes where they are.
//
// Each function returns the offset just past what it consumed, so the caller
// steps on without asking anything a second time.
type decodeFn func(p unsafe.Pointer, d *Doc, i int) (int, error)

var decoderCache sync.Map // reflect.Type -> decodeFn

func decoderFor(t reflect.Type) decodeFn {
	if f, ok := decoderCache.Load(t); ok {
		return f.(decodeFn)
	}
	// Stored before compiling so a recursive type — a tree whose children are
	// the same type — terminates. The indirection is resolved on first use.
	var self decodeFn
	var once sync.Once
	stub := decodeFn(func(p unsafe.Pointer, d *Doc, i int) (int, error) {
		once.Do(func() {
			if f, ok := decoderCache.Load(t); ok {
				if g, ok := f.(decodeFn); ok {
					self = g
				}
			}
		})
		return self(p, d, i)
	})
	decoderCache.Store(t, stub)
	f := compile(t)
	decoderCache.Store(t, f)
	return f
}

func compile(t reflect.Type) decodeFn {
	// A type that decodes itself, or one reached only through reflection,
	// takes the general path.
	if implementsUnmarshaler(t) || reflect.PointerTo(t).Implements(textUnmarshalerType) {
		return reflectFallback(t)
	}
	switch t.Kind() {
	case reflect.String:
		return decString
	case reflect.Bool:
		return decBool
	case reflect.Float64:
		return decFloat64
	case reflect.Float32:
		return decFloat32
	case reflect.Int:
		return decInt
	case reflect.Int8:
		return decInt8
	case reflect.Int16:
		return decInt16
	case reflect.Int32:
		return decInt32
	case reflect.Int64:
		return decInt64
	case reflect.Uint:
		return decUint
	case reflect.Uint8:
		return decUint8
	case reflect.Uint16:
		return decUint16
	case reflect.Uint32:
		return decUint32
	case reflect.Uint64, reflect.Uintptr:
		return decUint64
	case reflect.Struct:
		return compileStruct(t)
	case reflect.Pointer:
		return compilePointer(t)
	case reflect.Slice:
		return compileSlice(t)
	case reflect.Array:
		return compileArray(t)
	}
	return reflectFallback(t)
}

// compileArray decodes a JSON array into [N]T at fixed offsets. This was the
// reflect fallback, and a [2]float64 coordinate pair paid an unmarshaler
// probe and a reflect walk per element — 1.1 M times in canada.json, a fifth
// of the decode.
//
// encoding/json's array rules, held by the differential: excess JSON elements
// are parsed and discarded, Go elements the document did not supply are
// zeroed, and null leaves the array unchanged — arrays are not in the
// null-sets-nil list (pointer, interface, map, slice).
func compileArray(t reflect.Type) decodeFn {
	elem := t.Elem()
	if elem.Kind() == reflect.Interface || elem.Kind() == reflect.Map ||
		implementsUnmarshaler(elem) {
		return reflectFallback(t)
	}
	inner := decoderFor(elem)
	esize := elem.Size()
	n := t.Len()
	if leafKind(elem) != kOther {
		return compileLeafArray(t, elem, inner, esize, n)
	}
	return func(p unsafe.Pointer, d *Doc, i int) (int, error) {
		if d.data[i] == 'n' {
			if end, ok := nullAt(d, i); ok {
				return end, nil
			}
		}
		if d.data[i] != '[' {
			return 0, kindErr(d, i, t)
		}
		end, err := d.matchBracket(i)
		if err != nil {
			return 0, err
		}
		j := d.skip(i + 1)
		k := 0
		for j < end-1 {
			var next int
			var err error
			if k < n {
				next, err = inner(unsafe.Add(p, uintptr(k)*esize), d, j)
			} else if d.strictSkip {
				// Excess elements are discarded, not trusted: when this
				// decode is the document's only walk, they still have to be
				// well-formed, exactly as encoding/json parses what it drops.
				next, err = d.validateValue(j)
			} else {
				next, err = d.skipValue(j)
			}
			if err != nil {
				return 0, err
			}
			k++
			j = d.skip(next)
			if j >= end-1 {
				break
			}
			if d.data[j] != ',' {
				return 0, errAt("expected ',' or ']'", j)
			}
			j = d.skip(j + 1)
			if j >= end-1 {
				return 0, errAt("expected a value after ','", j)
			}
		}
		for ; k < n; k++ {
			reflect.NewAt(elem, unsafe.Add(p, uintptr(k)*esize)).Elem().SetZero()
		}
		return end, nil
	}
}

// compileLeafSlice is compileSlice for scalar element kinds. The general path
// walks the array twice — countElems to size the allocation, then the decode
// — and on []float64 the counting pass was the difference between this
// decoder and sonic's. One pass instead: decode into the destination growing
// by doubling, with the growth (a MakeSlice and a Copy) outside the per-
// element loop. Existing capacity is reused as before; "[]" still leaves a
// non-nil empty slice; null still zeroes, as encoding/json has it for slices.
func compileLeafSlice(t, elem reflect.Type, inner decodeFn, esize uintptr) decodeFn {
	return func(p unsafe.Pointer, d *Doc, i int) (int, error) {
		data := d.data
		if data[i] == 'n' {
			if end, ok := nullAt(d, i); ok {
				reflect.NewAt(t, p).Elem().SetZero()
				return end, nil
			}
		}
		if data[i] != '[' {
			return 0, kindErr(d, i, t)
		}
		rv := reflect.NewAt(t, p).Elem()
		capacity := rv.Cap()
		if capacity == 0 {
			rv.Set(reflect.MakeSlice(t, 8, 8))
			capacity = 8
		} else {
			rv.SetLen(capacity)
		}
		base := rv.UnsafePointer()
		k := 0
		j := d.skip(i + 1)
		for {
			if j >= len(data) {
				return 0, errAt("unexpected end of input", j)
			}
			if data[j] == ']' {
				j++
				break
			}
			if k > 0 {
				if data[j] != ',' {
					return 0, errAt("expected ',' or ']'", j)
				}
				j = d.skip(j + 1)
				if j >= len(data) {
					return 0, errAt("unexpected end of input", j)
				}
				if data[j] == ']' {
					return 0, errAt("expected a value after ','", j)
				}
			}
			if k == capacity {
				grown := reflect.MakeSlice(t, capacity*2, capacity*2)
				reflect.Copy(grown, rv)
				rv.Set(grown)
				capacity *= 2
				base = rv.UnsafePointer()
			}
			next, err := inner(unsafe.Add(base, uintptr(k)*esize), d, j)
			if err != nil {
				return 0, err
			}
			k++
			j = d.skip(next)
		}
		rv.SetLen(k)
		return j, nil
	}
}

// compileLeafArray is compileArray for scalar element kinds, which is what a
// coordinate pair is. The general path found the closing bracket first with
// matchBracket — a binary search over the structural positions, run once per
// [2]float64, 1.1 million times in canada.json. Leaf elements cannot nest, so
// the loop just parses values and looks at the byte after each one: the index
// has already proved the brackets balance, and the ']' is simply there to be
// found. Semantics are compileArray's exactly — excess parsed into a scratch
// slot and discarded, missing zeroed, null untouched — under the same
// differential.
func compileLeafArray(t, elem reflect.Type, inner decodeFn, esize uintptr, n int) decodeFn {
	scratch := reflect.New(elem).UnsafePointer()
	var scratchMu sync.Mutex
	return func(p unsafe.Pointer, d *Doc, i int) (int, error) {
		data := d.data
		if data[i] == 'n' {
			if end, ok := nullAt(d, i); ok {
				return end, nil
			}
		}
		if data[i] != '[' {
			return 0, kindErr(d, i, t)
		}
		j := d.skip(i + 1)
		k := 0
		for {
			if j >= len(data) {
				return 0, errAt("unexpected end of input", j)
			}
			if data[j] == ']' {
				j++
				break
			}
			if k > 0 {
				if data[j] != ',' {
					return 0, errAt("expected ',' or ']'", j)
				}
				j = d.skip(j + 1)
				if j >= len(data) {
					return 0, errAt("unexpected end of input", j)
				}
				if data[j] == ']' {
					return 0, errAt("expected a value after ','", j)
				}
			}
			var next int
			var err error
			if k < n {
				next, err = inner(unsafe.Add(p, uintptr(k)*esize), d, j)
			} else {
				// Excess is parsed and discarded. The scratch slot keeps the
				// leaf decoders' pointer contract; one lock guards the rare
				// case, and concurrent decoders of the same type only meet
				// here when a document actually overflows the array.
				scratchMu.Lock()
				next, err = inner(scratch, d, j)
				scratchMu.Unlock()
			}
			if err != nil {
				return 0, err
			}
			k++
			j = d.skip(next)
		}
		for ; k < n; k++ {
			reflect.NewAt(elem, unsafe.Add(p, uintptr(k)*esize)).Elem().SetZero()
		}
		return j, nil
	}
}

// reflectFallback rebuilds a reflect.Value over the destination and uses the
// general decoder. reflect.NewAt is the documented way back from a pointer to a
// value of a known type.
func reflectFallback(t reflect.Type) decodeFn {
	return func(p unsafe.Pointer, d *Doc, i int) (int, error) {
		v, end, err := d.value(i)
		if err != nil {
			return 0, err
		}
		return end, v.decode(reflect.NewAt(t, p).Elem())
	}
}

// ------------------------------------------------------------------- leaves

// nullAt reports whether the literal at i is null, and where it ends.
func nullAt(d *Doc, i int) (int, bool) {
	if i+4 <= len(d.data) && string(d.data[i:i+4]) == "null" {
		return i + 4, true
	}
	return 0, false
}

func decString(p unsafe.Pointer, d *Doc, i int) (int, error) {
	if d.data[i] != '"' {
		if end, ok := nullAt(d, i); ok {
			return end, nil
		}
		return 0, kindErr(d, i, stringType)
	}
	end, ok := d.stringEnd(i)
	if !ok {
		var err error
		if end, err = d.stringEndSlow(i); err != nil {
			return 0, err
		}
	}
	*(*string)(p) = d.decodeStr(i, end)
	return end, nil
}

func decBool(p unsafe.Pointer, d *Doc, i int) (int, error) {
	switch d.data[i] {
	case 't':
		if end, err := d.litEnd(i, "true"); err == nil {
			*(*bool)(p) = true
			return end, nil
		}
	case 'f':
		if end, err := d.litEnd(i, "false"); err == nil {
			*(*bool)(p) = false
			return end, nil
		}
	case 'n':
		if end, ok := nullAt(d, i); ok {
			return end, nil
		}
	}
	return 0, kindErr(d, i, boolType)
}

// numAt returns the number literal at i and where it ends, or reports null.
func numAt(d *Doc, i int, t reflect.Type) (raw []byte, end int, isNull bool, err error) {
	if d.data[i] == 'n' {
		if e, ok := nullAt(d, i); ok {
			return nil, e, true, nil
		}
	}
	e, ok := d.number(i)
	if !ok {
		return nil, 0, false, kindErr(d, i, t)
	}
	return d.data[i:e], e, false, nil
}

func numErr(raw []byte, t reflect.Type) error {
	return &json.UnmarshalTypeError{Value: "number " + string(raw), Type: t}
}

// decFloat64 parses and validates in the same pass: parseFloat64At walks
// number()'s grammar itself, so the extent scan that numAt ran first is not
// run twice. The statuses keep the error surface identical — a syntax reject
// reports the same kindErr, and a fallback hands the exact extent to strconv.
func decFloat64(p unsafe.Pointer, d *Doc, i int) (int, error) {
	if d.data[i] == 'n' {
		if e, ok := nullAt(d, i); ok {
			return e, nil
		}
	}
	f, end, status := parseFloat64At(d.data, i)
	switch status {
	case floatParsed:
	case floatFallback:
		var perr error
		f, perr = strconv.ParseFloat(bstr(d.data[i:end]), 64)
		if perr != nil {
			return 0, numErr(d.data[i:end], float64Type)
		}
	default:
		return 0, kindErr(d, i, float64Type)
	}
	*(*float64)(p) = f
	return end, nil
}

func decFloat32(p unsafe.Pointer, d *Doc, i int) (int, error) {
	raw, end, isNull, err := numAt(d, i, float32Type)
	if err != nil || isNull {
		return end, err
	}
	f, perr := strconv.ParseFloat(bstr(raw), 32)
	if perr != nil {
		return 0, numErr(raw, float32Type)
	}
	*(*float32)(p) = float32(f)
	return end, nil
}

// intFn and uintFn build the signed and unsigned leaves, which differ only in
// their width and the store.
func intFn(bits int, t reflect.Type, store func(unsafe.Pointer, int64)) decodeFn {
	return func(p unsafe.Pointer, d *Doc, i int) (int, error) {
		raw, end, isNull, err := numAt(d, i, t)
		if err != nil || isNull {
			return end, err
		}
		n, perr := strconv.ParseInt(bstr(raw), 10, bits)
		if perr != nil {
			return 0, numErr(raw, t)
		}
		store(p, n)
		return end, nil
	}
}

func uintFn(bits int, t reflect.Type, store func(unsafe.Pointer, uint64)) decodeFn {
	return func(p unsafe.Pointer, d *Doc, i int) (int, error) {
		raw, end, isNull, err := numAt(d, i, t)
		if err != nil || isNull {
			return end, err
		}
		n, perr := strconv.ParseUint(bstr(raw), 10, bits)
		if perr != nil {
			return 0, numErr(raw, t)
		}
		store(p, n)
		return end, nil
	}
}

var (
	decInt    = intFn(strconv.IntSize, intType, func(p unsafe.Pointer, n int64) { *(*int)(p) = int(n) })
	decInt8   = intFn(8, int8Type, func(p unsafe.Pointer, n int64) { *(*int8)(p) = int8(n) })
	decInt16  = intFn(16, int16Type, func(p unsafe.Pointer, n int64) { *(*int16)(p) = int16(n) })
	decInt32  = intFn(32, int32Type, func(p unsafe.Pointer, n int64) { *(*int32)(p) = int32(n) })
	decInt64  = intFn(64, int64Type, func(p unsafe.Pointer, n int64) { *(*int64)(p) = n })
	decUint   = uintFn(strconv.IntSize, uintType, func(p unsafe.Pointer, n uint64) { *(*uint)(p) = uint(n) })
	decUint8  = uintFn(8, uint8Type, func(p unsafe.Pointer, n uint64) { *(*uint8)(p) = uint8(n) })
	decUint16 = uintFn(16, uint16Type, func(p unsafe.Pointer, n uint64) { *(*uint16)(p) = uint16(n) })
	decUint32 = uintFn(32, uint32Type, func(p unsafe.Pointer, n uint64) { *(*uint32)(p) = uint32(n) })
	decUint64 = uintFn(64, uint64Type, func(p unsafe.Pointer, n uint64) { *(*uint64)(p) = n })
)

func kindErr(d *Doc, i int, t reflect.Type) error {
	v, _, err := d.value(i)
	if err != nil {
		return err
	}
	return v.typeErr(t)
}

// ---------------------------------------------------------------- composites

func compilePointer(t reflect.Type) decodeFn {
	elem := t.Elem()
	inner := decoderFor(elem)
	return func(p unsafe.Pointer, d *Doc, i int) (int, error) {
		pp := (*unsafe.Pointer)(p)
		if d.data[i] == 'n' {
			if end, ok := nullAt(d, i); ok {
				*(*unsafe.Pointer)(p) = nil
				return end, nil
			}
		}
		if *pp == nil {
			// Allocated through reflect so the pointer the collector sees is
			// one it handed out.
			*(*unsafe.Pointer)(p) = unsafe.Pointer(reflect.New(elem).Pointer())
		}
		return inner(*pp, d, i)
	}
}

func compileSlice(t reflect.Type) decodeFn {
	elem := t.Elem()
	// []byte and anything needing reflection stays on the general path.
	if elem.Kind() == reflect.Uint8 || elem.Kind() == reflect.Interface ||
		elem.Kind() == reflect.Map || implementsUnmarshaler(elem) {
		return reflectFallback(t)
	}
	inner := decoderFor(elem)
	esize := elem.Size()
	if leafKind(elem) != kOther {
		return compileLeafSlice(t, elem, inner, esize)
	}
	return func(p unsafe.Pointer, d *Doc, i int) (int, error) {
		if d.data[i] == 'n' {
			if end, ok := nullAt(d, i); ok {
				reflect.NewAt(t, p).Elem().SetZero()
				return end, nil
			}
		}
		if d.data[i] != '[' {
			return 0, kindErr(d, i, t)
		}
		end, err := d.matchBracket(i)
		if err != nil {
			return 0, err
		}
		rv := reflect.NewAt(t, p).Elem()
		n, err := countElems(d, i, end)
		if err != nil {
			return 0, err
		}
		if rv.IsNil() || rv.Cap() < n {
			rv.Set(reflect.MakeSlice(t, n, n))
		} else {
			rv.SetLen(n)
		}
		if n == 0 {
			return end, nil
		}
		base := unsafe.Pointer(rv.Pointer())
		j := d.skip(i + 1)
		k := 0
		for j < end-1 && k < n {
			next, err := inner(unsafe.Add(base, uintptr(k)*esize), d, j)
			if err != nil {
				return 0, err
			}
			k++
			j = d.skip(next)
			if j >= end-1 {
				break
			}
			if d.data[j] != ',' {
				return 0, errAt("expected ',' or ']'", j)
			}
			j = d.skip(j + 1)
			if j >= end-1 {
				return 0, errAt("expected a value after ','", j)
			}
		}
		return end, nil
	}
}

// countElems counts an array's elements by stepping over them, which for a
// container is a bracket lookup rather than a walk of its contents.
func countElems(d *Doc, start, end int) (int, error) {
	n := 0
	i := d.skip(start + 1)
	for i < end-1 {
		// The error is returned, not swallowed. Returning the count so far
		// made `[A]` look like an empty array, so the decode loop never ran and
		// nothing ever saw the A.
		next, err := d.skipValue(i)
		if err != nil {
			return 0, err
		}
		n++
		i = d.skip(next)
		if i >= end-1 {
			break
		}
		if d.data[i] != ',' {
			return 0, errAt("expected ',' or ']'", i)
		}
		i = d.skip(i + 1)
		if i >= end-1 {
			return 0, errAt("expected a value after ','", i)
		}
	}
	return n, nil
}

// compiledField is one struct field: where it is and how to fill it.
// Leaf kinds handled inline by the struct loop.
//
// The loop used to call cf.fn for every field, and a call through a function
// value is an indirect branch whose target changes with the field's type. perf
// put us at 57 million branch misses against goccy's 21 million on the same
// work — more than one per field. A switch over a small dense code is a jump
// table, and the sequence of codes is fixed for a given struct type, so it
// repeats identically for every object of that type and the predictor can learn
// it. The bodies are inline as well, so there is no return to predict either.
const (
	kOther = iota
	kString
	kBool
	kInt
	kInt64
	kFloat64
)

// leafKind maps a type to its inline code, or kOther for everything the loop
// does not special-case.
func leafKind(t reflect.Type) uint8 {
	if implementsUnmarshaler(t) || reflect.PointerTo(t).Implements(textUnmarshalerType) {
		return kOther
	}
	switch t.Kind() {
	case reflect.String:
		return kString
	case reflect.Bool:
		return kBool
	case reflect.Int:
		return kInt
	case reflect.Int64:
		return kInt64
	case reflect.Float64:
		return kFloat64
	}
	return kOther
}

type compiledField struct {
	offset   uintptr
	fn       decodeFn
	typ      reflect.Type
	kind     uint8
	asString bool
}

// compiledStruct finds a field by name without hashing it.
//
// A map lookup on the key string was 22% of decoding twitter.json into a
// struct, between the hash and the probe. Real structs have a handful of
// fields, and their names differ in length far more often than not — so the
// names are bucketed by length and the bucket is scanned with a byte compare.
// Most buckets hold one entry, which makes the common case a length check and
// a memequal.
type compiledStruct struct {
	byLen  [][]namedField // indexed by len(name), for names up to maxFieldName
	byName map[string]*compiledField
	byFold map[string]*compiledField
	// longName records whether any field name was too long for byLen. When
	// none was, byLen holds every name, and a key longer than the longest one
	// cannot match anything -- so the map does not have to be asked.
	longName bool
	// hints caches, per position in the object, the outcome for the key seen
	// there in the last object decoded — records in a stream carry their
	// fields in the same order, so this hits nearly always and costs one
	// length test and one memeq. A nil cf is a cached miss, which documents
	// make the common case. The pointee is immutable; the store is racy on
	// purpose, and a wrong hint is just a failed compare.
	hints [16]atomic.Pointer[fieldHint]
}

// fieldHint is one remembered (key, outcome) pair; cf nil means the key
// matches no field.
type fieldHint struct {
	name string
	cf   *compiledField
}

type namedField struct {
	// first is name[0], checked before the string compare. A miss is the
	// common case — a document carries keys the struct does not name — and one
	// byte rejects most of them without a call into memequal.
	first byte
	name  string
	f     *compiledField
}

// maxFieldName bounds the length table. A longer name falls back to the map,
// which is correct and no slower than it was.
const maxFieldName = 64

func (cs *compiledStruct) lookup(key []byte) (*compiledField, bool) {
	if len(key) >= len(cs.byLen) {
		// Longer than the longest name in the table. Unless a name was too
		// long to be in the table at all, nothing can match: exact comparison
		// needs equal lengths and so does the ASCII fold. Asking the map is a
		// hash and a probe to be told what the length already said.
		//
		// This is not a rare path on real documents. byLen is sized by the
		// struct's longest field name, and a document's keys are not -- the
		// struct behind these benchmarks has nothing as long as
		// in_reply_to_status_id_str, so every occurrence of that key and its
		// neighbours was hashed. runtime.mapaccess2_faststr was 7.1% of
		// unmarshalling twitter.json into a struct.
		if !cs.longName {
			return nil, false
		}
		f, ok := cs.byName[string(key)]
		return f, ok
	}
	if len(key) == 0 {
		f, ok := cs.byName[string(key)]
		return f, ok
	}
	b := cs.byLen[len(key)]
	c := key[0]
	for i := range b {
		if b[i].first == c && b[i].name == string(key) {
			return b[i].f, true
		}
	}
	// The case-insensitive pass happens here rather than through the fold map,
	// and only after every exact match has failed, which is encoding/json's
	// order. Keeping it in the bucket is what makes a MISS cheap — and misses
	// are the common case, because a document carries fields a struct does not
	// name. twitter.json has about thirty keys per status against a struct with
	// twelve. The first version left misses to the map and made them cost a
	// bucket scan, an unquote and two hashes; that version measured slower than
	// the plain map it replaced.
	//
	// Worth ~2% over the map, interleaved: 812/811 us against 847/823. Measured
	// across separate runs it looked like a loss, because the machine had moved
	// between them — the map version's 752 us and this one's 781 us were taken
	// twenty minutes apart and are not comparable.
	for i := range b {
		if foldEqualASCII(b[i].name, key) {
			return b[i].f, true
		}
	}
	return nil, false
}

// hasEscape reports whether a key's raw bytes contain a backslash, which is the
// only reason the bytes could differ from the name they stand for.
func hasEscape(b []byte) bool {
	for i := 0; i < len(b); i++ {
		if b[i] == '\\' {
			return true
		}
	}
	return false
}

// foldEqualASCII compares case-insensitively without allocating. Keys outside
// ASCII fall through to the general path, which unquotes and folds properly.
func foldEqualASCII(name string, key []byte) bool {
	if len(name) != len(key) {
		return false
	}
	for i := 0; i < len(name); i++ {
		a, b := name[i], key[i]
		if a == b {
			continue
		}
		if a >= 'A' && a <= 'Z' {
			a += 32
		}
		if b >= 'A' && b <= 'Z' {
			b += 32
		}
		if a != b {
			return false
		}
	}
	return true
}

func compileStruct(t reflect.Type) decodeFn {
	// Fields reached through an embedded pointer have no fixed offset, so a
	// type with any of them takes the reflect path whole. They are rare, and
	// splitting the two would double the code that has to agree.
	if hasPointerEmbed(t) {
		return reflectFallback(t)
	}
	plan := planFor(t)
	cs := &compiledStruct{
		byName: make(map[string]*compiledField, len(plan.byName)),
		byFold: make(map[string]*compiledField, len(plan.byFold)),
	}
	build := func(f field) *compiledField {
		var off uintptr
		ft := t
		for _, i := range f.index {
			sf := ft.Field(i)
			off += sf.Offset
			ft = sf.Type
		}
		return &compiledField{offset: off, fn: decoderFor(ft), typ: ft,
			kind: leafKind(ft), asString: f.asString}
	}
	maxLen := 0
	for name, f := range plan.byName {
		cf := build(f)
		cs.byName[name] = cf
		if len(name) > maxFieldName {
			cs.longName = true
		} else if len(name) > maxLen {
			maxLen = len(name)
		}
	}
	cs.byLen = make([][]namedField, maxLen+1)
	for name, cf := range cs.byName {
		if len(name) <= maxFieldName {
			cs.byLen[len(name)] = append(cs.byLen[len(name)], namedField{name[0], name, cf})
		}
	}
	for name, f := range plan.byFold {
		cs.byFold[name] = build(f)
	}

	return func(p unsafe.Pointer, d *Doc, at int) (int, error) {
		// Hoisted so the compiler can track its length. d.data is a field, and
		// across a bounds test and the access that follows it the compiler will
		// not assume the field has not changed — so every access in this loop
		// carried its own check.
		data := d.data
		if data[at] == 'n' {
			if end, ok := nullAt(d, at); ok {
				return end, nil
			}
		}
		if data[at] != '{' {
			return 0, kindErr(d, at, t)
		}
		vEnd, err := d.matchBracket(at)
		if err != nil {
			return 0, err
		}
		i := d.skip(at + 1)
		pos := 0
		for i < vEnd-1 {
			if data[i] != '"' {
				return 0, errAt("expected a string key", i)
			}
			kstart := i
			kend, ok := d.stringEnd(i)
			if !ok {
				var err error
				if kend, err = d.stringEndSlow(i); err != nil {
					return 0, err
				}
			}
			// Go elides the allocation for a string conversion used only to
			// index a map, so an exact match on an unescaped key costs nothing.
			raw := data[i+1 : kend-1]
			// The field seen at this position in the LAST object is checked
			// first: records in a stream carry their fields in the same order,
			// so one length test and one memeq replaces the bucket scan — and
			// a remembered miss replaces it too, which real documents make the
			// common case.
			var cf *compiledField
			var found bool
			h := (*fieldHint)(nil)
			if pos < len(cs.hints) {
				h = cs.hints[pos].Load()
			}
			pos++
			if h != nil && string(raw) == h.name {
				cf, found = h.cf, h.cf != nil
			} else {
				cf, found = cs.lookup(raw)
				// The fallback is only for a key carrying an escape, where the
				// bytes in the document are not the name being matched. Without
				// that guard every unknown field paid an unquote and two map
				// lookups to learn what the bucket had already decided — 17% of
				// the decode, on a document whose keys are mostly ones the struct
				// does not name.
				if !found && hasEscape(raw) {
					key, _ := unquote(data[i:kend])
					if cf, found = cs.byName[key]; !found {
						cf, found = cs.byFold[toLowerASCII(key)]
					}
					// An escaped key's bytes are not the name that matched;
					// remembering them would hint the wrong thing.
				} else if pos-1 < len(cs.hints) {
					nh := &fieldHint{name: string(raw)}
					if found {
						nh.cf = cf
					}
					cs.hints[pos-1].Store(nh)
				}
			}

			i = d.skip(kend)
			if i >= vEnd || data[i] != ':' {
				return 0, errAt("expected ':' after object key", i)
			}
			i = d.skip(i + 1)

			var next int
			var err error
			switch {
			case !found:
				if d.disallowUnknown {
					return 0, unknownFieldErr(data, kstart, kend)
				}
				// An unknown field is stepped over, not decoded — a bracket
				// lookup rather than a walk, unless this decode is the thing
				// proving the document well-formed.
				if d.strictSkip {
					next, err = d.validateValue(i)
				} else {
					next, err = d.skipValue(i)
				}
			case cf.asString:
				var e Value
				e, next, err = d.value(i)
				if err == nil {
					fp := unsafe.Add(p, cf.offset)
					if e.kind == Null {
						err = e.decode(reflect.NewAt(cf.typ, fp).Elem())
					} else {
						err = decodeQuoted(e, reflect.NewAt(cf.typ, fp).Elem())
					}
				}
			default:
				fp := unsafe.Add(p, cf.offset)
				switch cf.kind {
				case kString:
					next, err = decString(fp, d, i)
				case kBool:
					next, err = decBool(fp, d, i)
				case kInt:
					next, err = decInt(fp, d, i)
				case kInt64:
					next, err = decInt64(fp, d, i)
				case kFloat64:
					next, err = decFloat64(fp, d, i)
				default:
					next, err = cf.fn(fp, d, i)
				}
			}
			if err != nil {
				return 0, err
			}

			// The separator is checked here rather than left to a grammar
			// pass. When this decode is the only walk of the document, nothing
			// else is going to notice that `{"a":1"b":2}` has no comma in it —
			// the loop would simply read the next key. The fuzzer found that
			// within a second of the descent being removed.
			i = d.skip(next)
			if i >= vEnd-1 {
				break
			}
			if data[i] != ',' {
				return 0, errAt("expected ',' or '}'", i)
			}
			i = d.skip(i + 1)
			// A comma promises another member. Without this the loop condition
			// would take `{"a":1,}` as a clean exit.
			if i >= vEnd-1 {
				return 0, errAt("expected a string key", i)
			}
		}
		return vEnd, nil
	}
}

func hasPointerEmbed(t reflect.Type) bool {
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if sf.Anonymous && sf.Type.Kind() == reflect.Pointer {
			return true
		}
		if sf.Anonymous && sf.Type.Kind() == reflect.Struct && hasPointerEmbed(sf.Type) {
			return true
		}
	}
	return false
}

// toLowerASCII is strings.ToLower for the ASCII case, which is every key that
// reaches it in practice, and allocates only when the key is not already lower.
func toLowerASCII(s string) string {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= 'A' && c <= 'Z' {
			b := []byte(s)
			for ; i < len(b); i++ {
				if c := b[i]; c >= 'A' && c <= 'Z' {
					b[i] = c + 32
				}
			}
			return string(b)
		}
	}
	return s
}

var (
	stringType  = reflect.TypeOf("")
	boolType    = reflect.TypeOf(false)
	float64Type = reflect.TypeOf(float64(0))
	float32Type = reflect.TypeOf(float32(0))
	intType     = reflect.TypeOf(int(0))
	int8Type    = reflect.TypeOf(int8(0))
	int16Type   = reflect.TypeOf(int16(0))
	int32Type   = reflect.TypeOf(int32(0))
	int64Type   = reflect.TypeOf(int64(0))
	uintType    = reflect.TypeOf(uint(0))
	uint8Type   = reflect.TypeOf(uint8(0))
	uint16Type  = reflect.TypeOf(uint16(0))
	uint32Type  = reflect.TypeOf(uint32(0))
	uint64Type  = reflect.TypeOf(uint64(0))
)
