package simdjson

// Parallel Unmarshal for a large root array decoded into a slice -- the
// decode-side completion of the parallel family: index (#150), Marshal
// (#154), Valid's walk, and now this.
//
// The bracket index gives every top-level element's exact byte extent, the
// compiled element decoder takes an unsafe destination and a start offset and
// returns where it ended, and slice elements are independent memory -- so
// workers decode element ranges straight into the result slice with no
// coordination beyond a private Doc header each (the index's wsW/wsX
// whitespace cache is the one mutable thing, as everywhere in this family).
//
// EXACTNESS POLICY: the parallel path commits only when everything is clean.
// Any anomaly -- an element the enumeration cannot prove, a gap that is not
// exactly one comma of whitespace, any element decoder error -- discards the
// parallel attempt and reruns the ordinary serial decode, which then produces
// exactly the error the caller would always have seen. Broken documents pay
// one wasted partial pass; clean documents, which are what a hot path feeds,
// pay nothing.

import (
	"reflect"
	"runtime"
	"sync"
	"unsafe"
)

// unmarshalParallelMinElems gates the fan-out; a var for the differential.
var unmarshalParallelMinElems = 2

// unmarshalParallel decodes data into out when out is a pointer to a slice of
// containers and the document is a root array of them. ok=false means the
// caller should run the ordinary path; when ok, err is final.
func unmarshalParallel(data []byte, d *Doc, out any) (err error, ok bool) {
	rv := reflect.ValueOf(out)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return nil, false
	}
	sl := rv.Elem()
	if sl.Kind() != reflect.Slice {
		return nil, false
	}
	elemT := sl.Type().Elem()
	switch elemT.Kind() {
	case reflect.Uint8, reflect.Interface:
		// []byte is base64; []any decodes through encodeAny-style paths that
		// the serial decoder owns.
		return nil, false
	}
	ix := d.ix
	elems, _, shaped := enumerateTopContainers(ix, data, unmarshalParallelMinElems)
	if !shaped {
		return nil, false
	}

	maxw := runtime.GOMAXPROCS(0)
	if maxw > parallelMaxProcs {
		maxw = parallelMaxProcs
	}
	ranges := splitTopWork(elems, maxw)
	if ranges == nil {
		return nil, false
	}

	dec := decoderFor(elemT)
	res := reflect.MakeSlice(sl.Type(), len(elems), len(elems))
	base := res.UnsafePointer()
	size := elemT.Size()

	fail := make([]bool, len(ranges))
	var wg sync.WaitGroup
	for w, r := range ranges {
		lo, hi := r[0], r[1]
		wg.Add(1)
		go func(w, lo, hi int) {
			defer wg.Done()
			my := *ix
			my.wsW, my.wsX = -1, 0
			pd := &Doc{data: data, ix: &my, inStr: my.inStr, noWS: my.noWS,
				wsw: my.wsw, strictSkip: true}
			for i := lo; i < hi; i++ {
				p := unsafe.Add(base, uintptr(i)*size)
				end, err := dec(p, pd, int(elems[i].startB))
				if err != nil || end != int(elems[i].endB) {
					fail[w] = true
					return
				}
			}
		}(w, lo, hi)
	}
	wg.Wait()
	for _, f := range fail {
		if f {
			// Something in an element is wrong. The serial decode owns the
			// error, so the caller reruns it and reports exactly what it
			// always would have.
			return nil, false
		}
	}
	sl.Set(res)
	return nil, true
}
