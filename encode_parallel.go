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
	parallelMarshalMaxProc = 8
)

// marshalParallelSlice encodes a top-level slice across workers. It reports
// whether it ran; when it declines, the caller encodes serially and nothing
// has been touched.
func (e *encodeState) marshalParallelSlice(rv reflect.Value) (bool, error) {
	if e.hint < parallelMarshalMin {
		return false, nil
	}
	workers := e.hint / parallelMarshalPerWork
	if m := runtime.GOMAXPROCS(0); workers > m {
		workers = m
	}
	if workers > parallelMarshalMaxProc {
		workers = parallelMarshalMaxProc
	}
	n := rv.Len()
	if workers < 2 || n < 2*workers {
		return false, nil
	}

	type result struct {
		buf []byte // the worker's whole output, brackets included
		st  *encodeState
		err error
	}
	res := make([]result, workers)
	chunk := (n + workers - 1) / workers
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		lo, hi := w*chunk, min((w+1)*chunk, n)
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
			// parallel encode -- which is exactly what the first measurement
			// showed: the arm fired once and never again.
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

	// The first failing range by element order is the error the serial encode
	// would have stopped at; every state goes home whatever happened.
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

	// Stitch: each worker produced "[elems]", and the join keeps the inner
	// bytes only. Sizing once keeps the copies from growing the buffer
	// mid-merge.
	total := 2
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
	b = append(b, '[')
	first := true
	for w := range res {
		if res[w].st == nil {
			continue
		}
		inner := res[w].buf
		if len(inner) > 2 {
			if !first {
				b = append(b, ',')
			}
			first = false
			b = append(b, inner[1:len(inner)-1]...)
		}
		encoderPool.Put(res[w].st)
	}
	e.buf = append(b, ']')
	return true, nil
}
