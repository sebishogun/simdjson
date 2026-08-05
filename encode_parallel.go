package simdjson

// Parallel Marshal for large top-level slices -- the encode-side mirror of the
// parallel index.
//
// A slice encodes element by element with no cross-element state, so element
// RANGES shard across workers, each encoding its range with the ordinary
// compiled slice encoder into its own pooled buffer, and the parent stitches
// the results in order. Reusing the slice encoder per range is the point:
// every element path -- leaf kinds, options, registered and generated
// encoders, nested anything -- is inherited exactly, so byte-identity with the
// serial encode is by construction, and the differential holds it there.
//
// The measured ceiling (docs/wrong.md has the table): 2.84x at four workers on
// a 1.17 MB output, allocation the serializer at that size. The gate is the
// OUTPUT size, known from the pool's hint of the last encode, exactly as the
// parallel index gates on input size. Only the top-level value goes parallel;
// a nested slice is encoded by whatever worker owns its element, or the
// goroutine count would grow with the document.

import (
	"reflect"
	"runtime"
	"sync"
)

// Tunables, as variables so the differential can force the path onto small
// values. The ceiling turned over past eight workers at the megabyte scale,
// and under two the arm is overhead.
var (
	// Swept on the decoded-twitter row (770 KB of output, serial 857 us):
	//
	//	per-worker   192K   128K   96K    64K
	//	us            457    333   279    246
	//
	// 64 KiB per worker and the eight-worker cap give 3.49x there -- BETTER
	// than the hand-sharded ceiling's 2.84x, because pooled worker states
	// arrive with warm buffers where the ceiling paid a cold allocation per
	// range. The quarter-megabyte gate keeps the arm off everything the
	// per-call goroutine cost would eat.
	parallelMarshalMin     = 256 << 10 // output-size hint that arms the path
	parallelMarshalPerWork = 64 << 10
	// Sixteen, re-swept at scale: a 63 MB output ran 3,185 MB/s at eight
	// workers and 4,057 at sixteen (7.7x over 524 serial), while the 770 KB
	// flagship was unchanged (264 vs 268 us) because est/PerWork already
	// bounds small outputs below the cap. Twenty-four bought 2% more at
	// 63 MB; not worth the extra fan-out everywhere else.
	parallelMarshalMaxProc = 16
)

// marshalParallelSlice encodes a top-level slice across workers. It reports
// whether it ran; when it declines, the caller encodes serially and nothing
// has been touched.
func (e *encodeState) marshalParallelSlice(rv reflect.Value) (bool, error) {
	if e.hint < parallelMarshalMin {
		return false, nil
	}
	n := rv.Len()
	if n < 8 {
		return false, nil
	}
	maxw := runtime.GOMAXPROCS(0)
	if maxw > parallelMarshalMaxProc {
		maxw = parallelMarshalMaxProc
	}
	if maxw < 2 {
		return false, nil
	}

	// Element zero is encoded serially into the output first, and its size
	// decides for the rest. The pooled hint is the WHOLE last output, and
	// gating on it alone armed the path for every sufficiently-long slice
	// anywhere under a big document -- canada is thousands of small
	// coordinate rings under one 2 MB value, and paying a fan-out per ring
	// cost the floats gate row 21.5%. One element's bytes times the count is
	// an estimate the slice itself provides, and the work of producing it is
	// not wasted: the element is already encoded either way.
	// FOUR elements, not one: element sizes in real documents are skewed, and
	// a single short first element under-estimated twitter's statuses by half
	// and disarmed the path that is worth 3.5x there. Four smooths the skew
	// and their encoding is not wasted -- they are the head of the output
	// either way.
	sample := 4
	if sample > n/2 {
		sample = n / 2
	}
	mark := len(e.buf)
	e.buf = append(e.buf, '[')
	for i := 0; i < sample; i++ {
		if err := e.encodeElem(rv, i); err != nil {
			return true, err
		}
	}
	est := (len(e.buf) - mark - 1) / sample * (n - sample)
	workers := est / parallelMarshalPerWork
	if workers > maxw {
		workers = maxw
	}
	if est < parallelMarshalMin || workers < 2 || n-sample < 2*workers {
		// The rest goes serially, right here: the element loop for []any, one
		// stripped sub-encode for a typed slice. Falling back to the caller
		// instead would re-encode the sampled head.
		if err := e.encodeRestSerial(rv, sample, n); err != nil {
			return true, err
		}
		e.buf = append(e.buf, ']')
		return true, nil
	}

	type result struct {
		buf []byte
		st  *encodeState
		err error
	}
	res := make([]result, workers)
	rest := n - sample
	chunk := (rest + workers - 1) / workers
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		lo, hi := sample+w*chunk, sample+min((w+1)*chunk, rest)
		if lo >= hi {
			break
		}
		wg.Add(1)
		go func(w, lo, hi int) {
			defer wg.Done()
			st := encoderPool.Get().(*encodeState)
			st.opts = e.opts
			st.buf = st.buf[:0]
			// The worker state goes back to the pool when this range is done,
			// and the NEXT Marshal may draw it as its parent. A cold hint
			// there disarms the gate, so the pool degrades to serial after one
			// parallel encode.
			if st.hint < e.hint {
				st.hint = e.hint
			}
			// Through marshal, not through a compiled encoder directly: the
			// range takes exactly the path the whole value would have -- the
			// encodeAny fast path for []any, the reflect path for typed
			// slices, registered encoders for their elements -- so there is
			// nothing here that can drift from serial.
			st.noParallel = true
			res[w] = result{st: st}
			res[w].err = st.marshal(rv.Slice(lo, hi).Interface())
			st.noParallel = false
			res[w].buf = st.buf
		}(w, lo, hi)
	}
	wg.Wait()

	var firstErr error
	for w := range res {
		if res[w].err != nil && firstErr == nil {
			firstErr = res[w].err
		}
	}
	if firstErr != nil {
		for w := range res {
			if res[w].st != nil {
				encoderPool.Put(res[w].st)
			}
		}
		return true, firstErr
	}

	total := 1
	for w := range res {
		if res[w].st != nil && len(res[w].buf) > 2 {
			total += len(res[w].buf) - 2 + 1
		}
	}
	b := e.buf
	if cap(b)-len(b) < total {
		nb := make([]byte, len(b), len(b)+total)
		copy(nb, b)
		b = nb
	}
	for w := range res {
		if res[w].st == nil {
			continue
		}
		inner := res[w].buf
		if len(inner) > 2 {
			b = append(b, ',')
			b = append(b, inner[1:len(inner)-1]...)
		}
		encoderPool.Put(res[w].st)
	}
	e.buf = append(b, ']')
	return true, nil
}

// shardAnyRest is the []any loop's mid-flight decision, called once when
// four elements are already in the buffer (a trailing comma for the fifth has
// been written and is rewound here if the shard happens). It reports whether
// it took over and finished the slice, closing bracket included.
func (e *encodeState) shardAnyRest(x []any, from, mark int) (bool, error) {
	est := (len(e.buf) - mark - 1) / from * (len(x) - from)
	if est < parallelMarshalMin {
		return false, nil
	}
	maxw := runtime.GOMAXPROCS(0)
	if maxw > parallelMarshalMaxProc {
		maxw = parallelMarshalMaxProc
	}
	workers := est / parallelMarshalPerWork
	if workers > maxw {
		workers = maxw
	}
	rest := len(x) - from
	if workers < 2 || rest < 2*workers {
		return false, nil
	}
	// The loop wrote the separator for the element it thought it was about to
	// encode; the workers' bracket-stripped output starts with its own.
	e.buf = e.buf[:len(e.buf)-1]

	type result struct {
		buf []byte
		st  *encodeState
		err error
	}
	res := make([]result, workers)
	chunk := (rest + workers - 1) / workers
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		lo, hi := from+w*chunk, from+min((w+1)*chunk, rest)
		if lo >= hi {
			break
		}
		wg.Add(1)
		go func(w, lo, hi int) {
			defer wg.Done()
			st := encoderPool.Get().(*encodeState)
			st.opts = e.opts
			st.buf = st.buf[:0]
			if st.hint < e.hint {
				st.hint = e.hint
			}
			st.noParallel = true
			res[w] = result{st: st}
			res[w].err = st.marshal(x[lo:hi])
			st.noParallel = false
			res[w].buf = st.buf
		}(w, lo, hi)
	}
	wg.Wait()

	var firstErr error
	for w := range res {
		if res[w].err != nil && firstErr == nil {
			firstErr = res[w].err
		}
	}
	if firstErr != nil {
		for w := range res {
			if res[w].st != nil {
				encoderPool.Put(res[w].st)
			}
		}
		return true, firstErr
	}
	total := 1
	for w := range res {
		if res[w].st != nil && len(res[w].buf) > 2 {
			total += len(res[w].buf) - 2 + 1
		}
	}
	b := e.buf
	if cap(b)-len(b) < total {
		nb := make([]byte, len(b), len(b)+total)
		copy(nb, b)
		b = nb
	}
	for w := range res {
		if res[w].st == nil {
			continue
		}
		inner := res[w].buf
		if len(inner) > 2 {
			b = append(b, ',')
			b = append(b, inner[1:len(inner)-1]...)
		}
		encoderPool.Put(res[w].st)
	}
	e.buf = append(b, ']')
	return true, nil
}

// encodeElem writes one element of a TYPED slice through its own compiled
// encoder, separator included when it is not the first -- a one-element
// bracket-stripped sub-encode, the same trick the merge uses.
func (e *encodeState) encodeElem(rv reflect.Value, i int) error {
	return e.encodeRestSerial(rv, i, i+1)
}

// encodeRestSerial writes elements [lo,n) of a typed slice: one sub-encode of
// the range through the type's compiled encoder, stripped of its brackets and
// joined to what the caller already wrote.
func (e *encodeState) encodeRestSerial(rv reflect.Value, lo, n int) error {
	if lo >= n {
		return nil
	}
	sub := reflect.New(rv.Type()).Elem()
	sub.Set(rv.Slice(lo, n))
	mark := len(e.buf)
	if err := encoderFor(rv.Type())(e, ptrOf(sub), sub); err != nil {
		return err
	}
	inner := e.buf[mark:]
	if len(inner) <= 2 {
		e.buf = e.buf[:mark]
		return nil
	}
	if lo > 0 {
		// "[elems]" becomes ",elems": the opener turns into the separator and
		// the closer goes.
		inner[0] = ','
		e.buf = e.buf[:len(e.buf)-1]
		return nil
	}
	// "[elems]" becomes "elems": both brackets go.
	copy(inner, inner[1:])
	e.buf = e.buf[:len(e.buf)-2]
	return nil
}
