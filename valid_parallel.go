package simdjson

// The parallel grammar walk for Valid on large documents -- the research half
// of the Valid-at-scale work, made non-speculative by the bracket index.
//
// Valid returns a bool, so the correctness bar is exact bool agreement with
// the serial walk; there is no error ordering to reproduce. The differential
// holds it there.
//
// The shape it accelerates is the shape huge documents have: a root ARRAY
// whose elements are containers. The parallel bracket index (the Scan path,
// 6x at this size) gives every element's exact extent in match, so elements
// shard across workers and each worker runs the ordinary validateValue over
// its element -- the same function the serial walk runs, on the same index.
// Nothing is speculative: extents are facts, and the bytes BETWEEN elements
// (comma and whitespace) are validated serially with the whitespace mask,
// O(elements + gap words).
//
// Anything else -- a root object, an array with scalar elements, trailing
// content the enumeration cannot prove -- falls back to the serial walk over
// the same already-built index, so the fallback wastes nothing.
//
// The index's wsW/wsX whitespace-word cache is MUTATED by skips, so each
// worker gets a shallow copy of the index: shared mask slices, private cache
// scalars. That is the entire synchronization story; workers share nothing
// mutable.

import (
	"runtime"
	"sync"
)

// topExtent is one top-level element's byte extent.
type topExtent struct{ startB, endB int32 }

// enumerateTopContainers lists a root array's elements when every one is a
// container and every gap is exactly its separator, using the bracket index
// and the whitespace mask. ok=false means the document is not that shape --
// not wrong, just not this fast path -- and rootClose is the pos index of the
// root's closing bracket when ok.
func enumerateTopContainers(ix *index, data []byte, minElems int) (elems []topExtent, rootClose int, ok bool) {
	if len(ix.pos) == 0 || data[ix.pos[0]] != '[' {
		return nil, 0, false
	}
	rootClose = int(ix.match[0])
	if !wsOnly(ix, data, 0, int(ix.pos[0])) ||
		!wsOnly(ix, data, int(ix.pos[rootClose])+1, len(data)) {
		return nil, 0, false
	}
	k := 1
	for k < rootClose {
		c := data[ix.pos[k]]
		if c != '{' && c != '[' {
			return nil, 0, false
		}
		cl := int(ix.match[k])
		elems = append(elems, topExtent{ix.pos[k], ix.pos[cl] + 1})
		k = cl + 1
	}
	if len(elems) < minElems {
		return nil, 0, false
	}
	if !gapOK(ix, data, int(ix.pos[0])+1, int(elems[0].startB), false) {
		return nil, 0, false
	}
	for i := 1; i < len(elems); i++ {
		if !gapOK(ix, data, int(elems[i-1].endB), int(elems[i].startB), true) {
			return nil, 0, false
		}
	}
	if !gapOK(ix, data, int(elems[len(elems)-1].endB), int(ix.pos[rootClose]), false) {
		return nil, 0, false
	}
	return elems, rootClose, true
}

// validParallelMinElems is the least elements worth sharding; below it the
// serial walk on the same index runs. A var for the differential.
var validParallelMinElems = 2

// splitTopWork assigns contiguous element ranges to workers by BYTES, not
// count. A root array can be twenty-eight two-megabyte documents or three
// million forty-byte records; count-chunking starves the first shape -- the
// Parse walk never engaged on it at all -- and byte skew unbalances the
// second. Ranges cover [lo,hi) element indices; at most maxw of them, none
// empty.
func splitTopWork(elems []topExtent, maxw int) [][2]int {
	if maxw > len(elems) {
		maxw = len(elems)
	}
	if maxw < 2 {
		return nil
	}
	total := 0
	for _, e := range elems {
		total += int(e.endB - e.startB)
	}
	share := total/maxw + 1
	var ranges [][2]int
	lo, acc := 0, 0
	for i, e := range elems {
		acc += int(e.endB - e.startB)
		if acc >= share || i == len(elems)-1 {
			ranges = append(ranges, [2]int{lo, i + 1})
			lo, acc = i+1, 0
		}
	}
	if len(ranges) < 2 {
		return nil
	}
	return ranges
}

// validParallel validates data using a full bracket index, walking top-level
// array elements across workers when the shape allows. ok=false means the
// caller should use its ordinary path; when ok, valid is the answer.
func validParallel(data []byte, ix *index) (valid, ok bool) {
	px, err, ran := buildIndexParallel(data, ix, true, false)
	if !ran {
		return false, false
	}
	if err != nil {
		// The index rejected it -- unbalanced brackets, a control character in
		// a string, a bad escape. The serial path would reject it too, at the
		// walk if not at the index.
		return false, true
	}
	ix = px
	elems, _, shaped := enumerateTopContainers(ix, data, validParallelMinElems)
	if !shaped {
		return validSerialOnIndex(data, ix), true
	}
	// A malformed gap or tail is a firm no for a root array; the enumeration
	// cannot tell "wrong shape" from "broken document", so the serial walk
	// answers either way and stays the single authority.

	maxw := runtime.GOMAXPROCS(0)
	if maxw > parallelMaxProcs {
		maxw = parallelMaxProcs
	}
	ranges := splitTopWork(elems, maxw)
	if ranges == nil {
		return validSerialOnIndex(data, ix), true
	}
	bad := make([]bool, len(ranges))
	var wg sync.WaitGroup
	for w, r := range ranges {
		lo, hi := r[0], r[1]
		wg.Add(1)
		go func(w, lo, hi int) {
			defer wg.Done()
			// A private index header: shared slices, private wsW/wsX cache.
			my := *ix
			my.wsW, my.wsX = -1, 0
			d := &Doc{data: data, ix: &my, inStr: my.inStr, noWS: my.noWS, wsw: my.wsw,
				// Top-level elements sit at absolute depth 1; the cap counts from there.
				depth: 1}
			for i := lo; i < hi; i++ {
				end, err := d.validateValue(int(elems[i].startB))
				if err != nil || end != int(elems[i].endB) {
					bad[w] = true
					return
				}
			}
		}(w, lo, hi)
	}
	wg.Wait()
	for _, b := range bad {
		if b {
			return false, true
		}
	}
	return true, true
}

// validSerialOnIndex is the ordinary walk over an index that happens to carry
// brackets -- the same code Valid runs, minus rebuilding.
func validSerialOnIndex(data []byte, ix *index) bool {
	d := &Doc{data: data, ix: ix, inStr: ix.inStr, noWS: ix.noWS, wsw: ix.wsw}
	if ix.wsCount*wsDenom > len(data) {
		return d.validTokens()
	}
	start, err := docFront2(data, d)
	if err != nil {
		return false
	}
	end, err := d.validateValue(start)
	if err != nil {
		return false
	}
	return d.skip(end) >= len(data)
}

// wsOnly reports whether data[lo:hi] is entirely JSON whitespace, using the
// whitespace mask words.
func wsOnly(ix *index, data []byte, lo, hi int) bool {
	for i := lo; i < hi; i++ {
		if !isJSONSpace[data[i]] {
			return false
		}
		// Whole words at a time once aligned, via the mask.
		if i%64 == 0 && hi-i >= 64 {
			w := i / 64
			for ; hi-w*64 >= 64; w++ {
				if ix.wsw[w] != ^uint64(0) {
					break
				}
			}
			i = w*64 - 1
		}
	}
	return true
}

// gapOK reports whether data[lo:hi] is whitespace holding exactly one comma
// (needComma) or no comma at all.
func gapOK(ix *index, data []byte, lo, hi int, needComma bool) bool {
	commas := 0
	for i := lo; i < hi; i++ {
		switch {
		case data[i] == ',':
			commas++
		case !isJSONSpace[data[i]]:
			return false
		default:
			// A long whitespace run steps by mask words.
			if i%64 == 0 && hi-i >= 64 {
				w := i / 64
				for ; hi-w*64 >= 64; w++ {
					if ix.wsw[w] != ^uint64(0) {
						break
					}
				}
				i = w*64 - 1
			}
		}
	}
	if needComma {
		return commas == 1
	}
	return commas == 0
}
