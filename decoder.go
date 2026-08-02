package simdjson

import (
	"encoding/json"
	"reflect"
	"strconv"
	"sync"
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
type decodeFn func(p unsafe.Pointer, v Value) error

var decoderCache sync.Map // reflect.Type -> decodeFn

func decoderFor(t reflect.Type) decodeFn {
	if f, ok := decoderCache.Load(t); ok {
		return f.(decodeFn)
	}
	// Stored before compiling so a recursive type — a tree whose children are
	// the same type — terminates. The indirection is resolved on first use.
	var self decodeFn
	var once sync.Once
	stub := decodeFn(func(p unsafe.Pointer, v Value) error {
		once.Do(func() {
			if f, ok := decoderCache.Load(t); ok {
				if d, ok := f.(decodeFn); ok {
					self = d
				}
			}
		})
		return self(p, v)
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
	}
	return reflectFallback(t)
}

// reflectFallback rebuilds a reflect.Value over the destination and uses the
// general decoder. reflect.NewAt is the documented way back from a pointer to a
// value of a known type.
func reflectFallback(t reflect.Type) decodeFn {
	return func(p unsafe.Pointer, v Value) error {
		return v.decode(reflect.NewAt(t, p).Elem())
	}
}

// ------------------------------------------------------------------- leaves

func decString(p unsafe.Pointer, v Value) error {
	if v.kind == Null {
		return nil
	}
	if v.kind != String {
		return v.typeErr(stringType)
	}
	s, _ := v.str()
	*(*string)(p) = s
	return nil
}

func decBool(p unsafe.Pointer, v Value) error {
	if v.kind == Null {
		return nil
	}
	if v.kind != Bool {
		return v.typeErr(boolType)
	}
	*(*bool)(p) = v.Bool()
	return nil
}

func decFloat64(p unsafe.Pointer, v Value) error {
	if v.kind == Null {
		return nil
	}
	if v.kind != Number {
		return v.typeErr(float64Type)
	}
	f, err := strconv.ParseFloat(bstr(v.Raw()), 64)
	if err != nil {
		return &json.UnmarshalTypeError{Value: "number " + string(v.Raw()), Type: float64Type}
	}
	*(*float64)(p) = f
	return nil
}

func decFloat32(p unsafe.Pointer, v Value) error {
	if v.kind == Null {
		return nil
	}
	if v.kind != Number {
		return v.typeErr(float32Type)
	}
	f, err := strconv.ParseFloat(bstr(v.Raw()), 32)
	if err != nil {
		return &json.UnmarshalTypeError{Value: "number " + string(v.Raw()), Type: float32Type}
	}
	*(*float32)(p) = float32(f)
	return nil
}

// intVal parses a signed number and checks it fits in bits.
func intVal(v Value, bits int, t reflect.Type) (int64, error) {
	if v.kind != Number {
		return 0, v.typeErr(t)
	}
	n, err := strconv.ParseInt(bstr(v.Raw()), 10, bits)
	if err != nil {
		return 0, &json.UnmarshalTypeError{Value: "number " + string(v.Raw()), Type: t}
	}
	return n, nil
}

func uintVal(v Value, bits int, t reflect.Type) (uint64, error) {
	if v.kind != Number {
		return 0, v.typeErr(t)
	}
	n, err := strconv.ParseUint(bstr(v.Raw()), 10, bits)
	if err != nil {
		return 0, &json.UnmarshalTypeError{Value: "number " + string(v.Raw()), Type: t}
	}
	return n, nil
}

func decInt(p unsafe.Pointer, v Value) error {
	if v.kind == Null {
		return nil
	}
	n, err := intVal(v, strconv.IntSize, intType)
	if err != nil {
		return err
	}
	*(*int)(p) = int(n)
	return nil
}

func decInt8(p unsafe.Pointer, v Value) error {
	if v.kind == Null {
		return nil
	}
	n, err := intVal(v, 8, int8Type)
	if err != nil {
		return err
	}
	*(*int8)(p) = int8(n)
	return nil
}

func decInt16(p unsafe.Pointer, v Value) error {
	if v.kind == Null {
		return nil
	}
	n, err := intVal(v, 16, int16Type)
	if err != nil {
		return err
	}
	*(*int16)(p) = int16(n)
	return nil
}

func decInt32(p unsafe.Pointer, v Value) error {
	if v.kind == Null {
		return nil
	}
	n, err := intVal(v, 32, int32Type)
	if err != nil {
		return err
	}
	*(*int32)(p) = int32(n)
	return nil
}

func decInt64(p unsafe.Pointer, v Value) error {
	if v.kind == Null {
		return nil
	}
	n, err := intVal(v, 64, int64Type)
	if err != nil {
		return err
	}
	*(*int64)(p) = n
	return nil
}

func decUint(p unsafe.Pointer, v Value) error {
	if v.kind == Null {
		return nil
	}
	n, err := uintVal(v, strconv.IntSize, uintType)
	if err != nil {
		return err
	}
	*(*uint)(p) = uint(n)
	return nil
}

func decUint8(p unsafe.Pointer, v Value) error {
	if v.kind == Null {
		return nil
	}
	n, err := uintVal(v, 8, uint8Type)
	if err != nil {
		return err
	}
	*(*uint8)(p) = uint8(n)
	return nil
}

func decUint16(p unsafe.Pointer, v Value) error {
	if v.kind == Null {
		return nil
	}
	n, err := uintVal(v, 16, uint16Type)
	if err != nil {
		return err
	}
	*(*uint16)(p) = uint16(n)
	return nil
}

func decUint32(p unsafe.Pointer, v Value) error {
	if v.kind == Null {
		return nil
	}
	n, err := uintVal(v, 32, uint32Type)
	if err != nil {
		return err
	}
	*(*uint32)(p) = uint32(n)
	return nil
}

func decUint64(p unsafe.Pointer, v Value) error {
	if v.kind == Null {
		return nil
	}
	n, err := uintVal(v, 64, uint64Type)
	if err != nil {
		return err
	}
	*(*uint64)(p) = n
	return nil
}

// ---------------------------------------------------------------- composites

func compilePointer(t reflect.Type) decodeFn {
	elem := t.Elem()
	inner := decoderFor(elem)
	return func(p unsafe.Pointer, v Value) error {
		pp := (*unsafe.Pointer)(p)
		if v.kind == Null {
			*pp = nil
			return nil
		}
		if *pp == nil {
			// Allocated through reflect so the pointer the collector sees is
			// one it handed out.
			np := reflect.New(elem)
			*(*unsafe.Pointer)(p) = unsafe.Pointer(np.Pointer())
		}
		return inner(*pp, v)
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
	return func(p unsafe.Pointer, v Value) error {
		if v.kind == Null {
			reflect.NewAt(t, p).Elem().SetZero()
			return nil
		}
		if v.kind != Array {
			return v.typeErr(t)
		}
		rv := reflect.NewAt(t, p).Elem()
		n := v.Len()
		if rv.IsNil() || rv.Cap() < n {
			rv.Set(reflect.MakeSlice(t, n, n))
		} else {
			rv.SetLen(n)
		}
		if n == 0 {
			return nil
		}
		base := unsafe.Pointer(rv.Pointer())
		d := v.d
		i := d.skip(v.start + 1)
		k := 0
		for i < v.end-1 && k < n {
			e, next, err := d.value(i)
			if err != nil {
				return err
			}
			if err := inner(unsafe.Add(base, uintptr(k)*esize), e); err != nil {
				return err
			}
			k++
			i = d.skip(next)
			if i < v.end-1 && d.data[i] == ',' {
				i = d.skip(i + 1)
			}
		}
		return nil
	}
}

// compiledField is one struct field: where it is and how to fill it.
type compiledField struct {
	offset   uintptr
	fn       decodeFn
	typ      reflect.Type
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
}

type namedField struct {
	name string
	f    *compiledField
}

// maxFieldName bounds the length table. A longer name falls back to the map,
// which is correct and no slower than it was.
const maxFieldName = 64

func (cs *compiledStruct) lookup(key []byte) (*compiledField, bool) {
	if len(key) >= len(cs.byLen) {
		f, ok := cs.byName[string(key)]
		return f, ok
	}
	b := cs.byLen[len(key)]
	for i := range b {
		if b[i].name == string(key) {
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
		return &compiledField{offset: off, fn: decoderFor(ft), typ: ft, asString: f.asString}
	}
	maxLen := 0
	for name, f := range plan.byName {
		cf := build(f)
		cs.byName[name] = cf
		if len(name) <= maxFieldName && len(name) > maxLen {
			maxLen = len(name)
		}
	}
	cs.byLen = make([][]namedField, maxLen+1)
	for name, cf := range cs.byName {
		if len(name) <= maxFieldName {
			cs.byLen[len(name)] = append(cs.byLen[len(name)], namedField{name, cf})
		}
	}
	for name, f := range plan.byFold {
		cs.byFold[name] = build(f)
	}

	return func(p unsafe.Pointer, v Value) error {
		if v.kind == Null {
			return nil
		}
		if v.kind != Object {
			return v.typeErr(t)
		}
		d := v.d
		i := d.skip(v.start + 1)
		for i < v.end-1 {
			if d.data[i] != '"' {
				return errAt("expected a string key", i)
			}
			kend, ok := d.stringEnd(i)
			if !ok {
				var err error
				if kend, err = d.stringEndSlow(i); err != nil {
					return err
				}
			}
			// Go elides the allocation for a string conversion used only to
			// index a map, so an exact match on an unescaped key costs nothing.
			cf, found := cs.lookup(d.data[i+1 : kend-1])
			if !found {
				key, _ := unquote(d.data[i:kend])
				if cf, found = cs.byName[key]; !found {
					cf, found = cs.byFold[toLowerASCII(key)]
				}
			}

			i = d.skip(kend)
			if i >= v.end || d.data[i] != ':' {
				return errAt("expected ':' after object key", i)
			}
			i = d.skip(i + 1)
			e, next, err := d.value(i)
			if err != nil {
				return err
			}
			if found {
				fp := unsafe.Add(p, cf.offset)
				if cf.asString && e.kind != Null {
					err = decodeQuoted(e, reflect.NewAt(cf.typ, fp).Elem())
				} else {
					err = cf.fn(fp, e)
				}
				if err != nil {
					return err
				}
			}

			i = d.skip(next)
			if i < v.end-1 && d.data[i] == ',' {
				i = d.skip(i + 1)
			}
		}
		return nil
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
