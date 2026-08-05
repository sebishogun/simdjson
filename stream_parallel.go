package simdjson

// Parallel batch validation for the streaming Value path.
//
// Value validates every record before handing it out — about a third of the
// throughput, and not optional (see Value's comment). Validation is
// side-effect-free and per-record independent, so when load produces a batch
// of top-level container values, the walk fans across workers — the same
// worker body as validParallel. Delivery then needs none of the per-record
// machinery: the staged extent already holds the start, the end, the kind and
// the verdict, and the gaps were proved whitespace when the staging was
// built, so Value hands the record out from the extent alone — no whitespace
// skip, no bracket match, no validate (the fast path at the top of Value). A
// record a worker rejected is re-validated serially at delivery, so the
// caller sees exactly the serial error at exactly the serial position.
//
// Decode gets no such treatment, deliberately. Decoding element k into the
// caller's variable starts from that variable's state after element k-1 —
// absent struct fields keep their prior values, matching encoding/json — so
// the results form a serial chain by contract, and predecoding into fresh
// slots would silently change what repeated Decode(&v) leaves behind whenever
// a record omits a field an earlier one set. See docs/parallelism.md.
//
// Staging never changes what is read or when: the batch was already in the
// buffer, the workers are joined before load returns, and any surprise —
// a scalar at top level, a Decode advancing the cursor, a batch the walk
// cannot prove — just falls back to the serial validate at delivery.

import (
	"runtime"
	"sync"
)

// streamStageMinBytes and streamStageMinElems gate the fan-out; a batch below
// either is validated serially at delivery as before. Vars for the tests.
var (
	streamStageMinBytes = 64 << 10
	streamStageMinElems = 8
	// streamStageStreak is how many consecutive Value deliveries arm staging.
	// The first batch of a stream always runs serial, so a caller reading one
	// value from a large buffer never pays for validating the rest.
	streamStageStreak = 2
)

// enumerateBatchValues lists the batch's top-level values when every one is a
// container and every gap is whitespace — the shape a record stream has.
// Unlike enumerateTopContainers there is no root bracket and no commas: the
// values are simply concatenated. ok=false means some other shape, which is
// not an error, just not this fast path.
func enumerateBatchValues(ix *index, data []byte, minElems int) (elems []topExtent, ok bool) {
	if len(ix.pos) == 0 {
		return nil, false
	}
	k, prevEnd := 0, 0
	for k < len(ix.pos) {
		p := int(ix.pos[k])
		if p >= len(data) {
			// The index may run past the batch — at the safeEnd site it is
			// built over the whole buffer and the batch stops at the last
			// closed container. Everything past that belongs to the next one.
			break
		}
		if c := data[p]; c != '{' && c != '[' {
			return nil, false
		}
		if !wsOnly(ix, data, prevEnd, p) {
			return nil, false
		}
		cl := int(ix.match[k])
		if cl <= k || cl >= len(ix.pos) {
			return nil, false
		}
		end := int(ix.pos[cl]) + 1
		elems = append(elems, topExtent{int32(p), int32(end)})
		prevEnd = end
		k = cl + 1
	}
	if len(elems) < minElems || !wsOnly(ix, data, prevEnd, len(data)) {
		return nil, false
	}
	return elems, true
}

// stageValidate validates the freshly loaded batch across workers, filling
// the staging Value delivers from, and hands the buffer's remainder to the
// prefetch. Called with d.doc/d.data just set; on any decline the staging
// stays empty and nothing changes.
func (d *Decoder) stageValidate() {
	d.stElems, d.stNext = d.stElems[:0], 0
	if d.valStreak >= streamStageStreak && len(d.data) >= streamStageMinBytes {
		if elems, ok := enumerateBatchValues(d.doc.ix, d.data, streamStageMinElems); ok {
			d.stElems, d.stBad = stageValidateElems(d.doc, elems)
		}
	}
	d.pfLaunch()
}

// stageValidateElems fans validateValue over the elements across workers and
// returns them with the index of the first invalid one (len(elems) if none).
// Runs on the caller's goroutine until every worker is joined, so it is safe
// from the prefetch task as well as from load.
func stageValidateElems(doc *Doc, elems []topExtent) ([]topExtent, int) {
	ix, data := doc.ix, doc.data
	maxw := runtime.GOMAXPROCS(0)
	if maxw > parallelMaxProcs {
		maxw = parallelMaxProcs
	}
	ranges := splitTopWork(elems, maxw)
	if ranges == nil {
		return nil, 0
	}
	bad := make([]int, len(ranges))
	var wg sync.WaitGroup
	for w, r := range ranges {
		lo, hi := r[0], r[1]
		wg.Add(1)
		go func(w, lo, hi int) {
			defer wg.Done()
			bad[w] = len(elems)
			// A private index header: shared slices, private wsW/wsX cache,
			// as everywhere in the parallel family.
			my := *ix
			my.wsW, my.wsX = -1, 0
			dd := &Doc{data: data, ix: &my, inStr: my.inStr, noWS: my.noWS, wsw: my.wsw}
			for i := lo; i < hi; i++ {
				end, err := dd.validateValue(int(elems[i].startB))
				if err != nil || end != int(elems[i].endB) {
					bad[w] = i
					return
				}
			}
		}(w, lo, hi)
	}
	wg.Wait()
	first := len(elems)
	for _, b := range bad {
		if b < first {
			first = b
		}
	}
	return elems, first
}
