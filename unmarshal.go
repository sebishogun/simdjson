package simdjson

import (
	"encoding"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"unsafe"
)

// Unmarshal parses data and stores the result in the value pointed to by v.
//
// It is [encoding/json.Unmarshal]'s contract, decoded from the structural index
// instead of by scanning the document a second time. Where the two could differ
// they are held together by a differential fuzz test rather than by inspection:
// tag handling, case-insensitive field matching, the `,string` option, embedded
// structs, and what null does to each kind are all places the standard library
// does something particular, and each of them was found by the fuzzer rather
// than by reading the documentation.
//
// v must be a non-nil pointer.
// indexPool recycles the scratch an Unmarshal needs.
//
// The masks, the bracket array and the in-string words are a megabyte of
// buffers for a megabyte of document, and a caller decoding a stream of
// payloads was allocating all of it again for each one. Nothing the caller
// keeps points into them — decoded strings live in the Doc's own buffer — so
// they can go straight back.
var indexPool sync.Pool

func Unmarshal(data []byte, v any) error {
	ix, _ := indexPool.Get().(*index)
	defer func() {
		if ix != nil {
			indexPool.Put(ix)
		}
	}()

	// The index, then one walk that both decodes and validates — not Parse,
	// whose grammar descent would walk the whole document before the decoder
	// walked it again. On twitter.json that second walk was 218 us of 559.
	ix, err := buildIndex(data, ix, true)
	if err != nil {
		return err
	}
	d, err := scanRoot(data, ix)
	if err != nil {
		return err
	}
	d.strictSkip = true
	if err := d.Root().Decode(v); err != nil {
		return err
	}
	// Nothing may follow the top-level value. Parse's descent used to prove
	// this; the decode does not reach past the root, so it is checked here.
	if p := d.skip(d.root.end); p < len(data) {
		return errAt("trailing data", p)
	}
	return nil
}

// Unmarshal decodes an already-parsed document into v.
//
// Use this when the same bytes are decoded more than once, or when a document
// is navigated first and decoded after: the parse is the expensive half and
// this skips it.
func (d *Doc) Unmarshal(v any) error { return d.Root().Decode(v) }

// Decode stores this value in the value pointed to by v.
//
// It is Unmarshal for a part of a document, so a large payload can be navigated
// to the field that matters and only that field decoded.
func (v Value) Decode(out any) error {
	rv := reflect.ValueOf(out)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return &json.InvalidUnmarshalError{Type: reflect.TypeOf(out)}
	}
	if v.kind == Invalid {
		return errSyntax("decoding an invalid value")
	}
	// Through the compiled decoder when the destination is addressable, which
	// it is for every pointer that gets here. See decoder.go.
	el := rv.Elem()
	if el.CanAddr() {
		_, err := decoderFor(el.Type())(unsafe.Pointer(el.UnsafeAddr()), v.d, v.start)
		return err
	}
	return v.decode(el)
}

// decodeAt decodes the value beginning at i into out, and returns where it
// ends.
//
// The end is the reason this exists rather than Value.Decode. Building a Value
// first means matching its brackets over the index to find its extent, and the
// compiled decoder is going to walk to that same place and report it anyway --
// so a stream, which needs the end only to know where the next value starts,
// was paying for a bracket match per value to learn something it was about to
// be told.
func (d *Doc) decodeAt(i int, out any) (int, error) {
	rv := reflect.ValueOf(out)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return 0, &json.InvalidUnmarshalError{Type: reflect.TypeOf(out)}
	}
	if el := rv.Elem(); el.CanAddr() {
		return decoderFor(el.Type())(unsafe.Pointer(el.UnsafeAddr()), d, i)
	}
	// Not addressable, which a pointer handed to Decode always is unless it
	// came from reflection. Worth no cleverness.
	v, end, err := d.value(i)
	if err != nil {
		return 0, err
	}
	return end, v.decode(rv.Elem())
}

// validate walks v's bytes and reports the first thing wrong with them.
func (v Value) validate() error {
	if v.d == nil {
		return nil
	}
	_, err := v.d.validateValue(v.start)
	return err
}

// decode stores v into the settable reflect.Value rv.
func (v Value) decode(rv reflect.Value) error {
	// The two interfaces come first, and before the null check, because a type
	// that implements them decides for itself what null means.
	if u, ok := unmarshalerFor(rv); ok {
		// The bytes have to be proved well-formed before they go out. Getting
		// here means the decoder found the extent of the value — by matching
		// its brackets over the index — and then skipped its contents, because
		// a type with UnmarshalJSON is going to interpret them itself. Nothing
		// checked them, and UnmarshalJSON is under no obligation to:
		// json.RawMessage's copies whatever it is given. So "[A]" decoded into
		// a RawMessage came back with no error and a RawMessage holding "[A]".
		//
		// This is the one place in the package where a value is handed over
		// without having been walked, so it is the one place that has to walk
		// it. encoding/json pays the same cost for the same reason.
		if err := v.validate(); err != nil {
			return err
		}
		return u.UnmarshalJSON(v.Raw())
	}
	if v.kind == String {
		if u, ok := textUnmarshalerFor(rv); ok {
			s, esc := v.str()
			if !esc {
				return u.UnmarshalText([]byte(s))
			}
			return u.UnmarshalText([]byte(s))
		}
	}

	if v.kind == Null {
		// null clears the kinds that have a nil, and leaves everything else
		// alone. That is what encoding/json does, and it is load-bearing: a
		// struct field of type int keeps whatever it had.
		switch rv.Kind() {
		case reflect.Interface, reflect.Pointer, reflect.Map, reflect.Slice:
			rv.SetZero()
		}
		return nil
	}

	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			rv.Set(reflect.New(rv.Type().Elem()))
		}
		return v.decode(rv.Elem())
	}

	switch rv.Kind() {
	case reflect.Interface:
		if rv.NumMethod() != 0 {
			return v.typeErr(rv.Type())
		}
		a, err := v.any()
		if err != nil {
			return err
		}
		rv.Set(reflect.ValueOf(a))
		return nil

	case reflect.Bool:
		if v.kind != Bool {
			return v.typeErr(rv.Type())
		}
		rv.SetBool(v.Bool())
		return nil

	case reflect.String:
		if v.kind != String {
			return v.typeErr(rv.Type())
		}
		s, _ := v.str()
		rv.SetString(s)
		return nil

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.decodeInt(rv)

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.decodeUint(rv)

	case reflect.Float32, reflect.Float64:
		if v.kind != Number {
			return v.typeErr(rv.Type())
		}
		f, err := strconv.ParseFloat(bstr(v.Raw()), rv.Type().Bits())
		if err != nil {
			return &json.UnmarshalTypeError{Value: "number " + string(v.Raw()), Type: rv.Type()}
		}
		rv.SetFloat(f)
		return nil

	case reflect.Slice:
		return v.decodeSlice(rv)

	case reflect.Array:
		return v.decodeArray(rv)

	case reflect.Map:
		return v.decodeMap(rv)

	case reflect.Struct:
		return v.decodeStruct(rv)
	}
	return v.typeErr(rv.Type())
}

// unknownFieldErr reports a field the destination struct does not name, in the
// words encoding/json uses -- callers match on this string.
func unknownFieldErr(data []byte, start, end int) error {
	key, ok := unquote(data[start:end])
	if !ok {
		key = string(data[start:end])
	}
	return fmt.Errorf("json: unknown field %q", key)
}

// bstr views b as a string without copying it.
//
// Only for callees that do not retain the string. strconv's parsers do not, and
// converting each number to a string the ordinary way allocated once per
// number — on canada.json, a couple of million times.
func bstr(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return unsafe.String(&b[0], len(b))
}

func (v Value) typeErr(t reflect.Type) error {
	return &json.UnmarshalTypeError{Value: v.kind.String(), Type: t}
}

// str returns a string value's contents and whether it needed unescaping.
func (v Value) str() (string, bool) {
	raw := v.d.data[v.start:v.end]
	s, _ := unquote(raw)
	return s, len(s) != len(raw)-2
}

func (v Value) decodeInt(rv reflect.Value) error {
	if v.kind != Number {
		return v.typeErr(rv.Type())
	}
	n, err := strconv.ParseInt(bstr(v.Raw()), 10, rv.Type().Bits())
	if err != nil {
		return &json.UnmarshalTypeError{Value: "number " + string(v.Raw()), Type: rv.Type()}
	}
	rv.SetInt(n)
	return nil
}

func (v Value) decodeUint(rv reflect.Value) error {
	if v.kind != Number {
		return v.typeErr(rv.Type())
	}
	n, err := strconv.ParseUint(bstr(v.Raw()), 10, rv.Type().Bits())
	if err != nil {
		return &json.UnmarshalTypeError{Value: "number " + string(v.Raw()), Type: rv.Type()}
	}
	rv.SetUint(n)
	return nil
}

func (v Value) decodeSlice(rv reflect.Value) error {
	// A []byte carried as a JSON string is base64. Carried as an array it is
	// an ordinary slice of numbers — encoding/json accepts both, and only the
	// string form is special.
	if v.kind == String && rv.Type().Elem().Kind() == reflect.Uint8 &&
		!implementsUnmarshaler(rv.Type().Elem()) {
		s, _ := v.str()
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return err
		}
		rv.SetBytes(b)
		return nil
	}
	if v.kind != Array {
		return v.typeErr(rv.Type())
	}
	n := v.Len()
	// An empty array gives a non-nil empty slice, not nil. reflect.DeepEqual
	// tells those apart and %v does not, so the difference is invisible until
	// something compares them.
	if rv.IsNil() || rv.Cap() < n {
		rv.Set(reflect.MakeSlice(rv.Type(), n, n))
	} else {
		rv.SetLen(n)
	}
	// Walked here rather than through ForEach, which returns silently when a
	// value does not parse. That swallowed the error for `[A]` and made the
	// document look valid.
	return v.eachValue(func(k int, e Value) error {
		if k >= rv.Len() {
			return nil
		}
		return e.decode(rv.Index(k))
	})
}

// eachValue calls fn for each element of an array, propagating both the walk's
// errors and fn's.
func (v Value) eachValue(fn func(int, Value) error) error {
	d := v.d
	i := d.skip(v.start + 1)
	k := 0
	for i < v.end-1 {
		e, next, err := d.value(i)
		if err != nil {
			return err
		}
		if err := fn(k, e); err != nil {
			return err
		}
		k++
		i = d.skip(next)
		if i >= v.end-1 {
			break
		}
		if d.data[i] != ',' {
			return errAt("expected ',' or ']'", i)
		}
		i = d.skip(i + 1)
		if i >= v.end-1 {
			return errAt("expected a value after ','", i)
		}
	}
	return nil
}

// eachField is eachValue for an object.
func (v Value) eachField(fn func(string, Value) error) error {
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
		key, _ := unquote(d.data[i:kend])
		i = d.skip(kend)
		if i >= v.end || d.data[i] != ':' {
			return errAt("expected ':' after object key", i)
		}
		i = d.skip(i + 1)
		e, next, err := d.value(i)
		if err != nil {
			return err
		}
		if err := fn(key, e); err != nil {
			return err
		}
		i = d.skip(next)
		if i >= v.end-1 {
			break
		}
		if d.data[i] != ',' {
			return errAt("expected ',' or '}'", i)
		}
		i = d.skip(i + 1)
		if i >= v.end-1 {
			return errAt("expected a string key", i)
		}
	}
	return nil
}

func (v Value) decodeArray(rv reflect.Value) error {
	if v.kind != Array {
		return v.typeErr(rv.Type())
	}
	n := 0
	// Extra elements are discarded, which is what encoding/json does — but they
	// are still walked, because they still have to be well-formed.
	if err := v.eachValue(func(k int, e Value) error {
		n = k + 1
		if k >= rv.Len() {
			return nil
		}
		return e.decode(rv.Index(k))
	}); err != nil {
		return err
	}
	// Elements the document did not supply are zeroed.
	for ; n < rv.Len(); n++ {
		rv.Index(n).SetZero()
	}
	return nil
}

func (v Value) decodeMap(rv reflect.Value) error {
	if v.kind != Object {
		return v.typeErr(rv.Type())
	}
	t := rv.Type()
	kt := t.Key()
	// encoding/json allows a string key, an integer key, or a key type that
	// implements encoding.TextUnmarshaler.
	switch {
	case kt.Kind() == reflect.String:
	case isIntKind(kt.Kind()) || isUintKind(kt.Kind()):
	case reflect.PointerTo(kt).Implements(textUnmarshalerType):
	default:
		return v.typeErr(t)
	}
	if rv.IsNil() {
		rv.Set(reflect.MakeMap(t))
	}
	elemT := t.Elem()
	return v.eachField(func(k string, e Value) error {
		kv := reflect.New(kt).Elem()
		if err := setMapKey(kv, k); err != nil {
			return err
		}
		ev := reflect.New(elemT).Elem()
		if err := e.decode(ev); err != nil {
			return err
		}
		rv.SetMapIndex(kv, ev)
		return nil
	})
}

func setMapKey(kv reflect.Value, k string) error {
	switch {
	case kv.Kind() == reflect.String:
		kv.SetString(k)
		return nil
	case isIntKind(kv.Kind()):
		n, err := strconv.ParseInt(k, 10, kv.Type().Bits())
		if err != nil {
			return &json.UnmarshalTypeError{Value: "number " + k, Type: kv.Type()}
		}
		kv.SetInt(n)
		return nil
	case isUintKind(kv.Kind()):
		n, err := strconv.ParseUint(k, 10, kv.Type().Bits())
		if err != nil {
			return &json.UnmarshalTypeError{Value: "number " + k, Type: kv.Type()}
		}
		kv.SetUint(n)
		return nil
	}
	if u, ok := kv.Addr().Interface().(encoding.TextUnmarshaler); ok {
		return u.UnmarshalText([]byte(k))
	}
	return fmt.Errorf("simdjson: unsupported map key type %s", kv.Type())
}

func (v Value) decodeStruct(rv reflect.Value) error {
	if v.kind != Object {
		return v.typeErr(rv.Type())
	}
	plan := planFor(rv.Type())
	d := v.d
	i := d.skip(v.start + 1)
	for i < v.end-1 {
		if d.data[i] != '"' {
			return errAt("expected a string key", i)
		}
		i0 := i
		kend, ok := d.stringEnd(i)
		if !ok {
			var err error
			if kend, err = d.stringEndSlow(i); err != nil {
				return err
			}
		}

		// Looked up straight from the document's bytes. Go elides the
		// allocation for a string conversion used only to index a map, so the
		// common case — an exact match on a key with no escape — costs nothing
		// per field. Building the key string first allocated once for every
		// field of every object.
		raw := d.data[i+1 : kend-1]
		f, found := plan.byName[string(raw)]
		if !found {
			// An escaped key, or one that only matches case-insensitively.
			// encoding/json tries the fold only after every exact match fails.
			key, _ := unquote(d.data[i:kend])
			if f, found = plan.byName[key]; !found {
				f, found = plan.byFold[strings.ToLower(key)]
			}
		}

		i = d.skip(kend)
		if i >= v.end || d.data[i] != ':' {
			return errAt("expected ':' after object key", i)
		}
		i = d.skip(i + 1)
		if !found && d.disallowUnknown {
			return unknownFieldErr(d.data, i0, kend)
		}
		e, next, err := d.value(i)
		if err != nil {
			return err
		}
		if found {
			fv := rv
			for _, x := range f.index {
				if fv.Kind() == reflect.Pointer {
					if fv.IsNil() {
						if !fv.CanSet() {
							fv = reflect.Value{}
							break
						}
						fv.Set(reflect.New(fv.Type().Elem()))
					}
					fv = fv.Elem()
				}
				fv = fv.Field(x)
			}
			if fv.IsValid() {
				// null reaches the ordinary path even on a `,string` field: the
				// option says how a value is carried, and null is not carried
				// that way.
				if f.asString && e.kind != Null {
					err = decodeQuoted(e, fv)
				} else {
					err = e.decode(fv)
				}
				if err != nil {
					return err
				}
			}
		}

		i = d.skip(next)
		if i < v.end-1 && d.data[i] == ',' {
			i = d.skip(i + 1)
		}
	}
	return nil
}

// decodeQuoted handles the `,string` tag option, where a value is carried
// inside a JSON string.
//
// The content is a JSON literal, not a Go one, and the difference is not
// cosmetic: strconv.ParseInt takes "+0" and JSON does not, strconv.ParseBool
// takes "1" and "T" and JSON does not, and a string field tagged `,string`
// expects a value that is quoted twice. All three were found by fuzzing against
// encoding/json rather than by reading its source.
func decodeQuoted(v Value, rv reflect.Value) error {
	if v.kind != String {
		return v.typeErr(rv.Type())
	}
	s, _ := v.str()
	bad := func() error {
		return fmt.Errorf("json: invalid use of ,string struct tag, "+
			"trying to unmarshal %q into %s", s, rv.Type())
	}
	switch rv.Kind() {
	case reflect.Bool:
		switch s {
		case "true":
			rv.SetBool(true)
		case "false":
			rv.SetBool(false)
		default:
			return bad()
		}
	case reflect.String:
		// Quoted twice: the outer string carries a JSON string.
		inner, ok := unquote([]byte(s))
		if !ok {
			return bad()
		}
		rv.SetString(inner)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if !numberStart(s) {
			return bad()
		}
		n, err := strconv.ParseInt(s, 10, rv.Type().Bits())
		if err != nil {
			return bad()
		}
		rv.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		if !numberStart(s) {
			return bad()
		}
		n, err := strconv.ParseUint(s, 10, rv.Type().Bits())
		if err != nil {
			return bad()
		}
		rv.SetUint(n)
	case reflect.Float32, reflect.Float64:
		if !numberStart(s) {
			return bad()
		}
		f, err := strconv.ParseFloat(s, rv.Type().Bits())
		if err != nil {
			return bad()
		}
		rv.SetFloat(f)
	default:
		return v.typeErr(rv.Type())
	}
	return nil
}

// numberStart is the check encoding/json applies to a `,string` number: the
// first byte must be a minus or a digit, and after that Go's own parser
// decides. That is narrower than JSON in one place — it rejects a leading plus
// — and wider in another, since "00" passes. Both were found by fuzzing
// against it; neither follows from the JSON grammar.
func numberStart(s string) bool {
	if s == "" {
		return false
	}
	return s[0] == '-' || isDigit(s[0])
}

// any builds the map[string]any / []any tree encoding/json produces for an
// interface{} destination.
//
// It walks the document directly rather than through ForEach and value(). Those
// build a five-word Value and call through a closure for every element, which
// on canada.json is a couple of million of each — and the tree being built is
// already allocation-bound, so the extra was pure overhead.
func (v Value) any() (any, error) {
	switch v.kind {
	case Null:
		return nil, nil
	case Bool:
		return v.Bool(), nil
	case Number:
		if v.d.useNumber {
			// The exact text, interned rather than allocated: a document of
			// numbers would otherwise be one allocation per number, and the
			// point of UseNumber is to keep digits that a float64 would lose.
			return json.Number(v.d.intern(v.Raw())), nil
		}
		f, err := strconv.ParseFloat(bstr(v.Raw()), 64)
		if err != nil || math.IsInf(f, 0) {
			return nil, &json.UnmarshalTypeError{
				Value: "number " + string(v.Raw()),
				Type:  reflect.TypeOf(float64(0)),
			}
		}
		return f, nil
	case String:
		s, _ := v.str()
		return s, nil
	case Array:
		return v.anyArray()
	case Object:
		return v.anyObject()
	}
	return nil, errSyntax("decoding an invalid value")
}

func (v Value) anyArray() (any, error) {
	out := []any{}
	err := v.eachValue(func(_ int, e Value) error {
		a, err := e.any()
		if err != nil {
			return err
		}
		out = append(out, a)
		return nil
	})
	return out, err
}

func (v Value) anyObject() (any, error) {
	out := map[string]any{}
	err := v.eachField(func(k string, e Value) error {
		a, err := e.any()
		if err != nil {
			return err
		}
		out[k] = a
		return nil
	})
	return out, err
}

// ---------------------------------------------------------------- type plans

var (
	unmarshalerType     = reflect.TypeOf((*json.Unmarshaler)(nil)).Elem()
	textUnmarshalerType = reflect.TypeOf((*encoding.TextUnmarshaler)(nil)).Elem()
)

func implementsUnmarshaler(t reflect.Type) bool {
	return t.Implements(unmarshalerType) || reflect.PointerTo(t).Implements(unmarshalerType)
}

func unmarshalerFor(rv reflect.Value) (json.Unmarshaler, bool) {
	if rv.Kind() != reflect.Pointer && rv.CanAddr() {
		if u, ok := rv.Addr().Interface().(json.Unmarshaler); ok {
			return u, true
		}
		return nil, false
	}
	if rv.Kind() == reflect.Pointer && rv.Type().Implements(unmarshalerType) {
		if rv.IsNil() {
			if !rv.CanSet() {
				return nil, false
			}
			rv.Set(reflect.New(rv.Type().Elem()))
		}
		u, ok := rv.Interface().(json.Unmarshaler)
		return u, ok
	}
	return nil, false
}

func textUnmarshalerFor(rv reflect.Value) (encoding.TextUnmarshaler, bool) {
	if rv.Kind() != reflect.Pointer && rv.CanAddr() {
		u, ok := rv.Addr().Interface().(encoding.TextUnmarshaler)
		return u, ok
	}
	if rv.Kind() == reflect.Pointer && rv.Type().Implements(textUnmarshalerType) {
		if rv.IsNil() {
			if !rv.CanSet() {
				return nil, false
			}
			rv.Set(reflect.New(rv.Type().Elem()))
		}
		u, ok := rv.Interface().(encoding.TextUnmarshaler)
		return u, ok
	}
	return nil, false
}

func isIntKind(k reflect.Kind) bool {
	return k >= reflect.Int && k <= reflect.Int64
}

func isUintKind(k reflect.Kind) bool {
	return k >= reflect.Uint && k <= reflect.Uintptr
}

// field is one decodable struct field: where it lives and what its tag said.
type field struct {
	index    []int
	asString bool
}

// structPlan is a type's fields indexed by the names a document might use.
//
// Built once per type and cached. Without it every object field would re-parse
// struct tags and re-walk embedded types, which on a document with a million
// fields is a million times more reflection than the type requires.
type structPlan struct {
	byName map[string]field
	byFold map[string]field
}

var planCache sync.Map // reflect.Type -> *structPlan

func planFor(t reflect.Type) *structPlan {
	if p, ok := planCache.Load(t); ok {
		return p.(*structPlan)
	}
	p := buildPlan(t)
	planCache.Store(t, p)
	return p
}

func buildPlan(t reflect.Type) *structPlan {
	p := &structPlan{byName: map[string]field{}, byFold: map[string]field{}}
	var walk func(t reflect.Type, prefix []int, depth int)
	walk = func(t reflect.Type, prefix []int, depth int) {
		if depth > 10 {
			return // cycle guard; encoding/json has its own
		}
		for i := 0; i < t.NumField(); i++ {
			sf := t.Field(i)
			tag := sf.Tag.Get("json")
			if tag == "-" {
				continue
			}
			name, opts, _ := strings.Cut(tag, ",")

			// An embedded struct with no name of its own contributes its
			// fields to the outer type.
			ft := sf.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if sf.Anonymous && name == "" && ft.Kind() == reflect.Struct {
				if !sf.IsExported() && ft.Kind() != reflect.Struct {
					continue
				}
				walk(ft, append(append([]int{}, prefix...), i), depth+1)
				continue
			}
			if !sf.IsExported() {
				continue
			}
			if name == "" {
				name = sf.Name
			}
			idx := append(append([]int{}, prefix...), i)
			f := field{index: idx, asString: strings.Contains(opts, "string")}
			// Shallower fields win, which is how embedding resolves ties.
			if old, ok := p.byName[name]; ok && len(old.index) <= len(idx) {
				continue
			}
			p.byName[name] = f
			p.byFold[strings.ToLower(name)] = f
		}
	}
	walk(t, nil, 0)
	return p
}
